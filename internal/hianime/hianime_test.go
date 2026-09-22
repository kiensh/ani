package hianime

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ani/internal/hostgate"
)

// encodeBlob builds a window.__P blob: base64(json XOR embedKey).
func encodeBlob(payload string) string {
	raw := []byte(payload)
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = b ^ embedKey[i%len(embedKey)]
	}
	return base64.StdEncoding.EncodeToString(out)
}

// TestMain keeps the site-gate pace at zero for the fakes: one test host
// serves every endpoint, so the production 200ms spacing would make the
// multi-request tests (the all-episodes cap test alone does ~300 requests)
// crawl. The pacing itself is exercised with a real interval in
// TestSiteGatePacesRequests.
func TestMain(m *testing.M) {
	siteGate.Pace = 0
	os.Exit(m.Run())
}

// TestSiteGatePacesRequests: requests to the site host start no closer than
// the gate's pace apart (hianime.at answers bursts with 429; sequential
// traffic is accepted). Stream-host URLs (a different host) skip the gate.
func TestSiteGatePacesRequests(t *testing.T) {
	var hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()
	oldPace := siteGate.Pace
	siteGate.Pace = 40 * time.Millisecond
	defer func() { siteGate.Pace = oldPace }()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, _, err := get(srv.URL+"/x", ""); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if d := time.Since(start); d < 2*siteGate.Pace {
		t.Errorf("3 site requests finished in %v, want >= %v (spaced starts)", d, 2*siteGate.Pace)
	}

	// A different host is not the site host: ungated, no spacing.
	start = time.Now()
	for i := 0; i < 3; i++ {
		u := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/x"
		if _, _, err := get(u, ""); err != nil {
			t.Fatalf("off-site request %d: %v", i, err)
		}
	}
	if d := time.Since(start); d >= siteGate.Pace {
		t.Errorf("3 off-site requests took %v — the gate must not apply to other hosts", d)
	}
}

// TestRateLimitCooldown: a 429 trips a cooldown — the failing request surfaces
// hostgate.ErrLimited, further requests fail fast WITHOUT hitting the server,
// and once the cooldown passes traffic resumes.
func TestRateLimitCooldown(t *testing.T) {
	var hits int32
	var limit int32 = 1 // the first request is answered 429
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= atomic.LoadInt32(&limit) {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()
	oldPace, oldCool := siteGate.Pace, siteGate.Cooldown
	siteGate.Pace, siteGate.Cooldown = 0, 60*time.Millisecond
	defer func() { siteGate.Pace, siteGate.Cooldown = oldPace, oldCool }()

	// First request trips the gate.
	if _, status, err := get(srv.URL+"/x", ""); err == nil || !errors.Is(err, hostgate.ErrLimited) || status != 429 {
		t.Fatalf("429 response = (%d, %v), want (429, hostgate.ErrLimited)", status, err)
	}
	// During the cooldown, requests fail fast without reaching the server.
	n := atomic.LoadInt32(&hits)
	for i := 0; i < 3; i++ {
		if _, _, err := get(srv.URL+"/x", ""); !errors.Is(err, hostgate.ErrLimited) {
			t.Fatalf("cooldown request %d = %v, want ErrLimited", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != n {
		t.Fatalf("server hit during cooldown (%d → %d) — requests must fail fast", n, got)
	}
	// After the cooldown, traffic resumes.
	time.Sleep(70 * time.Millisecond)
	if _, _, err := get(srv.URL+"/x", ""); err != nil {
		t.Fatalf("request after cooldown: %v", err)
	}
}

// fakeHianime is a stand-in hianime.at: /search returns the given result rows
// (plus a sidebar that repeats the first row — the dupe must be stripped),
// /api/theme/episode/list/<num> lists the given episodes, and each episode
// resolves ZokoAnime sub (+dub) embeds → config blob → a master playlist whose
// variant URIs are RELATIVE (exercising the join) and which 403s unless the
// request carries the embed origin as Referer. serversHits counts requests to
// the servers endpoint (a missing episode must not reach it). Returns a
// cleanup that restores baseURL.
func fakeHianime(t *testing.T, episodes []Episode, heights []string, withDub bool) (*int32, func()) {
	t.Helper()
	var serversHits int32
	var badReferer int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch {
		case r.URL.Path == "/search":
			var b strings.Builder
			b.WriteString(`<h3 class="film-name"> <a href="` + srv.URL + `/watch/show-1" title="Show One">`)
			b.WriteString(`<div id="main-sidebar">`)
			b.WriteString(`<h3 class="film-name"> <a href="` + srv.URL + `/watch/other-2" title="Sidebar Dupe">`)
			w.Write([]byte(b.String()))

		case strings.HasPrefix(r.URL.Path, "/api/theme/episode/list/"):
			var b strings.Builder
			for _, e := range episodes {
				// Attributes on their own lines, like the real fragment.
				fmt.Fprintf(&b, "<div class=\"item ep-item\" data-number=\"%g\"\n    data-id=\"%d\">\n", e.Number, e.ID)
			}
			writeFragment(w, b.String())

		case r.URL.Path == "/api/theme/episode/servers":
			atomic.AddInt32(&serversHits, 1)
			subHash := base64.StdEncoding.EncodeToString([]byte(srv.URL + "/embed/sub"))
			var b strings.Builder
			fmt.Fprintf(&b, "<div class=\"item server-item\" data-type=\"sub\"\n    data-server-name=\"ZokoAnime\"\n    data-hash=\"%s\">\n", subHash)
			fmt.Fprintf(&b, "<div class=\"item server-item\" data-type=\"sub\"\n    data-server-name=\"HD-1\"\n    data-hash=\"ignored\">\n")
			if withDub {
				dubHash := base64.StdEncoding.EncodeToString([]byte(srv.URL + "/embed/dub"))
				fmt.Fprintf(&b, "<div class=\"item server-item\" data-type=\"dub\"\n    data-server-name=\"ZokoAnime\"\n    data-hash=\"%s\">\n", dubHash)
			}
			writeFragment(w, b.String())

		case r.URL.Path == "/embed/sub" || r.URL.Path == "/embed/dub":
			cfg := map[string]any{
				"src": srv.URL + "/master.m3u8",
				"subtitles": []map[string]any{
					{"label": "English", "default": r.URL.Path == "/embed/sub", "src": srv.URL + "/en.vtt"},
					{"label": "Spanish", "default": false, "src": srv.URL + "/es.vtt"},
				},
			}
			payload, _ := json.Marshal(cfg)
			fmt.Fprintf(w, "<script>window.__P=%q;window.__Q=%q</script>", encodeBlob(string(payload)), encodeBlob(`{"src":"decoy"}`))

		case r.URL.Path == "/master.m3u8":
			// The stream host enforces the embed origin as referer (403 else).
			if r.Header.Get("Referer") != srv.URL+"/" {
				atomic.AddInt32(&badReferer, 1)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			var b strings.Builder
			b.WriteString("#EXTM3U\n")
			for _, h := range heights {
				fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=0x%s\n%s/index.m3u8\n", h, h)
			}
			w.Write([]byte(b.String()))

		default:
			http.NotFound(w, r)
		}
	}))
	old := baseURL
	baseURL = srv.URL
	t.Cleanup(func() {
		srv.Close()
		baseURL = old
		if n := atomic.LoadInt32(&badReferer); n != 0 {
			t.Errorf("master playlist fetched %d times without the embed-origin referer (would 403 live)", n)
		}
	})
	return &serversHits, func() {}
}

// writeFragment emits the {"status","html"} JSON envelope the episode-list and
// servers endpoints answer with.
func writeFragment(w http.ResponseWriter, html string) {
	b, _ := json.Marshal(htmlFragment{Status: true, HTML: html})
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// TestSearchSidebarStripped: the top-10 sidebar repeats the result markup;
// entries after it must not duplicate (or replace) the real results.
func TestSearchSidebarStripped(t *testing.T) {
	_, cleanup := fakeHianime(t, nil, nil, false)
	defer cleanup()

	shows, err := Search("show")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(shows) != 1 {
		t.Fatalf("got %d shows, want 1 (sidebar dupe stripped): %+v", len(shows), shows)
	}
	if shows[0].ID != "show-1" || shows[0].Name != "Show One" {
		t.Errorf("show = %+v, want slug show-1 / Show One", shows[0])
	}
}

// TestFetchReleasesMissingEpisodeEmpty: an episode hianime doesn't list yields
// no releases (no silent substitute) and never touches the servers endpoint —
// otherwise the picker would show another episode's streams mislabeled, and
// MAL write-back would record the wrong watched count.
func TestFetchReleasesMissingEpisodeEmpty(t *testing.T) {
	hits, cleanup := fakeHianime(t, []Episode{
		{ID: 11, Number: 1},
		{ID: 12, Number: 2},
		{ID: 13, Number: 3},
	}, []string{"1080"}, false)
	defer cleanup()

	rels, err := FetchReleases("show-1", 5)
	if err != nil {
		t.Fatalf("FetchReleases(5): %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("expected no releases for missing episode, got %d", len(rels))
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("servers endpoint hit %d times; a missing episode must not fetch another episode's streams", n)
	}
}

// TestFetchReleasesEpisodeVariants: one episode resolves its sub/dub ×
// resolution ladder with per-release Referer (the embed origin) and the
// default subtitle track, sorted sub-first then height-descending, and variant
// URIs joined against the master playlist's directory.
func TestFetchReleasesEpisodeVariants(t *testing.T) {
	_, cleanup := fakeHianime(t, []Episode{{ID: 11, Number: 1}}, []string{"360", "1080", "720"}, true)
	defer cleanup()

	rels, err := FetchReleases("show-1", 1)
	if err != nil {
		t.Fatalf("FetchReleases(1): %v", err)
	}
	want := []struct{ group, res string }{
		{"sub", "1080p"}, {"sub", "720p"}, {"sub", "360p"},
		{"dub", "1080p"}, {"dub", "720p"}, {"dub", "360p"},
	}
	if len(rels) != len(want) {
		t.Fatalf("got %d releases, want %d: %+v", len(rels), len(want), rels)
	}
	for i, w := range want {
		r := rels[i]
		if r.Group != w.group || r.Resolution != w.res {
			t.Errorf("rels[%d] = %s/%s, want %s/%s", i, r.Group, r.Resolution, w.group, w.res)
		}
		if r.Episode != 1 {
			t.Errorf("rels[%d].Episode = %d, want 1", i, r.Episode)
		}
		if dir := strings.TrimSuffix(w.res, "p"); !strings.HasSuffix(r.StreamURL, "/"+dir+"/index.m3u8") {
			t.Errorf("rels[%d].StreamURL = %q, want the relative variant joined to the master dir", i, r.StreamURL)
		}
		if !strings.HasPrefix(r.StreamURL, "http") {
			t.Errorf("rels[%d].StreamURL = %q, want absolute", i, r.StreamURL)
		}
		if r.Referer == "" {
			t.Errorf("rels[%d].Referer empty — mpv would get a 403 from the stream host", i)
		}
		// The English subtitle belongs to the sub embed only.
		if r.Group == "sub" && r.SubtitleURL == "" {
			t.Errorf("sub row has no SubtitleURL (the default English track)")
		}
		if r.Group == "dub" && r.SubtitleURL != "" {
			t.Errorf("dub row carries SubtitleURL %q — subtitles belong to the sub embed", r.SubtitleURL)
		}
	}
}

// TestFetchReleasesAllEpisodes: episode 0 (the picker's "all" filter) returns
// every listed whole episode's variants, newest first, each labeled with its
// own episode number (so MAL write-back and client-side ep filtering stay
// correct). Fractional specials are skipped.
func TestFetchReleasesAllEpisodes(t *testing.T) {
	hits, cleanup := fakeHianime(t, []Episode{
		{ID: 11, Number: 1},
		{ID: 12, Number: 2},
		{ID: 99, Number: 2.5}, // fractional special: not fetchable by episode input
		{ID: 13, Number: 3},
	}, []string{"1080"}, false)
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
		t.Fatalf("servers endpoint hit %d times, want 3 (one per whole episode)", n)
	}
}

// TestFetchReleasesAllCapsEpisodes: the all-episodes fetch keeps only the
// newest allEpisodesCap episodes of long series (each episode costs ~3 GETs).
func TestFetchReleasesAllCapsEpisodes(t *testing.T) {
	eps := make([]Episode, 120)
	for i := range eps {
		eps[i] = Episode{ID: 1000 + i, Number: float64(i + 1)}
	}
	hits, cleanup := fakeHianime(t, eps, []string{"1080"}, false)
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
		t.Fatalf("servers endpoint hit %d times, want %d (cap respected)", n, allEpisodesCap)
	}
}

// TestAiredCount: a fetch failure (HTTP != 200 on either request) must surface
// as an error — callers cache answers, and an outage returned as 0 pinned every
// aired count to "?" for the rest of the session. "No show on hianime" and
// "no episodes" are real (0, nil) answers, safe to cache. hianime numbers
// per-season, so the answer needs no offset: eps 1–16 → 16.
func TestAiredCount(t *testing.T) {
	searchStatus, searchBody := http.StatusOK, "ok"
	epsStatus, epsBody := http.StatusOK, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			w.WriteHeader(searchStatus)
			if searchStatus == http.StatusOK {
				w.Write([]byte(searchBody))
			}
		case strings.HasPrefix(r.URL.Path, "/api/theme/episode/list/"):
			w.WriteHeader(epsStatus)
			if epsStatus == http.StatusOK {
				writeFragment(w, epsBody)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()

	const row = `<h3 class="film-name"> <a href="` + "https://x/watch/slime-1663" + `" title="Slime">`
	epsFragment := `<div class="item ep-item" data-number="1" data-id="1">` +
		`<div class="item ep-item" data-number="16" data-id="16">`
	setOK := func() {
		searchStatus, searchBody = http.StatusOK, row
		epsStatus, epsBody = http.StatusOK, epsFragment
	}
	setOK()

	// Success: max listed episode, in MAL per-season terms already.
	if n, err := AiredCount("Slime"); err != nil || n != 16 {
		t.Fatalf("AiredCount = %v, %v; want 16, <nil>", n, err)
	}

	// Site down (search answers 503): an error, not a 0-as-answer.
	searchStatus = http.StatusServiceUnavailable
	if n, err := AiredCount("Slime"); err == nil || n != 0 {
		t.Fatalf("AiredCount on 503 = %v, %v; want 0, <error>", n, err)
	}
	setOK()

	// Search 200s but the show isn't on hianime: a real (0, nil) answer.
	searchBody = ""
	if n, err := AiredCount("Slime"); err != nil || n != 0 {
		t.Fatalf("AiredCount no-show = %v, %v; want 0, <nil>", n, err)
	}
	setOK()

	// Episodes endpoint errors: an error.
	epsStatus = http.StatusNotFound
	if n, err := AiredCount("Slime"); err == nil || n != 0 {
		t.Fatalf("AiredCount on episodes 404 = %v, %v; want 0, <error>", n, err)
	}
	setOK()

	// Show exists but lists no episodes: a real (0, nil) answer.
	epsBody = ""
	if n, err := AiredCount("Slime"); err != nil || n != 0 {
		t.Fatalf("AiredCount empty episodes = %v, %v; want 0, <nil>", n, err)
	}
}

// TestCloudflareBlocked: a challenge page (200 + "Just a moment") is an error,
// not an empty success — the site being up but refusing the client must reach
// the down warning.
func TestCloudflareBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>Just a moment...</title></head></html>`))
	}))
	defer srv.Close()
	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()

	if _, err := Search("x"); err == nil || !strings.Contains(err.Error(), "cloudflare") {
		t.Fatalf("Search through a challenge page = %v, want a cloudflare error", err)
	}
	if err := Ping(); err == nil || !strings.Contains(err.Error(), "cloudflare") {
		t.Fatalf("Ping through a challenge page = %v, want a cloudflare error", err)
	}
}

// TestDecodeEmbedBlob: the blob decode is the base64 + XOR("otaku-embed-v1")
// round trip the ZokoAnime embed ships.
func TestDecodeEmbedBlob(t *testing.T) {
	payload := `{"src":"https://x/master.m3u8","subtitles":[]}`
	out, err := decodeEmbedBlob(encodeBlob(payload))
	if err != nil {
		t.Fatalf("decodeEmbedBlob: %v", err)
	}
	if string(out) != payload {
		t.Fatalf("round trip = %q, want %q", out, payload)
	}
	if _, err := decodeEmbedBlob("!!!not base64!!!"); err == nil {
		t.Fatal("garbage blob decoded without error")
	}
}

// TestLiveFetchReleases is a manual live smoke against the real site (the
// recorded shapes drift silently; this catches it). Skipped unless
// HIANIME_LIVE=1 — run with:
//
//	HIANIME_LIVE=1 go test ./internal/hianime -run TestLive -v
func TestLiveFetchReleases(t *testing.T) {
	if os.Getenv("HIANIME_LIVE") == "" {
		t.Skip("set HIANIME_LIVE=1 to hit the real site")
	}
	for _, title := range []string{
		"Frieren",
		"Re:ZERO -Starting Life in Another World- Season 3",                  // "-Word" fragments (search exclusion operator)
		"That Time I Got Reincarnated as a Slime Season 3",                   // plain English, no punctuation
		"Slime Taoshite 300-nen, Shiranai Uchi ni Level Max ni Nattemashita", // commas + hyphen inside a word
	} {
		show, err := ResolveShow(title)
		if err != nil {
			t.Errorf("ResolveShow(%q): %v", title, err)
			continue
		}
		t.Logf("ResolveShow(%q) -> %s (%s)", title, show.Name, show.ID)
		eps, err := Episodes(show.ID)
		if err != nil {
			t.Errorf("Episodes(%s): %v", show.ID, err)
			continue
		}
		if len(eps) == 0 {
			t.Errorf("Episodes(%s): none", show.ID)
			continue
		}
		n, err := AiredCount(title)
		if err != nil {
			t.Errorf("AiredCount(%q): %v", title, err)
			continue
		}
		t.Logf("aired: %g over %d listed eps", n, len(eps))
		rels, err := FetchReleases(show.ID, 1)
		if err != nil {
			t.Errorf("FetchReleases(%s, 1): %v", show.ID, err)
			continue
		}
		if len(rels) == 0 {
			t.Errorf("FetchReleases(%s, 1): no variants", show.ID)
			continue
		}
		for _, r := range rels {
			t.Logf("  %s %s %s (referer set: %v, subs: %v)", r.Group, r.Resolution, r.StreamURL, r.Referer != "", r.SubtitleURL != "")
		}
	}
}
