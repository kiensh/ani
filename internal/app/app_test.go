package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ani/internal/animetosho"
	"ani/internal/hianime"
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

// The bulk response can cover just a fraction of a season. In particular
// a miss must not trigger /series/<aid>, and must not look like a real 0.
func TestSeasonPrefetchRequestBudget(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/json/v1/search" {
			t.Errorf("unexpected per-show request: %v", r.URL)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"release_group":"A","series":{"anidb_aid":1,"episode_number":5}},{"release_group":"B","series":{"anidb_aid":1,"episode_number":5}},{"release_group":"C","series":{"anidb_aid":1,"episode_number":5}}]}`)
	}))
	defer srv.Close()
	defer animetosho.SetToshoBaseForTest(srv.URL)()
	items := make([]mal.Item, 90)
	for i := range items {
		items[i] = mal.Item{MalID: i + 10000, AnidbAID: i + 1, TotalEps: 12}
	}
	health := tui.NewProviderHealth()
	snapshots := newAiredSnapshots(health)
	for _, rows := range [][]mal.Item{nil, items[:3], items, items} {
		got := snapshots.prefetch(Options{})(rows)
		if len(rows) > 0 && (len(got) != 1 || got[10000] != 5) {
			t.Fatalf("counts = %v, want only the confirmed show", got)
		}
	}
	health.MarkDown("torrent", "rate-limited") // a release fetch failed after the snapshot
	snapshots.prefetch(Options{})(items)
	if !health.IsDown("torrent") {
		t.Fatal("an in-memory hit cleared a newer rate-limit warning")
	}

	if n := requests.Load(); n != 1 {
		t.Errorf("%d provider requests for the season, want 1", n)
	}
}

func TestSeasonPrefetchRateLimited(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	defer animetosho.SetToshoBaseForTest(srv.URL)()
	health := tui.NewProviderHealth()
	s := newAiredSnapshots(health)
	prefetch := s.prefetch(Options{})
	items := []mal.Item{{MalID: 99, AnidbAID: 1}}
	for range 3 {
		if got := prefetch(items); len(got) != 0 {
			t.Fatal(got)
		}
	}
	if !strings.Contains(health.Warning("torrent"), "rate-limited") || requests.Load() != 1 {
		t.Errorf("warning=%q requests=%d", health.Warning("torrent"), requests.Load())
	}
}

func TestUnlistedHiAnimeDoesNotFanOut(t *testing.T) {
	var calls atomic.Int32
	s := airedSnapshots{listing: cachedSnapshot(func() (hianime.EpisodeListing, error) { calls.Add(1); return hianime.EpisodeListing{}, nil }, time.Now)}
	lookup := s.prefetch(Options{Source: "hianime"})
	items := make([]mal.Item, 100)
	for i := range items {
		items[i] = mal.Item{MalID: i + 1, Title: fmt.Sprintf("Show %d", i)}
	}
	for range 2 {
		if got := lookup(items); len(got) != 0 {
			t.Fatal(got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d listing requests, want 1", n)
	}
}

func TestSnapshotSharesRefreshAndBacksOff(t *testing.T) {
	now := time.Now()
	n := 0
	fail := false
	load := cachedSnapshot(func() (int, error) {
		n++
		if fail {
			return 0, errors.New("unavailable")
		}
		return n, nil
	}, func() time.Time { return now })
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := load()
			if v != 1 || err != nil {
				t.Errorf("cold load %v, %v", v, err)
			}
		}()
	}
	wg.Wait()
	now = now.Add(11 * time.Minute)
	fail = true
	for range 20 {
		v, err := load()
		if v != 1 || err == nil || n != 2 {
			t.Fatalf("during outage: got %d, %v after %d requests", v, err, n)
		}
	}
	now = now.Add(2 * time.Minute)
	fail = false
	if v, err := load(); v != 3 || err != nil {
		t.Fatalf("after recovery: %d, %v", v, err)
	}
}
