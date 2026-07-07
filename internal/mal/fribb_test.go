package mal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadAndBuildFribb(t *testing.T) {
	body := []byte(`[
		{"mal_id":1535,"anidb_id":4563,"name":"Death Note"},
		{"mal_id":62604,"anidb_id":19628,"name":"Otaku ni Yasashii Gal wa Inai!?"},
		{"mal_id":99999,"anidb_id":0},
		{"anidb_id":1234}
	]`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	prev := fribbFullURL
	fribbFullURL = srv.URL
	defer func() { fribbFullURL = prev }()

	m, err := downloadAndBuildFribb(false)
	if err != nil {
		t.Fatalf("downloadAndBuildFribb: %v", err)
	}
	if got := m["1535"]; got != 4563 {
		t.Errorf(`m["1535"] = %d, want 4563`, got)
	}
	if got := m["62604"]; got != 19628 {
		t.Errorf(`m["62604"] = %d, want 19628`, got)
	}
	if len(m) != 2 {
		t.Errorf("len(m) = %d, want 2 (entries lacking mal_id or anidb_id must be skipped)", len(m))
	}
}

// With a fresh on-disk cache, AnidbAIDViaFribb must answer from the cache and
// never touch the network.
func TestAnidbAIDViaFribb_CacheHitNoNetwork(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cache := filepath.Join(dir, "fribb-anidb.json")
	writeFribbCache(cache, map[string]int{"1535": 4563, "62604": 19628})

	restoreVars(t, srv.URL, cache)

	if aid, ok := AnidbAIDViaFribb(1535, true); !ok || aid != 4563 {
		t.Errorf("AnidbAIDViaFribb(1535) = (%d, %v), want (4563, true)", aid, ok)
	}
	if aid, ok := AnidbAIDViaFribb(62604, true); !ok || aid != 19628 {
		t.Errorf("AnidbAIDViaFribb(62604) = (%d, %v), want (19628, true)", aid, ok)
	}
	if aid, ok := AnidbAIDViaFribb(1, true); ok || aid != 0 {
		t.Errorf("AnidbAIDViaFribb(1) = (%d, %v), want (0, false)", aid, ok)
	}
	if hits != 0 {
		t.Errorf("network was hit %d time(s); fresh cache must be served offline", hits)
	}
}

// A stale cache (>7 days) triggers a rebuild from the network.
func TestAnidbAIDViaFribb_StaleRebuild(t *testing.T) {
	body, _ := json.Marshal([]struct {
		MalID   int `json:"mal_id"`
		AnidbID int `json:"anidb_id"`
	}{{1535, 4563}, {62604, 19628}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cache := filepath.Join(dir, "fribb-anidb.json")
	// Old, now-superseded cache value for 62604 to prove a rebuild happened.
	writeFribbCache(cache, map[string]int{"62604": 1})
	stale := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(cache, stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	restoreVars(t, srv.URL, cache)

	if aid, ok := AnidbAIDViaFribb(62604, false); !ok || aid != 19628 {
		t.Errorf("after rebuild AnidbAIDViaFribb(62604) = (%d, %v), want (19628, true)", aid, ok)
	}
	// New entry from the network payload should now be present too.
	if aid, ok := AnidbAIDViaFribb(1535, false); !ok || aid != 4563 {
		t.Errorf("after rebuild AnidbAIDViaFribb(1535) = (%d, %v), want (4563, true)", aid, ok)
	}
}

// If the rebuild fails (network error), a stale cache is still served.
func TestAnidbAIDViaFribb_StaleFallbackOnDownloadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cache := filepath.Join(dir, "fribb-anidb.json")
	writeFribbCache(cache, map[string]int{"1535": 4563})
	stale := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(cache, stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	restoreVars(t, srv.URL, cache)

	if aid, ok := AnidbAIDViaFribb(1535, false); !ok || aid != 4563 {
		t.Errorf("stale fallback AnidbAIDViaFribb(1535) = (%d, %v), want (4563, true)", aid, ok)
	}
}

// restoreVars points the package at a test server + cache path and reverts on
// test end so package vars don't leak between tests.
func restoreVars(t *testing.T, url, cache string) {
	t.Helper()
	prevURL, prevCache := fribbFullURL, fribbCacheFile
	fribbFullURL, fribbCacheFile = url, cache
	t.Cleanup(func() {
		fribbFullURL, fribbCacheFile = prevURL, prevCache
	})
}
