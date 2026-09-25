package mal

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// The user's MAL favorite studios, fetched once per process. Used by the anime
// picker's preview pane ("(favorite)" marker) and its fuzzy filter ("favorite").
var (
	favOnce    sync.Once
	favStudios map[string]bool
)

// FavoriteStudios returns the set of studio names on the user's MAL favorites
// ("MAPPA" → true). MAL's API doesn't expose favorites — /users/@me has no
// such field (it silently ignores fields=favorites) — so this reads the public
// profile favorites page, whose companies section is server-rendered.
// Best-effort: one fetch per process; any failure (not logged in, network,
// unrecognized layout) yields an empty set and the UI simply doesn't mark
// studios. Never triggers the browser OAuth flow.
func FavoriteStudios(debug bool) map[string]bool {
	favOnce.Do(func() {
		favStudios = fetchFavoriteStudios(debug)
	})
	return favStudios
}

func fetchFavoriteStudios(debug bool) map[string]bool {
	out := map[string]bool{}
	if !LoggedIn() {
		return out // no session — must not trigger the OAuth flow
	}
	u, err := WhoAmI(debug) // the profile URL needs the username
	if err != nil || u == nil || u.Name == "" {
		return out
	}
	url := "https://myanimelist.net/profile/" + u.Name + "/favorites"
	dbg(debug, "DEBUG MAL GET %s (favorite studios)\n", url)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return out
	}
	req.Header.Set("User-Agent", "Mozilla/5.0") // plain clients get a challenge page
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out
	}
	return parseFavoriteStudios(body)
}

// favoriteCompanyRe matches the favorites page's company blocks: an
// /anime/producer/<id>/<Name> link whose image alt text carries the exact
// display name (matching the API's studio names).
var favoriteCompanyRe = regexp.MustCompile(`<a href="[^"]*/anime/producer/\d+/[^"]*">\s*<img[^>]*alt="([^"]+)"`)

// parseFavoriteStudios extracts the favorite company (studio) names from a
// profile favorites page. Anime/manga/character/person favorites don't carry
// producer links, so only companies match. An unrecognized page yields an
// empty set — the marker is cosmetic, never an error.
func parseFavoriteStudios(body []byte) map[string]bool {
	out := map[string]bool{}
	for _, m := range favoriteCompanyRe.FindAllStringSubmatch(string(body), -1) {
		if name := strings.TrimSpace(m[1]); name != "" {
			out[name] = true
		}
	}
	return out
}
