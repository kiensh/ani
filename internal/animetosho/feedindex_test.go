package animetosho

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchEpisodeFeed(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != toshoSearchPath || r.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected request: %v", r.URL)
		}
		w.Write([]byte(`{"data":[
     {"series":{"anidb_aid":1,"episode_number":7},"release_group":"A"},
     {"series":{"anidb_aid":1,"episode_number":7},"release_group":"B"},
     {"series":{"anidb_aid":1,"episode_number":7},"release_group":"C"},
     {"series":{"anidb_aid":1,"episode_number":8},"release_group":"A"},
     {"series":{"anidb_aid":1,"episode_number":8},"release_group":"A"},
     {"series":{"anidb_aid":1,"episode_number":8},"release_group":"B"},
     {"series":{"anidb_aid":1,"episode_number":8},"release_group":"C","is_batch":true},
     {"series":{"anidb_aid":1,"episode_number":8},"release_group":""},
     {"series":{"anidb_aid":2,"episode_number":99},"release_group":"A"},
     {"series":{"anidb_aid":1,"episode_number":8.5},"release_group":"C"}
   ]}`))
	}))
	defer srv.Close()
	defer SetToshoBaseForTest(srv.URL)()
	feed, err := FetchEpisodeFeed()
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := feed.Latest(1, 12); !ok || n != 7 {
		t.Errorf("count = %d, %v; want 7, true", n, ok)
	}
	for _, aid := range []int{2, 99} {
		if _, ok := feed.Latest(aid, 0); ok {
			t.Errorf("aid %d had no confirmed episode", aid)
		}
	}
	if hits != 1 {
		t.Errorf("made %d requests, want just one feed page", hits)
	}
}
