// Package animetosho is a client for the animetosho feed/series JSON API.
package animetosho

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	httpTimeout     = 30 * time.Second

	// CoverBase is the prefix for AniDB cover images.
	CoverBase = "https://animetosho.xyz/static/img/anidb_covers/"
)

// toshoBase is the feed root (a var so tests can point it at httptest).
var toshoBase = "https://feed.animetosho.xyz"

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

	resp, err := http.DefaultClient.Do(req)
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
	params := url.Values{
		"limit":  {strconv.Itoa(pageLimit)},
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

// mixedGap is the minimum spread between two episode numbers released around the
// same day for them to count as the same aired episode numbered two ways — a
// small per-season number ("2") and a large cumulative one ("26" = S1+S2+…+S3).
// Two genuinely different episodes that air the same day are consecutive
// (spread 1–2), so 3 cleanly separates mixed numbering from a double release.
const mixedGap = 3

// dayWindow is how much upload-timing skew we tolerate when grouping releases of
// the same aired episode: per-season and cumulative groups may upload hours or a
// day apart. A weekly show's previous episode is 7 days back, safely outside it.
const dayWindow = 2 * 24 * time.Hour

// LatestEpisode returns the highest episode number that ≥ minGroups distinct
// release groups have put out — a same-day proxy for the latest aired episode.
// Counting distinct groups (not raw releases) ignores cumulative-numbered and
// preview/pre-release outliers, which come from few groups; and using the int
// episode_number directly (no title regex) avoids truncating 4-digit episodes
// (e.g. One Piece 1168).
//
// When a season-specific aid is polluted with BOTH per-season and cumulative
// numbering for the same aired episode — per-season "2" (many groups) and
// cumulative "26" (≥3 groups) released on the same day — the group-count filter
// alone can't tell them apart (both clear minGroups), so we detect it by date:
// the newest aired episode shows up under two numbers released within dayWindow,
// and the per-season (smaller) number wins.
//
// Returns 0 — which the caller treats as "unknown, fall back to Jikan" — if no
// episode meets the threshold or on error.
func LatestEpisode(aid int) int {
	entries, err := SeriesReleasesPage(aid, 0, 0) // page 1, all episodes, newest-first
	if err != nil {
		return 0
	}
	type epInfo struct {
		groups map[string]struct{}
		day    time.Time // latest date_added day (zero if none parsed)
	}
	eps := map[int]*epInfo{}
	for _, e := range entries {
		ep := e.Series.EpisodeNumber
		if ep <= 0 {
			continue
		}
		info := eps[ep]
		if info == nil {
			info = &epInfo{groups: map[string]struct{}{}}
			eps[ep] = info
		}
		info.groups[e.ReleaseGroup] = struct{}{}
		if d := parseDay(e.DateAdded); !d.IsZero() && (info.day.IsZero() || d.After(info.day)) {
			info.day = d
		}
	}

	// supported = episodes with ≥ minGroups distinct groups; newestDay = the
	// latest release day among them (defines "the newest aired episode").
	var supported []int
	var newestDay time.Time
	for ep, info := range eps {
		if len(info.groups) < minGroups {
			continue
		}
		supported = append(supported, ep)
		if !info.day.IsZero() && (newestDay.IsZero() || info.day.After(newestDay)) {
			newestDay = info.day
		}
	}
	if len(supported) == 0 {
		return 0
	}
	maxSupp := slices.Max(supported)
	// No usable dates → can't detect mixed numbering; fall back to the plain max.
	if newestDay.IsZero() {
		return maxSupp
	}

	// Among the supported episodes released on/around the newest day, if two
	// numbers ≥ mixedGap apart share that window they're the same aired episode
	// numbered two ways (per-season vs cumulative) — return the smaller one.
	var lo, hi int
	haveCluster := false
	for _, ep := range supported {
		d := eps[ep].day
		if d.IsZero() || absDuration(d.Sub(newestDay)) > dayWindow {
			continue
		}
		if !haveCluster {
			lo, hi = ep, ep
			haveCluster = true
			continue
		}
		lo, hi = min(lo, ep), max(hi, ep)
	}
	if haveCluster && hi-lo >= mixedGap {
		return lo
	}
	return maxSupp
}

// parseDay parses the date portion of an animetosho date_added value
// ("YYYY-MM-DD…") and truncates it to midnight. Zero time if absent/unparseable.
func parseDay(s string) time.Time {
	if len(s) < 10 {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return time.Time{}
	}
	return t
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
