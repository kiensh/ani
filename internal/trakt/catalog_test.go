package trakt

import (
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	guidePoster = "walter-r2.trakt.tv/images/movies/000/012/601/posters/thumb/e0d9dd35c5.jpg.webp"
	fullMovie   = `{"title":"A Film","year":2010,"ids":{"trakt":12601,"imdb":"tt1104001","tmdb":20526},"overview":"An\nexample.\u001b","rating":7.4,"runtime":125,"genres":["action","science-fiction"],"images":{"poster":["https://example.org/tracker","` + guidePoster + `"]}}`
)

func expectedFullMovie() Title {
	return Title{Name: "A Film", Kind: "movie", Year: 2010, ID: 12601, IMDb: "tt1104001", TMDB: 20526,
		Overview: "An example.", Genres: "action, science-fiction", Rating: 7.4, Runtime: 125, PosterURL: "https://" + guidePoster}
}

func TestSearchTitlesMixedKindsAndIDs(t *testing.T) {
	query := "A Film: (2000)"
	movie := `{"type":"movie","movie":` + fullMovie + `}`
	show := `{"type":"show","show":{"title":"A Film","year":2010,"ids":{"trakt":12601,"tmdb":99}}}`
	remake := `{"type":"movie","movie":{"title":"A Film","year":2021,"ids":{"trakt":526}}}`
	const path = "/search/movie,show"
	c := libraryClient(t,
		libGet(path+"/exact?extended=full&page=1&limit=50&query="+url.QueryEscape(query), "["+movie+","+show+"]"),
		libGet(path+"?extended=full&page=1&limit=50&query="+url.QueryEscape(`A Film\: \(2000\)`),
			"["+remake+","+movie+`,{"type":"movie","movie":{"title":"Has no canonical ID","ids":{"tmdb":14}}},{"type":"episode"}]`))
	// Both public requests must work even if the user never signed in.
	if err := os.Remove(c.tokenPath); err != nil {
		t.Fatal(err)
	}
	got, err := c.SearchTitles(t.Context(), query)
	want := []Title{expectedFullMovie(), {Kind: "series", Name: "A Film", Year: 2010, ID: 12601, TMDB: 99}, {Kind: "movie", Name: "A Film", Year: 2021, ID: 526}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchTitles = %+v, %v; want %+v", got, err, want)
	}
}

func TestCatalogPosterAllowlist(t *testing.T) {
	const base = "https://media.trakt.tv/images/shows/000/001/123/posters/thumb/"
	for _, p := range []string{"https://" + guidePoster, base + "a123.webp", strings.Replace(base, "media.", "walter.", 1) + "original.png.webp", "https://image.tmdb.org/t/p/w185/example.jpg"} {
		if !ValidPosterURL(p) {
			t.Errorf("rejected valid poster %q", p)
		}
	}
	for _, p := range []string{
		guidePoster, "http://" + guidePoster, "//" + guidePoster,
		strings.Replace(base, "media.trakt.tv", "media.trakt.tv.evil.test", 1) + "a.jpg",
		strings.Replace(base, "media.trakt.tv", "media.trakt.tv@evil.test", 1) + "a.jpg",
		strings.Replace(base, "media.trakt.tv", "localhost:8080", 1) + "a.jpg",
		strings.Replace(base, "media.trakt.tv", "media.trakt.tv:123", 1) + "a.jpg",
		strings.Replace(base, "/posters/", "/fanarts/", 1) + "a.jpg",
		strings.Replace(base, "/000/", "/../", 1) + "a.jpg", base + "%2e%2e%2fsecret.jpg",
		base + "a.jpg?url=file:///tmp/x", base + "a.jpg#x", base + "pic.svg", base + "x.jpg\n",
		base + strings.Repeat("x", 200) + ".webp", "https://image.tmdb.org/t/p/original/../../file.jpg",
	} {
		if ValidPosterURL(p) {
			t.Errorf("accepted unsafe poster %q", p)
		}
	}
	for _, images := range []string{`null`, `{"poster":{"full":"https://` + guidePoster + `"}}`, `{"fanart":["` + guidePoster + `"]}`, `{"poster":["http://` + guidePoster + `"]}`} {
		if got := posterURL([]byte(images)); got != "" {
			t.Errorf("invalid artwork produced %q", got)
		}
	}
}

func TestWatchlistAndFocusedDetails(t *testing.T) {
	// Trakt returns cached account rows: keep full embedded media, hydrate only
	// the known series ID that lacks artwork. Never search its ambiguous name.
	const path = "/users/me/watchlist/movie,show/rank?extended=full&limit=100&page=1"
	one := libGet(path, `[{"type":"movie","movie":`+fullMovie+`},{"type":"show","show":{"title":"A Film","year":2000,"ids":{"trakt":12601,"imdb":"tt0123456","tmdb":33}}}]`)
	one.page, one.pageCount = "1", "1"
	const tmdbPoster = "https://image.tmdb.org/t/p/w500/sample123.jpg"
	c := libraryClient(t, one, libGet("/shows/12601?extended=full", `{"title":"A Film (new title)","year":2000,"ids":{"trakt":12601,"tmdb":33},"images":{"poster":["`+tmdbPoster+`"]}}`),
		libGet("/movies/12601?extended=full", fullMovie))
	got, err := c.Watchlist(t.Context())
	want := []Title{expectedFullMovie(), {Kind: "series", Name: "A Film", Year: 2000, ID: 12601, IMDb: "tt0123456", TMDB: 33}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Watchlist = %+v, %v; want %+v", got, err, want)
	}
	s, err := c.TitleDetails(t.Context(), got[1])
	if err != nil || s.PosterURL != tmdbPoster || s.ID != 12601 || s.Kind != "series" || s.Name != "A Film (new title)" {
		t.Fatalf("focused show = %+v, %v", s, err)
	}
	m, err := c.TitleDetails(t.Context(), got[0])
	if err != nil || m != expectedFullMovie() {
		t.Fatalf("focused movie = %+v, %v", m, err)
	}
}

func TestDetailsRequiresMatchingCanonicalID(t *testing.T) {
	for _, reply := range []string{`null`, `{"title":"Same","ids":{"tmdb":10}}`, `{"title":"Same","ids":{"trakt":11}}`} {
		c := libraryClient(t, libGet("/movies/10?extended=full", reply))
		if got, err := c.TitleDetails(t.Context(), Title{Kind: "movie", Name: "Same", ID: 10}); err == nil || got != (Title{}) {
			t.Fatalf("unconfirmed details = %+v, %v", got, err)
		}
	}
	c := libraryClient(t)
	for _, input := range []Title{{Kind: "movie", TMDB: 10}, {Kind: "episode", ID: 10}} {
		if _, err := c.TitleDetails(t.Context(), input); err == nil {
			t.Errorf("accepted input: %+v", input)
		}
	}
}

func TestRecentReleaseCalendarsInUTC(t *testing.T) {
	// Local time is still the 7th, but both dates and sorting use the 8th UTC.
	now := time.Date(2025, 2, 7, 19, 30, 0, 0, time.FixedZone("example", -5*3600))
	movies := libGet("/calendars/all/movies/2025-02-02/7?extended=full", `[
		{"released":"2025-02-02","movie":`+fullMovie+`},
		{"released":"2025-02-08","movie":{"title":"Today","ids":{"trakt":42}}},
		{"released":"2025-02-03","movie":{"title":"Today","ids":{"trakt":42}}},
		{"released":"2025-02-01","movie":{"title":"Outside window","ids":{"trakt":999}}},
		{"released":"2025-02-09","movie":{"title":"Future","ids":{"trakt":999}}},
		{"movie":{"title":"Only recently edited","updated_at":"2025-02-08T00:10:00Z","ids":{"trakt":888}}}
	]`)
	shows := libGet("/calendars/all/shows/new/2025-02-02/7?extended=full", `[
		{"first_aired":"2025-02-08T00:10:00Z","episode":{"season":1,"number":1},"show":{"title":"Premiere","ids":{"trakt":42}}},
		{"first_aired":"2025-02-08T00:20:00Z","episode":{"season":1,"number":2},"show":{"title":"Not a new show","ids":{"trakt":111}}},
		{"first_aired":"2025-02-08T00:31:00Z","episode":{"season":1,"number":1},"show":{"title":"Later today","ids":{"trakt":222}}}
	]`)
	got, err := libraryClient(t, movies, shows).recentReleases(t.Context(), now)
	want := []Title{{Kind: "series", Name: "Premiere", ID: 42}, {Kind: "movie", Name: "Today", ID: 42}, expectedFullMovie()}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentReleases = %+v, %v; want %+v", got, err, want)
	}
	shows.status = 403
	if got, err := libraryClient(t, movies, shows).recentReleases(t.Context(), now); got != nil || err == nil {
		t.Fatal("a failed calendar must not be replaced with edits or a partial feed")
	}
}

func TestEpisodesCanonicalOrderedAcrossSeasons(t *testing.T) {
	const path = "/shows/71/seasons?extended=episodes"
	const reply = `[
		{"number":2,"ids":{"trakt":933},"episodes":[{"season":2,"number":2,"ids":{"trakt":103}},{"season":2,"number":1,"title":"One","ids":{"trakt":102}},{"season":2,"number":1,"title":"One","ids":{"trakt":102}}]},
		{"number":0,"ids":{"trakt":960},"episodes":[{"season":0,"number":3,"title":"Special\nPart 3","ids":{"trakt":101}}]},
		{"number":3,"episodes":[]}
	]`
	c := libraryClient(t, libGet(path, reply))
	got, err := c.Episodes(t.Context(), Title{Kind: "series", ID: 71, TMDB: 45})
	want := []Episode{{0, 3, 101, "Special Part 3"}, {2, 1, 102, "One"}, {2, 2, 103, ""}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Episodes = %+v, %v; want %+v", got, err, want)
	}
}

func TestEpisodesRejectsAmbiguousOrMissingIdentities(t *testing.T) {
	const path = "/shows/71/seasons?extended=episodes"
	for _, reply := range []string{
		`null`, `[{"number":0}]`, `[{"number":1,"episodes":[{"season":0,"number":1,"ids":{"trakt":4}}]}]`,
		`[{"number":0,"episodes":[{"number":1,"ids":{"trakt":4}}]}]`,
		`[{"number":0,"episodes":[{"season":0,"number":1,"ids":{"tmdb":4}}]}]`,
		`[{"number":1,"episodes":[{"season":1,"number":1,"ids":{"trakt":4}},{"season":1,"number":1,"ids":{"trakt":5}}]}]`,
		`[{"number":1,"episodes":[{"season":1,"number":1,"ids":{"trakt":4}},{"season":1,"number":2,"ids":{"trakt":4}}]}]`,
	} {
		c := libraryClient(t, libGet(path, reply))
		if got, err := c.Episodes(t.Context(), Title{Kind: "series", ID: 71}); got != nil || err == nil {
			t.Fatalf("accepted malformed episode list: %+v, %v", got, err)
		}
	}
	c := libraryClient(t)
	for _, input := range []Title{{Kind: "series", TMDB: 71}, {Kind: "movie", ID: 71}} {
		if _, err := c.Episodes(t.Context(), input); err == nil {
			t.Error("accepted unconfirmed series")
		}
	}
}

func TestWatchedMoviesAllTimeAggregate(t *testing.T) {
	const path = "/sync/watched/movies"
	var rows []string
	var want []Title
	for i := 1; i <= 35; i++ {
		rows = append(rows, fmt.Sprintf(`{"plays":3,"movie":{"title":"Same name","year":%d,"ids":{"trakt":%d,"tmdb":%d}}}`, 1950+i, i, i+100))
		want = append(want, Title{Kind: "movie", ID: i, Year: 1950 + i, Name: "Same name", TMDB: i + 100})
	}
	rows = append(rows, rows[0])
	got, err := libraryClient(t, libGet(path, "["+strings.Join(rows, ",")+"]")).WatchedMovies(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("WatchedMovies = %+v, %v; want %+v", got, err, want)
	}
	for _, call := range []libCall{
		libGet(path, `null`), libGet(path, "["+rows[0]+`,{"show":{"ids":{"trakt":1}}}]`),
		libGet(path, `[{"movie":{"title":"Same name","ids":{"tmdb":1}}}]`),
		libGet(path, `[{"plays":0,"movie":{"title":"Not a play","ids":{"trakt":1}}}]`),
		{method: "GET", path: path, status: 403}, {method: "GET", path: path, page: "1", pageCount: "2", reply: "[" + rows[0] + "]"},
	} {
		if got, err := libraryClient(t, call).WatchedMovies(t.Context()); got != nil || err == nil {
			t.Fatalf("reported partial or unconfirmed watched state: %+v, %v", got, err)
		}
	}
}

func TestAccountShelvesRequireSignIn(t *testing.T) {
	c := libraryClient(t) // no API request when there are no credentials
	if err := os.Remove(c.tokenPath); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WatchedMovies(t.Context()); err == nil {
		t.Error("signed-out watched state is unknown, not empty")
	}
	if _, err := c.Watchlist(t.Context()); err == nil {
		t.Error("signed-out watchlist is unknown, not empty")
	}
}

func TestMarkEpisodeWatchedUsesRetainedID(t *testing.T) {
	const played = `[{"type":"episode","episode":{"season":0,"number":3,"ids":{"trakt":71}}}]`
	c := libraryClient(t, libGet(checkEp, `[]`), libPost("/sync/history", `{"episodes":[{"ids":{"trakt":71}}]}`, `{"added":{"episodes":1}}`), libGet(checkEp, played))
	for range 2 {
		if err := c.MarkEpisodeWatched(t.Context(), Episode{Season: 0, Number: 3, ID: 71}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ep := range []Episode{{Season: 0, Number: 3}, {Season: -1, Number: 3, ID: 71}, {Season: 1, ID: 71}} {
		if c.MarkEpisodeWatched(t.Context(), ep) == nil {
			t.Error("accepted incomplete episode target")
		}
	}
	c = libraryClient(t, libGet(checkEp, `[{"type":"episode","episode":{"ids":{"trakt":70}}}]`))
	if err := c.MarkEpisodeWatched(t.Context(), Episode{Season: 0, Number: 3, ID: 71}); err == nil {
		t.Error("accepted inconclusive history read")
	}
}
