package ui

import (
	"regexp"
	"strings"

	"ani/internal/animetosho"
)

// FallbackAnidbByTitle searches animetosho by title (and shortened variants)
// and returns the top anidb id (used when a MAL anime has no AniDB external
// link). Returns 0 if none found.
func FallbackAnidbByTitle(title string) int {
	for _, candidate := range titleVariants(title) {
		series, err := animetosho.SearchSeries(candidate)
		if err != nil {
			continue
		}
		series = DedupSeries(series)
		if len(series) > 0 {
			return series[0].AnidbAID
		}
	}
	return 0
}

// titleVariants returns progressively shorter versions of a title for fallback
// searching (animetosho indexes franchises under the base name).
func titleVariants(title string) []string {
	var out []string
	out = append(out, title)
	stripped := stripSeasonSuffix(title)
	if stripped != title {
		out = append(out, stripped)
	}
	words := strings.Fields(stripped)
	if len(words) > 3 {
		out = append(out, strings.Join(words[:3], " "))
	}
	return out
}

var seasonSuffixRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\s+\d+(?:st|nd|rd|th)\s+Season$`),
	regexp.MustCompile(`(?i)\s+Season\s+\d+$`),
	regexp.MustCompile(`(?i)\s+Part\s+\d+$`),
}

func stripSeasonSuffix(title string) string {
	for _, re := range seasonSuffixRes {
		title = re.ReplaceAllString(title, "")
	}
	return strings.TrimSpace(title)
}
