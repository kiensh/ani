package ui

import (
	"strings"

	"ani/internal/animetosho"
	"ani/internal/mal"
)

// SearchAnidbSeries searches animetosho by title (and shortened variants) and
// returns the de-duplicated hits (by anidb_aid) from the first variant that
// matches. Used by the manual animetosho-series fallback picker.
func SearchAnidbSeries(title string) []animetosho.SeriesSummary {
	for _, candidate := range titleVariants(title) {
		series, err := animetosho.SearchSeries(candidate)
		if err != nil {
			continue
		}
		if series = DedupSeries(series); len(series) > 0 {
			return series
		}
	}
	return nil
}

// titleVariants returns progressively shorter versions of a title for fallback
// searching (animetosho indexes franchises under the base name).
func titleVariants(title string) []string {
	var out []string
	out = append(out, title)
	stripped := mal.StripSeasonSuffix(title)
	if stripped != title {
		out = append(out, stripped)
	}
	words := strings.Fields(stripped)
	if len(words) > 3 {
		out = append(out, strings.Join(words[:3], " "))
	}
	return out
}
