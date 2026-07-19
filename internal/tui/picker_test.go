package tui

import (
	"fmt"
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
func tabMsg() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyTab} }
func spaceMsg() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeySpace} }

// overlaySelect positions the active anime overlay's cursor on the given label
// (robust to the menu's filtering/ordering).
func overlaySelect(t *testing.T, m *animePicker, label string) {
	t.Helper()
	for i, it := range m.overlay.items {
		if it == label {
			m.overlay.cursor = i
			return
		}
	}
	t.Fatalf("overlay item %q not found in %v", label, m.overlay.items)
}

// fetchAll returns a fetch func that always returns the given releases
// regardless of episode — lets tests inject a fixed release set.
func fetchAll(all []*animetosho.Release) func(int) []*animetosho.Release {
	return func(int) []*animetosho.Release { return all }
}

// loadReleases seeds a picker as if a fetch completed for its current filter
// episode, so tests can drive filters/overlays/navigation against a populated list.
func loadReleases(m *releasePicker, all []*animetosho.Release) {
	m.all = all
	m.groups = DistinctGroups(all)
	m.qualities = DistinctQualities(all)
	m.fetching = false
	m.cursor = 0
	m.applyFilter()
}

// animeLoadAll returns an AnimeLoad that always returns the given items.
func animeLoadAll(items []mal.Item) AnimeLoad {
	return func(AnimeSource, string, string) []mal.Item { return items }
}

// loadAnime seeds an anime picker as if a load completed for its current
// (query, season).
func loadAnime(m *animePicker, items []mal.Item) {
	m.items = items
	m.loading = false
	m.cursor = 0
	m.applyFilter()
}

func homeMsg() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyHome} }
func endMsg() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyEnd} }

func sliceContains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---- anime picker ----

func TestAnimePickerRender(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Frieren: Beyond Journey's End", TotalEps: 28, WatchedEps: 3, MeanScore: 9.3, Genres: "Adventure", CoverURL: ""},
		{MalID: 2, Title: "Re:Zero", TotalEps: 25, WatchedEps: 25, CoverURL: ""},
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	out := m.View()
	if out == "" {
		t.Fatal("View returned empty string")
	}
	_, _, label := mal.CurrentSeason()
	if !strings.Contains(out, label) {
		t.Errorf("header missing current season %q: %q", label, firstLine(out))
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
	m := newAnimePicker(SourceSeason, "rezero", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	out := m.View()
	if !strings.Contains(out, `Search: "rezero"`) {
		t.Errorf("search header missing: %q", firstLine(out))
	}
}

// TestAnimePickerFilterNav verifies that keys inside fuzzy filter mode become
// filter text (not navigation), and the list filters accordingly.
func TestAnimePickerFilterNav(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha"},
		{MalID: 2, Title: "Beta"},
		{MalID: 3, Title: "Gamma"},
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	m.filter.Status = "All" // this test exercises fuzzy, not the status filter
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(keyMsg('/'))
	m.Update(keyMsg('g'))
	if m.cursor != 0 {
		t.Errorf("filter 'g' moved cursor to %d, want 0", m.cursor)
	}
	if m.filter.FuzzyText != "g" {
		t.Errorf("FuzzyText = %q, want 'g'", m.filter.FuzzyText)
	}
	if len(m.view) != 1 {
		t.Errorf("view len = %d, want 1 (only Gamma)", len(m.view))
	}
}

// TestAnimePickerFilterEnterAccepts: Enter in filter mode keeps the filter
// applied and returns to normal mode — it must NOT select/quit.
func TestAnimePickerFilterEnterAccepts(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha"},
		{MalID: 2, Title: "Gamma"},
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	m.filter.Status = "All"
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(keyMsg('/'))
	m.Update(keyMsg('g'))
	if !m.filter.Filtering || len(m.view) != 1 {
		t.Fatalf("setup: want filter mode + 1 match, got Filtering=%v view=%d", m.filter.Filtering, len(m.view))
	}
	m.Update(enterMsg())
	if m.filter.Filtering {
		t.Errorf("after Enter: Filtering=true, want false (accept → normal mode)")
	}
	if m.filter.FuzzyText != "g" {
		t.Errorf("after Enter: FuzzyText=%q, want 'g' (kept)", m.filter.FuzzyText)
	}
	if len(m.view) != 1 {
		t.Errorf("after Enter: view=%d, want 1 (filter still applied)", len(m.view))
	}
	if m.result.Anime != nil {
		t.Errorf("after Enter: result.Anime set; Enter must not select")
	}
}

// TestAnimePickerFilterEscDiscards: Esc in filter mode discards the filter.
func TestAnimePickerFilterEscDiscards(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha"},
		{MalID: 2, Title: "Gamma"},
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	m.filter.Status = "All"
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(keyMsg('/'))
	m.Update(keyMsg('g'))
	m.Update(escMsg())
	if m.filter.Filtering {
		t.Errorf("after Esc: Filtering=true, want false")
	}
	if m.filter.FuzzyText != "" {
		t.Errorf("after Esc: FuzzyText=%q, want '' (discarded)", m.filter.FuzzyText)
	}
	if len(m.view) != 2 {
		t.Errorf("after Esc: view=%d, want 2 (filter cleared)", len(m.view))
	}
}

// TestAnimePickerRemoveG verifies g/G are no-ops (not top/bottom jumps).
func TestAnimePickerRemoveG(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}, {MalID: 2, Title: "B"}, {MalID: 3, Title: "C"}}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	m.filter.Status = "All" // navigation test, not status
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(keyMsg('j'))
	m.Update(keyMsg('G'))
	if m.cursor != 1 {
		t.Errorf("after j then G, cursor = %d, want 1 (G must be a no-op)", m.cursor)
	}
	m.Update(keyMsg('g'))
	if m.cursor != 1 {
		t.Errorf("after g, cursor = %d, want 1 (g must be a no-op)", m.cursor)
	}
}

// TestAnimePickerCyclicNav: 'k' at the first item wraps to the last; 'j' at the
// last wraps to the first.
func TestAnimePickerCyclicNav(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}, {MalID: 2, Title: "B"}, {MalID: 3, Title: "C"}}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	m.filter.Status = "All"
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.cursor = 0
	m.Update(keyMsg('k'))
	if m.cursor != 2 {
		t.Errorf("k at first: cursor = %d, want 2 (wrap to last)", m.cursor)
	}
	m.Update(keyMsg('j'))
	if m.cursor != 0 {
		t.Errorf("j at last: cursor = %d, want 0 (wrap to first)", m.cursor)
	}
}

// TestAnimePickerStatusFilter opens the status overlay, selects Completed, and
// checks the view is filtered to completed items.
func TestAnimePickerStatusFilter(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha", ListStatus: "watching"},
		{MalID: 2, Title: "Beta", ListStatus: "completed"},
		{MalID: 3, Title: "Gamma", ListStatus: "completed"},
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	// 't' opens the 8-item status overlay (cursor on the current "My List").
	m.Update(keyMsg('t'))
	if !m.overlay.active() || m.overlay.kind != animeOverlayStatus {
		t.Fatalf("t did not open status overlay")
	}
	overlaySelect(t, m, "Completed")
	m.Update(enterMsg())
	if m.filter.Status != "Completed" {
		t.Errorf("Status = %q, want Completed", m.filter.Status)
	}
	if len(m.view) != 2 {
		t.Errorf("view len = %d, want 2 (completed items)", len(m.view))
	}
	if m.overlay.active() {
		t.Errorf("overlay still active after Enter")
	}
}

// TestStatusFilterPredicates exercises the status-filter predicate directly.
func TestStatusFilterPredicates(t *testing.T) {
	cases := []struct {
		label, status string
		want          bool
	}{
		{"All", "watching", true},
		{"All", "", true},
		{"Not in My List", "", true},
		{"Not in My List", "watching", false},
		{"My List", "watching", true},
		{"My List", "", false},
		{"Watching", "watching", true},
		{"Watching", "completed", false},
		{"Completed", "completed", true},
		{"On-Hold", "on_hold", true},
		{"Plan to Watch", "plan_to_watch", true},
		{"Dropped", "dropped", true},
	}
	for _, c := range cases {
		if got := statusKeeps(c.label, c.status); got != c.want {
			t.Errorf("statusKeeps(%q,%q) = %v, want %v", c.label, c.status, got, c.want)
		}
	}
}

// TestAnimePickerSortOverlay verifies 's' opens a sort overlay and selecting
// Score applies it (no longer a cycle).
func TestAnimePickerSortOverlay(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	if m.filter.Sort != "updated" {
		t.Fatalf("default sort = %q, want updated", m.filter.Sort)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.Update(keyMsg('s'))
	if !m.overlay.active() || m.overlay.kind != animeOverlaySort {
		t.Fatalf("s did not open sort overlay")
	}
	// Cursor on Updated; move down to Air Date, enter.
	m.Update(downMsg())
	m.Update(enterMsg())
	if m.filter.Sort != "airdate" {
		t.Errorf("Sort = %q, want airdate", m.filter.Sort)
	}
	if m.overlay.active() {
		t.Errorf("overlay still active after Enter")
	}
}

// TestAnimePickerSearchDefaultSort verifies search mode (query != "") defaults to
// "relevance" (preserve MAL's search ranking), while browse defaults to popularity.
func TestAnimePickerSearchDefaultSort(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}}
	sm := newAnimePicker(SourceSeason, "frieren", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	if sm.filter.Sort != "relevance" {
		t.Errorf("search default sort = %q, want relevance", sm.filter.Sort)
	}
	bm := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	if bm.filter.Sort != "updated" {
		t.Errorf("browse default sort = %q, want updated", bm.filter.Sort)
	}
}

// TestSortAnimesRelevance verifies "relevance" preserves the input order (no
// re-sort), so MAL's search ranking is kept.
func TestSortAnimesRelevance(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Members: 100},
		{MalID: 2, Members: 5},
		{MalID: 3, Members: 50},
	}
	got := sortAnimes(items, "relevance")
	for i := range items {
		if got[i].MalID != items[i].MalID {
			t.Errorf("relevance sort reordered items; want input order preserved")
		}
	}
}

// TestAnimePickerStatusOptionsBySource verifies My List offers 6 status options
// (no Not in My List / My List) while Season and Search offer 8.
func TestAnimePickerStatusOptionsBySource(t *testing.T) {
	listM := newAnimePicker(SourceList, "", animeLoadAll([]mal.Item{{}}), nil, nil, nil, nil, nil, false)
	if n := len(listM.statusOptions()); n != 6 {
		t.Errorf("My List status options = %d, want 6", n)
	}
	seasonM := newAnimePicker(SourceSeason, "", animeLoadAll([]mal.Item{{}}), nil, nil, nil, nil, nil, false)
	if n := len(seasonM.statusOptions()); n != 8 {
		t.Errorf("Season status options = %d, want 8", n)
	}
	searchM := newAnimePicker(SourceSeason, "x", animeLoadAll([]mal.Item{{}}), nil, nil, nil, nil, nil, false)
	if n := len(searchM.statusOptions()); n != 8 {
		t.Errorf("Search status options = %d, want 8", n)
	}
}

// TestAnimePickerSeasonDisabledInMyList verifies 'e' is a no-op in My List
// (season filter is forced to All there).
func TestAnimePickerSeasonDisabledInMyList(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}}
	m := newAnimePicker(SourceList, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.Update(keyMsg('e'))
	if m.overlay.active() {
		t.Errorf("'e' opened a season overlay in My List (should be disabled)")
	}
	if m.season != mal.SeasonAll {
		t.Errorf("My List season = %q, want All", m.season)
	}
}

// TestAnimePickerTabToggle verifies Tab flips Season ↔ My List and reloads.
func TestAnimePickerTabToggle(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(tabMsg())
	if m.source != SourceList {
		t.Errorf("after Tab, source = %v, want SourceList", m.source)
	}
	if m.season != mal.SeasonAll {
		t.Errorf("after Tab to My List, season = %q, want All", m.season)
	}
	if !m.loading {
		t.Errorf("after Tab, loading = false, want true (reload)")
	}
	m.Update(tabMsg())
	if m.source != SourceSeason {
		t.Errorf("after second Tab, source = %v, want SourceSeason", m.source)
	}
}

// TestSeasonSourceSwitch verifies selecting "Later" in the Season source sets the
// season and triggers a load that calls the loader with season "Later".
func TestSeasonSourceSwitch(t *testing.T) {
	var seasons []string
	items := []mal.Item{{MalID: 1, Title: "A"}}
	load := func(_ AnimeSource, _ string, season string) []mal.Item {
		seasons = append(seasons, season)
		return items
	}
	m := newAnimePicker(SourceSeason, "", load, nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	// Open season overlay (cursor on current season), jump to "Later" (index 0).
	m.Update(keyMsg('e'))
	if !m.overlay.active() || m.overlay.kind != animeOverlaySeason {
		t.Fatalf("e did not open season overlay")
	}
	m.Update(homeMsg())
	_, cmd := m.Update(enterMsg())
	if m.season != mal.SeasonLater {
		t.Errorf("season = %q, want Later", m.season)
	}
	if cmd == nil {
		t.Fatal("expected a load cmd after switching season")
	}
	msg := cmd()
	if len(seasons) == 0 || seasons[len(seasons)-1] != mal.SeasonLater {
		t.Errorf("loader seasons = %v, want last %q", seasons, mal.SeasonLater)
	}
	if _, ok := msg.(itemsLoadedMsg); !ok {
		t.Fatalf("load cmd returned %T, want itemsLoadedMsg", msg)
	}
}

// TestSeasonArchiveWindow verifies the browse season list windows the Jikan
// archive to ~12 years back, keeping the real future and the current season.
func TestSeasonArchiveWindow(t *testing.T) {
	m := &animePicker{
		currentYear:   2026,
		seasonArchive: []string{"Winter 2027", "Fall 2026", "Summer 2026", "Winter 2014", "Fall 2013"},
	}
	got := m.seasonArchiveItems()
	if !sliceContains(got, "Winter 2027") {
		t.Errorf("missing future Winter 2027: %v", got)
	}
	if !sliceContains(got, "Summer 2026") {
		t.Errorf("missing current Summer 2026: %v", got)
	}
	if sliceContains(got, "Fall 2013") {
		t.Errorf("Fall 2013 should be excluded (< 2014): %v", got)
	}
}

// TestAnimePickerListFixedHeight verifies a long list doesn't overflow the
// terminal (the header-hidden regression).
func TestAnimePickerListFixedHeight(t *testing.T) {
	items := make([]mal.Item, 50)
	for i := range items {
		items[i] = mal.Item{MalID: i + 1, Title: fmt.Sprintf("Anime %d", i+1)}
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) > 20 {
		t.Errorf("View rendered %d lines, want <= 20 (terminal height)", len(lines))
	}
}

// TestAnimePickerSetStatus verifies Space → actions menu → confirm → apply: the
// setter is called with the right StatusAction, the item updates, and Esc at the
// confirm cancels.
func TestAnimePickerSetStatus(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha", ListStatus: "watching", WatchedEps: 3},
	}
	var got StatusAction
	var gotID, gotWatched int
	apply := func(malID, watched int, act StatusAction) bool {
		gotID, gotWatched, got = malID, watched, act
		return true
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), apply, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	// Space opens the top-level group menu; "Set Status" expands to the status
	// sub-menu (current status "watching" hidden there).
	m.Update(spaceMsg())
	if !m.overlay.active() || m.overlay.kind != animeOverlayActions {
		t.Fatalf("Space did not open actions menu")
	}
	overlaySelect(t, m, "Set Status")
	m.Update(enterMsg())
	if m.overlay.kind != animeOverlayStatusMenu {
		t.Fatalf("Set Status did not open the status sub-menu (kind=%v)", m.overlay.kind)
	}
	if sliceContains(m.overlay.items, "Set Watching") {
		t.Errorf("sub-menu should hide the current-status option; got %v", m.overlay.items)
	}
	overlaySelect(t, m, "Set Completed")
	m.Update(enterMsg())
	if m.overlay.kind != animeOverlayConfirm {
		t.Fatalf("Enter did not open confirm modal (kind=%v)", m.overlay.kind)
	}
	// 'y' applies via a background cmd; execute it and feed the result back.
	_, cmd := m.Update(keyMsg('y'))
	if cmd == nil {
		t.Fatal("no apply cmd returned after confirm")
	}
	m.Update(cmd())
	if got.Status != "completed" || got.Remove {
		t.Errorf("applyStatus got = %+v, want completed", got)
	}
	if gotID != 1 || gotWatched != 3 {
		t.Errorf("applyStatus got malID=%d watched=%d, want 1/3", gotID, gotWatched)
	}
	if m.overlay.active() {
		t.Errorf("overlay still active after apply")
	}
	if m.items[0].ListStatus != "completed" {
		t.Errorf("item ListStatus = %q, want completed", m.items[0].ListStatus)
	}
}

// TestAnimePickerSetCompletedSetsWatchedTotal: marking Completed sends watched =
// total episodes to MAL (not the current partial count) and reflects it locally.
func TestAnimePickerSetCompletedSetsWatchedTotal(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha", ListStatus: "watching", WatchedEps: 11, TotalEps: 12},
	}
	var gotWatched int
	apply := func(malID, watched int, act StatusAction) bool {
		gotWatched = watched
		return true
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), apply, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(spaceMsg())
	overlaySelect(t, m, "Set Status")
	m.Update(enterMsg())
	overlaySelect(t, m, "Set Completed")
	m.Update(enterMsg()) // → confirm
	_, cmd := m.Update(keyMsg('y'))
	if cmd == nil {
		t.Fatal("no apply cmd returned after confirm")
	}
	m.Update(cmd())

	if gotWatched != 12 {
		t.Errorf("applyStatus got watched = %d, want 12 (total) for completed", gotWatched)
	}
	if m.items[0].WatchedEps != 12 {
		t.Errorf("local WatchedEps = %d, want 12 (reflected)", m.items[0].WatchedEps)
	}
	if m.items[0].ListStatus != "completed" {
		t.Errorf("ListStatus = %q, want completed", m.items[0].ListStatus)
	}
}

// TestAnimePickerSetStatusEscCancels verifies Esc at the confirm modal does not
// apply the action.
func TestAnimePickerSetStatusEscCancels(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "Alpha", ListStatus: "watching"}}
	called := false
	apply := func(int, int, StatusAction) bool { called = true; return true }
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), apply, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(spaceMsg())
	overlaySelect(t, m, "Set Status")
	m.Update(enterMsg()) // → status sub-menu
	overlaySelect(t, m, "Set Completed")
	m.Update(enterMsg()) // → confirm
	m.Update(escMsg())
	if called {
		t.Errorf("applyStatus was called after Esc at confirm")
	}
	if m.overlay.active() {
		t.Errorf("overlay still active after Esc")
	}
	if m.items[0].ListStatus != "watching" {
		t.Errorf("ListStatus changed after cancelled action: %q", m.items[0].ListStatus)
	}
}

// TestAnimePickerSubMenuEscBacks verifies Esc in a sub-menu (Set Status / Set
// Score) returns to the group menu, not out to the list.
func TestAnimePickerSubMenuEscBacks(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "Alpha", ListStatus: "watching"}}
	apply := func(int, int, StatusAction) bool { return false }
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), apply, nil, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	m.Update(spaceMsg())
	overlaySelect(t, m, "Set Status")
	m.Update(enterMsg())
	if m.overlay.kind != animeOverlayStatusMenu {
		t.Fatalf("Set Status did not open sub-menu (kind=%v)", m.overlay.kind)
	}
	m.Update(escMsg())
	if m.overlay.kind != animeOverlayActions {
		t.Errorf("Esc in status sub-menu: kind=%v, want animeOverlayActions (group menu)", m.overlay.kind)
	}

	// Score picker Esc also returns to the group menu.
	overlaySelect(t, m, "Set Score")
	m.Update(enterMsg())
	if m.overlay.kind != animeOverlayScore {
		t.Fatalf("Set Score did not open picker (kind=%v)", m.overlay.kind)
	}
	m.Update(escMsg())
	if m.overlay.kind != animeOverlayActions {
		t.Errorf("Esc in score picker: kind=%v, want animeOverlayActions (group menu)", m.overlay.kind)
	}
}

// TestAnimePickerRemoveFromList verifies Remove: in My List the item is dropped;
// in Season its ListStatus clears.
func TestAnimePickerRemoveFromList(t *testing.T) {
	apply := func(int, int, StatusAction) bool { return true }

	// My List source: item is dropped from the cached set.
	listItems := []mal.Item{{MalID: 1, Title: "Alpha", ListStatus: "watching"}}
	lm := newAnimePicker(SourceList, "", animeLoadAll(listItems), apply, nil, nil, nil, nil, false)
	loadAnime(lm, listItems)
	lm.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	lm.Update(spaceMsg())
	overlaySelect(t, lm, "Remove from My List")
	lm.Update(enterMsg())
	_, cmd := lm.Update(keyMsg('y'))
	if cmd != nil {
		lm.Update(cmd())
	}
	if len(lm.items) != 0 {
		t.Errorf("My List: items len = %d after remove, want 0", len(lm.items))
	}

	// Season source: ListStatus clears (item stays, now Not in My List).
	seasonItems := []mal.Item{{MalID: 1, Title: "Alpha", ListStatus: "watching"}}
	sm := newAnimePicker(SourceSeason, "", animeLoadAll(seasonItems), apply, nil, nil, nil, nil, false)
	loadAnime(sm, seasonItems)
	sm.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	sm.Update(spaceMsg())
	overlaySelect(t, sm, "Remove from My List")
	sm.Update(enterMsg())
	_, scmd := sm.Update(keyMsg('y'))
	if scmd != nil {
		sm.Update(scmd())
	}
	if len(sm.items) != 1 {
		t.Errorf("Season: items len = %d after remove, want 1 (cleared, not dropped)", len(sm.items))
	}
	if sm.items[0].ListStatus != "" {
		t.Errorf("Season: ListStatus = %q after remove, want empty", sm.items[0].ListStatus)
	}
}

// TestAnimePickerActionsNoMalID verifies Space is a no-op without a MAL id
// (AnimeTosho fallback items) or without an injected writer.
func TestAnimePickerActionsNoMalID(t *testing.T) {
	apply := func(int, int, StatusAction) bool { return true }

	// MalID == 0 → no menu.
	noID := []mal.Item{{MalID: 0, Title: "Animetosho hit"}}
	m := newAnimePicker(SourceSeason, "x", animeLoadAll(noID), apply, nil, nil, nil, nil, false)
	loadAnime(m, noID)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.Update(spaceMsg())
	if m.overlay.active() {
		t.Errorf("Space opened actions menu for an item with MalID==0")
	}

	// nil applyStatus → no menu even with a MAL id.
	hasID := []mal.Item{{MalID: 5, Title: "MAL hit"}}
	m2 := newAnimePicker(SourceSeason, "", animeLoadAll(hasID), nil, nil, nil, nil, nil, false)
	loadAnime(m2, hasID)
	m2.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m2.Update(spaceMsg())
	if m2.overlay.active() {
		t.Errorf("Space opened actions menu with nil applyStatus")
	}
}

// TestActionGroups verifies the grouped actions menu: the top menu offers Set
// Status always (+ Set Score / Remove when on the list); the Set Status sub-menu
// drops the current status.
func TestActionGroups(t *testing.T) {
	// On-list item: top menu has all three.
	onList := actionGroupLabels("watching")
	if !sliceContains(onList, "Set Status") || !sliceContains(onList, "Set Score") || !sliceContains(onList, "Remove from My List") {
		t.Errorf("on-list top menu = %v, want all three groups", onList)
	}
	// Off-list item: top menu is Set Status + Open Web.
	off := actionGroupLabels("")
	if len(off) != 2 || off[0] != "Set Status" || off[1] != "Open Web" {
		t.Errorf("off-list top menu = %v, want [Set Status Open Web]", off)
	}
	// Status sub-menu drops the current status.
	watching := statusActionLabels("watching")
	if sliceContains(watching, "Set Watching") {
		t.Errorf("watching sub-menu should hide Set Watching: %v", watching)
	}
	if len(watching) != 4 {
		t.Errorf("watching sub-menu = %d options, want 4: %v", len(watching), watching)
	}
	all := statusActionLabels("")
	if len(all) != 5 {
		t.Errorf("off-list status sub-menu = %d options, want 5: %v", len(all), all)
	}
}

// TestAnimePickerDefaultStatus verifies the status filter default is "My List"
// for the Season source and "All" elsewhere.
func TestAnimePickerDefaultStatus(t *testing.T) {
	season := newAnimePicker(SourceSeason, "", animeLoadAll([]mal.Item{{}}), nil, nil, nil, nil, nil, false)
	if season.filter.Status != "My List" {
		t.Errorf("Season default status = %q, want My List", season.filter.Status)
	}
	list := newAnimePicker(SourceList, "", animeLoadAll([]mal.Item{{}}), nil, nil, nil, nil, nil, false)
	if list.filter.Status != "All" {
		t.Errorf("My List default status = %q, want All", list.filter.Status)
	}
	search := newAnimePicker(SourceSeason, "x", animeLoadAll([]mal.Item{{}}), nil, nil, nil, nil, nil, false)
	if search.filter.Status != "All" {
		t.Errorf("Search default status = %q, want All", search.filter.Status)
	}
}

// TestAnimePickerSetScore verifies Space → Set Score → picker → apply: the score
// is set on MAL (via the injected fn) and on the local item; "Remove Score" → 0.
func TestAnimePickerSetScore(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "Alpha", ListStatus: "watching", Score: 0}}
	var gotMalID, gotScore int
	applyScore := func(malID, score int) bool { gotMalID, gotScore = malID, score; return true }
	applyStatus := func(int, int, StatusAction) bool { return false } // dummy; this test exercises score
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), applyStatus, applyScore, nil, nil, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	m.Update(spaceMsg())
	if !sliceContains(m.overlay.items, "Set Score") {
		t.Fatalf("on-list item should offer Set Score: %v", m.overlay.items)
	}
	overlaySelect(t, m, "Set Score")
	m.Update(enterMsg())
	if m.overlay.kind != animeOverlayScore {
		t.Fatalf("Set Score did not open score overlay (kind=%v)", m.overlay.kind)
	}
	overlaySelect(t, m, "7")
	_, cmd := m.Update(enterMsg())
	if cmd == nil {
		t.Fatal("no score apply cmd")
	}
	m.Update(cmd())
	if gotMalID != 1 || gotScore != 7 {
		t.Errorf("applyScore got malID=%d score=%d, want 1/7", gotMalID, gotScore)
	}
	if m.items[0].Score != 7 {
		t.Errorf("item.Score = %d, want 7", m.items[0].Score)
	}

	// Remove Score → 0.
	m.Update(spaceMsg())
	overlaySelect(t, m, "Set Score")
	m.Update(enterMsg())
	overlaySelect(t, m, "Remove Score")
	_, cmd = m.Update(enterMsg())
	m.Update(cmd())
	if gotScore != 0 {
		t.Errorf("Remove Score: applyScore got %d, want 0", gotScore)
	}
	if m.items[0].Score != 0 {
		t.Errorf("item.Score = %d, want 0", m.items[0].Score)
	}

	// Off-list item: Set Score (and Remove) hidden.
	noList := []mal.Item{{MalID: 2, Title: "Beta", ListStatus: ""}}
	m2 := newAnimePicker(SourceSeason, "", animeLoadAll(noList), nil, applyScore, nil, nil, nil, false)
	loadAnime(m2, noList)
	if sliceContains(actionGroupLabels(""), "Set Score") {
		t.Errorf("off-list item should hide Set Score")
	}
}

// TestAnimePickerLatestEpisode verifies the focused airing anime's metadata shows
// watched/aired/total once the latest-aired fetch resolves.
func TestAnimePickerLatestEpisode(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "Airing A", TotalEps: 12, WatchedEps: 0, AirStatus: "currently_airing", ListStatus: "watching"}}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, func(*mal.Item) int { return 4 }, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	// Before the fetch resolves: plain watched/total.
	if strings.Contains(m.View(), "ep 0/4/12") {
		t.Fatalf("before fetch, should not show aired")
	}
	cmd := m.latestEpisodeCmd()
	if cmd == nil {
		t.Fatal("latestEpisodeCmd returned nil for an airing anime")
	}
	m.Update(cmd()) // latestEpMsg → cache m.aired[1]=4
	if m.aired[1] != 4 {
		t.Errorf("aired cache = %v, want {1:4}", m.aired)
	}
	if !strings.Contains(m.View(), "ep 0/4/12") {
		t.Errorf("after fetch, metadata should show 'ep 0/4/12'")
	}
}

// TestAnimePickerLatestEpisodeNoPoisonOnFailure verifies a failed latest-aired
// fetch (aired 0) does not poison the cache, so a later successful fetch for the
// same anime still wins. This guards the "shows 100" bug: mal.LatestEpisode now
// returns 0 (never the wrong page-1 value) on failure, and the cache must not
// store that 0.
func TestAnimePickerLatestEpisodeNoPoisonOnFailure(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "Airing A", TotalEps: 12, AirStatus: "currently_airing", ListStatus: "watching"}}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, func(*mal.Item) int { return 4 }, nil, false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	// A failed fetch (0) must not be cached.
	m.Update(latestEpMsg{malID: 1, aired: 0})
	if _, ok := m.aired[1]; ok {
		t.Errorf("aired cache poisoned with 0 after a failed fetch; want entry absent")
	}

	// Since nothing was cached, a later successful fetch still populates it.
	cmd := m.latestEpisodeCmd()
	if cmd == nil {
		t.Fatal("latestEpisodeCmd returned nil after a failed fetch; want a retry cmd")
	}
	m.Update(cmd()) // latestEpMsg → cache m.aired[1]=4
	if m.aired[1] != 4 {
		t.Errorf("aired cache = %v after successful fetch, want {1:4}", m.aired)
	}
}

// ---- release picker ----

func TestReleasePickerRender(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("Erai-raws", "1080p", 1, false),
		mkRel("SubsPlease", "1080p", 2, false),
		mkRel("EMBER", "720p", 1, false),
	}
	item := &mal.Item{Title: "Frieren", TotalEps: 28, WatchedEps: 11}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
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

// TestReleasePickerFilterEnterAccepts: Enter in filter mode keeps the filter
// applied and returns to normal mode (no select/quit). Guards the filter.go
// change that makes the fuzzy text persist beyond Filtering mode.
func TestReleasePickerFilterEnterAccepts(t *testing.T) {
	all := []*animetosho.Release{
		{Entry: &animetosho.Entry{Title: "Alpha 1080p"}, Group: "A", Resolution: "1080p"},
		{Entry: &animetosho.Entry{Title: "Beta 720p"}, Group: "B", Resolution: "720p"},
	}
	item := &mal.Item{Title: "Show"}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.filter.Episode = 0 // disable the default episode filter so only fuzzy applies
	m.applyFilter()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('/'))
	m.Update(keyMsg('8')) // matches "1080p" (Alpha), not "720p" (Beta)
	if !m.filter.Filtering || len(m.view) != 1 {
		t.Fatalf("setup: want filter mode + 1 match, got Filtering=%v view=%d", m.filter.Filtering, len(m.view))
	}
	m.Update(enterMsg())
	if m.filter.Filtering {
		t.Errorf("after Enter: Filtering=true, want false (accept → normal mode)")
	}
	if m.filter.FuzzyText != "8" {
		t.Errorf("after Enter: FuzzyText=%q, want '8' (kept)", m.filter.FuzzyText)
	}
	if len(m.view) != 1 {
		t.Errorf("after Enter: view=%d, want 1 (filter still applied)", len(m.view))
	}
	if m.result.Release != nil {
		t.Errorf("after Enter: result.Release set; Enter must not select")
	}
	// Esc then discards it (re-enter filter mode and re-type first).
	m.Update(keyMsg('/'))
	m.Update(keyMsg('8'))
	m.Update(escMsg())
	if m.filter.FuzzyText != "" || len(m.view) != 2 {
		t.Errorf("after Esc: FuzzyText=%q view=%d, want '' and 2 (discarded)", m.filter.FuzzyText, len(m.view))
	}
}

func TestReleasePickerDefaultFilters(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "720p", 1, false),
		mkRel("b", "1080p", 2, false),
		mkRel("c", "2160p", 3, false),
		mkRel("d", "1080p", 4, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 10}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	if m.filter.Quality != "" {
		t.Errorf("default quality = %q, want \"\" (all)", m.filter.Quality)
	}
	if m.filter.Episode != 11 {
		t.Errorf("default episode = %d, want 11", m.filter.Episode)
	}
}

func TestReleasePickerDefaultEpisodeFinished(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 12, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 12}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	if m.filter.Episode != 0 {
		t.Errorf("default episode (finished) = %d, want 0", m.filter.Episode)
	}
}

// TestReleasePickerEpisodeDisabled verifies that with disableEpisode the default
// episode filter is 0 and 'e' is a no-op (latest-uploads mode).
func TestReleasePickerEpisodeDisabled(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 5, false)}
	item := &mal.Item{Title: "Latest uploads"}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), true, nil, nil, false)
	if m.filter.Episode != 0 {
		t.Errorf("disabled default episode = %d, want 0", m.filter.Episode)
	}
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(keyMsg('e'))
	if m.overlay.active() {
		t.Errorf("'e' opened an overlay in episodeDisabled mode")
	}
}

func TestReleasePickerOverlayGroup(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("Erai-raws", "1080p", 1, false),
		mkRel("SubsPlease", "1080p", 2, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('g'))
	if !m.overlay.active() || m.overlay.kind != overlayGroup {
		t.Fatalf("g did not open group overlay (active=%t kind=%v)", m.overlay.active(), m.overlay.kind)
	}
	if len(m.overlay.items) != 3 {
		t.Errorf("overlay items = %v, want 3", m.overlay.items)
	}
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

func TestReleasePickerOverlayQuality(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
		mkRel("b", "720p", 2, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('r'))
	if !m.overlay.active() || m.overlay.kind != overlayQuality {
		t.Fatalf("r did not open quality overlay")
	}
	m.Update(enterMsg())
	if m.filter.Quality != "" {
		t.Errorf("Quality = %q, want \"\" (All)", m.filter.Quality)
	}
}

func TestReleasePickerOverlayEpisodeEnter(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
		mkRel("b", "1080p", 5, false),
		mkRel("c", "1080p", 5, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.filter.Episode = 0
	m.applyFilter()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('e'))
	m.Update(keyMsg('5'))
	m.Update(enterMsg())
	if m.filter.Episode != 5 {
		t.Errorf("Episode = %d, want 5", m.filter.Episode)
	}
	m.Update(releasesLoadedMsg{releases: all, ep: 5})
	if len(m.view) != 2 {
		t.Errorf("after ep=5 filter, view len = %d, want 2", len(m.view))
	}
}

func TestReleasePickerOverlayEpisodeEscCancels(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 6, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 5}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
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

func TestReleasePickerSortOverlay(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(keyMsg('s'))
	if !m.overlay.active() || m.overlay.kind != overlaySort {
		t.Fatalf("s did not open sort overlay")
	}
	// Cursor on Newest (0); move to Oldest (1), enter.
	m.Update(downMsg())
	m.Update(enterMsg())
	if m.filter.Sort != "oldest" {
		t.Errorf("Sort = %q, want oldest", m.filter.Sort)
	}
	if m.overlay.active() {
		t.Errorf("overlay still active after Enter")
	}
}

// TestReleasePickerEnterPlaysDDownloads verifies Enter selects play and 'd'
// selects download (the play/download prompt is gone).
func TestReleasePickerEnterPlaysDDownloads(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(enterMsg())
	if m.result.Action != "play" || m.result.Release == nil {
		t.Errorf("Enter: Action=%q Release=%v, want play/<non-nil>", m.result.Action, m.result.Release)
	}

	// Fresh picker for the 'd' case.
	m = newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(keyMsg('d'))
	if m.result.Action != "download" || m.result.Release == nil {
		t.Errorf("d: Action=%q Release=%v, want download/<non-nil>", m.result.Action, m.result.Release)
	}
}

// TestReleasePickerReusesCachedAired: when the anime picker cached the aired
// count on the item (AiredEps), the release picker seeds m.aired from it and
// skips the aired fetch — so entering the release screen doesn't re-fetch.
func TestReleasePickerReusesCachedAired(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	fn := func(*mal.Item) int { return 9 }

	cached := newReleasePicker(&mal.Item{MalID: 5, TotalEps: 12, AiredEps: 7}, "", "", "newest", fetchAll(all), false, nil, fn, false)
	if cached.aired != 7 {
		t.Errorf("cached: m.aired = %d, want 7 (reused from item.AiredEps)", cached.aired)
	}
	if cached.airedFetchCmd() != nil {
		t.Errorf("cached: airedFetchCmd = non-nil, want nil (no re-fetch)")
	}

	uncached := newReleasePicker(&mal.Item{MalID: 5, TotalEps: 12}, "", "", "newest", fetchAll(all), false, nil, fn, false)
	if uncached.aired != 0 {
		t.Errorf("uncached: m.aired = %d, want 0", uncached.aired)
	}
	if uncached.airedFetchCmd() == nil {
		t.Errorf("uncached: airedFetchCmd = nil, want a fetch cmd")
	}
}

// TestReleasePickerActionsMenu verifies Space → Play/Download/Copy Magnet menu.
func TestReleasePickerActionsMenu(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	all[0].Entry.Magnet = "magnet:?xt=urn:btih:FAKE"
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}

	var copied string
	copyFn := func(s string) error { copied = s; return nil }

	// Play via the menu quits with action "play".
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, copyFn, nil, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(spaceMsg())
	if !m.overlay.active() || m.overlay.kind != overlayActions {
		t.Fatalf("Space did not open actions menu")
	}
	m.Update(enterMsg()) // cursor on Play (index 0)
	if m.result.Action != "play" || m.result.Release == nil {
		t.Errorf("Play: Action=%q Release=%v, want play/<non-nil>", m.result.Action, m.result.Release)
	}

	// Copy Magnet via the menu: copies the magnet, sets a toast, does NOT select.
	m2 := newReleasePicker(item, "", "", "newest", fetchAll(all), false, copyFn, nil, false)
	loadReleases(m2, all)
	m2.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m2.Update(spaceMsg())
	m2.overlay.cursor = 2 // Copy Magnet URL
	m2.Update(enterMsg())
	if copied != "magnet:?xt=urn:btih:FAKE" {
		t.Errorf("copyMagnet got %q, want the magnet", copied)
	}
	if m2.toast == "" {
		t.Errorf("toast not set after copy")
	}
	if m2.result.Release != nil {
		t.Errorf("Copy Magnet should not select a release")
	}
}

func TestReleasePickerEscBack(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(escMsg())
	if !m.result.Back {
		t.Errorf("Esc did not set Back")
	}
}

// TestReleasePickerCyclicNav: 'k' at the first release wraps to the last; 'j' at
// the last wraps to the first.
func TestReleasePickerCyclicNav(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
		mkRel("b", "1080p", 2, false),
		mkRel("c", "1080p", 3, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
	loadReleases(m, all)
	m.filter.Episode = 0 // show all episodes so all 3 releases are in view
	m.applyFilter()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.cursor = 0
	m.Update(keyMsg('k'))
	if m.cursor != 2 {
		t.Errorf("k at first: cursor = %d, want 2 (wrap to last)", m.cursor)
	}
	m.Update(keyMsg('j'))
	if m.cursor != 0 {
		t.Errorf("j at last: cursor = %d, want 0 (wrap to first)", m.cursor)
	}
}

func TestReleasePickerEscInOverlayCancels(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, nil, nil, false)
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

// ---- completed prompt (green-flash modal) ----

// TestCompletedPromptYesFlashes verifies 'y' marks result true and enters the
// green-flash phase (a tick later it quits).
func TestCompletedPromptYesFlashes(t *testing.T) {
	m := &completedPrompt{title: "Frieren", phase: phaseConfirm, result: false}
	model, _ := m.Update(enterMsg())
	cp := model.(*completedPrompt)
	if !cp.result {
		t.Errorf("Enter: result = false, want true")
	}
	if cp.phase != phaseDone {
		t.Errorf("Enter: phase = %v, want phaseDone", cp.phase)
	}
	// A success tick should now quit (tea.Quit is a non-nil cmd).
	model, cmd := cp.Update(successTickMsg{})
	if cmd == nil {
		t.Errorf("after tick, cmd = nil, want a tea.Quit cmd")
	}
	_ = model
}

// TestCompletedPromptEscIsNo verifies Esc/cancel means NO (not the old default-yes).
func TestCompletedPromptEscIsNo(t *testing.T) {
	m := &completedPrompt{title: "Frieren", phase: phaseConfirm, result: true}
	model, _ := m.Update(escMsg())
	cp := model.(*completedPrompt)
	if cp.result {
		t.Errorf("Esc: result = true, want false (cancel ≠ yes)")
	}
}

// TestRenderListOverlayScrolls verifies that a long overlay list is windowed
// around the cursor (with ↑/↓ hints) instead of rendering every item — the fix
// for the season filter overflowing its pane.
func TestRenderListOverlayScrolls(t *testing.T) {
	items := make([]string, 50)
	for i := range items {
		items[i] = fmt.Sprintf("Season %d", 2000+i)
	}
	out := renderListOverlayContent("Season", items, 40, 8)
	for _, must := range []string{"↑ more", "↓ more", "(50)"} {
		if !strings.Contains(out, must) {
			t.Errorf("windowed overlay missing %q\n%s", must, out)
		}
	}
	// The focused item must be present; far-off items must not be.
	if !strings.Contains(out, "Season 2040") {
		t.Errorf("cursor item missing from window")
	}
	if strings.Contains(out, "Season 2000") {
		t.Errorf("first item leaked into a scrolled-down window")
	}
	// And it must fit: never more than ~maxItems+title+hints lines.
	if got := len(strings.Split(out, "\n")); got > 12 {
		t.Errorf("rendered %d lines, expected a small windowed slice", got)
	}
}

// TestClampTopScrolloff verifies the vim-style scrolloff: the view keeps ~7
// lines of context above/below the cursor once scrolling engages, and never
// scrolls past the list ends.
func TestClampTopScrolloff(t *testing.T) {
	const ps, total = 20, 100
	wantBottom := func(c int) int { return c - (ps - 1) + scrollOff } // cursor sits 7 above the bottom edge

	if got := clampTop(5, 0, ps, total); got != 0 {
		t.Errorf("cursor=5 topItem=%d want 0 (within top scrolloff)", got)
	}
	if got := clampTop(50, 0, ps, total); got != wantBottom(50) {
		t.Errorf("cursor=50 topItem=%d want %d (cursor kept %d from bottom)", got, wantBottom(50), scrollOff)
	}
	// Cursor stays at a fixed view position while scrolling down (scrolloff margin).
	prev := clampTop(60, 0, ps, total)
	for c := 61; c < 80; c++ {
		cur := clampTop(c, prev, ps, total)
		if c-cur != c-1-prev { // view position unchanged
			t.Errorf("cursor=%d view-pos drifted: topItem %d→%d", c, prev, cur)
		}
		prev = cur
	}
	if got := clampTop(total-1, 0, ps, total); got != total-ps {
		t.Errorf("last cursor topItem=%d want %d (no scroll past end)", got, total-ps)
	}
	if got := clampTop(0, 50, ps, total); got != 0 {
		t.Errorf("cursor=0 topItem=%d want 0 (no scroll past start)", got)
	}
}

// firstLine returns the first newline-separated chunk of s.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
