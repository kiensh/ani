package trakt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type libCall struct {
	method, path, body, reply string
	status                    int
	page, pageCount           string
}

func libGet(path, reply string) libCall { return libCall{method: "GET", path: path, reply: reply} }
func libPost(path, body, reply string) libCall {
	return libCall{method: "POST", path: path, body: body, reply: reply, status: 201}
}

// Only the scripted requests are allowed. In particular, duplicates, rejected
// targets, and inconclusive history checks must not produce a POST.
func libraryClient(t *testing.T, calls ...libCall) *Client {
	t.Helper()
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(count.Add(1)) - 1
		if i >= len(calls) {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		call := calls[i]
		want, err := url.ParseRequestURI(call.path)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if r.Method != call.method || r.URL.Path != want.Path || !reflect.DeepEqual(r.URL.Query(), want.Query()) {
			t.Errorf("request %d: got %s %s, want %s %s", i, r.Method, r.URL, call.method, call.path)
		}
		auth := ""
		if strings.HasPrefix(want.Path, "/users/me/") || strings.HasPrefix(want.Path, "/sync/") {
			auth = "Bearer test-access"
		}
		if r.Header.Get("trakt-api-version") != "2" || r.Header.Get("trakt-api-key") != "test-client" {
			t.Error("missing Trakt API headers on", r.URL.Path)
		}
		if r.Header.Get("Authorization") != auth {
			t.Error("incorrect authorization on", r.URL.Path)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		} else if call.body == "" && len(b) != 0 {
			t.Error("unexpected request body")
		} else if call.body != "" {
			var gotJSON, wantJSON any
			if json.Unmarshal(b, &gotJSON) != nil || json.Unmarshal([]byte(call.body), &wantJSON) != nil || !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Errorf("body = %s; want %s", b, call.body)
			}
		}
		if call.page != "" {
			w.Header().Set("X-Pagination-Page", call.page)
		}
		if call.pageCount != "" {
			w.Header().Set("X-Pagination-Page-Count", call.pageCount)
		}
		if call.status != 0 {
			w.WriteHeader(call.status)
		}
		fmt.Fprint(w, call.reply)
	}))
	t.Cleanup(func() {
		s.Close()
		if n := int(count.Load()); n != len(calls) {
			t.Errorf("got %d requests, want %d", n, len(calls))
		}
	})
	p := filepath.Join(t.TempDir(), "token.json")
	fixture := fmt.Sprintf(`{"access_token":"test-access","token_type":"bearer","expires_in":86400,"created_at":%d,"refresh_token":"test-refresh"}`, time.Now().Unix())
	if err := os.WriteFile(p, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	return newClient("test-client", s.URL, p, s.Client())
}

func TestLibrarySearchPreservesSameNameCandidates(t *testing.T) {
	// Same title, different Trakt IDs: retain both for the UI to disambiguate.
	name := "A/B & C?"
	base := "/search/show"
	exact := `{"type":"show","show":{"title":"A/B & C?","year":1960,"ids":{"trakt":12}}}`
	broad := `{"type":"show","show":{"title":"A/B &\n‮C?","year":2019,"ids":{"trakt":20}}}`
	otherKind := `{"type":"movie","movie":{"title":"A/B & C?","ids":{"trakt":99}}}`
	c := libraryClient(t,
		libGet(base+"/exact?extended=full&page=1&limit=50&query="+url.QueryEscape(name), "["+exact+"]"),
		libGet(base+"?extended=full&page=1&limit=50&query="+url.QueryEscape(`A\/B \& C\?`), "["+exact+","+broad+","+otherKind+",{}]"))
	got, err := c.Search(t.Context(), "series", name)
	want := []Title{{Name: name, Kind: "series", Year: 1960, ID: 12}, {Name: name, Kind: "series", Year: 2019, ID: 20}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Search = %+v, %v; want %+v", got, err, want)
	}
}

func TestLibraryRejectsUnconfirmedTargets(t *testing.T) {
	c := libraryClient(t)
	ctx := t.Context()
	if _, err := c.Search(ctx, "show", "x"); err == nil {
		t.Error("accepted unsupported kind")
	}
	if _, err := c.Search(ctx, "movie", " \t"); err == nil {
		t.Error("accepted empty title")
	}
	if err := c.AddWatchlist(ctx, Title{Name: "Not an identifier", Kind: "movie"}); err == nil {
		t.Error("accepted bare watchlist title")
	}
	for _, tc := range []struct {
		title           Title
		season, episode int
	}{
		{Title{Kind: "movie", ID: 1}, 1, 1},
		{Title{Kind: "series", ID: 1}, 0, 0},
		{Title{Kind: "series", ID: 1}, 2, 0},
		{Title{Kind: "series", ID: 1}, -1, 2},
		{Title{Kind: "whatever", ID: 1}, 1, 1},
		{Title{Kind: "movie"}, 0, 0},
	} {
		if err := c.MarkWatched(ctx, tc.title, tc.season, tc.episode); err == nil {
			t.Errorf("accepted ambiguous history target: %+v", tc)
		}
	}
}

func TestLibraryWatchlistAcknowledgments(t *testing.T) {
	for _, tc := range []struct {
		label, kind, body, reply string
		status                   int
		wantErr                  string
	}{
		{"movie added", "movie", `{"movies":[{"ids":{"trakt":1}}]}`, `{"added":{"movies":1}}`, 201, ""},
		{"series already listed", "series", `{"shows":[{"ids":{"trakt":1}}]}`, `{"existing":{"shows":1}}`, 201, ""},
		{"not found wins", "series", `{"shows":[{"ids":{"trakt":1}}]}`, `{"added":{"shows":1},"not_found":{"episodes":[{}]}}`, 201, "not found"},
		{"wrong object", "movie", `{"movies":[{"ids":{"trakt":1}}]}`, `{"added":{"shows":1}}`, 201, "confirm"},
		{"empty", "movie", `{"movies":[{"ids":{"trakt":1}}]}`, `{}`, 200, "confirm"},
		{"no content", "movie", `{"movies":[{"ids":{"trakt":1}}]}`, ``, 204, "confirm"},
		{"account limit", "movie", `{"movies":[{"ids":{"trakt":1}}]}`, ``, 420, "limit"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			post := libPost("/sync/watchlist", tc.body, tc.reply)
			post.status = tc.status
			err := libraryClient(t, post).AddWatchlist(t.Context(), Title{Kind: tc.kind, ID: 1})
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

const (
	checkMovie  = "/users/me/history/movies/12?page=1&limit=1"
	resolveEp   = "/shows/17/seasons/0/episodes/3"
	checkEp     = "/users/me/history/episodes/71?page=1&limit=1"
	epResponse  = `{"season":0,"number":3,"ids":{"trakt":71}}`
	moviePlayed = `[{"type":"movie","movie":{"ids":{"trakt":12}}}]`
)

func TestLibraryMarkWatched(t *testing.T) {
	c := libraryClient(t,
		libGet(checkMovie, `[]`), libPost("/sync/history", `{"movies":[{"ids":{"trakt":12}}]}`, `{"added":{"movies":1}}`),
		libGet(checkMovie, moviePlayed), // The second mark must be a no-op.
		libGet(resolveEp, epResponse), libGet(checkEp, `[]`),
		libPost("/sync/history", `{"episodes":[{"ids":{"trakt":71}}]}`, `{"updated":{"episodes":1}}`))
	movie := Title{Kind: "movie", ID: 12}
	for range 2 {
		if err := c.MarkWatched(t.Context(), movie, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.MarkWatched(t.Context(), Title{Kind: "series", ID: 17}, 0, 3); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryEpisodeHistoryIsTargeted(t *testing.T) {
	c := libraryClient(t, libGet(resolveEp, epResponse), libGet(checkEp, `[{"type":"episode","episode":{"ids":{"trakt":71}}}]`))
	watched, err := c.IsWatched(t.Context(), Title{Kind: "series", ID: 17}, 0, 3)
	if err != nil || !watched {
		t.Fatalf("IsWatched = %t, %v", watched, err)
	}
}

func TestLibraryHistoryFailsClosed(t *testing.T) {
	series := Title{Kind: "series", ID: 17}
	movie := Title{Kind: "movie", ID: 12}
	for _, tc := range []struct {
		name            string
		title           Title
		season, episode int
		calls           []libCall
	}{
		{"different season", series, 0, 3, []libCall{libGet(resolveEp, `{"season":1,"number":3,"ids":{"trakt":71}}`)}},
		{"absent season", series, 0, 3, []libCall{libGet(resolveEp, `{"number":3,"ids":{"trakt":71}}`)}},
		{"absent episode ID", series, 0, 3, []libCall{libGet(resolveEp, `{"season":0,"number":3}`)}},
		{"private read failed", movie, 0, 0, []libCall{{method: "GET", path: checkMovie, status: 403}}},
		{"inconclusive read", movie, 0, 0, []libCall{libGet(checkMovie, `null`)}},
		{"wrong movie", movie, 0, 0, []libCall{libGet(checkMovie, `[{"type":"movie","movie":{"ids":{"trakt":888}}}]`)}},
		{"empty write", movie, 0, 0, []libCall{libGet(checkMovie, `[]`), libPost("/sync/history", `{"movies":[{"ids":{"trakt":12}}]}`, `{"added":{"movies":0}}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := libraryClient(t, tc.calls...).MarkWatched(t.Context(), tc.title, tc.season, tc.episode); err == nil {
				t.Fatal("expected error; must not treat inconclusive history as successful")
			}
		})
	}
}

func TestLibraryWatchlistPagination(t *testing.T) {
	const prefix = "/users/me/watchlist/movie,show/rank?extended=full&limit=100&page="
	one := libGet(prefix+"1", `[{"type":"movie","movie":{"title":"One","ids":{"trakt":7}}}]`)
	two := libGet(prefix+"2", `[{"type":"show","show":{"title":"Different show, same numeric ID","year":2021,"ids":{"trakt":7}}}]`)
	want := []Title{{Kind: "movie", ID: 7, Name: "One"}, {Kind: "series", ID: 7, Name: "Different show, same numeric ID", Year: 2021}}
	for _, hasCounts := range []bool{true, false} {
		t.Run(fmt.Sprintf("headers=%t", hasCounts), func(t *testing.T) {
			calls := []libCall{one, two}
			if hasCounts {
				calls[0].pageCount, calls[1].pageCount = "2", "2"
			} else {
				calls = append(calls, libGet(prefix+"3", `[]`))
			}
			got, err := libraryClient(t, calls...).Watchlist(t.Context())
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Watchlist = %+v, %v", got, err)
			}
		})
	}
	t.Run("wrong page header", func(t *testing.T) {
		wrong := two
		wrong.page, wrong.pageCount = "1", "2"
		if _, err := libraryClient(t, one, wrong).Watchlist(t.Context()); err == nil {
			t.Fatal("ignored mismatched page number")
		}
	})
	t.Run("failed second page", func(t *testing.T) {
		failed := two
		failed.status = 503
		got, err := libraryClient(t, one, failed).Watchlist(t.Context())
		if err == nil || got != nil {
			t.Fatalf("returned partial watchlist: %+v, %v", got, err)
		}
	})
	t.Run("repeated page", func(t *testing.T) {
		two.reply, two.status = one.reply, 200
		if _, err := libraryClient(t, one, two).Watchlist(t.Context()); err == nil {
			t.Fatal("ignored repeated page")
		}
	})
}

func TestLibraryRecentHistory(t *testing.T) {
	const rows = `[
		{"type":"episode","watched_at":"2025-02-01T01:02:03.000Z","show":{"title":"Series","ids":{"trakt":1}},"episode":{"season":0,"number":2,"ids":{"trakt":3}}},
		{"type":"movie","watched_at":"2025-01-31T01:02:03Z","movie":{"title":"Film","year":2020,"ids":{"trakt":2}}},
		{"type":"show","show":{"title":"Not a play","ids":{"trakt":5}}},
		{"type":"episode","show":null,"episode":null}
	]`
	c := libraryClient(t, libGet("/users/me/history?limit=20&page=1", rows))
	got, err := c.History(context.Background())
	want := []HistoryItem{
		{Title: Title{Kind: "series", Name: "Series", ID: 1}, Season: 0, Episode: 2, WatchedAt: time.Date(2025, 2, 1, 1, 2, 3, 0, time.UTC)},
		{Title: Title{Kind: "movie", Name: "Film", Year: 2020, ID: 2}, WatchedAt: time.Date(2025, 1, 31, 1, 2, 3, 0, time.UTC)},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("History = %+v, %v", got, err)
	}
}
