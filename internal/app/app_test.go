package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ani/internal/animetosho"
	"ani/internal/mal"
	"ani/internal/tui"
)

// toshoEpBody builds a minimal seriesDetailResponse where `ep` is released by the
// given groups (same day), so LatestEpisode returns ep.
func toshoEpBody(ep int, groups []string) string {
	b := `{"data":{"releases":[`
	for i, g := range groups {
		if i > 0 {
			b += ","
		}
		b += fmt.Sprintf(`{"release_group":%q,"date_added":"2026-07-12T00:00:00Z","series":{"episode_number":%d}}`, g, ep)
	}
	return b + `]}}`
}

// TestLatestEpisodePrefetchNilItem: a nil item short-circuits to 0.
func TestLatestEpisodePrefetchNilItem(t *testing.T) {
	if got := latestEpisodePrefetchFn(&Options{}, tui.NewProviderHealth())(nil); got != 0 {
		t.Errorf("prefetch(nil) = %v, want 0", got)
	}
}

// TestCachedFetchSessionReuse: the release cache is session-scoped — a second
// fetch func for the same anime (what a re-entry into the release picker
// builds after Esc-back) serves from the cache without touching the provider;
// a different anime or episode fetches; a provider switch's Reset clears it.
func TestCachedFetchSessionReuse(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(toshoEpBody(7, []string{"Erai-raws", "SubsPlease", "ASW"})))
	}))
	defer srv.Close()
	defer animetosho.SetToshoBaseForTest(srv.URL)()

	cache := newReleaseCache()
	health := tui.NewProviderHealth()
	fetch := cachedFetch(123, cache, health)

	if rels := fetch(7); len(rels) == 0 {
		t.Fatal("first fetch(7): no releases")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("provider hits after first fetch = %d, want 1", n)
	}

	// The "re-entry" — a fresh closure over the same session cache.
	if rels := cachedFetch(123, cache, health)(7); len(rels) == 0 {
		t.Fatal("re-entry fetch(7): no releases")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("re-entry hit the provider (%d hits, want still 1 — cached)", n)
	}

	// A different episode is a different key.
	cachedFetch(123, cache, health)(8)
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("fetch(8) hits = %d, want 2 (new episode key)", n)
	}

	// A provider switch resets: the next fetch re-downloads.
	cache.Reset()
	cachedFetch(123, cache, health)(7)
	if n := atomic.LoadInt32(&hits); n != 3 {
		t.Fatalf("post-Reset fetch(7) hits = %d, want 3 (cache cleared)", n)
	}
}

// TestCachedFetchErrorNotCached: a failed fetch (provider error) returns nil
// without caching — the next entry retries it instead of serving a stale empty
// list forever.
func TestCachedFetchErrorNotCached(t *testing.T) {
	fail := true
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(toshoEpBody(7, []string{"Erai-raws", "SubsPlease", "ASW"})))
	}))
	defer srv.Close()
	defer animetosho.SetToshoBaseForTest(srv.URL)()

	cache := newReleaseCache()
	health := tui.NewProviderHealth()
	fetch := cachedFetch(123, cache, health)

	if rels := fetch(7); rels != nil {
		t.Fatalf("fetch during outage = %d releases, want nil", len(rels))
	}
	if !health.IsDown("torrent") {
		t.Error("torrent not marked down after a fetch error")
	}

	// The outage passes: the next fetch (not a cached nil) recovers.
	fail = false
	if rels := fetch(7); len(rels) == 0 {
		t.Fatal("fetch after recovery: no releases (nil was cached)")
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("provider hits = %d, want 2 (error was not cached)", n)
	}
}

// TestDownReason: the warning line's reason is the HTTP code when the error
// carries one — except 429, which reads as "rate-limited" (a cooldown, not an
// outage) — else the error clipped to fit one line.
func TestDownReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("hianime search: HTTP 503"), "HTTP 503"},
		{fmt.Errorf("get: connection refused"), "get: connection refused"},
		{fmt.Errorf("%s: rate limited (HTTP 429)", "https://hianime.at/search"), "rate-limited"},
	}
	for _, c := range cases {
		if got := downReason(c.err); got != c.want {
			t.Errorf("downReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	long := strings.Repeat("x", 80)
	if got, want := downReason(errors.New(long)), long[:48]+"…"; got != want {
		t.Errorf("downReason(long) = %q, want %q", got, want)
	}
}

// TestLatestEpisodePrefetchResolvable: with the aid resolvable (item.AnidbAID
// short-circuits resolveAidFast), the prefetch returns AnimeTosho's latest episode.
func TestLatestEpisodePrefetchResolvable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(toshoEpBody(7, []string{"A", "B", "C"})))
	}))
	defer srv.Close()
	defer animetosho.SetToshoBaseForTest(srv.URL)()

	got := latestEpisodePrefetchFn(&Options{}, tui.NewProviderHealth())(&mal.Item{AnidbAID: 12345})
	if got != 7 {
		t.Errorf("prefetch(resolvable aid) = %v, want 7", got)
	}
}

// TestLatestEpisodePrefetchEmptyNoFallback: when the feed is empty, the prefetch
// returns 0 — it must NOT fall back to Jikan (unlike the full latestEpisodeFn).
func TestLatestEpisodePrefetchEmptyNoFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"releases":[]}}`))
	}))
	defer srv.Close()
	defer animetosho.SetToshoBaseForTest(srv.URL)()

	got := latestEpisodePrefetchFn(&Options{}, tui.NewProviderHealth())(&mal.Item{AnidbAID: 12345})
	if got != 0 {
		t.Errorf("prefetch(empty feed) = %v, want 0 (no Jikan fallback in prefetch)", got)
	}
}

// TestLatestEpisodePrefetchFetchError: a fetch failure (the feed answering 429
// during a prefetch burst) must surface as AiredFailed — unknown and retried —
// and mark the provider down for the warning line. Returning 0 here would get
// cached as a real "no episodes yet" and pin the display to "?" all session
// (the bar counted it done), which is exactly what the animetosho 429s did.
func TestLatestEpisodePrefetchFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	defer animetosho.SetToshoBaseForTest(srv.URL)()

	health := tui.NewProviderHealth()
	got := latestEpisodePrefetchFn(&Options{}, health)(&mal.Item{AnidbAID: 12345})
	if got != tui.AiredFailed {
		t.Errorf("prefetch(429) = %v, want AiredFailed (retry later, don't cache)", got)
	}
	if !health.IsDown("torrent") {
		t.Error("torrent not marked down after a 429 — the warning line stays silent")
	}
	if w := health.Warning("torrent"); !strings.Contains(w, "rate-limited") {
		t.Errorf("Warning(torrent) = %q, want the rate-limited line", w)
	}
}
