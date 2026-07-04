package ui

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ani/internal/animetosho"
)

// ---- sorting (client-side; the API ignores sort params) ----

func sortByDateDesc(rs []*animetosho.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.DateAdded > rs[j].Entry.DateAdded })
}
func sortByDateAsc(rs []*animetosho.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.DateAdded < rs[j].Entry.DateAdded })
}
func sortBySizeAsc(rs []*animetosho.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.SizeBytes < rs[j].Entry.SizeBytes })
}
func sortBySizeDesc(rs []*animetosho.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.SizeBytes > rs[j].Entry.SizeBytes })
}

// SortedReleases returns a sorted copy of rs for the named sort order.
func SortedReleases(rs []*animetosho.Release, sortName string) []*animetosho.Release {
	cp := append([]*animetosho.Release(nil), rs...)
	switch NormalizeSort(sortName) {
	case "oldest":
		sortByDateAsc(cp)
	case "smallest":
		sortBySizeAsc(cp)
	case "largest":
		sortBySizeDesc(cp)
	default: // "newest"
		sortByDateDesc(cp)
	}
	return cp
}

// NormalizeSort maps a free-form sort name to a known value (default newest).
func NormalizeSort(s string) string {
	switch s {
	case "newest", "oldest", "smallest", "largest":
		return s
	}
	return "newest"
}

// ---- group pre-filter (--group; fzf fuzzy filter is also available) ----

// FilterByGroup keeps only releases whose group matches (case-insensitive).
// Empty or "All" returns everything.
func FilterByGroup(rs []*animetosho.Release, group string) []*animetosho.Release {
	if group == "" || strings.EqualFold(group, "All") {
		return rs
	}
	out := make([]*animetosho.Release, 0)
	for _, r := range rs {
		if strings.EqualFold(r.Group, group) {
			out = append(out, r)
		}
	}
	return out
}

// GroupLabel renders a display label for a group filter ("" → "All").
func GroupLabel(g string) string {
	if g == "" {
		return "All"
	}
	return g
}

// ---- series dedup + season detection ----

// DedupSeries collapses series entries that share an AniDB id (one id maps to
// many title-keys), keeping the title with the most torrents. Season is derived
// from that kept title only — scanning every title over-counts. Insertion order
// preserved; the caller sorts the enriched candidates.
func DedupSeries(in []animetosho.SeriesSummary) []animetosho.SeriesSummary {
	best := map[int]animetosho.SeriesSummary{}
	order := []int{}
	for i := range in {
		s := in[i]
		if cur, ok := best[s.AnidbAID]; !ok {
			best[s.AnidbAID] = s
			order = append(order, s.AnidbAID)
		} else if s.TorrentCount > cur.TorrentCount {
			best[s.AnidbAID] = s
		}
	}
	out := make([]animetosho.SeriesSummary, 0, len(order))
	for _, aid := range order {
		s := best[aid]
		s.Season = DetectSeason(s.Title)
		out = append(out, s)
	}
	return out
}

var seasonRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\d+)(?:st|nd|rd|th)\s+season`),
	regexp.MustCompile(`(?i)season\s*(\d+)`),
	regexp.MustCompile(`(?i)part\s*(\d+)`),
}

// DetectSeason pulls a season ordinal out of a (messy) title via known tokens.
func DetectSeason(title string) int {
	var vals []int
	for _, re := range seasonRes {
		for _, m := range re.FindAllStringSubmatch(title, -1) {
			vals = append(vals, atoiSafe(m[1]))
		}
	}
	max := 0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	return max
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
