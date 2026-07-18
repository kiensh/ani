package tui

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestCoverCacheStartsEmpty: a fresh cache serves nothing until Download runs.
func TestCoverCacheStartsEmpty(t *testing.T) {
	c := NewCoverCache()
	defer c.Cleanup()
	if got := c.Get("https://example.com/a.jpg"); got != "" {
		t.Errorf("Get on empty cache = %q, want \"\"", got)
	}
}

// TestCoverCacheDownloadDedupsAndCaches: Download fetches each distinct URL once
// (duplicates skipped), and Get returns a path for each afterwards.
func TestCoverCacheDownloadDedupsAndCaches(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte{0x42})
	}))
	defer srv.Close()
	c := NewCoverCache()
	defer c.Cleanup()
	a := srv.URL + "/a.jpg"
	b := srv.URL + "/b.jpg"

	msg := c.Download([]string{a, b, a})() // a listed twice → one download
	if _, ok := msg.(coverReadyMsg); !ok {
		t.Fatalf("Download returned %T, want coverReadyMsg", msg)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (a deduped)", got)
	}
	if c.Get(a) == "" || c.Get(b) == "" {
		t.Errorf("after Download: Get(a)=%q Get(b)=%q, both want non-empty", c.Get(a), c.Get(b))
	}
}

// TestCoverCacheDownloadSkipsCached: re-passing an already-cached URL doesn't
// re-download it (page 2 overlapping page 1 is safe).
func TestCoverCacheDownloadSkipsCached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte{1})
	}))
	defer srv.Close()
	c := NewCoverCache()
	defer c.Cleanup()
	u := srv.URL + "/x.jpg"

	c.Download([]string{u})()
	if hits != 1 {
		t.Fatalf("first Download: server hits = %d, want 1", hits)
	}
	c.Download([]string{u})()
	if hits != 1 {
		t.Errorf("second Download: server hits = %d, want 1 (cached URL skipped)", hits)
	}
}

// TestCoverCacheDownloadFailureEmitsReady: a failing URL doesn't abort the batch
// — the good URL still caches and coverReadyMsg is still returned.
func TestCoverCacheDownloadFailureEmitsReady(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer badSrv.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{1})
	}))
	defer okSrv.Close()

	c := NewCoverCache()
	defer c.Cleanup()
	bad := badSrv.URL + "/bad.jpg"
	good := okSrv.URL + "/good.jpg"

	msg := c.Download([]string{bad, good})()
	if _, ok := msg.(coverReadyMsg); !ok {
		t.Fatalf("Download returned %T, want coverReadyMsg despite a failure", msg)
	}
	if c.Get(good) == "" {
		t.Errorf("good URL should still cache alongside the failed one")
	}
	if c.Get(bad) != "" {
		t.Errorf("failed URL should not be cached")
	}
}
