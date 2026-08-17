// Package anidb is a client for the anidb.app streaming site (search → episodes →
// direct HLS stream URL). It produces playable.Release items so the release picker
// pipeline works unchanged: each episode's variants (audio × resolution) become
// rows where Group = "sub"/"dub" and Resolution = "1080"/"720"/"360".
package anidb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ani/internal/playable"
)

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	timeout   = 20 * time.Second
)

// baseURL is the site root (a var so tests can point it at httptest).
var baseURL = "https://anidb.app"

// Pooled client: Go's DefaultTransport reuses only ~2 idle conns/host. A pooled
// transport lets the aired-count prefetch (one search+episodes per airing anime)
// actually run in parallel.
var client = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
	},
	Timeout: timeout,
}

// debugLog is the always-on debug sink (set by main via SetDebugLog).
var debugLog io.Writer = io.Discard

// SetDebugLog sets the debug log destination (called from main).
func SetDebugLog(w io.Writer) { debugLog = w }

func dbg(format string, args ...any) {
	if debugLog != nil && debugLog != io.Discard {
		fmt.Fprintf(debugLog, format, args...)
	}
}

// Show is one anime from anidb.app. ID is the slug-num (e.g. "frieren-…-1663").
type Show struct {
	ID   string
	Name string
}

// Episode is one episode with its anidb numeric id and episode number. Number is
// float64 because anidb can have fractional specials (0.5, 5.5); AiredCount
// filters those out. Filler flags recaps/specials that shouldn't count as "aired".
type Episode struct {
	ID     int
	Number float64
	Filler bool
}

// ---- HTTP helper ----

func get(u string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// ---- Search ----

var cardRe = regexp.MustCompile(`anime/([a-z0-9-]+-[0-9]+)"[^>]*title="([^"]+)"`)

// Search queries anidb.app/browse and returns the matching shows.
func Search(query string) ([]Show, error) {
	u := baseURL + "/browse?q=" + url.QueryEscape(query)
	dbg("anidb: GET %s\n", u)
	body, status, err := get(u)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("anidb search: HTTP %d", status)
	}
	matches := cardRe.FindAllSubmatch(body, -1)
	out := make([]Show, 0, len(matches))
	for _, m := range matches {
		out = append(out, Show{
			ID:   string(m[1]),
			Name: htmlUnesc(string(m[2])),
		})
	}
	return out, nil
}

// ResolveShow searches by title (cleaned) and returns the top result.
func ResolveShow(title string) (Show, error) {
	shows, err := Search(cleanQuery(title))
	if err != nil {
		return Show{}, err
	}
	if len(shows) == 0 {
		return Show{}, fmt.Errorf("anidb: no results for %q", title)
	}
	return shows[0], nil
}

// ---- Episodes ----

// slugNum extracts the trailing numeric id from a slug (e.g. "frieren-…-1663" → "1663").
func slugNum(slug string) string {
	if i := strings.LastIndex(slug, "-"); i >= 0 {
		return slug[i+1:]
	}
	return slug
}

// Episodes returns the episode list for a show (by slug ID).
func Episodes(showID string) ([]Episode, error) {
	num := slugNum(showID)
	u := baseURL + "/api/frontend/anime/" + num + "/episodes"
	dbg("anidb: GET %s\n", u)
	body, status, err := get(u)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("anidb episodes: HTTP %d", status)
	}
	var resp struct {
		Episodes []struct {
			ID     int     `json:"id"`
			Number float64 `json:"number"`
			Filler bool    `json:"filler"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("anidb episodes: decode: %w", err)
	}
	out := make([]Episode, 0, len(resp.Episodes))
	for _, e := range resp.Episodes {
		out = append(out, Episode{ID: e.ID, Number: e.Number, Filler: e.Filler})
	}
	return out, nil
}

// AiredCount resolves the show by title and returns the latest aired episode number
// in MAL per-season terms (max anidb episode number − cumulative offset). Returns
// float64 so fractional specials (e.g. 3.5) are preserved in the display.
// For Slime S4 (anidb eps 73–88, offset 72): 88−72 = 16.0.
// For a show with a 3.5 special: 3.5.
func AiredCount(title string) float64 {
	show, err := ResolveShow(title)
	if err != nil {
		return 0
	}
	eps, err := Episodes(show.ID)
	if err != nil {
		return 0
	}
	if len(eps) == 0 {
		return 0
	}
	offset := EpisodeOffset(eps)
	return eps[len(eps)-1].Number - offset
}

// EpisodeOffset returns the cumulative numbering offset for a show's episode list.
// Uses the first whole-numbered (≥1) episode so fractional specials (0.5) at the
// start don't corrupt the mapping: anidb_ep = MAL_ep + offset.
// For Slime S4 (first real ep = 73): offset = 72.
// For a show starting at 1 (or with a 0.5 special before ep 1): offset = 0.
func EpisodeOffset(eps []Episode) float64 {
	for _, e := range eps {
		if e.Number >= 1 && e.Number == float64(int(e.Number)) {
			return e.Number - 1
		}
	}
	return 0
}

// ---- Stream resolution ----

var fileRe = regexp.MustCompile(`file:\s*'([^']+)'`)
var variantRe = regexp.MustCompile(`#EXT-X-STREAM-INF:[^\n]*RESOLUTION=(\d+)x(\d+)[^\n]*\s+(https?://[^\s]+)`)

// languagesResponse is the JSON from /api/frontend/episode/<id>/languages.
type languageEntry struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	EmbedURL string `json:"embed_url"`
}

// FetchReleases resolves the playable variants for a specific episode of a show.
// It returns one playable.Release per (audio × resolution) combination: Group is
// "sub" (jpn) or "dub" (eng); Resolution is the height (e.g. "1080"); StreamURL is
// the HLS variant URL (playable by mpv). The release picker's group filter selects
// sub/dub; the quality filter selects resolution.
func FetchReleases(showID string, episode int) ([]*playable.Release, error) {
	eps, err := Episodes(showID)
	if err != nil {
		return nil, err
	}
	if len(eps) == 0 {
		return nil, fmt.Errorf("anidb: no episodes for this show")
	}
	// anidb uses cumulative episode numbering across seasons (e.g. Slime S4
	// lists episodes 73–88). The offset converts between MAL per-season numbers
	// and anidb cumulative numbers: anidb_ep = MAL_ep + offset.
	offset := EpisodeOffset(eps)
	anidbEp := float64(episode) + offset
	// Find the episode ID matching the anidb episode number. If not listed
	// (not yet aired / numbering gap): return no releases. The picker shows an
	// empty list; substituting another episode would mislabel its streams and
	// corrupt MAL write-back (watched = pick.Episode).
	var epID int
	for _, e := range eps {
		if e.Number == anidbEp {
			epID = e.ID
			break
		}
	}
	if epID == 0 {
		dbg("anidb: episode %d (anidb %g) not available\n", episode, anidbEp)
		return nil, nil
	}
	malEp := episode

	// Get the embed URLs for this episode's languages.
	u := baseURL + "/api/frontend/episode/" + strconv.Itoa(epID) + "/languages"
	dbg("anidb: GET %s\n", u)
	body, status, err := get(u)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("anidb languages: HTTP %d", status)
	}
	var resp struct {
		Languages []languageEntry `json:"languages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("anidb languages: decode: %w", err)
	}

	var out []*playable.Release
	for _, lang := range resp.Languages {
		group := "sub"
		if lang.Code == "eng" {
			group = "dub"
		}
		embed := strings.ReplaceAll(lang.EmbedURL, `\/`, `/`)
		variants, err := resolveVariants(embed)
		if err != nil {
			dbg("anidb: %s embed resolve failed: %v\n", group, err)
			continue
		}
		for _, v := range variants {
			// Append "p" to numeric heights (e.g. "1080" → "1080p") so the
			// release picker's resolutionHeight/matchResolution recognise them
			// (they only match \d+p or \d+x\d+ patterns).
			res := v.height
			if _, e := strconv.Atoi(v.height); e == nil {
				res = v.height + "p"
			}
			out = append(out, &playable.Release{
				Title:      fmt.Sprintf("%s Episode %d", showID, malEp),
				Group:      group,
				Resolution: res,
				Episode:    malEp,
				StreamURL:  v.url,
			})
		}
	}
	// Sort: sub before dub, then highest resolution first.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group == "sub"
		}
		return out[i].Resolution > out[j].Resolution
	})
	return out, nil
}

type variant struct {
	height string // "1080", "720", "360"
	url    string
}

// resolveVariants fetches the embed page, extracts the master m3u8, and parses its
// variants.
func resolveVariants(embedURL string) ([]variant, error) {
	embedBody, _, err := get(embedURL)
	if err != nil {
		return nil, err
	}
	m := fileRe.FindSubmatch(embedBody)
	if m == nil {
		return nil, fmt.Errorf("no master m3u8 in embed page")
	}
	masterURL := string(m[1])

	masterBody, _, err := get(masterURL)
	if err != nil {
		return nil, err
	}
	variants := []variant{}
	for _, v := range variantRe.FindAllSubmatch(masterBody, -1) {
		variants = append(variants, variant{
			height: string(v[2]), // the height (e.g. "1080")
			url:    string(v[3]),
		})
	}
	if len(variants) == 0 {
		// Fallback: return the master itself (mpv picks the variant).
		return []variant{{height: "auto", url: masterURL}}, nil
	}
	return variants, nil
}

// ---- Helpers ----

// cleanQuery trims the MAL title for anidb search. The full title (including
// season/subtitle qualifiers like " - Ryoushu no Youjo" or "4th Season") is
// passed as-is — cutting at " - " or ":" strips season identifiers and makes
// the search match the wrong (earlier) season. anidb's search handles long
// romanized titles fine.
func cleanQuery(title string) string {
	return strings.TrimSpace(title)
}

func htmlUnesc(s string) string {
	return strings.NewReplacer("&#039;", "'", "&quot;", "\"", "&amp;", "&").Replace(s)
}
