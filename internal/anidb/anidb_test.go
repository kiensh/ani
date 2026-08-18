package anidb

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// anidbServer is a fake anidb.app: episodes lists the given (id, number) pairs
// and every listed episode resolves one 1080p sub stream. languagesHits counts
// requests to /api/frontend/episode/<id>/languages. Returns a cleanup that
// restores baseURL.
func anidbServer(t *testing.T, episodes []Episode) (*int32, func()) {
	return anidbServerWithVariants(t, episodes, []string{"1080"})
}

// anidbServerWithVariants is anidbServer with a custom per-episode variant
// (resolution height) list.
func anidbServerWithVariants(t *testing.T, episodes []Episode, heights []string) (*int32, func()) {
	t.Helper()
	var languagesHits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/episodes"):
			var b strings.Builder
			b.WriteString(`{"episodes":[`)
			for i, e := range episodes {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"id":%d,"number":%g,"filler":false}`, e.ID, e.Number)
			}
			b.WriteString(`]}`)
			w.Write([]byte(b.String()))
		case strings.Contains(r.URL.Path, "/languages"):
			atomic.AddInt32(&languagesHits, 1)
			w.Write([]byte(`{"languages":[{"code":"jpn","name":"Japanese","embed_url":"` +
				srv.URL + `/embed"}]}`))
		case strings.HasSuffix(r.URL.Path, "/embed"):
			w.Write([]byte(`<script>file: '` + srv.URL + `/master.m3u8'</script>`))
		case strings.HasSuffix(r.URL.Path, ".m3u8"):
			var b strings.Builder
			b.WriteString("#EXTM3U\n")
			for _, h := range heights {
				fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=0x%s\n%s/v%s.m3u8\n", h, srv.URL, h)
			}
			w.Write([]byte(b.String()))
		default:
			http.NotFound(w, r)
		}
	}))
	old := baseURL
	baseURL = srv.URL
	return &languagesHits, func() {
		srv.Close()
		baseURL = old
	}
}

// TestFetchReleasesMissingEpisodeEmpty: an episode anidb doesn't list yields no
// releases (no silent substitute) and never touches the languages endpoint —
// otherwise the picker would show another episode's streams mislabeled, and MAL
// write-back would record the wrong watched count.
func TestFetchReleasesMissingEpisodeEmpty(t *testing.T) {
	hits, cleanup := anidbServer(t, []Episode{
		{ID: 11, Number: 1},
		{ID: 12, Number: 2},
		{ID: 13, Number: 3},
	})
	defer cleanup()

	rels, err := FetchReleases("show-1", 5)
	if err != nil {
		t.Fatalf("FetchReleases(5): %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("expected no releases for missing episode, got %d", len(rels))
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("languages endpoint hit %d times; a missing episode must not fetch another episode's streams", n)
	}
}

// TestFetchReleasesOffsetMapping: cumulative numbering maps MAL ep → anidb ep
// (offset 72: MAL 1 = anidb 73) and the release is labeled with the MAL number,
// so write-back records the per-season episode.
func TestFetchReleasesOffsetMapping(t *testing.T) {
	_, cleanup := anidbServer(t, []Episode{
		{ID: 731, Number: 73},
		{ID: 732, Number: 74},
	})
	defer cleanup()

	rels, err := FetchReleases("show-1", 1)
	if err != nil {
		t.Fatalf("FetchReleases(1): %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 release, got %d", len(rels))
	}
	r := rels[0]
	if r.Episode != 1 {
		t.Errorf("Episode = %d, want 1 (MAL per-season number)", r.Episode)
	}
	if r.Group != "sub" {
		t.Errorf("Group = %q, want sub", r.Group)
	}
	if r.Resolution != "1080p" {
		t.Errorf("Resolution = %q, want 1080p", r.Resolution)
	}
	if !strings.Contains(r.StreamURL, "v1080.m3u8") {
		t.Errorf("StreamURL = %q, want the 1080 variant", r.StreamURL)
	}
}

// TestFetchReleasesAllEpisodes: episode 0 (the picker's "all" filter) returns
// every listed whole episode's variants, newest first, each labeled with its
// own episode number (so MAL write-back and client-side ep filtering stay
// correct). Fractional specials are skipped.
func TestFetchReleasesAllEpisodes(t *testing.T) {
	hits, cleanup := anidbServer(t, []Episode{
		{ID: 11, Number: 1},
		{ID: 12, Number: 2},
		{ID: 99, Number: 2.5}, // fractional special: not fetchable by episode input
		{ID: 13, Number: 3},
	})
	defer cleanup()

	rels, err := FetchReleases("show-1", 0)
	if err != nil {
		t.Fatalf("FetchReleases(0): %v", err)
	}
	if len(rels) != 3 {
		t.Fatalf("expected 3 releases (one per whole episode), got %d", len(rels))
	}
	for i, want := range []int{3, 2, 1} { // newest episode first
		if rels[i].Episode != want {
			t.Errorf("rels[%d].Episode = %d, want %d (desc order)", i, rels[i].Episode, want)
		}
	}
	if n := atomic.LoadInt32(hits); n != 3 {
		t.Fatalf("languages endpoint hit %d times, want 3 (one per whole episode)", n)
	}
}

// TestFetchReleasesAllCapsEpisodes: the all-episodes fetch keeps only the
// newest allEpisodesCap episodes of long series (each episode costs ~3 GETs).
func TestFetchReleasesAllCapsEpisodes(t *testing.T) {
	eps := make([]Episode, 120)
	for i := range eps {
		eps[i] = Episode{ID: 1000 + i, Number: float64(i + 1)}
	}
	hits, cleanup := anidbServer(t, eps)
	defer cleanup()

	rels, err := FetchReleases("show-1", 0)
	if err != nil {
		t.Fatalf("FetchReleases(0): %v", err)
	}
	if len(rels) != allEpisodesCap {
		t.Fatalf("expected %d releases, got %d", allEpisodesCap, len(rels))
	}
	if rels[0].Episode != 120 {
		t.Errorf("first row Episode = %d, want 120 (newest kept)", rels[0].Episode)
	}
	if rels[len(rels)-1].Episode != 21 {
		t.Errorf("last row Episode = %d, want 21 (oldest kept after cap)", rels[len(rels)-1].Episode)
	}
	if n := atomic.LoadInt32(hits); n != allEpisodesCap {
		t.Fatalf("languages endpoint hit %d times, want %d (cap respected)", n, allEpisodesCap)
	}
}

// TestFetchReleasesResolutionSortNumeric: variants rank by numeric height —
// lexicographic order would put "720p" above "1080p".
func TestFetchReleasesResolutionSortNumeric(t *testing.T) {
	eps := []Episode{{ID: 11, Number: 1}}
	_, cleanup := anidbServerWithVariants(t, eps, []string{"360", "1080", "720"})
	defer cleanup()

	for _, ep := range []int{1, 0} { // single-episode and all-episodes paths
		rels, err := FetchReleases("show-1", ep)
		if err != nil {
			t.Fatalf("FetchReleases(%d): %v", ep, err)
		}
		if len(rels) != 3 {
			t.Fatalf("ep %d: expected 3 variants, got %d", ep, len(rels))
		}
		want := []string{"1080p", "720p", "360p"}
		for i, w := range want {
			if rels[i].Resolution != w {
				t.Errorf("ep %d: rels[%d].Resolution = %q, want %q", ep, i, rels[i].Resolution, w)
			}
		}
	}
}
