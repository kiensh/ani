package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const releasePageSize = 20

// Release is a thin, regex-free view over an Entry's API fields.
type Release struct {
	Entry      *Entry
	Group      string
	Resolution string
	Episode    int
	IsBatch    bool
}

func toRelease(e *Entry) *Release {
	return &Release{
		Entry:      e,
		Group:      e.ReleaseGroup,
		Resolution: e.Resolution,
		Episode:    e.Series.EpisodeNumber,
		IsBatch:    e.IsBatch,
	}
}

func toReleases(entries []Entry) []*Release {
	out := make([]*Release, 0, len(entries))
	for i := range entries {
		out = append(out, toRelease(&entries[i]))
	}
	return out
}

// dedupSeries collapses series entries that share an AniDB id (one id maps to
// many title-keys), keeping the title with the most torrents. Season is derived
// from that kept title only — scanning every title over-counts (stray batch /
// cross-reference titles inject false season tokens). Insertion order is
// preserved; the caller sorts the enriched candidates.
func dedupSeries(in []SeriesSummary) []SeriesSummary {
	best := map[int]SeriesSummary{}
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
	out := make([]SeriesSummary, 0, len(order))
	for _, aid := range order {
		s := best[aid]
		s.Season = detectSeason(s.Title)
		out = append(out, s)
	}
	return out
}

// detectSeason pulls a season ordinal out of a (messy) title via known tokens.
// Returns 0 when no token is present.
func detectSeason(title string) int {
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

var seasonRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\d+)(?:st|nd|rd|th)\s+season`),
	regexp.MustCompile(`(?i)season\s*(\d+)`),
	regexp.MustCompile(`(?i)part\s*(\d+)`),
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// ---- anime candidate (dedup'd + enriched for the picker) ----

type AnimeCandidate struct {
	Aid          int
	CleanTitle   string
	Year         string
	Season       int
	EpisodeCount int
	TorrentCount int
	PicURL       string // full cover URL, "" if unknown
}

// enrichAnime concurrently fetches clean title/year/episode_count/picurl per
// aid. On per-aid error it keeps the search title with empty year.
func enrichAnime(in []SeriesSummary) []AnimeCandidate {
	out := make([]AnimeCandidate, len(in))
	var wg sync.WaitGroup
	for i := range in {
		wg.Add(1)
		go func(i int, s SeriesSummary) {
			defer wg.Done()
			c := AnimeCandidate{
				Aid:          s.AnidbAID,
				Season:       s.Season,
				TorrentCount: s.TorrentCount,
				CleanTitle:   s.Title,
			}
			if title, year, eps, pic, err := seriesMeta(s.AnidbAID); err == nil && title != "" {
				c.CleanTitle = title
				c.Year = year
				c.EpisodeCount = eps
				c.PicURL = pic
			}
			out[i] = c
		}(i, in[i])
	}
	wg.Wait()
	return out
}

// pickAnime shows the picker. With thumbnails enabled it prints each cover
// above its numbered label; otherwise (or under fzf) it falls back to a plain
// numbered list / fzf.
func pickAnime(cands []AnimeCandidate, showThumbs, useFzf bool) (int, error) {
	lines := make([]string, len(cands))
	for i, c := range cands {
		lines[i] = renderAnimeLine(c)
	}
	if useFzf && fzfAvailable() {
		return pickIndex(lines, fmt.Sprintf("Anime (%d)", len(cands)), "Select anime", useFzf)
	}
	fmt.Printf("\nAnime (%d)\n\n", len(cands))
	for i, c := range cands {
		if showThumbs && c.PicURL != "" {
			if err := printCover(c.PicURL); err == nil {
				fmt.Println()
			}
		}
		fmt.Printf("  %d) %s\n", i+1, lines[i])
	}
	for {
		fmt.Printf("\nSelect anime [1-%d]: ", len(cands))
		line, err := readLine()
		if err != nil {
			return 0, err
		}
		n, perr := strconv.Atoi(line)
		if perr == nil && n >= 1 && n <= len(cands) {
			return n - 1, nil
		}
		fmt.Printf("  invalid: %q\n", line)
	}
}

func sortCandidatesByYear(c []AnimeCandidate) {
	sort.SliceStable(c, func(i, j int) bool {
		yi := atoiSafe(c[i].Year)
		yj := atoiSafe(c[j].Year)
		if yi != yj {
			return yi > yj
		}
		return c[i].TorrentCount > c[j].TorrentCount
	})
}

func renderAnimeLine(c AnimeCandidate) string {
	title := c.CleanTitle
	if title == "" {
		title = "(unknown)"
	}
	var b strings.Builder
	b.WriteString(truncate(title, 52))
	if c.Year != "" {
		fmt.Fprintf(&b, "  (%s)", c.Year)
	}
	if c.Season > 0 {
		fmt.Fprintf(&b, "  S%d", c.Season)
	}
	if c.EpisodeCount > 0 {
		fmt.Fprintf(&b, "  %dep", c.EpisodeCount)
	}
	fmt.Fprintf(&b, "  %d releases", c.TorrentCount)
	return b.String()
}

// ---- sorting (client-side; the API ignores sort params) ----

func sortByDateDesc(rs []*Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.DateAdded > rs[j].Entry.DateAdded })
}
func sortByDateAsc(rs []*Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.DateAdded < rs[j].Entry.DateAdded })
}
func sortBySizeAsc(rs []*Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.SizeBytes < rs[j].Entry.SizeBytes })
}
func sortBySizeDesc(rs []*Release) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Entry.SizeBytes > rs[j].Entry.SizeBytes })
}

// sortedReleases returns a sorted copy of rs for the named sort order.
func sortedReleases(rs []*Release, sortName string) []*Release {
	cp := append([]*Release(nil), rs...)
	switch sortName {
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

// ---- group filter ----

type groupCount struct {
	Name  string
	Count int
}

func distinctGroups(rs []*Release) []groupCount {
	counts := map[string]int{}
	for _, r := range rs {
		name := r.Group
		if name == "" {
			name = "Unknown"
		}
		counts[name]++
	}
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []groupCount{{Name: "All", Count: len(rs)}}
	for _, n := range names {
		out = append(out, groupCount{Name: n, Count: counts[n]})
	}
	return out
}

func filterByGroup(rs []*Release, group string) []*Release {
	if group == "" || strings.EqualFold(group, "All") {
		return rs
	}
	out := make([]*Release, 0)
	for _, r := range rs {
		if strings.EqualFold(r.Group, group) {
			out = append(out, r)
		}
	}
	return out
}

// ---- rendering ----

func renderReleaseLine(r *Release) string {
	date := "?"
	if t, err := time.Parse(time.RFC3339, r.Entry.DateAdded); err == nil {
		date = t.Format("2006-01-02")
	}
	grp := r.Group
	if grp == "" {
		grp = "?"
	}
	res := r.Resolution
	if res == "" {
		res = "-"
	}
	eps := "-"
	if r.IsBatch {
		eps = "BATCH"
	} else if r.Episode > 0 {
		eps = fmt.Sprintf("ep%d", r.Episode)
	}
	return fmt.Sprintf("%s %-15s %-5s %9s %-6s %5d↑ %4d↓  %s",
		date, "["+truncate(grp, 13)+"]", res, humanSize(r.Entry.SizeBytes), eps,
		r.Entry.Seeders, r.Entry.Leechers, truncate(r.Entry.Title, 50))
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---- menus ----

var sortOptions = []struct {
	name, label string
}{
	{"newest", "newest (date, newest first)"},
	{"oldest", "oldest (date, oldest first)"},
	{"smallest", "smallest (size, asc)"},
	{"largest", "largest (size, desc)"},
}

func pickGroup(groups []groupCount, current string, useFzf bool) (string, error) {
	lines := make([]string, len(groups))
	for i, g := range groups {
		mark := ""
		if current != "" && strings.EqualFold(g.Name, current) {
			mark = "  <="
		}
		lines[i] = fmt.Sprintf("%-16s %5d%s", g.Name, g.Count, mark)
	}
	cur := current
	if cur == "" {
		cur = "All"
	}
	idx, err := pickIndex(lines, "Group (currently "+cur+")", "Select group", useFzf)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(groups[idx].Name, "All") {
		return "", nil
	}
	return groups[idx].Name, nil
}

func pickSort(current string, useFzf bool) (string, error) {
	lines := make([]string, len(sortOptions))
	for i, o := range sortOptions {
		mark := ""
		if o.name == current {
			mark = "  <="
		}
		lines[i] = o.label + mark
	}
	idx, err := pickIndex(lines, "Sort (currently "+current+")", "Select sort", useFzf)
	if err != nil {
		return "", err
	}
	return sortOptions[idx].name, nil
}

// ---- release browser (command loop) ----

// browseState is the browser's mutable view state. It lives in the caller
// (main.go's post-play loop) so the group filter, sort, and page persist across
// play/download round-trips instead of resetting each re-entry.
type browseState struct {
	group string
	sort  string
	page  int
}

// browseReleases runs the paginated, filterable, sortable release list and
// returns the user's chosen release. It mutates st so state survives re-entry.
func browseReleases(all []*Release, st *browseState, useFzf bool) (*Release, error) {
	if st.sort == "" {
		st.sort = "newest"
	}

	for {
		items := sortedReleases(filterByGroup(all, st.group), st.sort)
		if len(items) == 0 {
			return nil, fmt.Errorf("no releases for group %q", st.group)
		}
		totalPages := (len(items) + releasePageSize - 1) / releasePageSize
		if st.page >= totalPages {
			st.page = totalPages - 1
		}
		if st.page < 0 {
			st.page = 0
		}
		start := st.page * releasePageSize
		end := start + releasePageSize
		if end > len(items) {
			end = len(items)
		}

		groupLabel := st.group
		if groupLabel == "" {
			groupLabel = "All"
		}
		fmt.Printf("\n  %d releases   [%s]  %s   page %d/%d\n\n", len(items), groupLabel, st.sort, st.page+1, totalPages)
		for i := start; i < end; i++ {
			fmt.Printf("  %2d) %s\n", i+1, renderReleaseLine(items[i]))
		}

		in, err := readCommand(fmt.Sprintf("\n  select %d-%d | n next  p prev  g group  s sort  q quit: ", start+1, end))
		if err != nil {
			return nil, err
		}
		in = strings.TrimSpace(in)

		switch in {
		case "", "?":
			continue
		case "q":
			return nil, errCancelled
		case "n":
			if st.page+1 < totalPages {
				st.page++
			}
		case "p":
			if st.page > 0 {
				st.page--
			}
		case "g":
			g, err := pickGroup(distinctGroups(all), st.group, useFzf)
			if err != nil {
				return nil, err
			}
			st.group = g
			st.page = 0
		case "s":
			s, err := pickSort(st.sort, useFzf)
			if err != nil {
				return nil, err
			}
			st.sort = s
			st.page = 0
		default:
			n, perr := strconv.Atoi(in)
			if perr != nil || n < start+1 || n > end {
				fmt.Printf("  invalid: %q (type a number %d-%d or n/p/g/s/q)\n", in, start+1, end)
				continue
			}
			return items[n-1], nil
		}
	}
}

func normalizeSort(s string) string {
	switch s {
	case "newest", "oldest", "smallest", "largest":
		return s
	}
	return "newest"
}
