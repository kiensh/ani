package flixhq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Exercise the real http.Client (including redirect handling) but never dial
// the named hosts or replace them with localhost exceptions to the allowlist.
type transport func(*http.Request) (*http.Response, error)

func (rt transport) RoundTrip(r *http.Request) (*http.Response, error) { return rt(r) }
func offline(handler http.HandlerFunc) *Client {
	c := New()
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			defer r.Body.Close()
		}
		w := httptest.NewRecorder()
		handler(w, r)
		resp := w.Result()
		resp.Request = r
		return resp, nil
	})
	return c
}

const (
	movie          = "/watch-movie/night-train/"
	series         = "/watch-series/campus/"
	episode        = "/episode/campus/s01-e02/"
	embed          = "https://kaembed.net/embed-ab12cd34.html"
	catalogFixture = `<div class="nav search-result-pop search-suggest">
<a href="https://flixhqz.net/watch-movie/night-train/" class="nav-item highlighted">
  <div class="film-poster"><img title="Poster fallback" class="film-poster-img" src="https://image.tmdb.org/t/p/w92/train01.jpg"></div>
  <div class="srp-detail"><h3 class="film-name">Night &amp; &#10; Train</h3>
  <div class="film-infor"><span>2025</span> <i class="dot"></i> <span>Movie</span></div></div>
</a><a class='nav-item' href='/watch-series/campus/'><img title='Campus 1999'></a>
<a class='nav-item' href='/watch-series/campus/'><img title='Duplicate'></a>
<a class='other-nav-item' href='/watch-movie/ad/'><img title='Ad'></a>
<a class='nav-item' href='https://flixhqz.net.evil.test/watch-movie/ad/'><img title='Ad'></a>
<a class='nav-item' href='/ad/'><img title='Ad'></a></div>`
	seriesFixture = `<a title='Eps 3: Finale' href='/episode/campus/s02-e03/'>3</a>
<a href="https://flixhqz.net/episode/campus/s01-e02/" title='Eps 2: A &amp; B&#x1b;!'>2</a>
<a href='/episode/campus/s01-e01/' title='Eps 1: Pilot'>1</a>
<a href='/episode/campus/s01-e02/' title='Duplicate'>2</a>
<a href='/episode/other-series/s01-e04/' title='Unrelated'>4</a>
<a href='https://other.test/episode/campus/s01-e05/'>ad</a>`
	playerFixture = `[{"name":"Other","link":"https://kaembed.net/embed-wrong.html"},
{"name":"Vidmoly","link":"https://ads.test/embed-bad.html"},
{"name":"Vidmoly","link":"https://kaembed.net/embed-ab12cd34.html?subget=https%3A%2F%2Fsubtitles.test%2Ftrack.vtt","en_sub":{"ignore":true}},
{"name":"Videasy","link":"https://player.example/movie/ad"}]`
	pageFixture = `<!-- <div data-token="unused"> --><script>var fake="<div data-token='unused'>";</script>
<span data-token='ignored'></span><div id='player' data-token='offline+value&amp;end='></div>`
)

func TestMovieAndSeries(t *testing.T) {
	const root = "https://" + primary
	fields := make(map[string]int)
	c := offline(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != primary || r.URL.Scheme != "https" {
			t.Fatalf("unexpected host %v", r.URL.Host)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /livesearch":
			if q := r.URL.Query(); len(q) != 1 || q.Get("q") != "night & campus?" {
				t.Fatalf("search query %v", q)
			}
			io.WriteString(w, catalogFixture)
		case "GET " + series:
			io.WriteString(w, seriesFixture)
		case "GET " + movie, "GET " + episode:
			io.WriteString(w, pageFixture)
		case "POST " + ajaxPath:
			if r.Header.Get("Origin") != root {
				t.Fatal("wrong POST origin")
			}
			field := map[string]string{root + movie: "players", root + episode: "players_show"}[r.Referer()]
			if field == "" {
				t.Fatal("wrong POST referer")
			}
			if err := r.ParseMultipartForm(16 << 10); err != nil {
				t.Fatal(err)
			}
			if len(r.MultipartForm.File) != 0 || len(r.MultipartForm.Value) != 1 || !slices.Equal(r.MultipartForm.Value[field], []string{"offline+value&end="}) {
				t.Fatal("wrong multipart selection field/value")
			}
			fields[field]++
			io.WriteString(w, playerFixture)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := t.Context()
	titles, err := c.Search(ctx, " night & campus? ")
	wantTitles := []Title{{Name: "Night & Train", Kind: "movie", URL: root + movie, Year: "2025", PosterURL: "https://image.tmdb.org/t/p/w780/train01.jpg"}, {Name: "Campus 1999", Kind: "series", URL: root + series}}
	if err != nil || !slices.Equal(titles, wantTitles) {
		t.Fatalf("Search: %+v, %v", titles, err)
	}
	eps, err := c.Episodes(ctx, titles[1])
	wantEps := []Episode{{1, 1, "Pilot", root + "/episode/campus/s01-e01/"}, {1, 2, "A & B !", root + episode}, {2, 3, "Finale", root + "/episode/campus/s02-e03/"}}
	if err != nil || !slices.Equal(eps, wantEps) {
		t.Fatalf("Episodes: %+v, %v", eps, err)
	}
	for _, target := range []string{titles[0].URL, eps[1].URL} {
		r, err := c.Resolve(ctx, target)
		if err != nil || r == nil || r.StreamURL != embed || r.Referer != target || r.SubtitleURL != "" {
			t.Fatalf("Resolve: %+v, %v", r, err)
		}
	}
	if fields["players"] != 1 || fields["players_show"] != 1 {
		t.Fatalf("unexpected POST counts: %v", fields)
	}
}

func TestSearchNotFound(t *testing.T) {
	calls := 0
	c := offline(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `<div class="nav search-result-pop search-suggest"><div class="nav-item">No results found.</div></div>`)
	})
	got, err := c.Search(t.Context(), "unknown")
	if err != nil || len(got) != 0 || calls != 1 {
		t.Fatalf("Search returned %v, %v after %d requests", got, err, calls)
	}
}

func TestSearchMirror(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hosts []string
			c := offline(func(w http.ResponseWriter, r *http.Request) {
				hosts = append(hosts, r.URL.Host)
				if r.URL.Host == primary {
					w.WriteHeader(status)
					io.WriteString(w, `<html><title>Maintenance</title></html>`)
					return
				}
				io.WriteString(w, `<a class="nav-item" href="/watch-movie/night-train/"><img title="Night Train"></a>`)
			})
			got, err := c.Search(t.Context(), "Night Train")
			if err != nil || len(got) != 1 || got[0].URL != "https://"+mirror+movie || !slices.Equal(hosts, []string{primary, mirror}) {
				t.Fatalf("fallback: %+v, %v, hosts %v", got, err, hosts)
			}
		})
	}
}

func TestRedirectUsesActualPage(t *testing.T) {
	const actual = "https://" + mirror + movie
	var calls []string
	c := offline(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.String())
		switch {
		case r.URL.Host == primary:
			http.Redirect(w, r, actual, http.StatusMovedPermanently)
		case r.Method == http.MethodGet:
			io.WriteString(w, pageFixture)
		case r.Method == http.MethodPost:
			if r.Referer() != actual {
				t.Fatal("wrong referer after redirect")
			}
			io.WriteString(w, `[{"name":"Vidmoly","link":"`+embed+`"}]`)
		}
	})
	r, err := c.Resolve(t.Context(), "https://"+primary+movie)
	if err != nil || r == nil || r.Referer != actual || !slices.Equal(calls, []string{"https://" + primary + movie, actual, "https://" + mirror + ajaxPath}) {
		t.Fatalf("Resolve redirect: %+v, %v, calls %v", r, err, calls)
	}
}

func TestRejectInvalidPages(t *testing.T) {
	c := offline(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid page reached transport") })
	const root = "https://" + primary
	for _, raw := range []string{
		"http://" + primary + movie, root + ":8443" + movie, "https://someone@" + primary + movie,
		root + ".example" + movie, "//" + primary + movie, movie, root + "/watch-movie/../admin/",
		root + "/watch-movie/foo%2fbar/", root + movie + "?ad=1", root + movie + "#ad",
		root + series, root + ajaxPath, root + "/episode/campus/s01-x02/",
	} {
		if r, err := c.Resolve(t.Context(), raw); err == nil || r != nil {
			t.Errorf("Resolve accepted %q", raw)
		}
	}
	for _, title := range []Title{{Kind: "series", URL: "https://" + primary + movie}, {Kind: "movie", URL: "https://" + primary + series}, {Kind: "series", URL: "https://other.test" + series}} {
		if _, err := c.Episodes(t.Context(), title); err == nil {
			t.Errorf("Episodes accepted %+v", title)
		}
	}
}

func TestRejectRedirects(t *testing.T) {
	for _, tc := range []struct {
		name, location string
		status         int
		post           bool
	}{
		{"HTTP", "http://" + primary + movie, 302, false},
		{"off-site", "https://ad.test" + movie, 302, false},
		{"other route", "https://" + primary + ajaxPath, 302, false},
		{"other media kind", "https://" + primary + episode, 302, false},
		{"extra query", "https://" + primary + movie + "?secret=opaque-example", 302, false},
		{"POST replay", "https://" + mirror + ajaxPath, 307, true},
		{"POST converted to GET", "https://" + primary + movie, 303, true},
		{"malformed location", "https://%zz/opaque-example", 302, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := offline(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.post && calls == 1 {
					io.WriteString(w, pageFixture)
					return
				}
				w.Header().Set("Location", tc.location)
				w.WriteHeader(tc.status)
			})
			_, err := c.Resolve(t.Context(), "https://"+primary+movie)
			want := 1
			if tc.post {
				want = 2
			}
			if err == nil || calls != want || strings.Contains(err.Error(), "opaque-example") {
				t.Fatalf("redirect: %v, calls %d", err, calls)
			}
		})
	}
}

func TestRejectInvalidPlayerLinks(t *testing.T) {
	for _, link := range []string{
		"http://kaembed.net/embed-id.html", "//kaembed.net/embed-id.html",
		"https://kaembed.net:443/embed-id.html", "https://kaembed.net.ad.test/embed-id.html",
		"https://u@kaembed.net/embed-id.html", embed + "?ad=1", embed + "#fragment",
		embed + "?subget=hint&ad=1", embed + "?subget=a&subget=b", embed + "?subget=x#fragment",
		"https://kaembed.net/video/1", "https://kaembed.net/embed-a%2fb.html", "https://kaembed.net/embed-.html",
	} {
		c := offline(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				io.WriteString(w, pageFixture)
				return
			}
			json.NewEncoder(w).Encode([]map[string]string{{"name": "Other", "link": embed}, {"name": "Vidmoly (ad)", "link": embed}, {"name": "Vidmoly", "link": link}})
		})
		if got, err := c.Resolve(t.Context(), "https://"+primary+movie); err == nil || got != nil {
			t.Errorf("accepted invalid link %q", link)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	for _, tc := range []struct {
		name, page, json string
		status, calls    int
	}{
		{"not found", "opaque-example", "", 404, 1},
		{"large page", strings.Repeat("a", maxPage+1), "", 200, 1},
		{"no selection", `<span data-token="opaque-example"></span>`, "", 200, 1},
		{"empty selection", `<div data-token=""></div>`, "", 200, 1},
		{"ambiguous selection", `<div data-token="opaque-example"></div><div data-token="other"></div>`, "", 200, 1},
		{"large selection", `<div data-token="` + strings.Repeat("t", 8193) + `"></div>`, "", 200, 1},
		{"unsupported players", pageFixture, `[]`, 200, 2},
		{"bad JSON", pageFixture, `opaque-example`, 200, 2},
		{"large JSON", pageFixture, strings.Repeat(" ", maxPlayers+1), 200, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := offline(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				if r.Method == "POST" {
					io.WriteString(w, tc.json)
				} else {
					io.WriteString(w, tc.page)
				}
			})
			r, err := c.Resolve(t.Context(), "https://"+primary+movie)
			if err == nil || r != nil || strings.Contains(err.Error(), "opaque-example") || strings.Contains(err.Error(), "offline+value") || calls != tc.calls {
				t.Fatalf("unexpected result: %+v, %v, %d requests", r, err, calls)
			}
		})
	}
}

func TestCancellationSkipsMirror(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	c := New()
	c.http.Transport = transport(func(r *http.Request) (*http.Response, error) { calls++; cancel(); return nil, r.Context().Err() })
	if _, err := c.Search(ctx, "campus"); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation: %v, %d requests", err, calls)
	}
}

func TestValidPosterURL(t *testing.T) {
	const root = "https://image.tmdb.org"
	const path = "/t/p/w342/d5NXSklXo0qyIYkgV94XAgMIckC.jpg"
	for _, raw := range []string{root + path, root + "/t/p/w92/poster.webp", root + "/t/p/w780/a-b_C.png"} {
		if !ValidPosterURL(raw) {
			t.Errorf("refused %q", raw)
		}
	}
	for _, raw := range []string{
		"", path, "//image.tmdb.org" + path, "http://image.tmdb.org" + path,
		root + ".example" + path, "https://u@image.tmdb.org" + path, root + ":443" + path,
		root + "@other.test" + path, root + "\\other.test" + path, root + path + "?", root + path + "#",
		root + path + "?size=small", root + path + "#fragment", root + path + "\n",
		root + "/t/p/w342/../x.jpg", root + "/t/p/w342/x%2fy.jpg", root + "/t/p/w342/a.svg",
		root + "/t/p/w99999/a.jpg", root + "/t/p/w342/" + strings.Repeat("x", 300) + ".jpg",
	} {
		if ValidPosterURL(raw) {
			t.Errorf("accepted %q", raw)
		}
	}
}

const detailFixture = `
<meta property="og:image" content="https://image.tmdb.org/t/p/w780/backdrop.jpg">
<script>"<div class='m_i-d-content'><div class='description'>Fake</div></div>"</script>
<div class='m_i-detail'>
 <div class='m_i-d-poster'><div class='film-poster'><img class='film-poster-img' src='https://qqqcdn.cloud/images/posters/not-trusted.webp'></div></div>
 <div class='m_i-d-content'>
  <div id='vote-info' data-movie-id='unused'></div>
  <div class='stats'>
   <span class='item mr-3'><button>HD</button></span>
   <span class='item mr-3'><i class='fas fa-star mr-2'></i>7.3</span>
   <span class='item mr-3'>81 min</span>
  </div>
  <div class='description'> <strong>Campus FlixHQ Watch Online Streaming</strong> - Two friends &amp; rivals&#27;
   <em>return</em>.<style>.unused{}</style></div>
  <div class='elements'>
   <div class='row-line'><span class='type'>Country:</span><a href='/country/x/'>X</a></div>
   <div class='row-line'><span class='type'>Genre:</span><a href='/genre/drama/'>Drama</a>,
    <a href='/genre/fantasy/'>Sci-Fi &amp; Fantasy</a></div>
  </div>
 </div>
</div>
<div class='m_i-related'><div class='film-poster'><img src='https://image.tmdb.org/t/p/w92/related.jpg'></div></div>
<div data-token='unused'></div>`

func TestDetails(t *testing.T) {
	const poster = "https://image.tmdb.org/t/p/w780/campus.jpg"
	want := Details{Description: "Two friends & rivals return.", Genres: "Drama, Sci-Fi & Fantasy", Duration: "81 min", Rating: "7.3", PosterURL: poster}
	for _, tc := range []struct{ kind, path string }{{"movie", movie}, {"series", series}} {
		t.Run(tc.kind, func(t *testing.T) {
			calls := 0
			c := offline(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.String() != "https://"+mirror+tc.path {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				io.WriteString(w, strings.Replace(detailFixture, "https://qqqcdn.cloud/images/posters/not-trusted.webp", "https://image.tmdb.org/t/p/w185/campus.jpg", 1))
			})
			got, err := c.Details(t.Context(), Title{Name: "Campus", Kind: tc.kind, URL: "https://" + mirror + tc.path})
			if err != nil || got != want || calls != 1 {
				t.Fatalf("Details: %+v, %v (%d requests)", got, err, calls)
			}
		})
	}
}

func TestDetailsPosterFallback(t *testing.T) {
	const tmdb = "https://image.tmdb.org/t/p/"
	card := func(path, id string) string {
		return `<a class='nav-item' href='` + path + `'><img class='film-poster-img' title='Campus' src='` + tmdb + `w92/` + id + `.jpg'></a>`
	}
	for _, tc := range []struct{ name, existing, searchHTML, want string }{
		{"exact path across mirrors", "", card("/watch-series/campus-remake/", "wrong") + card(series, "right"), tmdb + "w780/right.jpg"},
		{"not a title or prefix match", "", card(movie, "wrong") + card("/watch-series/campus-remake/", "wrong") + card(strings.TrimSuffix(series, "/"), "wrong"), ""},
		{"lookup failure preserves details", "", "", ""},
		{"existing art needs no lookup", tmdb + "w342/selected.jpg", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := offline(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch {
				case r.Method == "GET" && r.URL.String() == "https://"+mirror+series:
					io.WriteString(w, detailFixture)
				case tc.existing == "" && r.Method == "GET" && r.URL.Path == "/livesearch" && r.URL.Query().Get("q") == "Campus":
					if tc.searchHTML == "" {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else {
						io.WriteString(w, tc.searchHTML)
					}
				default:
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
			})
			got, err := c.Details(t.Context(), Title{Name: "Campus", Kind: "series", URL: "https://" + mirror + series, PosterURL: tc.existing})
			if err != nil || got.PosterURL != tc.want || got.Rating != "7.3" || got.Description == "" || calls > 3 {
				t.Fatalf("Details: %+v, %v, calls %d", got, err, calls)
			}
		})
	}
}

func TestDetailsMissingFields(t *testing.T) {
	html := `<div class='description'>Unrelated synopsis</div><div class='stats'><span class='item'>81 min</span></div>
	<div class='m_i-d-content'><div class='stats'><span class='item'>2025</span><span class='item'>8</span><span class='item'> min</span></div>
	<div class='description'><strong>There</strong> is no promotional intro.</div></div>`
	got := parseDetails(html)
	if got != (Details{Description: "There is no promotional intro."}) {
		t.Fatalf("unexpected missing fields: %+v", got)
	}
}

func TestDetailsRejectsInvalidTarget(t *testing.T) {
	c := offline(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected network request") })
	const root = "https://" + primary
	for _, title := range []Title{
		{Kind: "series", URL: root + movie}, {Kind: "movie", URL: root + series}, {Kind: "episode", URL: root + episode},
		{Kind: "series", URL: root + series + "?"}, {Kind: "movie", URL: "https://evil.test" + movie},
		{Kind: "", URL: root + ajaxPath}, {Kind: "home", URL: root + "/home"},
	} {
		if _, err := c.Details(t.Context(), title); err == nil {
			t.Errorf("accepted invalid target %+v", title)
		}
	}
}

func TestCatalogFailures(t *testing.T) {
	calls := 0
	c := offline(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "opaque-example", http.StatusBadGateway)
	})
	_, err := c.Search(t.Context(), "campus")
	if err == nil || calls != 2 || !strings.Contains(err.Error(), fmt.Sprint(http.StatusBadGateway)) || strings.Contains(err.Error(), "opaque-example") {
		t.Fatalf("unexpected catalog error: %v, %d requests", err, calls)
	}
}
