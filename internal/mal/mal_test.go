package mal

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withJikanVars points jikanBaseURL at a test server and zeroes the retry
// backoff so transient-retry tests are instant. Returns a cleanup func.
func withJikanVars(t *testing.T, baseURL string) func() {
	t.Helper()
	oldBase, oldBackoff := jikanBaseURL, jikanRetryBackoff
	jikanBaseURL = baseURL
	jikanRetryBackoff = 0
	return func() {
		jikanBaseURL = oldBase
		jikanRetryBackoff = oldBackoff
	}
}

// jikanEpisodesData is a tiny decode target for the jikanGet retry tests.
type jikanTestData struct {
	Data []struct {
		MalID int    `json:"mal_id"`
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"data"`
}

func TestJikanGetRetriesTransientThenSucceeds(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			http.Error(w, "504 Gateway Time-out", http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"mal_id":42}]}`)
	}))
	defer srv.Close()
	defer withJikanVars(t, srv.URL)()

	var got jikanTestData
	if err := jikanGet(srv.URL+"/x", &got); err != nil {
		t.Fatalf("jikanGet: unexpected error: %v", err)
	}
	if hits != 3 {
		t.Errorf("server hits = %d, want 3 (2 retries after 504s)", hits)
	}
	if len(got.Data) != 1 || got.Data[0].MalID != 42 {
		t.Errorf("decoded = %+v, want mal_id 42", got.Data)
	}
}

func TestJikanGetNoRetryOn4xx(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	defer withJikanVars(t, srv.URL)()

	var got jikanTestData
	if err := jikanGet(srv.URL+"/x", &got); err == nil {
		t.Fatal("jikanGet: want error for 404, got nil")
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1 (4xx must not retry)", hits)
	}
}

func TestAnidbAIDRetriesPastTransient504(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			http.Error(w, "504", http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"name":"AniDB","url":"https://anidb.net/anime/12345"}]}`)
	}))
	defer srv.Close()
	defer withJikanVars(t, srv.URL)()

	aid, err := AnidbAID(999, false)
	if err != nil {
		t.Fatalf("AnidbAID: unexpected error after retries: %v", err)
	}
	if aid != 12345 {
		t.Errorf("AnidbAID = %d, want 12345", aid)
	}
}
