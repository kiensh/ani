package flixhq

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func recentCard(path, name, year, img string) string {
	return `<div class="flw-item"><div class='film-poster'><div class='pick film-poster-quality'>HD</div>
<img class='film-poster-img lazyload' data-src='` + img + `'><a class='film-poster-ahref' href='` + path + `'></a></div>
<div class='film-detail'><h3 class='film-name'><a href='` + path + `' title='Ignore SEO title'>` + name + `</a></h3>
<div class='fd-infor'><span class='fdi-item'>` + year + `</span><span class='fdi-item'>EPS 2026</span></div></div></div>`
}

func recentSection(name, cards string) string {
	return `<section class='block_area block_area_home section-id-02'><div class='block_area-header'><h2 class='cat-heading'>` + name + `</h2></div><div class='film_list-wrap'>` + cards + `</div></section>`
}

func TestRecentlyAdded(t *testing.T) {
	const root = "https://" + primary
	const cdn = "https://qqqcdn.cloud/images/posters/not-tmdb.webp"
	movieCard := recentCard(movie, `Night &amp; &#27;Train`, "1998", cdn)
	seriesCard := recentCard(series, "Campus 1984", "SS 1", "https://image.tmdb.org/t/p/w92/campus.jpg")
	html := recentSection("Trending", recentCard("/watch-movie/trending/", "Trending", "2024", "")) +
		recentSection("Latest Movies", movieCard+recentCard("https://"+mirror+strings.TrimSuffix(movie, "/"), "Duplicate", "1998", "")+seriesCard+
			recentCard("/watch-movie/bad/?query=x", "Ad", "2002", "")+recentCard("https://evil.test"+movie, "Ad", "2003", "")+
			recentCard("/watch-movie/next/", "Next", "2004", "")) +
		`<!-- ` + recentSection("Latest TV Shows", recentCard("/watch-series/ignored/", "Comment", "", "")) + ` -->` +
		recentSection("Latest TV Shows", seriesCard)
	calls := 0
	c := offline(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.String() != root+"/home" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		io.WriteString(w, html)
	})
	got, err := c.RecentlyAdded(t.Context())
	want := []Title{
		{Name: "Night & Train", Kind: "movie", URL: root + movie, Year: "1998"},
		{Name: "Campus 1984", Kind: "series", URL: root + series, PosterURL: "https://image.tmdb.org/t/p/w780/campus.jpg"},
		{Name: "Next", Kind: "movie", URL: root + "/watch-movie/next/", Year: "2004"},
	}
	if err != nil || !slices.Equal(got, want) || calls != 1 {
		t.Fatalf("RecentlyAdded: %+v, %v (%d requests)", got, err, calls)
	}
}

func TestRecentOrderingAndMirror(t *testing.T) {
	var movies, tv strings.Builder
	for i := range 30 {
		movies.WriteString(recentCard(fmt.Sprintf("/watch-movie/m%d/", i), fmt.Sprintf("M%d", i), "", ""))
		tv.WriteString(recentCard(fmt.Sprintf("/watch-series/s%d/", i), fmt.Sprintf("S%d", i), "", ""))
	}
	for _, primaryStatus := range []int{404, 200} {
		t.Run(http.StatusText(primaryStatus), func(t *testing.T) {
			var hosts []string
			c := offline(func(w http.ResponseWriter, r *http.Request) {
				hosts = append(hosts, r.URL.Host)
				if r.URL.Path != "/home" || r.Method != "GET" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				if r.URL.Host == primary {
					w.WriteHeader(primaryStatus)
					io.WriteString(w, recentSection("Trending", movies.String()))
					return
				}
				io.WriteString(w, recentSection("Latest TV Shows", tv.String())+recentSection("Latest Movies", movies.String()))
			})
			got, err := c.RecentlyAdded(t.Context())
			if err != nil || len(got) != 48 || !slices.Equal(hosts, []string{primary, mirror}) {
				t.Fatalf("RecentlyAdded: %d titles, %v, hosts %v", len(got), err, hosts)
			}
			for i := range 24 {
				if got[2*i].Name != fmt.Sprintf("M%d", i) || got[2*i+1].URL != fmt.Sprintf("https://%s/watch-series/s%d/", mirror, i) {
					t.Fatalf("pair %d: %+v, %+v", i, got[2*i], got[2*i+1])
				}
			}
		})
	}
}

func TestRecentRejectsOtherRoutes(t *testing.T) {
	var calls []string
	c := offline(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.String())
		http.Redirect(w, r, "https://"+primary+ajaxPath, http.StatusFound)
	})
	_, err := c.RecentlyAdded(t.Context())
	if err == nil || !slices.Equal(calls, []string{"https://" + primary + "/home", "https://" + mirror + "/home"}) {
		t.Fatalf("unexpected redirect: %v, calls %v", err, calls)
	}
}
