package ui

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ani/internal/animetosho"
	"ani/internal/playable"
)

// ---- sorting (client-side; the API ignores sort params) ----

func sortByDateDesc(rs []*playable.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].DateAdded > rs[j].DateAdded })
}
func sortByDateAsc(rs []*playable.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].DateAdded < rs[j].DateAdded })
}
func sortBySizeAsc(rs []*playable.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].SizeBytes < rs[j].SizeBytes })
}
func sortBySizeDesc(rs []*playable.Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].SizeBytes > rs[j].SizeBytes })
}

// SortedReleases returns a sorted copy of rs for the named sort order.
func SortedReleases(rs []*playable.Release, sortName string) []*playable.Release {
	cp := append([]*playable.Release(nil), rs...)
	switch NormalizeSort(sortName) {
	case "oldest":
		sortByDateAsc(cp)
	case "smallest":
		sortBySizeAsc(cp)
	case "largest":
		sortBySizeDesc(cp)
	case "seeders":
		sort.SliceStable(cp, func(i, j int) bool { return cp[i].Seeders > cp[j].Seeders })
	default: // "newest"
		sortByDateDesc(cp)
	}
	return cp
}

type ReleaseSortOption struct{ Label, Value string }

var sharedReleaseSorts = []ReleaseSortOption{
	{"Newest", "newest"}, {"Oldest", "oldest"}, {"Smallest", "smallest"}, {"Largest", "largest"},
}
var torrentReleaseSorts = append(sharedReleaseSorts, ReleaseSortOption{"Most seeders", "seeders"})

// ReleaseSortOptions lists the orders offered by both release-picker menus.
func ReleaseSortOptions(provider string) []ReleaseSortOption {
	if provider == "hianime" {
		return sharedReleaseSorts
	}
	return torrentReleaseSorts
}

// SortForProvider falls back to newest when a saved order doesn't apply.
func SortForProvider(value, provider string) string {
	for _, o := range ReleaseSortOptions(provider) {
		if o.Value == value {
			return value
		}
	}
	return "newest"
}

// NormalizeSort accepts all known config values, including torrent-only ones.
func NormalizeSort(value string) string { return SortForProvider(value, "torrent") }

// ---- group pre-filter (--group; fzf fuzzy filter is also available) ----

// FilterByGroup keeps only releases whose group matches (case-insensitive).
// Empty or "All" returns everything.
func FilterByGroup(rs []*playable.Release, group string) []*playable.Release {
	if group == "" || strings.EqualFold(group, "All") {
		return rs
	}
	out := make([]*playable.Release, 0)
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
