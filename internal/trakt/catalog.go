package trakt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Title is a catalog record, also used for snapshots embedded in account lists.
// Kind (movie or series) and the Trakt ID identify the title; names and years
// are not unique. Optional metadata stays zero-valued when unavailable.
type Title struct {
	Name, Kind, PosterURL, Overview, Genres, IMDb string
	Year, ID, TMDB, Runtime                       int
	Rating                                        float64
}

type titleKey struct {
	kind string
	id   int
}

func (t Title) key() titleKey { return titleKey{t.Kind, t.ID} }

// Episode carries the Trakt episode ID (not the show or season ID).
// Season zero designates specials; Number starts at one.
type Episode struct {
	Season, Number, ID int
	Name               string
}

type traktIDs struct {
	Trakt int    `json:"trakt"`
	IMDb  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
}

type mediaTitle struct {
	Title, Overview string
	Year, Runtime   int
	Rating          float64
	Genres          []string
	IDs             traktIDs
	Images          json.RawMessage // Optional artwork must not invalidate the title.
}

var (
	imdbID      = regexp.MustCompile(`^tt[0-9]{7,12}$`)
	traktPoster = regexp.MustCompile(`^https://(?:media|walter|walter-r2)\.trakt\.tv/images/(?:movies|shows)/[0-9]{3}/[0-9]{3}/[0-9]{3}/posters/(?:thumb|medium|full)/[A-Za-z0-9_-]{1,128}\.(?:(?:jpe?g|png)(?:\.webp)?|webp)$`)
	// Current API responses use media.trakt.tv/shows/<id>/medium/poster.webp;
	// older cached account rows still use the sharded /images/... paths above.
	mediaPoster = regexp.MustCompile(`^https://media\.trakt\.tv/(?:movies|shows)/[1-9][0-9]{0,11}/(?:thumb|medium|full)/poster\.webp$`)
	tmdbPoster  = regexp.MustCompile(`^https://image\.tmdb\.org/t/p/(?:w92|w154|w185|w342|w500|w780|original)/[A-Za-z0-9_-]{1,128}\.(?:jpg|jpeg|png|webp)$`)
)

// ValidPosterURL accepts absolute HTTPS poster URLs only; recheck on redirect.
// Trakt's images guide documents scheme-less WebP paths under /images/.../posters/.
// No queries, fragments, ports, escaped paths, or arbitrary CDN subdomains.
func ValidPosterURL(raw string) bool {
	return len(raw) <= 256 && (mediaPoster.MatchString(raw) || traktPoster.MatchString(raw) || tmdbPoster.MatchString(raw))
}

func posterURL(raw json.RawMessage) string {
	var images struct{ Poster []string }
	if len(raw) == 0 || json.Unmarshal(raw, &images) != nil {
		return ""
	}
	for _, p := range images.Poster {
		if !strings.HasPrefix(p, "https://") {
			p = "https://" + p
		}
		if ValidPosterURL(p) {
			return p
		}
	}
	return ""
}

// Trakt labels go directly into a terminal. Strip control and format characters
// (including bidi overrides) and collapse whitespace, including line separators.
func displayText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func (m *mediaTitle) title(kind string) (Title, bool) {
	if m == nil || m.IDs.Trakt <= 0 {
		return Title{}, false
	}
	t := Title{
		Name: displayText(m.Title), Kind: kind, ID: m.IDs.Trakt, Year: m.Year,
		TMDB: max(0, m.IDs.TMDB), Runtime: max(0, m.Runtime),
		PosterURL: posterURL(m.Images), Overview: displayText(m.Overview),
		Genres: displayText(strings.Join(m.Genres, ", ")),
	}
	if imdbID.MatchString(m.IDs.IMDb) {
		t.IMDb = m.IDs.IMDb
	}
	if m.Rating >= 0 && m.Rating <= 10 {
		t.Rating = m.Rating
	}
	return t, t.Name != ""
}

type mediaRow struct {
	Type        string
	Movie, Show *mediaTitle
}

func (r mediaRow) title() (Title, bool) {
	switch r.Type {
	case "movie":
		return r.Movie.title("movie")
	case "show":
		return r.Show.title("series")
	default:
		return Title{}, false
	}
}

func mediaType(kind string) (string, error) {
	switch kind {
	case "movie":
		return "movie", nil
	case "series":
		return "show", nil
	default:
		return "", errors.New("Trakt: expected a movie or series")
	}
}

// SearchTitles combines exact then broader public movie/show results, ordered
// by Trakt relevance within each page and deduplicated by (kind, Trakt ID).
func (c *Client) SearchTitles(ctx context.Context, query string) ([]Title, error) {
	return c.searchTitles(ctx, "movie,show", query)
}

// Search is the single-kind version of SearchTitles.
func (c *Client) Search(ctx context.Context, kind, name string) ([]Title, error) {
	apiType, err := mediaType(kind)
	if err != nil {
		return nil, err
	}
	return c.searchTitles(ctx, apiType, name)
}

func (c *Client) searchTitles(ctx context.Context, apiType, name string) ([]Title, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 512 {
		return nil, errors.New("Trakt: enter a title of at most 512 bytes")
	}
	// https://github.com/trakt/trakt-api/blob/master/projects/developer/src/lib/guides/search.md
	var titles []Title
	seen := make(map[titleKey]bool)
	for _, suffix := range []string{"/exact", ""} {
		query := name
		if suffix == "" {
			// Title punctuation is literal, not a search operator.
			var escaped strings.Builder
			for _, r := range name {
				if strings.ContainsRune(`+-&|!(){}[]^"~*?:/\`, r) {
					escaped.WriteByte('\\')
				}
				escaped.WriteRune(r)
			}
			query = escaped.String()
		}
		path := "/search/" + apiType + suffix + "?extended=full&page=1&limit=50&query=" + url.QueryEscape(query)
		var rows []mediaRow
		if err := c.do(ctx, http.MethodGet, path, nil, &rows, false); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("Trakt: missing title search results")
		}
		for _, r := range rows {
			if apiType != "movie,show" && r.Type != apiType {
				continue
			}
			if t, ok := r.title(); ok && !seen[t.key()] {
				titles = append(titles, t)
				seen[t.key()] = true
			}
		}
	}
	return titles, nil
}

// TitleDetails refreshes one known record from its canonical summary. Never
// search by label, or attach a summary for a different ID to an account row.
func (c *Client) TitleDetails(ctx context.Context, t Title) (Title, error) {
	apiType, err := mediaType(t.Kind)
	if err != nil {
		return Title{}, err
	}
	if t.ID <= 0 {
		return Title{}, errors.New("Trakt: title details require a confirmed Trakt ID")
	}
	var m mediaTitle
	path := fmt.Sprintf("/%ss/%d?extended=full", apiType, t.ID)
	if err := c.do(ctx, http.MethodGet, path, nil, &m, false); err != nil {
		return Title{}, err
	}
	details, ok := m.title(t.Kind)
	if !ok || details.ID != t.ID {
		return Title{}, errors.New("Trakt: could not confirm the selected title's identity")
	}
	return details, nil
}

// RecentReleases combines Trakt's curated public movie-release and new-series
// calendars for today and the preceding six UTC dates, most recent first. Movie
// releases are day-granular; TV premieres that haven't aired yet are excluded.
func (c *Client) RecentReleases(ctx context.Context) ([]Title, error) {
	return c.recentReleases(ctx, time.Now())
}

func (c *Client) recentReleases(ctx context.Context, now time.Time) ([]Title, error) {
	const days = 7
	now = now.UTC()
	start := now.Truncate(24*time.Hour).AddDate(0, 0, 1-days)
	type release struct {
		title Title
		date  time.Time
	}
	var releases []release
	for _, feed := range []string{"movies", "shows/new"} {
		var rows []struct {
			Released    string
			FirstAired  string `json:"first_aired"`
			Movie, Show *mediaTitle
			Episode     *mediaEpisode
		}
		path := fmt.Sprintf("/calendars/all/%s/%s/%d?extended=full", feed, start.Format(time.DateOnly), days)
		if err := c.do(ctx, http.MethodGet, path, nil, &rows, false); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("Trakt: missing release calendar")
		}
		for _, row := range rows {
			var date time.Time
			var err error
			var t Title
			var ok bool
			if feed == "movies" {
				date, err = time.Parse(time.DateOnly, row.Released)
				t, ok = row.Movie.title("movie")
			} else {
				// The calendar date belongs to the episode. Do not count later
				// episodes of a first season as fresh series premieres.
				if e := row.Episode; e != nil && (e.Season == nil || *e.Season != 1 || e.Number != 1) {
					continue
				}
				date, err = time.Parse(time.RFC3339, row.FirstAired)
				t, ok = row.Show.title("series")
			}
			if err == nil && ok && !date.Before(start) && !date.After(now) {
				releases = append(releases, release{t, date})
			}
		}
	}
	sort.SliceStable(releases, func(i, j int) bool { return releases[i].date.After(releases[j].date) })
	var titles []Title
	seen := make(map[titleKey]bool)
	for _, r := range releases {
		if !seen[r.title.key()] {
			seen[r.title.key()] = true
			titles = append(titles, r.title)
		}
	}
	return titles, nil
}

// Episodes uses Trakt's all-seasons expansion, not a streaming provider's
// directory. Return numbered episodes and specials in season/episode order.
func (c *Client) Episodes(ctx context.Context, show Title) ([]Episode, error) {
	if show.Kind != "series" || show.ID <= 0 {
		return nil, errors.New("Trakt: episode lookup requires a confirmed series ID")
	}
	var seasons []struct {
		Number   *int
		Episodes []mediaEpisode
	}
	path := fmt.Sprintf("/shows/%d/seasons?extended=episodes", show.ID)
	if err := c.do(ctx, http.MethodGet, path, nil, &seasons, false); err != nil {
		return nil, err
	}
	if seasons == nil {
		return nil, errors.New("Trakt: missing seasons")
	}
	var episodes []Episode
	byNumber := make(map[[2]int]int)
	byID := make(map[int][2]int)
	for _, s := range seasons {
		if s.Number == nil || *s.Number < 0 || s.Episodes == nil {
			return nil, errors.New("Trakt: missing season or episode data")
		}
		for _, e := range s.Episodes {
			if e.Season == nil || *e.Season != *s.Number || e.Number <= 0 || e.IDs.Trakt <= 0 {
				return nil, errors.New("Trakt: incomplete episode identity")
			}
			n := [2]int{*e.Season, e.Number}
			id := e.IDs.Trakt
			if prior, ok := byID[id]; ok && prior == n {
				continue
			}
			if _, used := byID[id]; used || byNumber[n] != 0 {
				return nil, errors.New("Trakt: conflicting episode identities")
			}
			byNumber[n], byID[id] = id, n
			episodes = append(episodes, Episode{*e.Season, e.Number, id, displayText(e.Title)})
		}
	}
	sort.Slice(episodes, func(i, j int) bool {
		a, b := episodes[i], episodes[j]
		return a.Season < b.Season || a.Season == b.Season && a.Number < b.Number
	})
	return episodes, nil
}
