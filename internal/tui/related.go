package tui

import (
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"ani/internal/mal"
)

// RelatedSource supplies the anime picker's series view (": Show Series"):
// the related ring of one anime, and full items for its entries. nil (the
// no-MAL paths) hides the command.
type RelatedSource struct {
	List func(baseID int) ([]mal.RelatedEntry, error)
	Item func(id int) (mal.Item, error)
}

// seriesDetailsCap bounds how many entries get their full details fetched
// when building a series view (each is one request; a monster franchise like
// One Piece has 90+ relations). Past the cap, entries stay thin (title +
// cover) and keep ring order after the dated ones.
var seriesDetailsCap = 40

// seriesDetailConcurrency is how many detail fetches run at once while
// building a series view.
const seriesDetailConcurrency = 6

// seriesEntry is one anime in the series view: its relation to the franchise
// anchor ("" for the anchor itself) and its item — full when details were
// fetched, otherwise the thin ring entry (title/cover).
type seriesEntry struct {
	Relation string
	Item     mal.Item
	Full     bool
}

// relatedRingMsg carries one anime's related ring for the focus warm-up (the
// preview's has-series indicator and the palette's gating). An error just
// skips caching — the next focus of that anime retries it.
type relatedRingMsg struct {
	base    int
	entries []mal.RelatedEntry
	err     error
}

// ringWarmCmd fetches the focused row's related ring once per anime per
// session, so the preview can indicate whether it has a series at all and the
// palette can offer "Show Series" only when it does. A failed warm-up doesn't
// retry on every focus (the Show Series flow still fetches fresh).
func (m *animePicker) ringWarmCmd() tea.Cmd {
	if m.related == nil || m.related.List == nil {
		return nil
	}
	id := m.focusedID()
	if id == 0 {
		return nil
	}
	if _, ok := m.rings[id]; ok || m.ringErrs[id] {
		return nil
	}
	list := m.related.List
	return func() tea.Msg {
		entries, err := list(id)
		return relatedRingMsg{base: id, entries: entries, err: err}
	}
}

// seriesSummaryLine is the preview's has-series indicator for the focused row:
// the distinct relation labels of its warmed ring — "Series: Sequel · Side
// story". Empty when the ring isn't warmed yet or has nothing (so anime
// without a series show no line at all).
func (m *animePicker) seriesSummaryLine() string {
	if m.seriesView || m.related == nil {
		return ""
	}
	ring, ok := m.rings[m.focusedID()]
	if !ok || len(ring) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var parts []string
	for _, e := range ring {
		if e.Relation == "" || seen[e.Relation] {
			continue
		}
		seen[e.Relation] = true
		parts = append(parts, e.Relation)
		if len(parts) == 4 {
			break // a monster franchise repeats the same few labels anyway
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Series: " + strings.Join(parts, " · ")
}

// seriesLoadedMsg carries a built series view (entries empty = nothing
// related or the build failed — the list stays).
type seriesLoadedMsg struct {
	base    int
	entries []seriesEntry
}

// buildSeries walks the whole franchise around base: the season chain (each
// sequel/prequel/parent's own ring, chained transitively) plus everything
// hanging off it (side stories, OVAs, movies, spin-offs, …), deduplicated and
// including base itself. Entries past seriesDetailsCap stay thin. Ordered by
// air date (StartDate; unknown dates sink), thin entries last in ring order.
// Best-effort: a failed ring or detail fetch just drops that branch/entry.
func buildSeries(list func(int) ([]mal.RelatedEntry, error), item func(int) (mal.Item, error), base mal.Item) []seriesEntry {
	seen := map[int]bool{base.MalID: true}
	enqueued := map[int]bool{base.MalID: true}
	var entries []seriesEntry
	queue := []int{base.MalID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ring, err := list(cur)
		if err != nil {
			continue // that branch ends here; the rest of the walk proceeds
		}
		for _, e := range ring {
			if e.MalID == 0 {
				continue
			}
			if !seen[e.MalID] {
				seen[e.MalID] = true
				entries = append(entries, seriesEntry{
					Relation: e.Relation,
					Item:     mal.Item{MalID: e.MalID, Title: e.Title, CoverURL: e.CoverURL},
				})
			}
			// Only the season chain leads further out: a movie's ring just
			// points back at its parent, so expanding it only costs requests.
			if e.Kind == "sequel" || e.Kind == "prequel" || e.Kind == "parent_story" {
				if !enqueued[e.MalID] {
					enqueued[e.MalID] = true
					queue = append(queue, e.MalID)
				}
			}
		}
	}

	// Details (dates, list status, genres) for the first seriesDetailsCap
	// entries, fetched concurrently; each goroutine writes its own slot.
	n := len(entries)
	if n > seriesDetailsCap {
		n = seriesDetailsCap
	}
	sem := make(chan struct{}, seriesDetailConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			it, err := item(entries[i].Item.MalID)
			if err != nil || it.MalID == 0 {
				return
			}
			entries[i].Item = it
			entries[i].Full = true
		}(i)
	}
	wg.Wait()

	// Chronological: by StartDate (ISO date, so lexical = chronological;
	// unknown sinks), stable so the undated tail keeps ring order. The anchor
	// joins as a full entry — the list row already carries its fields.
	all := append([]seriesEntry{{Item: base, Full: true}}, entries...)
	sort.SliceStable(all, func(i, j int) bool {
		di, dj := all[i].Item.StartDate, all[j].Item.StartDate
		if di == "" || dj == "" {
			return di != "" && dj == "" // dated before undated; else keep order
		}
		return di < dj
	})
	return all
}

// seriesBuildCmd builds the whole series view around the focused row in one
// background pass (rings for the season chain + details for the entries).
func (m *animePicker) seriesBuildCmd(base mal.Item) tea.Cmd {
	list, item := m.related.List, m.related.Item
	return func() tea.Msg {
		return seriesLoadedMsg{base: base.MalID, entries: buildSeries(list, item, base)}
	}
}

// openSeriesView swaps the left pane to the focused anime's whole franchise —
// every season, side story (OVAs/specials/movies), and spin-off around it,
// ordered by air date — with every list affordance intact: j/k, the fuzzy
// filter (relation labels are searchable), the per-anime actions, and Enter.
// Esc returns to the normal list, and opening the view again on a series row
// re-anchors it there.
func (m *animePicker) openSeriesView() (tea.Model, tea.Cmd) {
	if m.related == nil || m.related.List == nil {
		return m, nil
	}
	if m.cursor < 0 || m.cursor >= len(m.view) || m.view[m.cursor].MalID == 0 {
		return m, nil
	}
	row := m.view[m.cursor]
	if entries, ok := m.series[row.MalID]; ok {
		return m.applySeriesView(row.MalID, entries)
	}
	m.pendingSeries = true // open when the build lands
	return m, m.seriesBuildCmd(row)
}

// applySeriesView mounts a built (or cached) series as the list.
func (m *animePicker) applySeriesView(base int, entries []seriesEntry) (tea.Model, tea.Cmd) {
	if len(entries) <= 1 {
		return m, nil // only the anchor: nothing related / build failed — stay on the list
	}
	if !m.seriesView {
		// Restore points for Esc-back (a re-anchor keeps the originals).
		m.seriesCursor = m.cursor
		m.seriesStatus = m.filter.Status
		m.seriesSort = m.filter.Sort
	}
	m.seriesView = true
	m.seriesLabels = map[int]string{}
	items := make([]mal.Item, 0, len(entries))
	for _, e := range entries {
		m.seriesLabels[e.Item.MalID] = e.Relation
		if e.Full {
			m.peekItems[e.Item.MalID] = e.Item
		}
		items = append(items, e.Item)
	}
	m.items = items
	m.topItem = 0
	// The franchise mixes list statuses, so the list's status filter would
	// hide most of it (thin rows have none yet) — browse the view unfiltered.
	// "relevance" preserves the build's chronological order (the global Air
	// Date sort is newest-first, wrong for reading a series); the sort overlay
	// still re-sorts on demand. Both filters restore on the way out.
	m.filter.Status = "All"
	m.filter.Sort = "relevance"
	m.applyFilter()
	// Open on the anime the view was anchored on — it may sit anywhere in the
	// chronological order, not at the top.
	if i := indexOfID(m.view, base); i >= 0 {
		m.cursor = i
	} else {
		m.cursor = 0
	}
	m.fixScroll()
	// Prefetch every entry's cover in one batch (like the list's pages), so
	// j/k through the view shows thumbnails instantly instead of paying a
	// download per focus.
	urls := make([]string, 0, len(items))
	for _, it := range items {
		if it.CoverURL != "" {
			urls = append(urls, it.CoverURL)
		}
	}
	return m, tea.Batch(m.focusCmd(), m.cover.Download(urls))
}

// closeSeriesView returns to the normal list. The reload is served from the
// session cache (instant), and the pre-view cursor, status filter, and sort
// come back with it.
func (m *animePicker) closeSeriesView() (tea.Model, tea.Cmd) {
	m.seriesView = false
	m.seriesLabels = nil
	if m.seriesStatus != "" {
		m.filter.Status = m.seriesStatus
		m.seriesStatus = ""
	}
	if m.seriesSort != "" {
		m.filter.Sort = m.seriesSort
		m.seriesSort = ""
	}
	m.pendingCursor = m.seriesCursor // applied by the reload's applyLoaded
	m.seriesCursor = -1
	m.loading = true
	return m, m.loadCmd(m.source, m.query, m.season)
}

// seriesFocusCmd fetches the focused series row's full item — past-cap rows
// stay thin (title/cover) until focused; writes and Enter stay disabled for
// them, so they can never hit MAL with a half-loaded item (e.g. watched=0).
func (m *animePicker) seriesFocusCmd() tea.Cmd {
	if !m.seriesView || m.related == nil || m.related.Item == nil {
		return nil
	}
	id := m.focusedID()
	if id == 0 || m.itemPending[id] {
		return nil
	}
	if _, ok := m.peekItems[id]; ok {
		return nil // already full
	}
	m.itemPending[id] = true
	return m.peekItemFetchCmd(id)
}

// peekItemMsg carries the full item for one series row (err = the fetch
// failed; the row stays thin and the next focus retries).
type peekItemMsg struct {
	malID int
	item  mal.Item
	err   error
}

// peekItemFetchCmd fetches the full item for one series row id.
func (m *animePicker) peekItemFetchCmd(id int) tea.Cmd {
	item := m.related.Item
	return func() tea.Msg {
		it, err := item(id)
		return peekItemMsg{malID: id, item: it, err: err}
	}
}

// applyPeekItem ingests a series row's full item: cache it, patch its row in
// m.items (the view rows are copies), and refresh the pane when it's the
// focused one. The cursor stays on the same anime across the re-sort the
// freshly arrived air date may cause.
func (m *animePicker) applyPeekItem(msg peekItemMsg) (tea.Model, tea.Cmd) {
	delete(m.itemPending, msg.malID)
	if msg.err != nil {
		return m, nil // row stays thin; the next focus retries
	}
	m.peekItems[msg.malID] = msg.item
	for i := range m.items {
		if m.items[i].MalID == msg.malID {
			m.items[i] = msg.item
			break
		}
	}
	focused := m.focusedID()
	m.applyFilter()
	if i := indexOfID(m.view, focused); i >= 0 {
		m.cursor = i
		m.fixScroll()
	}
	if focused == msg.malID {
		return m, m.focusCmd()
	}
	return m, nil
}

// indexOfID finds id's row index in items (-1 when absent).
func indexOfID(items []mal.Item, id int) int {
	for i, it := range items {
		if it.MalID == id {
			return i
		}
	}
	return -1
}
