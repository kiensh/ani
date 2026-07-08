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
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), false)
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
	m := newAnimePicker(SourceSeason, "rezero", animeLoadAll(items), false)
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
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), false)
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

// TestAnimePickerRemoveG verifies g/G are no-ops (not top/bottom jumps).
func TestAnimePickerRemoveG(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}, {MalID: 2, Title: "B"}, {MalID: 3, Title: "C"}}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), false)
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

// TestAnimePickerStatusFilter opens the status overlay, selects Completed, and
// checks the view is filtered to completed items.
func TestAnimePickerStatusFilter(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, Title: "Alpha", ListStatus: "watching"},
		{MalID: 2, Title: "Beta", ListStatus: "completed"},
		{MalID: 3, Title: "Gamma", ListStatus: "completed"},
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})

	// 't' opens the 8-item status overlay: All, Not in My List, My List,
	// Watching, Completed, … → Completed is index 4.
	m.Update(keyMsg('t'))
	if !m.overlay.active() || m.overlay.kind != animeOverlayStatus {
		t.Fatalf("t did not open status overlay")
	}
	for i := 0; i < 4; i++ {
		m.Update(downMsg())
	}
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
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), false)
	loadAnime(m, items)
	if m.filter.Sort != "popularity" {
		t.Fatalf("default sort = %q, want popularity", m.filter.Sort)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.Update(keyMsg('s'))
	if !m.overlay.active() || m.overlay.kind != animeOverlaySort {
		t.Fatalf("s did not open sort overlay")
	}
	// Cursor on Popularity (0); move to Score (1), enter.
	m.Update(downMsg())
	m.Update(enterMsg())
	if m.filter.Sort != "score" {
		t.Errorf("Sort = %q, want score", m.filter.Sort)
	}
	if m.overlay.active() {
		t.Errorf("overlay still active after Enter")
	}
}

// TestAnimePickerStatusOptionsBySource verifies My List offers 6 status options
// (no Not in My List / My List) while Season and Search offer 8.
func TestAnimePickerStatusOptionsBySource(t *testing.T) {
	listM := newAnimePicker(SourceList, "", animeLoadAll([]mal.Item{{}}), false)
	if n := len(listM.statusOptions()); n != 6 {
		t.Errorf("My List status options = %d, want 6", n)
	}
	seasonM := newAnimePicker(SourceSeason, "", animeLoadAll([]mal.Item{{}}), false)
	if n := len(seasonM.statusOptions()); n != 8 {
		t.Errorf("Season status options = %d, want 8", n)
	}
	searchM := newAnimePicker(SourceSeason, "x", animeLoadAll([]mal.Item{{}}), false)
	if n := len(searchM.statusOptions()); n != 8 {
		t.Errorf("Search status options = %d, want 8", n)
	}
}

// TestAnimePickerSeasonDisabledInMyList verifies 'e' is a no-op in My List
// (season filter is forced to All there).
func TestAnimePickerSeasonDisabledInMyList(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "A"}}
	m := newAnimePicker(SourceList, "", animeLoadAll(items), false)
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
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), false)
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
	m := newAnimePicker(SourceSeason, "", load, false)
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
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), false)
	loadAnime(m, items)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) > 20 {
		t.Errorf("View rendered %d lines, want <= 20 (terminal height)", len(lines))
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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

func TestReleasePickerDefaultFilters(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "720p", 1, false),
		mkRel("b", "1080p", 2, false),
		mkRel("c", "2160p", 3, false),
		mkRel("d", "1080p", 4, false),
	}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 10}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), true, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(enterMsg())
	if m.result.Action != "play" || m.result.Release == nil {
		t.Errorf("Enter: Action=%q Release=%v, want play/<non-nil>", m.result.Action, m.result.Release)
	}

	// Fresh picker for the 'd' case.
	m = newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(keyMsg('d'))
	if m.result.Action != "download" || m.result.Release == nil {
		t.Errorf("d: Action=%q Release=%v, want download/<non-nil>", m.result.Action, m.result.Release)
	}
}

func TestReleasePickerEscBack(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
	loadReleases(m, all)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(escMsg())
	if !m.result.Back {
		t.Errorf("Esc did not set Back")
	}
}

func TestReleasePickerEscInOverlayCancels(t *testing.T) {
	all := []*animetosho.Release{mkRel("a", "1080p", 1, false)}
	item := &mal.Item{Title: "X", TotalEps: 12, WatchedEps: 0}
	m := newReleasePicker(item, "", "", "newest", fetchAll(all), false, false)
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
