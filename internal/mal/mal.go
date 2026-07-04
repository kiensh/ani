// Package mal wraps the MyAnimeList API (OAuth2 + Jikan for external links).
package mal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/nstratos/go-myanimelist/mal"
)

// Item is a flat, app-facing projection of a MAL anime (list or search hit).
type Item struct {
	MalID       int
	Title       string
	CoverURL    string
	TotalEps    int
	WatchedEps  int
	AirStatus   string
	ListStatus  string
	Score       int
	Genres      string
	MeanScore   float64
	Studios     string
	StartSeason string
	MediaType   string
	Rank        int
	Members     int
}

// TitleCase returns s with its first rune upper-cased.
func TitleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

func userAnimeToItem(ua mal.UserAnime) Item {
	return animeFieldsToItem(ua.Anime, ua.Status.NumEpisodesWatched, ua.Status.Score, ua.Status.Status)
}

func animeToItem(a mal.Anime) Item {
	return animeFieldsToItem(a, a.MyListStatus.NumEpisodesWatched, a.MyListStatus.Score, a.MyListStatus.Status)
}

func animeFieldsToItem(a mal.Anime, watchedEps, score int, listStatus mal.AnimeStatus) Item {
	cover := a.MainPicture.Large
	if cover == "" {
		cover = a.MainPicture.Medium
	}
	var genres, studios []string
	for _, g := range a.Genres {
		genres = append(genres, g.Name)
	}
	for _, s := range a.Studios {
		studios = append(studios, s.Name)
	}
	season := ""
	if a.StartSeason.Year > 0 {
		season = fmt.Sprintf("%s %d", TitleCase(a.StartSeason.Season), a.StartSeason.Year)
	}
	return Item{
		MalID:       a.ID,
		Title:       a.Title,
		CoverURL:    cover,
		TotalEps:    a.NumEpisodes,
		AirStatus:   a.Status,
		ListStatus:  string(listStatus),
		WatchedEps:  watchedEps,
		Score:       score,
		Genres:      strings.Join(genres, ", "),
		MeanScore:   a.Mean,
		Studios:     strings.Join(studios, ", "),
		StartSeason: season,
		MediaType:   a.MediaType,
		Rank:        a.Rank,
		Members:     a.NumListUsers,
	}
}

// ExtraFields are the MAL API fields needed for the preview pane.
var ExtraFields = mal.Fields{
	"title", "main_picture", "num_episodes", "status", "my_list_status",
	"genres", "mean", "studios", "start_season", "media_type", "rank", "num_list_users",
}

// ---- operations ----

// MyList returns the user's anime list, optionally filtered by status.
func MyList(status mal.AnimeStatus, debug bool) ([]Item, error) {
	c, err := Client(debug)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	const pageSize = 1000
	base := []mal.AnimeListOption{
		ExtraFields,
		mal.NSFW(true),
		mal.Limit(pageSize),
	}
	if status != "" {
		base = append(base, status)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG MAL GET /users/@me/animelist status=%s\n", status)
	}
	var out []Item
	for offset := 0; ; offset += pageSize {
		page, _, err := c.User.AnimeList(ctx, "@me", append(base, mal.Offset(offset))...)
		if err != nil {
			return nil, err
		}
		for _, ua := range page {
			out = append(out, userAnimeToItem(ua))
		}
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

// Search returns MAL anime matching a text query.
func Search(q string, debug bool) ([]Item, error) {
	c, err := Client(debug)
	if err != nil {
		return nil, err
	}
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG MAL GET /anime?q=%s\n", q)
	}
	anime, _, err := c.Anime.List(context.Background(), q,
		ExtraFields, mal.Limit(20))
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(anime))
	for _, a := range anime {
		out = append(out, animeToItem(a))
	}
	return out, nil
}

// AnidbAID resolves the AniDB id for a MAL anime via Jikan (which mirrors
// MAL's external links — the MAL v2 API doesn't expose external links).
// Returns 0, nil if no AniDB link is present.
func AnidbAID(malID int, debug bool) (int, error) {
	url := fmt.Sprintf("https://api.jikan.moe/v4/anime/%d/external", malID)
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG Jikan GET %s\n", url)
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("jikan external: %s", resp.Status)
	}
	var d struct {
		Data []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return 0, err
	}
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG Jikan external for mal/%d:\n", malID)
		for _, e := range d.Data {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", e.Name, e.URL)
		}
	}
	for _, e := range d.Data {
		if !strings.EqualFold(e.Name, "AniDB") && !strings.Contains(e.URL, "anidb.net") {
			continue
		}
		if aid := ParseAnidbAidFromURL(e.URL); aid > 0 {
			return aid, nil
		}
	}
	return 0, nil
}

// ParseAnidbAidFromURL extracts the numeric aid from an AniDB URL, handling
// both the old format (?aid=12345) and the new format (/anime/12345).
func ParseAnidbAidFromURL(rawurl string) int {
	for _, prefix := range []string{"aid=", "/anime/"} {
		if i := strings.Index(rawurl, prefix); i >= 0 {
			s := rawurl[i+len(prefix):]
			var n int
			for _, ch := range s {
				if ch < '0' || ch > '9' {
					break
				}
				n = n*10 + int(ch-'0')
			}
			if n > 0 {
				return n
			}
		}
	}
	return 0
}

// Update sets watched-episode count and status on MAL. When dryRun is true the
// request is printed but not sent.
func Update(malID, watchedEps int, status mal.AnimeStatus, dryRun, debug bool) error {
	if dryRun {
		fmt.Fprintf(os.Stderr, "DRY-RUN: MAL PATCH /anime/%d num_episodes_watched=%d status=%s (not sent)\n", malID, watchedEps, status)
		return nil
	}
	c, err := Client(debug)
	if err != nil {
		return err
	}
	_, _, err = c.Anime.UpdateMyListStatus(context.Background(), malID,
		status, mal.NumEpisodesWatched(watchedEps))
	return err
}

// RefreshItem re-fetches the anime from MAL and updates item in-place, so the
// fzf header reflects the real state after a write-back. No-op when dryRun.
func RefreshItem(item *Item, dryRun, debug bool) {
	if item == nil || dryRun {
		return
	}
	c, err := Client(debug)
	if err != nil {
		return
	}
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG MAL refresh anime/%d\n", item.MalID)
	}
	a, _, err := c.Anime.Details(context.Background(), item.MalID, ExtraFields)
	if err != nil {
		return
	}
	refreshed := animeFieldsToItem(*a, a.MyListStatus.NumEpisodesWatched, a.MyListStatus.Score, a.MyListStatus.Status)
	*item = refreshed
}
