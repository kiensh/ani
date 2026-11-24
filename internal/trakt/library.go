package trakt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Title identifies a Trakt movie or series. ID is a Trakt ID, not a provider
// ID. The kind and ID together identify an item; names and years need not be unique.
type Title struct {
	Name, Kind string
	Year, ID   int
}

// HistoryItem describes one play, not an aggregate. Title is the parent series
// for episodes; season zero is a special. Movies have no season or episode.
type HistoryItem struct {
	Title           Title
	Season, Episode int
	WatchedAt       time.Time
}

type traktIDs struct {
	Trakt int `json:"trakt"`
}

type mediaTitle struct {
	Title string
	Year  int
	IDs   traktIDs
}

func (m *mediaTitle) title(kind string) (Title, bool) {
	if m == nil || m.IDs.Trakt <= 0 {
		return Title{}, false
	}
	// Remote labels go directly into the terminal. Remove control and format
	// characters (including bidi overrides), then collapse line separators.
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, m.Title)
	name := strings.Join(strings.Fields(s), " ")
	return Title{Name: name, Kind: kind, Year: m.Year, ID: m.IDs.Trakt}, name != ""
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

// Search returns candidates for explicit selection, with exact results first
// and then broader results. A lone candidate is not proof of an identity match.
func (c *Client) Search(ctx context.Context, kind, name string) ([]Title, error) {
	apiType, err := mediaType(kind)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 512 {
		return nil, errors.New("Trakt: enter a title of at most 512 bytes")
	}
	// https://github.com/trakt/trakt-api/blob/master/projects/developer/src/lib/guides/search.md
	var titles []Title
	seen := make(map[int]bool)
	for _, suffix := range []string{"/exact", ""} {
		query := name
		if suffix == "" {
			// Punctuation in a catalog title is literal, not a search operator.
			var escaped strings.Builder
			for _, r := range name {
				if strings.ContainsRune(`+-&|!(){}[]^"~*?:/\`, r) {
					escaped.WriteByte('\\')
				}
				escaped.WriteRune(r)
			}
			query = escaped.String()
		}
		path := "/search/" + apiType + suffix + "?page=1&limit=50&query=" + url.QueryEscape(query)
		var rows []mediaRow
		if err := c.do(ctx, http.MethodGet, path, nil, &rows, false); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("Trakt: missing title search results")
		}
		for _, r := range rows {
			if t, ok := r.title(); ok && t.Kind == kind && !seen[t.ID] {
				titles = append(titles, t)
				seen[t.ID] = true
			}
		}
	}
	return titles, nil
}

// Watchlist reads movies and shows (not season/episode list entries) in the
// account's ranked order. A failed page never masquerades as a complete list.
func (c *Client) Watchlist(ctx context.Context) ([]Title, error) {
	var titles []Title
	type key struct {
		kind string
		id   int
	}
	seen := make(map[key]bool)
	for page := 1; ; page++ {
		var rows []mediaRow
		path := fmt.Sprintf("/users/me/watchlist/movie,show/rank?page=%d&limit=100", page)
		h, err := c.doWithHeaders(ctx, http.MethodGet, path, nil, &rows, true)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("Trakt: missing watchlist page")
		}
		if current := h.Get("X-Pagination-Page"); current != "" && current != strconv.Itoa(page) {
			return nil, errors.New("Trakt: returned the wrong watchlist page")
		}
		before := len(titles)
		for _, r := range rows {
			if t, ok := r.title(); ok && !seen[key{t.Kind, t.ID}] {
				seen[key{t.Kind, t.ID}] = true
				titles = append(titles, t)
			}
		}
		// Trakt can clamp the requested limit. Prefer its page count; when
		// headers are absent, continue even after a short page until [] arrives.
		if raw := h.Get("X-Pagination-Page-Count"); raw != "" {
			count, err := strconv.Atoi(raw)
			if err != nil || count < 0 || (count == 0 && len(rows) != 0) {
				return nil, errors.New("Trakt: invalid watchlist page count")
			}
			if page >= count {
				return titles, nil
			}
		} else if len(rows) == 0 {
			return titles, nil
		}
		if len(titles) == before {
			return nil, errors.New("Trakt: watchlist pagination did not advance")
		}
	}
}

type mediaEpisode struct {
	Season *int // Distinguish an absent season from an explicit season zero.
	Number int
	IDs    traktIDs
}

type historyRow struct {
	mediaRow
	Episode   *mediaEpisode
	WatchedAt time.Time `json:"watched_at"`
}

// History returns the 20 most recent plays, newest first. This is a recent
// activity view, not a full history export; repeated plays are kept separate.
func (c *Client) History(ctx context.Context) ([]HistoryItem, error) {
	var rows []historyRow
	if err := c.do(ctx, http.MethodGet, "/users/me/history?page=1&limit=20", nil, &rows, true); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("Trakt: missing recent history")
	}
	var items []HistoryItem
	for _, r := range rows {
		item := HistoryItem{WatchedAt: r.WatchedAt}
		var ok bool
		switch r.Type {
		case "movie":
			item.Title, ok = r.Movie.title("movie")
		case "episode":
			e := r.Episode
			if e == nil || e.Season == nil || *e.Season < 0 || e.Number <= 0 {
				continue
			}
			item.Title, ok = r.Show.title("series")
			item.Season, item.Episode = *e.Season, e.Number
		}
		if ok {
			items = append(items, item)
		}
		if len(items) == 20 {
			break
		}
	}
	return items, nil
}

type target struct {
	group string // Plural Trakt object name: movies, shows, or episodes.
	id    int
}

func (c *Client) AddWatchlist(ctx context.Context, t Title) error {
	kind, err := mediaType(t.Kind)
	if err != nil {
		return err
	}
	if t.ID <= 0 {
		return errors.New("Trakt: watchlist requires a confirmed Trakt ID")
	}
	return c.syncAdd(ctx, "/sync/watchlist", target{kind + "s", t.ID})
}

// historyTarget can only return movie or episode IDs, never a show or season
// ID. Accidentally submitting the latter to /sync/history logs every episode.
func (c *Client) historyTarget(ctx context.Context, t Title, season, episode int) (target, error) {
	if t.ID <= 0 {
		return target{}, errors.New("Trakt: history requires a confirmed Trakt ID")
	}
	switch t.Kind {
	case "movie":
		if season != 0 || episode != 0 {
			return target{}, errors.New("Trakt: movies must not have a season or episode")
		}
		return target{"movies", t.ID}, nil
	case "series":
		if season < 0 || episode <= 0 {
			return target{}, errors.New("Trakt: choose a specific season and episode")
		}
		var e mediaEpisode
		path := fmt.Sprintf("/shows/%d/seasons/%d/episodes/%d", t.ID, season, episode)
		if err := c.do(ctx, http.MethodGet, path, nil, &e, false); err != nil {
			return target{}, fmt.Errorf("resolve Trakt episode S%dE%d: %w", season, episode, err)
		}
		if e.Season == nil || *e.Season != season || e.Number != episode || e.IDs.Trakt <= 0 {
			return target{}, fmt.Errorf("Trakt: could not confirm the ID for episode S%dE%d", season, episode)
		}
		return target{"episodes", e.IDs.Trakt}, nil
	default:
		return target{}, errors.New("Trakt: expected a movie or series")
	}
}

func (c *Client) watched(ctx context.Context, item target) (bool, error) {
	var rows []historyRow
	path := fmt.Sprintf("/users/me/history/%s/%d?page=1&limit=1", item.group, item.id)
	if err := c.do(ctx, http.MethodGet, path, nil, &rows, true); err != nil {
		return false, err
	}
	if rows != nil && len(rows) == 0 {
		return false, nil
	}
	if len(rows) == 1 {
		r := rows[0]
		if item.group == "movies" && r.Type == "movie" && r.Movie != nil && r.Movie.IDs.Trakt == item.id ||
			item.group == "episodes" && r.Type == "episode" && r.Episode != nil && r.Episode.IDs.Trakt == item.id {
			return true, nil
		}
	}
	// A null/malformed or mismatched response is not evidence of no plays.
	return false, errors.New("Trakt: could not verify watched status for this item")
}

// IsWatched checks history for exactly one movie or episode, regardless of
// how large the user's history is.
func (c *Client) IsWatched(ctx context.Context, t Title, season, episode int) (bool, error) {
	item, err := c.historyTarget(ctx, t, season, episode)
	if err != nil {
		return false, err
	}
	return c.watched(ctx, item)
}

// MarkWatched records the first play only. Trakt's history POST appends, not
// upserts: never send it after an inconclusive read or retry an ambiguous POST.
// The check and write aren't atomic against another device marking the same play.
func (c *Client) MarkWatched(ctx context.Context, t Title, season, episode int) error {
	item, err := c.historyTarget(ctx, t, season, episode)
	if err != nil {
		return err
	}
	already, err := c.watched(ctx, item)
	if err != nil || already {
		return err
	}
	return c.syncAdd(ctx, "/sync/history", item)
}

func (c *Client) syncAdd(ctx context.Context, path string, item target) error {
	type object struct {
		IDs traktIDs `json:"ids"`
	}
	body := map[string][]object{item.group: {{IDs: traktIDs{Trakt: item.id}}}}
	var result struct {
		Added, Existing, Updated map[string]int
		NotFound                 map[string][]json.RawMessage `json:"not_found"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &result, true); err != nil {
		var status *statusError
		if path == "/sync/watchlist" && errors.As(err, &status) && status.StatusCode == 420 {
			return fmt.Errorf("Trakt watchlist limit reached; remove an item before adding another: %w", err)
		}
		return err
	}
	for _, missing := range result.NotFound {
		if len(missing) != 0 {
			return errors.New("Trakt: selected item not found; the update was not confirmed")
		}
	}
	count := result.Added[item.group]
	if path == "/sync/watchlist" {
		count += result.Existing[item.group]
	} else {
		count += result.Updated[item.group]
	}
	if count != 1 {
		return errors.New("Trakt: sync response did not confirm the requested item")
	}
	return nil
}
