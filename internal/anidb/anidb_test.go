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
			w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080\n" +
				srv.URL + "/v1080.m3u8\n"))
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
