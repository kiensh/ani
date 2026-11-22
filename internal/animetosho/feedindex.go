package animetosho

import (
	"net/url"
	"strconv"
)

// EpisodeFeed is one page of the site-wide recent uploads, grouped by AniDB id.
// Absence isn't evidence that a show has no episodes.
type EpisodeFeed map[int][]Entry

func FetchEpisodeFeed() (EpisodeFeed, error) {
	// A fractional special anywhere in the site-wide feed must not fail JSON
	// decoding for every other show. Retain only the fields the count uses.
	var page struct {
		Data []struct {
			ReleaseGroup string `json:"release_group"`
			IsBatch      bool   `json:"is_batch"`
			Series       struct {
				Aid     int     `json:"anidb_aid"`
				Episode float64 `json:"episode_number"`
			} `json:"series"`
		} `json:"data"`
	}
	if err := toshoGet(toshoSearchPath, url.Values{"limit": {strconv.Itoa(pageLimit)}}, &page); err != nil {
		return nil, err
	}
	feed := make(EpisodeFeed)
	for _, e := range page.Data {
		aid, ep := e.Series.Aid, int(e.Series.Episode)
		if aid > 0 && ep > 0 && float64(ep) == e.Series.Episode && e.ReleaseGroup != "" && !e.IsBatch {
			feed[aid] = append(feed[aid], Entry{ReleaseGroup: e.ReleaseGroup, Series: Series{EpisodeNumber: ep}})
		}
	}
	dbg("aired snapshot: %d recent uploads, %d anime\n", len(page.Data), len(feed))
	return feed, nil
}

// Latest uses the same release-group agreement and season-number rules as a
// per-series lookup. With insufficient evidence, leave the count unknown.
func (feed EpisodeFeed) Latest(aid, total int) (int, bool) {
	entries := feed[aid]
	if len(entries) == 0 {
		return 0, false
	}
	n := latestFromEntries(aid, total, entries[:min(len(entries), airedLimit)])
	return n, n > 0
}
