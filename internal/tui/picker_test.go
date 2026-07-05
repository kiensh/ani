package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"ani/internal/animetosho"
	"ani/internal/mal"
)

// keyMsg builds a tea.KeyMsg for a single rune keypress.
func keyMsg(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// downMsg / upMsg / enterMsg build navigation/confirm key messages.
func downMsg() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyDown} }
func upMsg() tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyUp} }
func enterMsg() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
func escMsg() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyEscape} }

// fetchAll returns a fetch func that always returns the given releases
// regardless of episode — lets tests inject a fixed release set.
func fetchAll(all []*animetosho.Release) func(int) []*animetosho.Release {
	return func(int) []*animetosho.Release { return all }
}

// loadReleases seeds a picker as if a fetch completed for its current filter
// episode (mirrors applyLoaded without the prefetch cmd), so tests can drive
// filters/overlays/navigation against a populated list.
func loadReleases(m *releasePicker, all []*animetosho.Release) {
	m.all = all
	m.groups = DistinctGroups(all)
	m.qualities = DistinctQualities(all)
	m.fetching = false
	m.cursor = 0
	m.applyFilter()
}

// TestAnimePickerRender exercises the anime picker model end-to-end without a
// real terminal: it drives Update with a WindowSizeMsg and then calls View,
// catching panics in the layout/render code (fixed pane heights, cover area,
// metadata). It also asserts the rendered height fits the terminal.
func TestAnimePickerRender(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Frieren: Beyond Journey's End", TotalEps: 28, WatchedEps: 3, MeanScore: 9.3, Genres: "Adventure", CoverURL: ""},
		{MalID: 2, Title: "Re:Zero", TotalEps: 25, WatchedEps: 25, CoverURL: ""},
	}
	m := newAnimePicker(items, AnimeModeList, "", false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	out := m.View()
	if out == "" {
		t.Fatal("View returned empty string")
	}
	if !strings.Contains(out, "Watching List") {
		t.Errorf("header missing: %q", firstLine(out))
	}
	if m.coverRows <= 0 || m.coverCols <= 0 {
		t.Fatalf("cover dims zeroed: %dx%d", m.coverCols, m.coverRows)
	}
	lines := strings.Split(out, "\n")
	if len(lines) > 30 {
		t.Errorf("View rendered %d lines, expected <= 30 (terminal height)", len(lines))
	}
}

func TestAnimePickerSearchHeader(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "Re:Zero"}}
	m := newAnimePicker(items, AnimeModeSearch, "rezero", false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	out := m.View()
	if !strings.Contains(out, `Search: "rezero"`) {
		t.Errorf("search header missing: %q", firstLine(out))
	}
}

// TestAnimePickerFilterNav verifies that j/k inside filter mode become filter
// text (not navigation), and that the list filters accordingly.
func TestAnimePickerFilterNav(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha"},
		{MalID: 2, Title: "Beta"},
		{MalID: 3, Title: "Gamma"},
	}
	m := newAnimePicker(items, AnimeModeList, "", false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(keyMsg('/'))
	m.Update(keyMsg('g'))
	if m.cursor != 0 {
		t.Errorf("filter 'g' moved cursor to %d, want 0", m.cursor)
	}
	if m.filterText != "g" {
		t.Errorf("filterText = %q, want 'g'", m.filterText)
	}
	if len(m.filtered) != 1 {
		t.Errorf("filtered len = %d, want 1 (only Gamma)", len(m.filtered))
	}
}

// TestAnimePickerRemoveG verifies g/G no longer go to top/bottom (removed).
func TestAnimePickerRemoveG(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}, {MalID: 2, Title: "B"}, {MalID: 3, Title: "C"}}
	m := newAnimePicker(items, AnimeModeList, "", false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	// Move down one, then press uppercase 'G' — should be a no-op (not last).
	m.Update(keyMsg('j'))
	m.Update(keyMsg('G'))
	if m.cursor != 1 {
		t.Errorf("after j then G, cursor = %d, want 1 (G must be a no-op)", m.cursor)
	}
	// Lowercase 'g' should also be a no-op now.
	m.Update(keyMsg('g'))
	if m.cursor != 1 {
		t.Errorf("after g, cursor = %d, want 1 (g must be a no-op)", m.cursor)
	}
}

// TestReleasePickerRender exercises the release picker render at a typical
// terminal size, ensuring the fixed-pane layout doesn't panic and stays within
// the terminal height.
func TestReleasePickerRender(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("Erai-raws", "1080p", 1, false),
		mkRel("SubsPlease", "1080p", 2, false),
		mkRel("EMBER", "720p", 1, false),
	}
	item := &mal.Item{Title: "Frieren", TotalEps: 28, WatchedEps: 11}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	out := m.View()
	if out == "" {
		t.Fatal("View returned empty string")
	}
	lines := strings.Split(out, "\n")
	if len(lines) > 24 {
		t.Errorf("View rendered %d lines, expected <= 24", len(lines))
	}
	if !strings.Contains(out, "rels") {
		t.Errorf("header missing rels count")
	}
}

// TestReleasePickerDefaultFilters checks next-episode default is applied on init
// (quality left at "all" — user preference).
func TestReleasePickerDefaultFilters(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "720p", 1, false),
		mkRel("b", "1080p", 2, false),
		mkRel("c", "2160p", 3, false),
		mkRel("d", "1080p", 4, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 10}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	rp := m
	if rp.filter.Quality != "" {
		t.Errorf("default quality = %q, want \"\" (all)", rp.filter.Quality)
	}
	if rp.filter.Episode != 11 {
		t.Errorf("default episode = %d, want 11", rp.filter.Episode)
	}
}

// TestReleasePickerDefaultEpisodeFinished verifies that when the user has
// finished the series (watched >= total), the default episode is 0 (all).
func TestReleasePickerDefaultEpisodeFinished(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 12, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 12}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	if m.filter.Episode != 0 {
		t.Errorf("default episode (finished) = %d, want 0", m.filter.Episode)
	}
}

// TestReleasePickerOverlayGroup opens the group overlay, moves, and selects.
func TestReleasePickerOverlayGroup(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("Erai-raws", "1080p", 1, false),
		mkRel("SubsPlease", "1080p", 2, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('g'))
	if !m.overlay.active() || m.overlay.kind != overlayGroup {
		t.Fatalf("g did not open group overlay (active=%v kind=%v)", m.overlay.active(), m.overlay.kind)
	}
	if len(m.overlay.items) != 3 {
		t.Errorf("overlay items = %v, want 3", m.overlay.items)
	}
	// Items: [0]=All, [1]=Erai-raws, [2]=SubsPlease → move down twice to
	// SubsPlease, then Enter.
	m.Update(downMsg())
	m.Update(downMsg())
	m.Update(enterMsg())
	if m.filter.Group != "SubsPlease" {
		t.Errorf("Group = %q, want SubsPlease", m.filter.Group)
	}
	if m.overlay.active() {
		t.Errorf("overlay still active after Enter")
	}
}

// TestReleasePickerOverlayQuality opens the quality overlay and selects All.
func TestReleasePickerOverlayQuality(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
		mkRel("b", "720p", 2, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('r'))
	if !m.overlay.active() || m.overlay.kind != overlayQuality {
		t.Fatalf("r did not open quality overlay")
	}
	// No default quality → cursor starts at "All" (index 0). Select it directly.
	m.Update(enterMsg())
	if m.filter.Quality != "" {
		t.Errorf("Quality = %q, want \"\" (All)", m.filter.Quality)
	}
}

// TestReleasePickerOverlayEpisodeEnter verifies typing a number + Enter in the
// episode overlay applies it as the episode filter.
func TestReleasePickerOverlayEpisodeEnter(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
		mkRel("b", "1080p", 5, false),
		mkRel("c", "1080p", 5, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	// Clear the default episode filter so we start from all episodes.
	m.filter.Episode = 0
	m.applyFilter()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('e'))
	m.Update(keyMsg('5'))
	m.Update(enterMsg())
	if m.filter.Episode != 5 {
		t.Errorf("Episode = %d, want 5", m.filter.Episode)
	}
	// Enter kicks off an async re-fetch; simulate it completing for ep 5 (the
	// injected fetch returns `all`), then the view holds only ep-5 releases.
	m.Update(releasesLoadedMsg{releases: all, ep: 5})
	if len(m.view) != 2 {
		t.Errorf("after ep=5 filter, view len = %d, want 2", len(m.view))
	}
}

// TestReleasePickerOverlayEpisodeEscCancels verifies Esc in the episode overlay
// cancels — restoring the pre-overlay episode even after typing a new number
// (Esc no longer clears to "all"; that's now Enter on an empty input).
func TestReleasePickerOverlayEpisodeEscCancels(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 6, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 5}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	// Default episode is 6 (WatchedEps+1).
	if m.filter.Episode != 6 {
		t.Fatalf("setup: default episode = %d, want 6", m.filter.Episode)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(keyMsg('e'))
	m.Update(keyMsg('1'))
	m.Update(keyMsg('0'))
	m.Update(escMsg())
	if m.filter.Episode != 6 {
		t.Errorf("Esc after typing: Episode = %d, want 6 (cancel restores)", m.filter.Episode)
	}
}

// TestReleasePickerSortCycle verifies 's' cycles sort and re-applies.
func TestReleasePickerSortCycle(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	first := m.filter.Sort
	m.Update(keyMsg('s'))
	if m.filter.Sort == first {
		t.Errorf("s did not advance sort (still %q)", first)
	}
}

// TestReleasePickerEscBack verifies Esc (with no overlay/filter) sets Back.
func TestReleasePickerEscBack(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(escMsg())
	if !m.result.Back {
		t.Errorf("Esc did not set Back")
	}
}

// TestReleasePickerEscInOverlayCancels verifies Esc inside an overlay closes it
// without setting Back.
func TestReleasePickerEscInOverlayCancels(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(keyMsg('g'))
	m.Update(escMsg())
	if m.overlay.active() {
		t.Errorf("overlay still active after Esc")
	}
	if m.result.Back {
		t.Errorf("Esc in overlay set Back (should only cancel overlay)")
	}
}

// firstLine returns the first newline-separated chunk of s.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
