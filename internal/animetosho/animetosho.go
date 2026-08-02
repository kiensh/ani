// Package animetosho is a client for the animetosho feed/series JSON API.
package animetosho

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"time"
)

const (
	toshoSeriesPath = "/json/v1/series"
	toshoAnidbPath  = "/json/v1/series/anidb/"
	toshoSearchPath = "/json/v1/search"
	pageLimit       = 100
	searchRowCap    = 400
	httpTimeout     = 15 * time.Second

	// CoverBase is the prefix for AniDB cover images.
	CoverBase = "https://animetosho.xyz/static/img/anidb_covers/"
)

// toshoBase is the feed root (a var so tests can point it at httptest).
var toshoBase = "https://feed.animetosho.xyz"

// toshoHTTP pools connections to the feed host. Go's http.DefaultClient reuses
// only ~2 idle conns per host (DefaultTransport.MaxIdleConnsPerHost=2), which
// serialized ani's concurrent per-series aired-episode fetches — ~20 series took
// 25–40s even though they were dispatched concurrently. A pooled transport lets
// them actually run in parallel (~3s). Verified: pooled client is ~12× faster
// than http.DefaultClient for the aired prefetch.
var toshoHTTP = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     0, // unlimited
	},
	Timeout: httpTimeout,
}

// SetToshoBaseForTest overrides the feed root and returns a restore func. For
// cross-package tests; same-package tests can set toshoBase directly.
func SetToshoBaseForTest(url string) func() {
	old := toshoBase
	toshoBase = url
	return func() { toshoBase = old }
}

// debugLog is the always-on debug sink — a file set by main via SetDebugLog on
// every run. Defaults to io.Discard so tests and library use stay silent.
// Mirrors internal/mal/debug.go.
var debugLog io.Writer = io.Discard

// SetDebugLog sets the always-on debug log destination (called from main, which
// opens <configdir>/ani/debug.log).
func SetDebugLog(w io.Writer) { debugLog = w }

// debugEcho, when true, also writes debug lines to stderr (driven by --debug).
var debugEcho bool

// SetDebugEcho toggles stderr echoing of debug lines (called from main under
// --debug; the TUI's alt screen hides stderr, so debugLog is the reliable
// record — main re-dumps the file after the TUI exits).
func SetDebugEcho(b bool) { debugEcho = b }

// dbg writes a debug line to the always-on log, and to stderr when debugEcho.
func dbg(format string, args ...any) {
	fmt.Fprintf(debugLog, format, args...)
	if debugEcho {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

// Series is the nested anime metadata on a release.
type Series struct {
	Title         string `json:"title"`
	Key           string `json:"key"`
	EpisodeNumber int    `json:"episode_number"`
	TVDBSeason    int    `json:"tvdb_season"`
	AnidbAID      int    `json:"anidb_aid"`
}

// Entry is the subset of the v1 release fields that ani uses.
type Entry struct {
	Title        string `json:"title"`
	Magnet       string `json:"magnet"`
	TorrentURL   string `json:"torrent_url"`
	InfoHash     string `json:"info_hash"`
	Seeders      int    `json:"seeders"`
	Leechers     int    `json:"leechers"`
	SizeBytes    int64  `json:"size_bytes"`
	FileCount    int    `json:"file_count"`
	ReleaseGroup string `json:"release_group"`
	Resolution   string `json:"resolution"`
	IsBatch      bool   `json:"is_batch"`
	DateAdded    string `json:"date_added"`
	Series       Series `json:"series"`
}

// Release is a thin, regex-free view over an Entry's API fields.
type Release struct {
	Entry      *Entry
	Group      string
	Resolution string
	Episode    int
	IsBatch    bool
}

// ToRelease projects an Entry into a Release.
func ToRelease(e *Entry) *Release {
	return &Release{
		Entry:      e,
		Group:      e.ReleaseGroup,
		Resolution: e.Resolution,
		Episode:    e.Series.EpisodeNumber,
		IsBatch:    e.IsBatch,
	}
}

// ToReleases projects a slice of Entries into Releases.
func ToReleases(entries []Entry) []*Release {
	out := make([]*Release, 0, len(entries))
	for i := range entries {
		out = append(out, ToRelease(&entries[i]))
	}
	return out
}

// SeriesSummary is one anime from /series?q=. Season is computed client-side
// (max season token across that aid's titles); the API has no reliable season.
type SeriesSummary struct {
	AnidbAID      int    `json:"anidb_aid"`
	Title         string `json:"title"`
	Key           string `json:"key"`
	TVDBSeason    int    `json:"tvdb_season"`
	TorrentCount  int    `json:"torrent_count"`
	LatestRelease string `json:"latest_release"`
	Season        int    `json:"-"`
}

type seriesSearchResponse struct {
	Data []SeriesSummary `json:"data"`
}

// searchResponse is the /json/v1/search payload: a flat list of releases. With
// no `q` the feed returns the newest uploads site-wide (each carries its series
// + episode), which powers the no-login `./ani` landing screen.
type searchResponse struct {
	Data []Entry `json:"data"`
}

type seriesDetailResponse struct {
	Data struct {
		Title        string  `json:"title"`
		Year         string  `json:"year"`
		EpisodeCount int     `json:"episode_count"`
		PicURL       string  `json:"picurl"`
		Releases     []Entry `json:"releases"`
	} `json:"data"`
}

func toshoGet(path string, params url.Values, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	u := toshoBase + path
	if encoded := params.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ani/0.1 (+https://animetosho.xyz)")
	req.Header.Set("Accept", "application/json")

	resp, err := toshoHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("animetosho returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode feed: %w", err)
	}
	return nil
}

// SearchSeries returns anime matching the query. The API returns one row per
// title-key (heavy anidb_aid duplication), so we paginate and let the caller
// dedup. Stops when a page is short, adds no new anidb_aid, or the row cap hits.
func SearchSeries(query string) ([]SeriesSummary, error) {
	var all []SeriesSummary
	seen := map[int]bool{}
	for offset := 0; offset < searchRowCap; offset += pageLimit {
		var resp seriesSearchResponse
		if err := toshoGet(toshoSeriesPath, url.Values{
			"q":      {query},
			"limit":  {strconv.Itoa(pageLimit)},
			"offset": {strconv.Itoa(offset)},
		}, &resp); err != nil {
			return nil, err
		}
		newAids := 0
		for _, s := range resp.Data {
			if !seen[s.AnidbAID] {
				seen[s.AnidbAID] = true
				newAids++
			}
		}
		all = append(all, resp.Data...)
		if len(resp.Data) < pageLimit || newAids == 0 {
			break
		}
	}
	return all, nil
}

// SeriesMeta fetches light per-series metadata (clean title, year, episode
// count, cover picurl) via the detail endpoint with limit=1 so the releases
// payload is tiny.
func SeriesMeta(aid int) (title, year string, episodes int, picURL string, err error) {
	var resp seriesDetailResponse
	if err := toshoGet(toshoAnidbPath+strconv.Itoa(aid), url.Values{
		"limit": {"1"},
	}, &resp); err != nil {
		return "", "", 0, "", err
	}
	pic := resp.Data.PicURL
	if pic != "" {
		pic = CoverBase + pic
	}
	return resp.Data.Title, resp.Data.Year, resp.Data.EpisodeCount, pic, nil
}

// SeriesReleasesPage fetches one page of releases for an AniDB id. When ep > 0
// the server filters to just that episode (verified: ?ep=N returns only
// episode-N releases), which keeps long series fast.
func SeriesReleasesPage(aid, offset, ep int) ([]Entry, error) {
	return seriesReleases(aid, offset, ep, pageLimit)
}

// seriesReleases is the limit-parameterized core of SeriesReleasesPage.
func seriesReleases(aid, offset, ep, limit int) ([]Entry, error) {
	params := url.Values{
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	if ep > 0 {
		params.Set("ep", strconv.Itoa(ep))
	}
	var resp seriesDetailResponse
	if err := toshoGet(toshoAnidbPath+strconv.Itoa(aid), params, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Releases, nil
}

// allReleasesCap bounds the "all episodes" (ep == 0) fetch so huge series like
// One Piece (~10k releases) don't paginate forever. Episode-scoped fetches
// (ep > 0) are already small (a single episode's releases) so they're uncapped.
const allReleasesCap = 500

// airedLimit is the page size LatestEpisode fetches. It only needs the newest few
// episodes (the latest aired, confirmed by ≥ minGroups release groups), so a small
// page is enough and far cheaper than pageLimit (100): ~20 newest releases cover
// the latest 1–3 episodes and their groups for almost every airing show.
const airedLimit = 20

// FetchReleases paginates releases for an AniDB id. With ep > 0 it returns just
// that episode's releases (fast); with ep == 0 ("all") it returns the whole
// series capped at allReleasesCap newest.
func FetchReleases(aid, ep int) ([]*Release, error) {
	var entries []Entry
	for offset := 0; ; offset += pageLimit {
		page, err := SeriesReleasesPage(aid, offset, ep)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page...)
		if ep == 0 && len(entries) >= allReleasesCap {
			break
		}
		if len(page) < pageLimit {
			break
		}
	}
	return ToReleases(entries), nil
}

// LatestReleases returns the most recent uploads site-wide (the search feed with
// no `q`). Each release carries its series + episode, so the list is playable
// directly. Used for the no-login `./ani` landing screen.
func LatestReleases(limit int) ([]*Release, error) {
	if limit <= 0 {
		limit = pageLimit
	}
	var resp searchResponse
	if err := toshoGet(toshoSearchPath, url.Values{
		"limit": {strconv.Itoa(limit)},
	}, &resp); err != nil {
		return nil, err
	}
	return ToReleases(resp.Data), nil
}

// minGroups is how many distinct release groups must have put out an episode for
// it to count as "aired". Cumulative-numbered files (a few groups number across
// all seasons) and preview/pre-release eps (one group) stay below this; real
// aired episodes are released by many groups.
const minGroups = 3

// seasonGap is the minimum gap between two supported episode numbers that signals
// a split between per-season numbering (low cluster) and cumulative-across-
// seasons numbering (high cluster). Cumulative offsets are typically ≥ one cour
// (~10+); within-season gaps (a couple of unsupported episodes) are ≤ ~3, so 8
// sits between. See LatestEpisode.
const seasonGap = 8

// LatestEpisode returns the latest aired episode number for an AniDB id, using
// episode-number agreement among release groups as the signal:
//
//   - Only episodes released by ≥ minGroups distinct groups count, which drops
//     one-group previews and few-group cumulative outliers.
//   - Re-uploads of old episodes have no effect — they don't introduce new
//     episode numbers, so they can't make a stale episode look "latest".
//   - total (the entry's MAL num_episodes, when known) is the primary
//     disambiguator: a real episode of this entry can't exceed it, so any
//     supported number above total is cross-season cumulative mislabeling (a
//     few groups number across all seasons) and is dropped; the answer is the
//     plain max of what remains. This handles unified multi-cour entries (e.g.
//     Yomi no Tsugai, 24 eps) where under-subscribed middle episodes form a gap
//     that would otherwise look like a per-season/cumulative split.
//   - When total is unknown (0), fall back to the gap-based walk: if a
//     season-specific aid carries BOTH per-season ("2") and cumulative ("26")
//     numbering for the same episodes, the cumulative numbers form a high
//     cluster separated from the per-season cluster by a large gap (≥ seasonGap);
//     the per-season (low) cluster's max is taken as the real latest aired.
//
// Returns 0 — which the caller treats as "unknown, fall back to Jikan" — if no
// episode reaches minGroups, or on error.
func LatestEpisode(aid, total int) int {
	entries, err := seriesReleases(aid, 0, 0, airedLimit) // newest airedLimit releases (latest episodes + their groups)
	if err != nil {
		dbg("LatestEpisode aid=%d total=%d fetch-err=%v -> 0\n", aid, total, err)
		return 0
	}
	groups := map[int]map[string]struct{}{} // ep -> set of release groups
	for _, e := range entries {
		ep := e.Series.EpisodeNumber
		if ep <= 0 {
			continue
		}
		if groups[ep] == nil {
			groups[ep] = map[string]struct{}{}
		}
		groups[ep][e.ReleaseGroup] = struct{}{}
	}

	// Keep only episodes with ≥ minGroups distinct groups.
	var supported []int
	for ep, gs := range groups {
		if len(gs) >= minGroups {
			supported = append(supported, ep)
		}
	}
	if len(supported) == 0 {
		dbg("LatestEpisode aid=%d total=%d supported=[] -> 0\n", aid, total)
		return 0
	}
	slices.Sort(supported)

	// Known total: a real episode can't exceed it, so drop supported numbers
	// above total (cross-season cumulative mislabeling) and return the plain
	// max. Yomi no Tsugai: total=24, supported=[2,3,4,12,13,14,15,16] -> 16,
	// not the gap-walk's 4.
	if total > 0 {
		latest := 0
		for _, ep := range supported {
			if ep <= total && ep > latest {
				latest = ep
			}
		}
		if latest > 0 {
			dbg("LatestEpisode aid=%d total=%d supported=%v -> %d (capped)\n", aid, total, supported, latest)
			return latest
		}
		// Every supported episode exceeded the total (only cumulative numbering
		// reached minGroups) — fall through to the gap-walk fallback below.
	}

	// total unknown: walk the per-season cluster from the lowest episode,
	// stopping at the first big gap (a per-season ↔ cumulative split). Its max
	// is the latest aired.
	latest := supported[0]
	for i := 1; i < len(supported); i++ {
		if supported[i]-supported[i-1] >= seasonGap {
			break
		}
		latest = supported[i]
	}
	dbg("LatestEpisode aid=%d total=%d supported=%v -> %d (gap-walk)\n", aid, total, supported, latest)
	return latest
}
