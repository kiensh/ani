package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ani/internal/animetosho"
	"ani/internal/mal"
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
	if got := latestEpisodePrefetchFn(&Options{})(nil); got != 0 {
		t.Errorf("prefetch(nil) = %d, want 0", got)
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

	got := latestEpisodePrefetchFn(&Options{})(&mal.Item{AnidbAID: 12345})
	if got != 7 {
		t.Errorf("prefetch(resolvable aid) = %d, want 7", got)
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

	got := latestEpisodePrefetchFn(&Options{})(&mal.Item{AnidbAID: 12345})
	if got != 0 {
		t.Errorf("prefetch(empty feed) = %d, want 0 (no Jikan fallback in prefetch)", got)
	}
}
