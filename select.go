package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

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
// from that kept title only — scanning every title over-counts. Insertion order
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
	switch normalizeSort(sortName) {
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

func normalizeSort(s string) string {
	switch s {
	case "newest", "oldest", "smallest", "largest":
		return s
	}
	return "newest"
}

// ---- group pre-filter (--group; fzf fuzzy filter is also available) ----

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

func groupLabel(g string) string {
	if g == "" {
		return "All"
	}
	return g
}

// ---- rendering ----

func renderReleaseFzfLine(r *Release) string {
	date := "??-??"
	if t, err := time.Parse(time.RFC3339, r.Entry.DateAdded); err == nil {
		date = t.Format("06-01-02") // YY-MM-DD, leads the line (sorted by date)
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
	return fmt.Sprintf("%s [%s] %-5s %-5s %9s %5d↑ %3d↓",
		date, truncate(grp, 14), res, eps, humanSize(r.Entry.SizeBytes),
		r.Entry.Seeders, r.Entry.Leechers)
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

// ---- pickers (fzf with numbered fallback) ----

// runFzf launches fzf with args, feeds input, returns the selected (full) line.
func runFzf(args []string, input string) (string, error) {
	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(input)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 130 {
			return "", errCancelled
		}
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// pickAnime chooses an anime via fzf (with cover preview in kitty) or a
// numbered fallback.
func pickAnime(cands []AnimeCandidate, useFzf bool) (*AnimeCandidate, error) {
	if useFzf && fzfAvailable() {
		return pickAnimeFzf(cands)
	}
	return pickAnimeNumbered(cands)
}

func pickAnimeFzf(cands []AnimeCandidate) (*AnimeCandidate, error) {
	var b strings.Builder
	for _, c := range cands {
		fmt.Fprintf(&b, "%d\t%s\t%s\n", c.Aid, renderAnimeLine(c), c.PicURL)
	}
	args := []string{
		"--delimiter=\t", "--with-nth=2", "--cycle",
		"--reverse", "--prompt=Anime> ",
		"--header", fmt.Sprintf("%d anime — Enter to select", len(cands)),
		"--preview-window=right:30%,border-vertical",
	}
	if kittyActive() {
		args = append(args, "--preview=kitten icat --clear --transfer-mode=memory --stdin=no --scale-up --place=${FZF_PREVIEW_COLUMNS}x${FZF_PREVIEW_LINES}@0x0 {3}")
	}
	line, err := runFzf(args, b.String())
	if err != nil {
		return nil, err
	}
	aid, _ := strconv.Atoi(strings.SplitN(line, "\t", 2)[0])
	for i := range cands {
		if cands[i].Aid == aid {
			return &cands[i], nil
		}
	}
	return nil, fmt.Errorf("selected aid %d not found", aid)
}

func pickAnimeNumbered(cands []AnimeCandidate) (*AnimeCandidate, error) {
	lines := make([]string, len(cands))
	for i, c := range cands {
		lines[i] = renderAnimeLine(c)
	}
	idx, err := pickIndex(lines, fmt.Sprintf("Anime (%d)", len(cands)), "Select anime", false)
	if err != nil {
		return nil, err
	}
	return &cands[idx], nil
}

// pickRelease chooses a release via fzf (fuzzy filter, pre-sorted, group
// pre-filtered) or a numbered fallback.
func pickRelease(all []*Release, group, sortName string, useFzf bool) (*Release, error) {
	view := sortedReleases(filterByGroup(all, group), sortName)
	if len(view) == 0 {
		return nil, fmt.Errorf("no releases for group %q", groupLabel(group))
	}
	if useFzf && fzfAvailable() {
		return pickReleaseFzf(view, group, sortName)
	}
	return pickReleaseNumbered(view, group, sortName)
}

func pickReleaseFzf(view []*Release, group, sortName string) (*Release, error) {
	var b strings.Builder
	for i, r := range view {
		// fields (tab-separated): 1=index, 2=concise display, 3=magnet, 4=title.
		// --with-nth=2,4 shows concise and title joined by the tab delimiter.
		fmt.Fprintf(&b, "%d\t%s\t%s\t%s\n", i, renderReleaseFzfLine(r), r.Entry.Magnet, r.Entry.Title)
	}

	args := []string{
		"--delimiter=\t", "--with-nth=2,4", "--cycle",
		"--reverse", "--prompt=Release> ",
		"--header", fmt.Sprintf("%d releases  [%s]  %s — type to filter, Enter to select", len(view), groupLabel(group), normalizeSort(sortName)),
		"--preview-window=right:30%,border-vertical",
	}
	// Text preview (full title + magnet) via a temp file + internal subcommand,
	// so titles with quotes/spaces stay shell-safe.
	if tmp, err := os.CreateTemp("", "ani-rel-*.tsv"); err == nil {
		tmp.WriteString(b.String())
		tmp.Close()
		defer os.Remove(tmp.Name())
		if exe, err := os.Executable(); err == nil {
			args = append(args, fmt.Sprintf("--preview=%s preview-release %s {1}", exe, tmp.Name()))
		}
	}

	line, err := runFzf(args, b.String())
	if err != nil {
		return nil, err
	}
	idx, _ := strconv.Atoi(strings.SplitN(line, "\t", 2)[0])
	if idx < 0 || idx >= len(view) {
		return nil, fmt.Errorf("selected release out of range")
	}
	return view[idx], nil
}

// previewRelease is the internal subcommand backing the fzf --preview pane: it
// reads the temp TSV (same lines fed to fzf) and prints the full title +
// concise detail + magnet for the line at index, word-wrapped to the pane.
func previewRelease(file, indexStr string) {
	idx, err := strconv.Atoi(indexStr)
	if err != nil || idx < 0 {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if idx >= len(lines) {
		return
	}
	f := strings.SplitN(lines[idx], "\t", 4) // [index, concise, magnet, title]
	if len(f) < 3 {
		return
	}
	title := "(no title)"
	if len(f) >= 4 && f[3] != "" {
		title = f[3]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Title:  %s\n", title)
	fmt.Fprintf(&b, "Detail: %s\n", f[1])
	if m := f[2]; m != "" {
		if len([]rune(m)) > 56 {
			m = string([]rune(m)[:56]) + "…"
		}
		fmt.Fprintf(&b, "Magnet: %s\n", m)
	}
	width := atoiSafe(os.Getenv("FZF_PREVIEW_COLUMNS"))
	if width <= 0 {
		width = 40
	}
	for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		fmt.Println(wrapLine(line, width))
	}
}

// wrapLine breaks s to no more than width runes per line, preferring to break
// after a space; long unbreakable tokens break at width. (Rune-based so CJK
// titles wrap too.)
func wrapLine(s string, width int) string {
	runes := []rune(s)
	if width <= 0 || len(runes) <= width {
		return s
	}
	var b strings.Builder
	for len(runes) > 0 {
		end := width
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			// prefer breaking after the last space in (0, end), if not too early
			for i := end - 1; i > width/2; i-- {
				if runes[i] == ' ' {
					end = i + 1
					break
				}
			}
		}
		b.WriteString(string(runes[:end]))
		runes = runes[end:]
		if len(runes) > 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func pickReleaseNumbered(view []*Release, group, sortName string) (*Release, error) {
	lines := make([]string, len(view))
	for i, r := range view {
		lines[i] = renderReleaseFzfLine(r)
	}
	header := fmt.Sprintf("%d releases  [%s]  %s", len(view), groupLabel(group), normalizeSort(sortName))
	idx, err := pickIndex(lines, header, "Select release", false)
	if err != nil {
		return nil, err
	}
	return view[idx], nil
}
