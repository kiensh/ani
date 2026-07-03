package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
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

// ---- MAL anime picker ----

func renderMALLine(m MALItem) string {
	var b strings.Builder
	b.WriteString(truncate(m.Title, 40))
	switch {
	case m.TotalEps > 0:
		fmt.Fprintf(&b, "  ep %d/%d", m.WatchedEps, m.TotalEps)
	case m.WatchedEps > 0:
		fmt.Fprintf(&b, "  ep %d", m.WatchedEps)
	}
	if a := malAirShort(m.AirStatus); a != "" {
		fmt.Fprintf(&b, "  [%s]", a)
	}
	if m.Score > 0 {
		fmt.Fprintf(&b, "  ★%d", m.Score)
	}
	return b.String()
}

func malListStatusShort(s string) string {
	switch s {
	case "watching":
		return "Watching"
	case "completed":
		return "Completed"
	case "on_hold":
		return "On Hold"
	case "dropped":
		return "Dropped"
	case "plan_to_watch":
		return "Plan to Watch"
	}
	return titleCase(s)
}

func malAirShort(s string) string {
	switch s {
	case "currently_airing":
		return "airing"
	case "finished_airing":
		return "done"
	case "not_yet_aired":
		return "unaired"
	}
	return s
}

// pickMALAnime chooses an anime via fzf (cover preview in kitty) or numbered.
func pickMALAnime(items []MALItem, useFzf bool) (*MALItem, error) {
	if useFzf && fzfAvailable() {
		return pickMALAnimeFzf(items)
	}
	return pickMALAnimeNumbered(items)
}

func pickMALAnimeFzf(items []MALItem) (*MALItem, error) {
	var b strings.Builder
	for _, m := range items {
		fmt.Fprintf(&b, "%d\t%s\n", m.MalID, renderMALLine(m))
	}
	args := []string{
		"--delimiter=\t", "--with-nth=2", "--cycle",
		"--reverse", "--prompt=Anime> ",
		"--header", fmt.Sprintf("%d anime — Enter to select", len(items)),
		"--preview-window=right:30%,border-vertical",
	}
	// Write items to a temp JSON for the preview subcommand (cover + info text).
	if tmp, err := os.CreateTemp("", "ani-anime-*.json"); err == nil {
		if data, err := json.Marshal(items); err == nil {
			tmp.Write(data)
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		if exe, err := os.Executable(); err == nil {
			args = append(args, fmt.Sprintf("--preview=%s preview-anime %s {1}", exe, tmp.Name()))
		}
	}
	line, err := runFzf(args, b.String())
	if err != nil {
		return nil, err
	}
	id, _ := strconv.Atoi(strings.SplitN(line, "\t", 2)[0])
	for i := range items {
		if items[i].MalID == id {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("selected mal id %d not found", id)
}

// previewAnime is the internal subcommand backing the fzf --preview pane for
// the anime picker: renders the cover image (kitty) then info text below.
func previewAnime(tmpfile, malIDStr string) {
	data, err := os.ReadFile(tmpfile)
	if err != nil {
		return
	}
	var items []MALItem
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}
	malID, _ := strconv.Atoi(malIDStr)
	var m *MALItem
	for i := range items {
		if items[i].MalID == malID {
			m = &items[i]
			break
		}
	}
	if m == nil {
		return
	}

	// Cover image (top ~40% of the preview pane).
	cols, lines := atoiSafe(os.Getenv("FZF_PREVIEW_COLUMNS")), atoiSafe(os.Getenv("FZF_PREVIEW_LINES"))
	if cols <= 0 {
		cols = 40
	}
	if lines <= 0 {
		lines = 20
	}
	if m.CoverURL != "" {
		imageRows := lines * 40 / 100
		if imageRows < 4 {
			imageRows = 4
		}
		if imageRows > 14 {
			imageRows = 14
		}
		cmd := exec.Command("kitten", "icat", "--clear", "--transfer-mode=memory",
			"--stdin=no", "--scale-up",
			fmt.Sprintf("--place=%dx%d@0x0", cols, imageRows), m.CoverURL)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
		fmt.Println()
	}

	// Text info below the cover (each field colored for readability).
	cprint := func(color, text string) {
		for _, line := range strings.Split(wrapLine(text, cols), "\n") {
			fmt.Printf("%s%s\033[0m\n", color, line)
		}
	}

	cprint("\033[1;36m", m.Title) // bold cyan: title

	progress := ""
	switch {
	case m.TotalEps > 0:
		progress = fmt.Sprintf("ep %d/%d", m.WatchedEps, m.TotalEps)
	default:
		progress = fmt.Sprintf("ep %d", m.WatchedEps)
	}
	if a := malAirShort(m.AirStatus); a != "" {
		progress += "  [" + a + "]"
	}
	if m.ListStatus != "" {
		progress += "  —  " + malListStatusShort(m.ListStatus)
	} else if m.WatchedEps > 0 {
		progress += "  ·  Watching"
	}
	cprint("\033[33m", progress) // yellow: progress + status

	if m.MeanScore > 0 {
		s := fmt.Sprintf("★ %.2f", m.MeanScore)
		if m.Score > 0 {
			s += fmt.Sprintf("   (your: %d)", m.Score)
		}
		cprint("\033[35m", s) // magenta: score
	} else if m.Score > 0 {
		cprint("\033[35m", fmt.Sprintf("your score: %d", m.Score))
	}

	if m.Genres != "" {
		cprint("\033[32m", "Genres: "+m.Genres) // green
	}
	if m.Studios != "" {
		cprint("\033[34m", "Studios: "+m.Studios) // blue
	}
	seasonType := ""
	if m.StartSeason != "" {
		seasonType = "Season: " + m.StartSeason
		if m.MediaType != "" {
			seasonType += "  (" + strings.ToUpper(m.MediaType) + ")"
		}
	} else if m.MediaType != "" {
		seasonType = "Type: " + strings.ToUpper(m.MediaType)
	}
	if seasonType != "" {
		cprint("\033[2m", seasonType) // dim: season/type
	}

	if m.Rank > 0 || m.Members > 0 {
		parts := []string{}
		if m.Rank > 0 {
			parts = append(parts, fmt.Sprintf("Rank #%d", m.Rank))
		}
		if m.Members > 0 {
			parts = append(parts, fmt.Sprintf("%s members", humanCount(m.Members)))
		}
		cprint("\033[2m", strings.Join(parts, "  ")) // dim: rank/members
	}
}

func humanCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

func pickMALAnimeNumbered(items []MALItem) (*MALItem, error) {
	lines := make([]string, len(items))
	for i, m := range items {
		lines[i] = renderMALLine(m)
	}
	idx, err := pickIndex(lines, fmt.Sprintf("Anime (%d)", len(items)), "Select anime", false)
	if err != nil {
		return nil, err
	}
	return &items[idx], nil
}

// fallbackAnidbByTitle searches animetosho by title (and shortened variants)
// and returns the top anidb id (used when a MAL anime has no AniDB external
// link). Returns 0 if none found.
func fallbackAnidbByTitle(title string) int {
	for _, candidate := range titleVariants(title) {
		series, err := searchSeries(candidate)
		if err != nil {
			continue
		}
		series = dedupSeries(series)
		if len(series) > 0 {
			return series[0].AnidbAID
		}
	}
	return 0
}

// titleVariants returns progressively shorter versions of a title for
// fallback searching (animetosho indexes franchises under the base name).
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
// In debug mode it prints the fzf command + input and auto-selects the first
// line (no fzf process is started).
func runFzf(args []string, input string) (string, error) {
	if dryRunMode {
		fmt.Fprintf(os.Stderr, "DEBUG fzf %s\n", shellQuote(append([]string{"fzf"}, args...)))
		fmt.Fprintln(os.Stderr, "DEBUG input (tab-delimited):")
		fmt.Fprintln(os.Stderr, input)
		first := strings.SplitN(input, "\n", 2)[0]
		if first == "" {
			return "", errCancelled
		}
		return first, nil
	}
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

// shellQuote joins args into a single shell-quoted command line (each arg in
// single quotes, inner ' escaped) so printed commands are copy-paste-runnable.
func shellQuote(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
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
	width := atoiSafe(os.Getenv("FZF_PREVIEW_COLUMNS"))
	if width <= 0 {
		width = 40
	}
	cprint := func(color, text string) {
		for _, line := range strings.Split(wrapLine(text, width), "\n") {
			fmt.Printf("%s%s\033[0m\n", color, line)
		}
	}
	cprint("\033[1;36m", title)
	cprint("\033[33m", f[1])
	if m := f[2]; m != "" {
		if len([]rune(m)) > 56 {
			m = string([]rune(m)[:56]) + "…"
		}
		cprint("\033[2m", "Magnet: "+m)
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
