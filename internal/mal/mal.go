// Package mal wraps the MyAnimeList API (OAuth2 + Jikan for external links).
package mal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
	UpdatedAt   time.Time // list-status update time (zero if not on your list) — for the "updated" sort
	StartDate   string    // anime air/start date (YYYY-MM-DD) — for the "air date" sort
	AnidbAID    int       // AniDB anime id; 0 for pure MAL items (resolved later), set directly for AnimeTosho hits
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
	return animeFieldsToItem(ua.Anime, ua.Status.NumEpisodesWatched, ua.Status.Score, ua.Status.Status, ua.Status.UpdatedAt)
}

func animeToItem(a mal.Anime) Item {
	return animeFieldsToItem(a, a.MyListStatus.NumEpisodesWatched, a.MyListStatus.Score, a.MyListStatus.Status, a.MyListStatus.UpdatedAt)
}

func animeFieldsToItem(a mal.Anime, watchedEps, score int, listStatus mal.AnimeStatus, updatedAt time.Time) Item {
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
		UpdatedAt:   updatedAt,
		StartDate:   a.StartDate,
	}
}

// ExtraFields are the MAL API fields needed for the preview pane + sorts.
// Includes both "my_list_status" (search/details) and "list_status"
// (user list) since the two endpoints use different field names.
var ExtraFields = mal.Fields{
	"title", "main_picture", "num_episodes", "status", "start_date",
	"my_list_status", "list_status",
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

// Season identifies a MAL broadcast season. It mirrors go-myanimelist's
// AnimeSeason constants as plain strings so callers don't depend on the
// upstream library.
type Season string

const (
	SeasonWinter Season = "winter"
	SeasonSpring Season = "spring"
	SeasonSummer Season = "summer"
	SeasonFall   Season = "fall"
)

// SeasonAll is the season-filter value meaning "every season" (the source
// becomes the user's full cross-season list rather than one Seasonal page).
const SeasonAll = "All"

// SeasonLater is the season-filter value for the upcoming/TBA lineup (the
// official MAL "upcoming" ranking — the /season/later web page isn't exposed by
// the API).
const SeasonLater = "Later"

// ParseSeason returns the "Summer 2026"-style label for a year/season.
func ParseSeason(year int, season Season) string {
	return fmt.Sprintf("%s %d", TitleCase(string(season)), year)
}

// ParseSeasonLabel parses a "Summer 2026" label into (year, season). ok is false
// for malformed labels or "All".
func ParseSeasonLabel(label string) (year int, season Season, ok bool) {
	parts := strings.SplitN(label, " ", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	var s Season
	switch strings.ToLower(parts[0]) {
	case "winter":
		s = SeasonWinter
	case "spring":
		s = SeasonSpring
	case "summer":
		s = SeasonSummer
	case "fall":
		s = SeasonFall
	default:
		return 0, "", false
	}
	y, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", false
	}
	return y, s, true
}

// SeasonArchive returns MAL's season archive via Jikan (the JSON mirror of
// myanimelist.net/anime/season/archive) as newest-first "Summer 2026"-style
// labels. nil on any error (best-effort; caller falls back to a local list).
func SeasonArchive(debug bool) []string {
	const u = "https://api.jikan.moe/v4/seasons"
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG Jikan GET %s\n", u)
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var d struct {
		Data []struct {
			Year    int      `json:"year"`
			Seasons []string `json:"seasons"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil
	}
	// Data is newest-year-first; within a year reverse seasons so later seasons
	// come first (fall → winter) for true chronological-descending order.
	var out []string
	for _, y := range d.Data {
		for i := len(y.Seasons) - 1; i >= 0; i-- {
			out = append(out, ParseSeason(y.Year, Season(y.Seasons[i])))
		}
	}
	return out
}

// Seasonal returns the seasonal anime lineup for a given year/season (e.g.
// summer 2026), sorted by MAL's default. Up to 100 titles; each carries
// my_list_status when authenticated, so client-side status filtering works.
func Seasonal(year int, season Season, debug bool) ([]Item, error) {
	c, err := Client(debug)
	if err != nil {
		return nil, err
	}
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG MAL GET /anime/season/%d/%s\n", year, season)
	}
	anime, _, err := c.Anime.Seasonal(context.Background(), year, mal.AnimeSeason(season),
		ExtraFields, mal.Limit(100))
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(anime))
	for _, a := range anime {
		out = append(out, animeToItem(a))
	}
	return out, nil
}

// Upcoming returns the top upcoming/TBA anime via the official MAL "upcoming"
// ranking. Authenticated, so each item carries my_list_status — "Not in My List"
// works on it. (The /season/later web page isn't exposed by Jikan or the
// Seasonal API.)
func Upcoming(debug bool) ([]Item, error) {
	c, err := Client(debug)
	if err != nil {
		return nil, err
	}
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG MAL GET /anime/ranking/upcoming\n")
	}
	anime, _, err := c.Anime.Ranking(context.Background(), mal.AnimeRankingUpcoming,
		ExtraFields, mal.Limit(100))
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(anime))
	for _, a := range anime {
		out = append(out, animeToItem(a))
	}
	return out, nil
}

// CurrentSeason returns the MAL season + human label for the current real-world
// date (Jan–Mar winter, Apr–Jun spring, Jul–Sep summer, Oct–Dec fall). label is
// "Summer 2026"-style.
func CurrentSeason() (year int, season Season, label string) {
	now := time.Now()
	year = now.Year()
	m := int(now.Month())
	switch {
	case m <= 3:
		season = SeasonWinter
	case m <= 6:
		season = SeasonSpring
	case m <= 9:
		season = SeasonSummer
	default:
		season = SeasonFall
	}
	label = fmt.Sprintf("%s %d", TitleCase(string(season)), year)
	return year, season, label
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
	refreshed := animeFieldsToItem(*a, a.MyListStatus.NumEpisodesWatched, a.MyListStatus.Score, a.MyListStatus.Status, a.MyListStatus.UpdatedAt)
	*item = refreshed
}
