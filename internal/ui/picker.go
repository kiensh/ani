package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"ani/internal/animetosho"
	"ani/internal/mal"
)

// PickRelease chooses a release via fzf (fuzzy filter, pre-sorted, group
// pre-filtered) or a numbered fallback. dryRun threads into the fzf shim.
func PickRelease(all []*animetosho.Release, group, sortName string, useFzf bool, item *mal.Item, dryRun bool) (*animetosho.Release, error) {
	view := SortedReleases(FilterByGroup(all, group), sortName)
	if len(view) == 0 {
		return nil, fmt.Errorf("no releases for group %q", GroupLabel(group))
	}
	if useFzf && FzfAvailable() {
		return pickReleaseFzf(view, group, sortName, item, dryRun)
	}
	return pickReleaseNumbered(view, group, sortName, item)
}

func pickReleaseFzf(view []*animetosho.Release, group, sortName string, item *mal.Item, dryRun bool) (*animetosho.Release, error) {
	var b strings.Builder
	for i, r := range view {
		// fields (tab-separated): 1=index, 2=concise display, 3=magnet, 4=title.
		// --with-nth=2,4 shows concise and title joined by the tab delimiter.
		fmt.Fprintf(&b, "%d\t%s\t%s\t%s\n", i, RenderReleaseLine(r), r.Entry.Magnet, r.Entry.Title)
	}

	headerParts := []string{}
	if info := MALItemHeader(item); info != "" {
		headerParts = append(headerParts, info)
	}
	headerParts = append(headerParts, fmt.Sprintf("%d releases  [%s]  %s — type to filter, Enter to select", len(view), GroupLabel(group), NormalizeSort(sortName)))

	args := []string{
		"--delimiter=\t", "--with-nth=2,4", "--cycle",
		"--reverse", "--prompt=Release> ",
		"--header", strings.Join(headerParts, "  ·  "),
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

	line, err := RunFzf(args, b.String(), dryRun)
	if err != nil {
		return nil, err
	}
	idx, _ := strconv.Atoi(strings.SplitN(line, "\t", 2)[0])
	if idx < 0 || idx >= len(view) {
		return nil, fmt.Errorf("selected release out of range")
	}
	return view[idx], nil
}

func pickReleaseNumbered(view []*animetosho.Release, group, sortName string, item *mal.Item) (*animetosho.Release, error) {
	lines := make([]string, len(view))
	for i, r := range view {
		lines[i] = RenderReleaseLine(r)
	}
	parts := []string{}
	if info := MALItemHeader(item); info != "" {
		parts = append(parts, info)
	}
	parts = append(parts, fmt.Sprintf("%d releases  [%s]  %s", len(view), GroupLabel(group), NormalizeSort(sortName)))
	header := strings.Join(parts, "  ·  ")
	idx, err := PickIndex(lines, header, "Select release", false)
	if err != nil {
		return nil, err
	}
	return view[idx], nil
}

// PickMALAnime chooses an anime via fzf (cover preview in kitty) or numbered.
func PickMALAnime(items []mal.Item, useFzf, dryRun bool) (*mal.Item, error) {
	if useFzf && FzfAvailable() {
		return pickMALAnimeFzf(items, dryRun)
	}
	return pickMALAnimeNumbered(items)
}

func pickMALAnimeFzf(items []mal.Item, dryRun bool) (*mal.Item, error) {
	var b strings.Builder
	for _, m := range items {
		fmt.Fprintf(&b, "%d\t%s\n", m.MalID, RenderMALLine(m))
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
	line, err := RunFzf(args, b.String(), dryRun)
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

func pickMALAnimeNumbered(items []mal.Item) (*mal.Item, error) {
	lines := make([]string, len(items))
	for i, m := range items {
		lines[i] = RenderMALLine(m)
	}
	idx, err := PickIndex(lines, fmt.Sprintf("Anime (%d)", len(items)), "Select anime", false)
	if err != nil {
		return nil, err
	}
	return &items[idx], nil
}

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
