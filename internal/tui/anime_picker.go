package tui

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ani/internal/mal"
	"ani/internal/ui"
)

// AnimeSource selects which MAL dataset the anime picker shows (browse only).
// Search (query != "") is separate; the source is ignored there.
type AnimeSource int

const (
	SourceList   AnimeSource = iota // user's full cross-season list
	SourceSeason                    // one season's lineup (or "Later" = upcoming)
)

func (s AnimeSource) String() string {
	if s == SourceList {
		return "My List"
	}
	return "Season"
}

// AnimeLoad returns the items for a (source, query, season) triple.
//   - query != ""                      → MAL search results
//   - SourceList                       → the user's full cross-season list
//   - SourceSeason + season == "Later" → upcoming ranking
//   - SourceSeason + season == label   → that season's lineup
//
// The picker caches results so re-visits are instant.
type AnimeLoad func(source AnimeSource, query, season string) []mal.Item

// AnimeFilter holds the client-side status/sort + fuzzy state. (Season is a
// top-level picker field: in Season it's the load key; in My List/search it's
// forced to "All". So it isn't a client filter.)
type AnimeFilter struct {
	Status    string // status overlay label
	Sort      string // popularity | score | title | updated | airdate
	FuzzyText string
	Filtering bool
}

// sortOption is one row of the sort overlay.
type sortOption struct{ label, value string }

var sortOptions = []sortOption{
	{"Popularity", "popularity"},
	{"Score", "score"},
	{"Title", "title"},
	{"Updated", "updated"},
	{"Air Date", "airdate"},
}

func sortLabel(value string) string {
	for _, o := range sortOptions {
		if o.value == value {
			return o.label
		}
	}
	return value
}

func sortValue(label string) (string, bool) {
	for _, o := range sortOptions {
		if o.label == label {
			return o.value, true
		}
	}
	return "", false
}

// statusOptionsFull is the status overlay for sources that include un-added
// anime (Season, Later, Search) — where "Not in My List" can match.
var statusOptionsFull = []string{"All", "Not in My List", "My List", "Watching", "Completed", "On-Hold", "Plan to Watch", "Dropped"}

// statusOptionsMyList is the status overlay for the My List source (your own
// list has no un-added anime, so "Not in My List"/"My List" are dropped).
var statusOptionsMyList = []string{"All", "Watching", "Completed", "On-Hold", "Plan to Watch", "Dropped"}

// statusValue maps a list-status overlay label to the MAL ListStatus string it
// matches (empty for the pseudo-statuses All / Not in My List / My List).
func statusValue(label string) string {
	switch label {
	case "Watching":
		return "watching"
	case "Completed":
		return "completed"
	case "On-Hold":
		return "on_hold"
	case "Plan to Watch":
		return "plan_to_watch"
	case "Dropped":
		return "dropped"
	}
	return ""
}

// statusKeeps reports whether an item with the given ListStatus is kept by the
// selected status label.
func statusKeeps(label, listStatus string) bool {
	switch label {
	case "", "All":
		return true
	case "Not in My List":
		return listStatus == ""
	case "My List":
		return listStatus != ""
	default:
		return listStatus == statusValue(label)
	}
}

// seasonRank orders seasons within a year (winter < spring < summer < fall).
func seasonRank(s mal.Season) int {
	switch s {
	case mal.SeasonWinter:
		return 0
	case mal.SeasonSpring:
		return 1
	case mal.SeasonSummer:
		return 2
	case mal.SeasonFall:
		return 3
	}
	return 0
}

// seasonNewer reports whether a is a later season than b (by label).
func seasonNewer(a, b string) bool {
	ay, as, ok := mal.ParseSeasonLabel(a)
	if !ok {
		return false
	}
	by, bs, ok := mal.ParseSeasonLabel(b)
	if !ok {
		return true
	}
	if ay != by {
		return ay > by
	}
	return seasonRank(as) > seasonRank(bs)
}

// recentSeasonLabels is the offline fallback for the season overlay: the current
// season plus `n-1` past seasons, newest-first (no fabricated future).
func recentSeasonLabels(year int, season mal.Season, n int) []string {
	order := []mal.Season{mal.SeasonWinter, mal.SeasonSpring, mal.SeasonSummer, mal.SeasonFall}
	idx := 0
	for i, s := range order {
		if s == season {
			idx = i
			break
		}
	}
	out := make([]string, 0, n)
	cur, y := idx, year
	for i := 0; i < n; i++ {
		out = append(out, mal.ParseSeason(y, order[cur]))
		cur--
		if cur < 0 {
			cur = 3
			y--
		}
	}
	return out
}

// sortAnimes returns a sorted copy of items by the given sort name.
func sortAnimes(items []mal.Item, sortName string) []mal.Item {
	out := make([]mal.Item, len(items))
	copy(out, items)
	switch sortName {
	case "score":
		sort.SliceStable(out, func(i, j int) bool { return out[i].MeanScore > out[j].MeanScore })
	case "title":
		sort.SliceStable(out, func(i, j int) bool {
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		})
	case "updated":
		// Zero times (not on your list) sink to the bottom.
		sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	case "airdate":
		// StartDate is YYYY-MM-DD, so lexical = chronological; empty sinks.
		sort.SliceStable(out, func(i, j int) bool { return out[i].StartDate > out[j].StartDate })
	default: // popularity
		sort.SliceStable(out, func(i, j int) bool { return out[i].Members > out[j].Members })
	}
	return out
}

// ---- anime overlay (status / season / sort) ----

type animeOverlayKind int

const (
	animeOverlayNone animeOverlayKind = iota
	animeOverlayStatus
	animeOverlaySeason
	animeOverlaySort
)

type animeOverlay struct {
	kind   animeOverlayKind
	items  []string
	cursor int
}

func (o *animeOverlay) active() bool { return o.kind != animeOverlayNone }

func (o *animeOverlay) open(kind animeOverlayKind, items []string, current string) {
	o.kind = kind
	o.items = items
	o.cursor = 0
	for i, it := range items {
		if it == current {
			o.cursor = i
			break
		}
	}
}

func (o *animeOverlay) close() { o.kind = animeOverlayNone; o.items = nil; o.cursor = 0 }

func (o *animeOverlay) move(delta int) {
	n := len(o.items)
	if n == 0 {
		return
	}
	o.cursor = (o.cursor + delta) % n
	if o.cursor < 0 {
		o.cursor += n
	}
}

func (o *animeOverlay) selected() string {
	if o.cursor >= 0 && o.cursor < len(o.items) {
		return o.items[o.cursor]
	}
	return ""
}

// String renders an animeOverlayKind as its overlay title.
func (k animeOverlayKind) String() string {
	switch k {
	case animeOverlayStatus:
		return "Status"
	case animeOverlaySeason:
		return "Season"
	case animeOverlaySort:
		return "Sort"
	}
	return ""
}

// ---- model ----

// animePicker is the MAL anime selection screen. Two browse sources (Tab):
// My List (the user's whole list, season filter disabled) and Season (browse one
// season or "Later"/upcoming, where "Not in My List" works). Search is a
// separate query-driven mode.
type animePicker struct {
	items   []mal.Item // loaded (unfiltered) for the current (source, query, season)
	view    []mal.Item // filtered + sorted display slice
	source  AnimeSource
	query   string // "" = browse, non-empty = search
	season  string // "All" (My List/search) | "Later" | "Summer 2026"
	load    AnimeLoad
	cache   *animeCache
	loading bool

	// current real-world season (default + window anchor)
	currentYear   int
	currentSeason mal.Season
	currentLabel  string

	seasonArchive []string // lazily-fetched Jikan archive (nil until first use)

	filter  AnimeFilter
	overlay animeOverlay

	cursor  int
	topItem int
	debug   bool

	cover     *CoverCache
	coverText string

	width, height int

	// Layout, recomputed on WindowSizeMsg. All in terminal cells.
	listWidth  int
	paneHeight int
	previewCol int
	coverCol   int
	coverRow   int
	coverCols  int
	coverRows  int

	result *Result
}

// animeCache memoizes loaded items per (source, query, season).
type animeCache struct {
	mu sync.Mutex
	m  map[string][]mal.Item
}

func (c *animeCache) get(key string) ([]mal.Item, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *animeCache) put(key string, items []mal.Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = items
}

func animeCacheKey(source AnimeSource, query, season string) string {
	return fmt.Sprintf("%d|%s|%s", source, query, season)
}

func newAnimePicker(source AnimeSource, query string, load AnimeLoad, debug bool) *animePicker {
	y, s, label := mal.CurrentSeason()
	ap := &animePicker{
		source:        source,
		query:         query,
		load:          load,
		cache:         &animeCache{m: map[string][]mal.Item{}},
		loading:       true,
		currentYear:   y,
		currentSeason: s,
		currentLabel:  label,
		filter:        AnimeFilter{Sort: "popularity", Status: "All"},
		debug:         debug,
		result:        &Result{},
	}
	ap.season = ap.defaultSeason()
	return ap
}

// defaultSeason is the season a source opens on: the current season for Season
// (browse), "All" otherwise (My List / search).
func (m *animePicker) defaultSeason() string {
	if m.source == SourceSeason && m.query == "" {
		return m.currentLabel
	}
	return mal.SeasonAll
}

func (m *animePicker) Init() tea.Cmd { return m.loadCmd(m.source, m.query, m.season) }

// itemsLoadedMsg carries one (source, query, season) load's items; Update
// discards stale results.
type itemsLoadedMsg struct {
	items  []mal.Item
	source AnimeSource
	query  string
	season string
}

func (m *animePicker) loadCmd(source AnimeSource, query, season string) tea.Cmd {
	load := m.load
	cache := m.cache
	return func() tea.Msg {
		key := animeCacheKey(source, query, season)
		if items, ok := cache.get(key); ok {
			return itemsLoadedMsg{items: items, source: source, query: query, season: season}
		}
		items := load(source, query, season)
		cache.put(key, items)
		return itemsLoadedMsg{items: items, source: source, query: query, season: season}
	}
}

func (m *animePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.recomputeLayout()
		m.fixScroll()
		return m, m.loadCoverCmd()

	case itemsLoadedMsg:
		return m.applyLoaded(msg)

	case coverReadyMsg:
		return m, m.loadCoverCmd()

	case coverTextMsg:
		m.coverText = msg.text
		return m, nil

	case tea.KeyMsg:
		if m.overlay.active() {
			return m.handleOverlayKey(msg)
		}
		if m.filter.Filtering {
			return m.handleFilterKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

// applyLoaded ingests a (source, query, season) load's items when it matches
// what we currently want; stale loads are discarded.
func (m *animePicker) applyLoaded(msg itemsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.source != m.source || msg.query != m.query || msg.season != m.season {
		return m, nil
	}
	m.items = msg.items
	m.loading = false
	m.cursor = 0
	m.topItem = 0
	m.applyFilter()
	return m, m.rebuildCoverCmd()
}

// rebuildCoverCmd pre-downloads covers for the current item set.
func (m *animePicker) rebuildCoverCmd() tea.Cmd {
	urls := make([]string, 0, len(m.items))
	for _, it := range m.items {
		if it.CoverURL != "" {
			urls = append(urls, it.CoverURL)
		}
	}
	cmd, cache := NewCoverCache(urls)
	m.cover = cache
	m.coverText = ""
	return cmd
}

func (m *animePicker) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.result.Quit = true
		return m, tea.Batch(tea.Quit, m.quitCmd())
	case "tab":
		// Toggle My List ↔ Season (browse only; search is query-driven).
		if m.query != "" {
			return m, nil
		}
		if m.source == SourceList {
			m.source = SourceSeason
		} else {
			m.source = SourceList
		}
		m.season = m.defaultSeason()
		m.filter.Status = "All"
		m.loading = true
		m.cursor = 0
		m.topItem = 0
		return m, m.loadCmd(m.source, m.query, m.season)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.fixScroll()
			return m, m.loadCoverCmd()
		}
	case "down", "j":
		if m.cursor < len(m.view)-1 {
			m.cursor++
			m.fixScroll()
			return m, m.loadCoverCmd()
		}
	case "t":
		m.overlay.open(animeOverlayStatus, m.statusOptions(), m.filter.Status)
		return m, nil
	case "e":
		// Season filter only applies to the Season source.
		if m.query == "" && m.source == SourceSeason {
			m.openSeasonOverlay()
		}
		return m, nil
	case "s":
		labels := make([]string, len(sortOptions))
		for i, o := range sortOptions {
			labels[i] = o.label
		}
		m.overlay.open(animeOverlaySort, labels, sortLabel(m.filter.Sort))
		return m, nil
	case "/":
		m.filter.Filtering = true
		m.filter.FuzzyText = ""
		return m, nil
	case "enter":
		if it := m.currentItemCopy(); it != nil {
			m.result.Anime = it
			return m, tea.Batch(tea.Quit, m.quitCmd())
		}
	}
	return m, nil
}

// statusOptions returns the status overlay list for the current source.
func (m *animePicker) statusOptions() []string {
	if m.query == "" && m.source == SourceList {
		return statusOptionsMyList
	}
	return statusOptionsFull
}

// openSeasonOverlay builds the Season source's season list: "Later" + the Jikan
// archive windowed to ~12 years (real future included).
func (m *animePicker) openSeasonOverlay() {
	items := []string{mal.SeasonLater}
	items = append(items, m.seasonArchiveItems()...)
	m.overlay.open(animeOverlaySeason, items, m.season)
}

// seasonArchiveItems returns the browse season list (sans the leading "Later"):
// the Jikan archive windowed to the latest ~12 years (real future included),
// falling back to a local current+past list if the archive can't be fetched.
func (m *animePicker) seasonArchiveItems() []string {
	if m.seasonArchive == nil {
		m.seasonArchive = mal.SeasonArchive(m.debug)
	}
	arch := m.seasonArchive
	if len(arch) == 0 {
		return recentSeasonLabels(m.currentYear, m.currentSeason, 49)
	}
	minYear := m.currentYear - 12
	out := make([]string, 0, len(arch))
	for _, label := range arch {
		if y, _, ok := mal.ParseSeasonLabel(label); ok && y >= minYear {
			out = append(out, label)
		}
	}
	return out
}

func (m *animePicker) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.overlay.close()
		return m, nil
	case "enter":
		return m.applyOverlaySelection()
	case "up", "k":
		m.overlay.move(-1)
	case "down", "j":
		m.overlay.move(1)
	case "home":
		m.overlay.cursor = 0
	case "end":
		if n := len(m.overlay.items); n > 0 {
			m.overlay.cursor = n - 1
		}
	}
	return m, nil
}

// applyOverlaySelection applies the overlay's selection and closes it.
func (m *animePicker) applyOverlaySelection() (tea.Model, tea.Cmd) {
	sel := m.overlay.selected()
	kind := m.overlay.kind
	m.overlay.close()
	switch kind {
	case animeOverlayStatus:
		if sel != "" {
			m.filter.Status = sel
		}
		m.applyFilter()
		return m, m.loadCoverCmd()
	case animeOverlaySort:
		if v, ok := sortValue(sel); ok {
			m.filter.Sort = v
		}
		m.applyFilter()
		return m, m.loadCoverCmd()
	case animeOverlaySeason:
		if sel == "" {
			return m, nil
		}
		m.season = sel
		m.loading = true
		m.cursor = 0
		m.topItem = 0
		return m, m.loadCmd(m.source, m.query, m.season)
	}
	return m, nil
}

func (m *animePicker) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.filter.Filtering = false
		m.fixScroll()
		return m, nil
	case "enter":
		m.filter.Filtering = false
		m.applyFilter()
		m.fixScroll()
		if len(m.view) > 0 {
			m.cursor = 0
			if it := m.currentItemCopy(); it != nil {
				m.result.Anime = it
				return m, tea.Batch(tea.Quit, m.quitCmd())
			}
		}
		return m, m.loadCoverCmd()
	case "up":
		if m.cursor > 0 {
			m.cursor--
			m.fixScroll()
			return m, m.loadCoverCmd()
		}
		return m, nil
	case "down":
		if m.cursor < len(m.view)-1 {
			m.cursor++
			m.fixScroll()
			return m, m.loadCoverCmd()
		}
		return m, nil
	case "backspace":
		if len(m.filter.FuzzyText) > 0 {
			r := []rune(m.filter.FuzzyText)
			m.filter.FuzzyText = string(r[:len(r)-1])
			m.applyFilter()
			m.fixScroll()
			return m, m.loadCoverCmd()
		}
		m.filter.Filtering = false
		m.applyFilter()
		return m, m.loadCoverCmd()
	case " ", "tab":
		m.filter.FuzzyText += " "
		m.applyFilter()
		m.fixScroll()
		return m, m.loadCoverCmd()
	default:
		if isPrintable(msg) {
			m.filter.FuzzyText += msg.String()
			m.applyFilter()
			m.fixScroll()
			return m, m.loadCoverCmd()
		}
	}
	return m, nil
}

// applyFilter recomputes m.view: status (always; options depend on source) →
// fuzzy → sort, then clamps the cursor. Season is never a client filter (it's
// either the Season load key or forced "All" in My List/search).
func (m *animePicker) applyFilter() {
	rs := m.items
	if m.filter.Status != "" && m.filter.Status != "All" {
		filtered := make([]mal.Item, 0, len(rs))
		for _, it := range rs {
			if statusKeeps(m.filter.Status, it.ListStatus) {
				filtered = append(filtered, it)
			}
		}
		rs = filtered
	}
	if m.filter.FuzzyText != "" {
		needle := strings.ToLower(m.filter.FuzzyText)
		filtered := make([]mal.Item, 0, len(rs))
		for _, it := range rs {
			if strings.Contains(strings.ToLower(it.Title), needle) {
				filtered = append(filtered, it)
			}
		}
		rs = filtered
	}
	m.view = sortAnimes(rs, m.filter.Sort)
	if m.cursor >= len(m.view) {
		m.cursor = max(0, len(m.view)-1)
	}
	m.fixScroll()
}

func (m *animePicker) currentItemCopy() *mal.Item {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return nil
	}
	it := m.view[m.cursor]
	return &it
}

// quitCmd cleans the cover temp dir. The cover image auto-clears when the alt
// screen is torn down.
func (m *animePicker) quitCmd() tea.Cmd {
	cache := m.cover
	return func() tea.Msg {
		if cache != nil {
			cache.Cleanup()
		}
		return nil
	}
}

// recomputeLayout sets the pane sizes and the cover/preview anchor cells.
// Layout (top→bottom): header (1), badges (1), panes (height-3), help (1).
func (m *animePicker) recomputeLayout() {
	w := m.width
	h := m.height

	listW := w * 60 / 100
	if listW < 35 {
		listW = 35
	}
	if listW > 60 {
		listW = 60
	}
	if w-listW < 25 {
		listW = max(10, w-25)
	}
	m.listWidth = listW
	m.paneHeight = h - 3 // header (1) + badges (1) + help (1)
	if m.paneHeight < 3 {
		m.paneHeight = 3
	}

	m.previewCol = listW
	m.coverCol = m.previewCol + 1
	m.coverRow = 2 /* header + badges */ + 1 /* right pane top border */

	previewContentW := w - m.previewCol - 2
	m.coverCols = clamp(previewContentW, 8, 40)

	contentH := m.paneHeight - 2
	if contentH < 1 {
		contentH = 1
	}
	coverRows := contentH
	if coverRows > 14 {
		coverRows = 14
	}
	if coverRows < 1 {
		coverRows = 1
	}
	m.coverRows = coverRows
}

func (m *animePicker) pageSize() int {
	if m.height == 0 {
		return 20
	}
	ps := m.paneHeight - 2 /* borders */ - 1 /* title */
	if ps < 1 {
		ps = 1
	}
	return ps
}

// scrollOff is the minimum number of context lines kept visible above and below
// the cursor while scrolling a list pane (vim-style scrolloff).
const scrollOff = 7

// clampTop returns the topItem that keeps cursor visible in a page of pageSize
// items (out of total), with at least scrollOff lines of context above and
// below. scrollOff is capped to (pageSize-1)/2 for tiny windows, and topItem is
// clamped to [0, total-pageSize] so the list never scrolls past its ends.
func clampTop(cursor, topItem, pageSize, total int) int {
	ps := pageSize
	so := scrollOff
	if 2*so+1 > ps {
		so = (ps - 1) / 2
	}
	if so < 0 {
		so = 0
	}
	if cursor < topItem+so {
		topItem = cursor - so
	}
	if cursor > topItem+ps-1-so {
		topItem = cursor - (ps - 1) + so
	}
	if topItem < 0 {
		topItem = 0
	}
	if maxTop := max(0, total-ps); topItem > maxTop {
		topItem = maxTop
	}
	return topItem
}

func (m *animePicker) fixScroll() {
	m.topItem = clampTop(m.cursor, m.topItem, m.pageSize(), len(m.view))
}

// coverTextMsg carries the unicode-placeholder text for a cover.
type coverTextMsg struct{ text string }

func (m *animePicker) loadCoverCmd() tea.Cmd {
	cur := m.currentItemCopy()
	if cur == nil || cur.CoverURL == "" || m.cover == nil {
		return func() tea.Msg { return coverTextMsg{text: ""} }
	}
	path := m.cover.Get(cur.CoverURL)
	cols, rows := m.coverCols, m.coverRows
	if path == "" {
		return func() tea.Msg { return coverTextMsg{text: ""} }
	}
	return func() tea.Msg {
		upload, text, err := RenderCoverPlaceholder(path, cols, rows)
		if err != nil {
			return coverTextMsg{text: ""}
		}
		WriteUpload(upload)
		return coverTextMsg{text: text}
	}
}

// sourceLabel returns the badge/header label for the active source.
func (m *animePicker) sourceLabel() string {
	if m.query != "" {
		return "Search"
	}
	return m.source.String()
}

func (m *animePicker) headerText() string {
	if m.query != "" {
		return HeaderStyle.Render(fmt.Sprintf("Search: %q — %d results", m.query, len(m.view)))
	}
	switch m.source {
	case SourceList:
		return HeaderStyle.Render(fmt.Sprintf("My List — %d anime", len(m.view)))
	default:
		if m.season == mal.SeasonLater {
			return HeaderStyle.Render(fmt.Sprintf("Later — %d anime", len(m.view)))
		}
		return HeaderStyle.Render(fmt.Sprintf("%s — %d anime", m.season, len(m.view)))
	}
}

func (m *animePicker) View() string {
	if m.width == 0 {
		return "Loading anime…"
	}
	if m.loading {
		label := m.season
		if m.query != "" {
			label = fmt.Sprintf("search %q", m.query)
		} else if m.source == SourceList {
			label = "my list"
		}
		return FaintStyle.Render(fmt.Sprintf("Loading %s…", label))
	}

	// ---- LEFT pane (list / overlay) ----
	var leftContent string
	if m.overlay.active() {
		leftContent = renderListOverlayContent(m.overlay.kind.String(), m.overlay.items, m.overlay.cursor, m.pageSize())
	} else {
		title := TitleStyle.Render("Anime") + FaintStyle.Render(fmt.Sprintf("  (%d)", len(m.view)))
		if m.filter.Filtering {
			title += "  " + FaintStyle.Render("filter: ") + m.filter.FuzzyText + "▏"
		}
		leftContent = title + "\n" + m.renderList()
	}
	leftPane := ListBorderStyle.
		Width(m.listWidth).
		Height(m.paneHeight - 2).
		Render(leftContent)

	// ---- RIGHT pane (cover + metadata) ----
	previewWidth := m.width - m.previewCol
	if previewWidth < 25 {
		previewWidth = 25
	}
	rightContent := m.renderMetadata()
	rightPane := PreviewBorderStyle.
		Width(previewWidth).
		Height(m.paneHeight - 2).
		Render(rightContent)

	header := m.headerText()
	badges := m.renderBadges()
	help := HelpStyle.Render("j/k nav  Tab source  t status  e season  s sort  / filter  Enter select  q quit")
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	return lipgloss.JoinVertical(lipgloss.Left, header, badges, panes, help)
}

// renderBadges renders the source/status/season/sort badge line. The season
// badge only shows for the Season source (it's forced "All" and hidden elsewhere).
func (m *animePicker) renderBadges() string {
	parts := []string{
		conditionalBadge("source:"+m.sourceLabel(), true),
		conditionalBadge("status:"+m.filter.Status, m.filter.Status != "All"),
	}
	if m.query == "" && m.source == SourceSeason {
		parts = append(parts, conditionalBadge("season:"+m.season, true))
	}
	parts = append(parts, conditionalBadge("sort:"+sortLabel(m.filter.Sort), false))
	return strings.Join(parts, " ")
}

// renderList draws exactly pageSize lines (the visible slice, padded with blanks)
// so the left pane never grows past its box and the header stays pinned.
func (m *animePicker) renderList() string {
	ps := m.pageSize()
	end := m.topItem + ps
	if end > len(m.view) {
		end = len(m.view)
	}
	avail := m.listWidth - 2 - len(CursorGlyph)
	if avail < 4 {
		avail = 4
	}
	lines := make([]string, 0, ps)
	for i := m.topItem; i < end; i++ {
		text := clip(ui.RenderMALLine(m.view[i]), avail)
		if i == m.cursor {
			lines = append(lines, SelectedStyle.Render(CursorGlyph+text))
		} else {
			lines = append(lines, "  "+text)
		}
	}
	for len(lines) < ps {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// renderMetadata builds the right pane content: cover placeholder region on top,
// colored metadata below.
func (m *animePicker) renderMetadata() string {
	cur := m.currentItemCopy()

	lines := make([]string, 0, m.coverRows+8)
	if m.coverText != "" {
		lines = append(lines, strings.Split(m.coverText+"\x1b[0m", "\n")...)
	} else {
		blank := strings.Repeat(" ", m.coverCols)
		for i := 0; i < m.coverRows; i++ {
			lines = append(lines, CoverBlankStyle.Render(blank))
		}
	}

	if cur == nil {
		return fitPaneHeight(strings.Join(padToHeight(lines, m.paneHeight-2), "\n"), m.paneHeight-2)
	}

	width := m.width - m.previewCol - 2
	if width < 12 {
		width = 12
	}

	lines = append(lines, TitleStyle.Render(wrap(cur.Title, width)))

	progress := ""
	switch {
	case cur.TotalEps > 0:
		progress = fmt.Sprintf("ep %d/%d", cur.WatchedEps, cur.TotalEps)
	default:
		progress = fmt.Sprintf("ep %d/?", cur.WatchedEps)
	}
	if a := ui.MALAirShort(cur.AirStatus); a != "" {
		progress += "  [" + a + "]"
	}
	if cur.ListStatus != "" {
		progress += "  —  " + ui.MALListStatusShort(cur.ListStatus)
	} else if cur.WatchedEps > 0 {
		progress += "  ·  Watching"
	}
	lines = append(lines, ProgressStyle.Render(wrap(progress, width)))

	if cur.MeanScore > 0 {
		s := fmt.Sprintf("★ %.2f", cur.MeanScore)
		if cur.Score > 0 {
			s += fmt.Sprintf("   (your: %d)", cur.Score)
		}
		lines = append(lines, ScoreStyle.Render(wrap(s, width)))
	} else if cur.Score > 0 {
		lines = append(lines, ScoreStyle.Render(wrap(fmt.Sprintf("your score: %d", cur.Score), width)))
	}

	if cur.Genres != "" {
		lines = append(lines, GenresStyle.Render(wrap("Genres: "+cur.Genres, width)))
	}
	if cur.Studios != "" {
		lines = append(lines, StudiosStyle.Render(wrap("Studios: "+cur.Studios, width)))
	}

	seasonType := ""
	if cur.StartSeason != "" {
		seasonType = "Season: " + cur.StartSeason
		if cur.MediaType != "" {
			seasonType += "  (" + strings.ToUpper(cur.MediaType) + ")"
		}
	} else if cur.MediaType != "" {
		seasonType = "Type: " + strings.ToUpper(cur.MediaType)
	}
	if seasonType != "" {
		lines = append(lines, FaintStyle.Render(wrap(seasonType, width)))
	}

	if cur.Rank > 0 || cur.Members > 0 {
		var parts []string
		if cur.Rank > 0 {
			parts = append(parts, fmt.Sprintf("Rank #%d", cur.Rank))
		}
		if cur.Members > 0 {
			parts = append(parts, fmt.Sprintf("%s members", ui.HumanCount(cur.Members)))
		}
		lines = append(lines, FaintStyle.Render(wrap(strings.Join(parts, "  "), width)))
	}

	return fitPaneHeight(strings.Join(lines, "\n"), m.paneHeight-2)
}

// fitPaneHeight ensures the joined content occupies exactly maxLines rows.
func fitPaneHeight(s string, maxLines int) string {
	if maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for len(lines) < maxLines {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func padToHeight(lines []string, target int) []string {
	for len(lines) < target {
		lines = append(lines, "")
	}
	return lines
}

// ---- small text helpers shared by the pickers ----

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ui.WrapLine(s, width)
}

func isPrintable(msg tea.KeyMsg) bool {
	s := msg.String()
	if len(s) != 1 {
		return false
	}
	r := rune(s[0])
	return r >= 0x20 && r != 0x7f
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
