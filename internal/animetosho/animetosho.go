// Package animetosho is a client for the animetosho feed/series JSON API.
package animetosho

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

const (
	toshoBase       = "https://feed.animetosho.xyz"
	toshoSeriesPath = "/json/v1/series"
	toshoAnidbPath  = "/json/v1/series/anidb/"
	pageLimit       = 100
	searchRowCap    = 400
	httpTimeout     = 30 * time.Second

	// CoverBase is the prefix for AniDB cover images.
	CoverBase = "https://animetosho.xyz/static/img/anidb_covers/"
)

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
	AnidbAID     int    `json:"anidb_aid"`
	Title        string `json:"title"`
	Key          string `json:"key"`
	TVDBSeason   int    `json:"tvdb_season"`
	TorrentCount int    `json:"torrent_count"`
	Season       int    `json:"-"`
}

type seriesSearchResponse struct {
	Data []SeriesSummary `json:"data"`
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

// SeriesReleasesPage fetches one page of releases for an AniDB id.
func SeriesReleasesPage(aid, offset int) ([]Entry, error) {
	var resp seriesDetailResponse
	if err := toshoGet(toshoAnidbPath+strconv.Itoa(aid), url.Values{
		"limit":  {strconv.Itoa(pageLimit)},
		"offset": {strconv.Itoa(offset)},
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Releases, nil
}

// FetchSeriesReleases paginates /series/anidb/{aid} until a page is short.
func FetchSeriesReleases(aid int) ([]Entry, error) {
	var all []Entry
	for offset := 0; ; offset += pageLimit {
		page, err := SeriesReleasesPage(aid, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		fmt.Fprintf(os.Stderr, "\rfetching releases… %d", len(all))
		if len(page) < pageLimit {
			break
		}
	}
	fmt.Fprintln(os.Stderr)
	return all, nil
}
