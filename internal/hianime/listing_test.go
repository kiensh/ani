package hianime

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"testing"
)

// listingCard renders one recently-updated card in the site's markup (badge
// in the poster, names in film-name with the romaji in data-jname).
func listingCard(slug, name, jname string, sub int) string {
	return fmt.Sprintf(`<div class="flw-item flw-item-big">
  <div class="film-poster">
    <div class="tick ltr">
      <div class="tick-item tick-sub"><i class="fas fa-closed-captioning mr-1"></i>%d</div>
    </div>
    <a href="https://x/watch/%s" class="film-poster-ahref item-qtip" title="%s" data-id="1"></a>
  </div>
  <div class="film-detail">
    <h3 class="film-name">
      <a href="https://x/%s" title="%s" class="dynamic-name" data-jname="%s">%s</a>
    </h3>
  </div>
</div>`, sub, slug, name, slug, name, jname, name)
}

func TestFetchEpisodeListing(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.RequestURI() != "/recently-updated" {
			t.Errorf("unexpected request %v", r.URL)
		}
		fmt.Fprint(w, listingCard("show-a-1", "Show A", "Show A Romaji", 9), listingCard("b-2", "Show B: &quot;Part 2&quot;", "Shared Alias", 4), listingCard("b-3", "Show B: Part 3", "Shared Alias", 2), `<div id="main-sidebar">`, listingCard("decoy-1", "Decoy", "", 1))
	}))
	defer srv.Close()
	old := baseURL
	baseURL = srv.URL
	defer func() { baseURL = old }()
	listing, err := FetchEpisodeListing()
	if err != nil {
		t.Fatal(err)
	}
	for title, want := range map[string]float64{"Show A": 9, "Show A Romaji": 9, `show b: "part 2"`: 4, "Show B": 0, "Shared Alias": 0, "Decoy": 0} {
		if n, ok := listing.Latest(title); n != want || ok != (want > 0) {
			t.Errorf("%s -> %v, %v; want %v", title, n, ok, want)
		}
	}
	if hits != 1 {
		t.Errorf("%d requests, want one", hits)
	}
}
