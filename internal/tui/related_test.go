package tui

import (
	"errors"
	"strings"
	"testing"

	"ani/internal/mal"
)

// seriesFixture wires an in-memory RelatedSource: rings per id + full items
// for the ids that have them. The franchise: Grand Blue (2018) → S2 (2025) →
// S3 (2026, via S2's own ring), an OVA (2018) off S1, and a spin-off (2027)
// off S2.
func seriesFixture() (map[int][]mal.RelatedEntry, map[int]mal.Item) {
	rings := map[int][]mal.RelatedEntry{
		100: {
			{Relation: "Side story", Kind: "side_story", MalID: 102, Title: "Grand Blue OVA", CoverURL: "c102"},
			{Relation: "Sequel", Kind: "sequel", MalID: 101, Title: "Grand Blue S2", CoverURL: "c101"},
		},
		101: {
			{Relation: "Prequel", Kind: "prequel", MalID: 100, Title: "Grand Blue"},
			{Relation: "Sequel", Kind: "sequel", MalID: 103, Title: "Grand Blue S3"},
			{Relation: "Spin-off", Kind: "spin_off", MalID: 104, Title: "Grand Blue Kids"},
		},
		103: { // S3 reaches back into the chain
			{Relation: "Prequel", Kind: "prequel", MalID: 101, Title: "Grand Blue S2"},
		},
	}
	full := map[int]mal.Item{
		101: {MalID: 101, Title: "Grand Blue S2", StartDate: "2025-07-01", TotalEps: 12, ListStatus: "completed", WatchedEps: 12, AirStatus: "finished_airing", Studios: "Zero-G"},
		102: {MalID: 102, Title: "Grand Blue OVA", StartDate: "2018-11-01", MediaType: "ova", TotalEps: 1},
		103: {MalID: 103, Title: "Grand Blue S3", StartDate: "2026-07-01", TotalEps: 0, AirStatus: "not_yet_aired"},
	}
	return rings, full
}

// newSeriesViewPicker builds a Season picker on the fixture's anchor row.
func newSeriesViewPicker(items []mal.Item, src *RelatedSource) *animePicker {
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
	m.related = src
	m.filter.Status = "All"
	loadAnime(m, items)
	m.width, m.height = 84, 30
	m.recomputeLayout()
	m.fixScroll()
	return m
}

// TestSeriesView drives the `: Show Series` flow: one background build
// collects the whole franchise (seasons chained through their own rings, side
// stories/spin-offs included), the view opens ordered by air date with every
// entry full, and actions/Enter/filter work on it. Esc restores the list.
func TestSeriesView(t *testing.T) {
	items := []mal.Item{
		{MalID: 100, Title: "Grand Blue", StartDate: "2018-07-01", TotalEps: 12, ListStatus: "watching", WatchedEps: 12, AirStatus: "finished_airing"},
		{MalID: 200, Title: "Unrelated"},
	}
	rings, full := seriesFixture()
	m := newSeriesViewPicker(items, relatedFixture(rings, full))

	// Open via the palette intent: the build runs in the returned cmd; its msg
	// mounts the view.
	_, cmd := m.applyCommand("series")
	if cmd == nil {
		t.Fatal("series: no build cmd for a cold anchor")
	}
	msg := cmd()
	if _, ok := msg.(seriesLoadedMsg); !ok {
		t.Fatalf("build cmd = %T, want seriesLoadedMsg", msg)
	}
	m.Update(msg)
	if !m.seriesView {
		t.Fatal("view did not open after the build landed")
	}
	// The whole franchise, chronological: OVA (2018-11), S2 (2025), S3 (2026)
	// chained via S2's own ring, the undated spin-off last. The anchor itself
	// (2018-07) leads.
	if got := ids(m.view); len(got) != 5 || got[0] != 100 || got[1] != 102 || got[2] != 101 || got[3] != 103 {
		t.Fatalf("series rows = %v, want [100 102 101 103 104] by air date", got)
	}
	if got := m.View(); !strings.Contains(got, "Series  (5)") {
		t.Errorf("pane title should read Series (5):\n%s", firstLine(got))
	}
	if got := m.View(); !strings.Contains(got, "Sequel · Grand Blue S2") {
		t.Errorf("rows should carry their relation label:\n%s", got)
	}
	// The build cached it — re-opening remounts the same view, no rebuild.
	if _, cached := m.series[100]; !cached {
		t.Fatal("build wasn't cached for the anchor")
	}
	m.applyCommand("series")
	if got := ids(m.view); len(got) != 5 {
		t.Errorf("re-open changed the view: %v", got)
	}

	// Every entry the fixture had details for is full: writes and Enter work
	// right away (S2).
	m.cursor = indexOfID(m.view, 101)
	if it := m.currentItemCopy(); it == nil || it.MalID != 101 || it.TotalEps != 12 {
		t.Fatalf("currentItemCopy on S2 = %+v, want the full item", it)
	}
	model, _ := m.Update(enterMsg())
	if res := model.(*animePicker).result.Anime; res == nil || res.MalID != 101 {
		t.Errorf("Enter in series view selected %v, want S2 (101)", res)
	}
	m.Update(scoreAppliedMsg{malID: 101, score: 8, applied: true})
	if it := m.items[indexOfID(m.items, 101)]; it.Score != 8 {
		t.Errorf("score apply: row.Score = %d, want 8", it.Score)
	}

	// The fuzzy filter matches relation labels, not just titles.
	m.filter.Filtering = true
	m.filter.FuzzyText = "spin-off"
	m.applyFilter()
	if got := ids(m.view); len(got) != 1 || got[0] != 104 {
		t.Errorf("filter 'spin-off': view = %v, want [104]", got)
	}
	m.filter.Filtering = false
	m.filter.FuzzyText = ""
	m.applyFilter()

	// Esc restores the normal list: same items, cursor back on the row the
	// view was opened from (not wherever it sat inside).
	m.cursor = 2
	model, cmd = m.Update(escMsg())
	m = model.(*animePicker)
	if m.seriesView {
		t.Fatal("Esc did not leave the series view")
	}
	if cmd == nil {
		t.Fatal("Esc-back issued no reload cmd")
	}
	reload := cmd()
	if _, ok := reload.(itemsLoadedMsg); !ok {
		t.Fatalf("reload cmd = %T, want itemsLoadedMsg", reload)
	}
	m.Update(reload)
	if got := ids(m.items); len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Fatalf("after Esc-back: items = %v, want the original list", got)
	}
	if m.cursor != 0 {
		t.Errorf("after Esc-back: cursor = %d, want 0 (the open-time row restored)", m.cursor)
	}
}

// TestSeriesViewFilterSortRestore: entering the view drops the status filter
// and sorts by air date; Esc brings both back.
func TestSeriesViewFilterSortRestore(t *testing.T) {
	items := []mal.Item{
		{MalID: 100, Title: "Grand Blue", ListStatus: "watching", StartDate: "2018-07-01"},
		{MalID: 201, Title: "Off-list row"},
	}
	rings, full := seriesFixture()
	m := newSeriesViewPicker(items, relatedFixture(rings, full))
	m.filter.Status = "Watching"
	m.filter.Sort = "title"
	m.applyFilter()
	if len(m.view) != 1 {
		t.Fatalf("setup: Watching filter view = %v, want 1 row", ids(m.view))
	}

	_, cmd := m.applyCommand("series")
	m.Update(cmd())
	if m.filter.Status != "All" || m.filter.Sort != "relevance" {
		t.Errorf("in-view filter = %q/%q, want All/relevance (chronological build order)", m.filter.Status, m.filter.Sort)
	}

	model, cmd := m.Update(escMsg())
	m = model.(*animePicker)
	if m.filter.Status != "Watching" || m.filter.Sort != "title" {
		t.Fatalf("after Esc: filter = %q/%q, want Watching/title restored", m.filter.Status, m.filter.Sort)
	}
	m.Update(cmd())
	if len(m.view) != 1 {
		t.Errorf("after Esc-back: view = %v, want the filtered list back", ids(m.view))
	}
}

// TestSeriesViewReanchor: opening the view again on a series row re-anchors
// the franchise there (with its own restore points still pointing at the
// normal list).
func TestSeriesViewReanchor(t *testing.T) {
	items := []mal.Item{{MalID: 100, Title: "Grand Blue", StartDate: "2018-07-01"}}
	rings, full := seriesFixture()
	m := newSeriesViewPicker(items, relatedFixture(rings, full))

	_, cmd := m.applyCommand("series")
	m.Update(cmd())
	// S3 (103) is in the view; anchor on it — same franchise rebuilt around it.
	m.cursor = indexOfID(m.view, 103)
	_, cmd = m.applyCommand("series")
	if cmd == nil {
		t.Fatal("re-anchor should rebuild (S3's own ring isn't cached yet)")
	}
	m.Update(cmd())
	if !m.seriesView {
		t.Fatal("re-anchor left the series view")
	}
	if rel := m.seriesLabels[100]; rel != "Prequel" {
		t.Errorf("re-anchored label for S1 = %q, want Prequel (via the chain)", rel)
	}
	// The cursor opens on the anime the view was anchored on — S3 sits mid-
	// order (after the older seasons), not at the top.
	if m.view[m.cursor].MalID != 103 {
		t.Errorf("re-anchored cursor on %d, want 103 (the anchor row)", m.view[m.cursor].MalID)
	}

	model, cmd := m.Update(escMsg())
	m = model.(*animePicker)
	if m.seriesView {
		t.Fatal("Esc after re-anchor should still leave the view")
	}
	m.Update(cmd())
	if got := ids(m.items); len(got) != 1 || got[0] != 100 {
		t.Errorf("after Esc from re-anchored view: items = %v, want the original list", got)
	}
}

// TestSeriesViewEmptyFranchise: an anime with nothing related stays on the
// list (and the empty build isn't cached — the next open retries).
func TestSeriesViewEmptyFranchise(t *testing.T) {
	rings, _ := seriesFixture()
	items := []mal.Item{{MalID: 300, Title: "Standalone"}, {MalID: 100, Title: "Grand Blue"}}
	m := newSeriesViewPicker(items, relatedFixture(rings, nil))

	m.cursor = 0
	_, cmd := m.applyCommand("series")
	if cmd == nil {
		t.Fatal("no build cmd")
	}
	m.Update(cmd())
	if m.seriesView {
		t.Error("view opened for an anime with no relations")
	}
	if len(m.view) != 2 {
		t.Errorf("list changed: %v", ids(m.view))
	}
	if _, cached := m.series[300]; cached {
		t.Error("empty build was cached — retries would be skipped")
	}
}

// TestSeriesViewNoSource: the command is absent (and a direct intent is a
// no-op) without a RelatedSource — the no-MAL paths.
func TestSeriesViewNoSource(t *testing.T) {
	items := []mal.Item{{MalID: 100, Title: "Grand Blue"}}
	m := newSeriesViewPicker(items, nil)
	for _, c := range m.animeCommands() {
		if c.Intent == "series" {
			t.Errorf("palette offers series commands without a source: %+v", c)
		}
	}
	if model, _ := m.applyCommand("series"); model.(*animePicker).seriesView {
		t.Error("series intent opened a view without a source")
	}
}

// TestSeriesBuildErrors: a failed ring just ends that branch; a failed detail
// fetch leaves its entry thin (listed, still focus-fillable).
func TestSeriesBuildErrors(t *testing.T) {
	items := []mal.Item{{MalID: 100, Title: "Grand Blue", StartDate: "2018-07-01"}}
	rings, full := seriesFixture()
	m := newSeriesViewPicker(items, &RelatedSource{
		List: func(id int) ([]mal.RelatedEntry, error) {
			if id == 101 {
				return nil, errors.New("boom") // S2's ring fails → S3/spin-off unreachable
			}
			return rings[id], nil
		},
		Item: func(id int) (mal.Item, error) {
			if id == 101 {
				return mal.Item{}, errors.New("boom") // S2's details fail → thin row
			}
			return full[id], nil
		},
	})
	_, cmd := m.applyCommand("series")
	msg := cmd().(seriesLoadedMsg)
	m.Update(msg)

	// S2 listed (thin) + OVA full + the anchor; S3 and the spin-off lost with
	// S2's ring.
	if got := ids(m.view); len(got) != 3 {
		t.Fatalf("rows = %v, want [anchor, OVA, thin S2]", got)
	}
	if it := m.items[indexOfID(m.items, 101)]; it.TotalEps != 0 || it.ListStatus != "" {
		t.Errorf("S2 row = %+v, want thin", it)
	}
	// Thin row: writes disabled; the focus fill retries the details.
	i := indexOfID(m.items, 101)
	m.cursor = i
	if it := m.currentItemCopy(); it != nil {
		t.Error("thin row should not be actionable")
	}
	m.Update(peekItemMsg{malID: 101, item: full[101]})
	if it := m.currentItemCopy(); it == nil || it.MalID != 101 {
		t.Fatalf("after focus fill: currentItemCopy = %v, want the full item", it)
	}
}

// TestSeriesDetailsCap: past the cap, entries stay thin and keep ring order
// after the dated ones.
func TestSeriesDetailsCap(t *testing.T) {
	items := []mal.Item{{MalID: 100, Title: "Base", StartDate: "2020-01-01"}}
	ring := make([]mal.RelatedEntry, 5)
	full := map[int]mal.Item{}
	for i := range ring {
		id := 500 + i
		ring[i] = mal.RelatedEntry{Relation: "Side story", Kind: "side_story", MalID: id, Title: "Entry"}
		full[id] = mal.Item{MalID: id, Title: "Entry", StartDate: "2021-01-01", TotalEps: 1}
	}
	old := seriesDetailsCap
	seriesDetailsCap = 2
	defer func() { seriesDetailsCap = old }()

	entries := buildSeries(relatedFixture(map[int][]mal.RelatedEntry{100: ring}, full).List,
		relatedFixture(map[int][]mal.RelatedEntry{100: ring}, full).Item, items[0])
	if len(entries) != 6 {
		t.Fatalf("entries = %d, want 6 (anchor + 5)", len(entries))
	}
	// The anchor (2020) leads; the two dated entries next; the thin tail keeps
	// ring order.
	if !entries[0].Full || entries[0].Item.MalID != 100 {
		t.Errorf("anchor = %+v, want the dated base first", entries[0])
	}
	fullCount := 0
	for _, e := range entries {
		if e.Full {
			fullCount++
		}
	}
	if fullCount != 3 { // anchor + 2 capped details
		t.Errorf("full entries = %d, want 3 (cap applied)", fullCount)
	}
	for i := 1; i+1 < len(entries) && i <= 2; i++ {
		if entries[i].Item.StartDate == "" {
			t.Errorf("entry %d undated but within the cap", i)
		}
	}
	if entries[3].Full || entries[4].Full || entries[5].Full {
		t.Error("past-cap entries should stay thin")
	}
}

// relatedFixture wires an in-memory RelatedSource: rings per id + full items
// for the ids that have them.
func relatedFixture(rings map[int][]mal.RelatedEntry, full map[int]mal.Item) *RelatedSource {
	return &RelatedSource{
		List: func(id int) ([]mal.RelatedEntry, error) { return rings[id], nil },
		Item: func(id int) (mal.Item, error) { return full[id], nil },
	}
}

// TestSeriesIndicatorAndGating: focusing a row warms its ring — the preview
// then shows the has-series line and the palette offers Show Series only for
// anime that actually have relations; standalone anime show neither.
func TestSeriesIndicatorAndGating(t *testing.T) {
	rings, full := seriesFixture()
	rings[300] = nil // standalone
	items := []mal.Item{
		{MalID: 300, Title: "Standalone", TotalEps: 12},
		{MalID: 100, Title: "Grand Blue", TotalEps: 12, StartDate: "2018-07-01"},
	}
	m := newSeriesViewPicker(items, relatedFixture(rings, full))
	m.cursor = 0

	// Uncached: no indicator, no command — then the warm-up lands.
	if got := m.renderMetadata(); strings.Contains(got, "Series:") {
		t.Errorf("indicator before warm:\n%s", got)
	}
	hasSeriesCmd := func() bool {
		for _, c := range m.animeCommands() {
			if c.Intent == "series" {
				return true
			}
		}
		return false
	}
	if hasSeriesCmd() {
		t.Error("palette offers Show Series before the ring is known")
	}
	if cmd := m.ringWarmCmd(); cmd == nil {
		t.Fatal("ringWarmCmd missing for an uncached row")
	} else {
		m.Update(cmd())
	}
	// Standalone: warmed empty ring → still no indicator, no command.
	if got := m.renderMetadata(); strings.Contains(got, "Series:") {
		t.Errorf("indicator on standalone anime:\n%s", got)
	}
	if hasSeriesCmd() {
		t.Error("palette offers Show Series for a standalone anime")
	}

	// Grand Blue: indicator with its distinct labels, command offered.
	m.cursor = 1
	if cmd := m.ringWarmCmd(); cmd == nil {
		t.Fatal("ringWarmCmd missing for the second row")
	} else {
		m.Update(cmd())
	}
	if got := m.renderMetadata(); !strings.Contains(got, "Series: Side story · Sequel") {
		t.Errorf("indicator after warm:\n%s", got)
	}
	if !hasSeriesCmd() {
		t.Error("palette lacks Show Series for an anime with relations")
	}
}

// TestSeriesLineSurvivesShortPane: when the metadata overflows the pane, whole
// lines drop from the least valuable up (rank/members, then wrapped genres)
// instead of chopping the tail — the Series indicator survives unless the pane
// is truly tiny. (The regression: it was appended last and always cut first.)
func TestSeriesLineSurvivesShortPane(t *testing.T) {
	item := mal.Item{MalID: 1, Title: "Mushoku Tensei III: Isekai Ittara Honki Dasu",
		TotalEps: 14, WatchedEps: 13, AirStatus: "currently_airing", ListStatus: "watching",
		MeanScore: 8.66, Genres: "Adventure, Drama, Fantasy, Isekai, Reincarnation",
		Studios: "Studio Bind", StartSeason: "summer 2026", MediaType: "tv",
		Rank: 86, Members: 334000, CoverURL: "u"}
	newPane := func(height int) *animePicker {
		m := newSeriesViewPicker([]mal.Item{item}, &RelatedSource{})
		m.rings[1] = []mal.RelatedEntry{{Relation: "Prequel", Kind: "prequel", MalID: 2}}
		m.height = height
		m.recomputeLayout()
		m.coverText = strings.Repeat("█\n", m.coverRows-1) + "█" // a full-height cover
		return m
	}

	// No personal score: score/rank/members share one line, so the whole
	// entry (9 lines) fits from budget 9 up — including Series.
	for _, h := range []int{29, 28} {
		got := newPane(h).renderMetadata()
		for _, want := range []string{"★ 8.66", "rank #86", "Series: Prequel", "Reincarnation"} {
			if !strings.Contains(got, want) {
				t.Errorf("budget %d: pane lost %q:\n%s", h-19, want, got)
			}
		}
	}
	// Budget 8: the wrapped genres tail drops, Series stays.
	got := newPane(27).renderMetadata()
	if strings.Contains(got, "Reincarnation") {
		t.Errorf("budget 8: genres tail should drop:\n%s", got)
	}
	if !strings.Contains(got, "Series: Prequel") || !strings.Contains(got, "rank #86") {
		t.Errorf("budget 8: Series/stats should survive:\n%s", got)
	}
	// Budget 7: genres gone entirely; Series and the stats line still there.
	got = newPane(26).renderMetadata()
	if strings.Contains(got, "Genres:") {
		t.Errorf("budget 7: genres should drop:\n%s", got)
	}
	if !strings.Contains(got, "Series: Prequel") || !strings.Contains(got, "★ 8.66") {
		t.Errorf("budget 7: Series/score should survive:\n%s", got)
	}
}

// TestStatsLineWithPersonalScore: a personal rating splits rank/members onto
// its own (score-colored) line — and it drops before Series on tight panes.
func TestStatsLineWithPersonalScore(t *testing.T) {
	item := mal.Item{MalID: 1, Title: "Mushoku Tensei III: Isekai Ittara Honki Dasu",
		TotalEps: 14, WatchedEps: 13, AirStatus: "currently_airing", ListStatus: "watching",
		MeanScore: 8.66, Score: 9, Genres: "Adventure, Drama",
		Studios: "Studio Bind", StartSeason: "summer 2026", MediaType: "tv",
		Rank: 86, Members: 334000, CoverURL: "u"}
	newPane := func(height int) *animePicker {
		m := newSeriesViewPicker([]mal.Item{item}, &RelatedSource{})
		m.rings[1] = []mal.RelatedEntry{{Relation: "Prequel", Kind: "prequel", MalID: 2}}
		m.height = height
		m.recomputeLayout()
		m.coverText = strings.Repeat("█\n", m.coverRows-1) + "█"
		return m
	}
	// Roomy pane: both stats lines show (score with your-rating, then rank).
	got := newPane(29).renderMetadata()
	if !strings.Contains(got, "(your: 9)") || !strings.Contains(got, "rank #86") {
		t.Errorf("roomy pane: both stats lines should show:\n%s", got)
	}
	// Tight pane: the rank line (lowest priority) drops before Series.
	got = newPane(27).renderMetadata()
	if strings.Contains(got, "rank #86") {
		t.Errorf("tight pane: rank should drop:\n%s", got)
	}
	if !strings.Contains(got, "(your: 9)") || !strings.Contains(got, "Series: Prequel") {
		t.Errorf("tight pane: score and Series should survive:\n%s", got)
	}
}
