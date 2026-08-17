package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
// The picker caches results so re-visits are instant. A non-nil error means the
// load failed; the picker renders it in the empty list (and, for auth errors,
// hints to log in) instead of an opaque blank.
type AnimeLoad func(source AnimeSource, query, season string) ([]mal.Item, error)

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
	{"Relevance", "relevance"},
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

// StatusAction is a chosen per-anime action from the actions menu: set a status,
// or remove the anime from the user's list.
type StatusAction struct {
	Remove bool
	Status string // MAL status value (watching/completed/on_hold/plan_to_watch/dropped); valid when !Remove
}

// actionOption is one row of the per-anime actions menu (Space).
type actionOption struct {
	label   string
	action  StatusAction
	display string // human status name for the confirm text (empty for Remove)
	score   bool   // opens the score picker instead of a status/remove action
}

// actionOptions is the actions menu, top to bottom.
var actionOptions = []actionOption{
	{"Set Watching", StatusAction{Status: "watching"}, "Watching", false},
	{"Set Completed", StatusAction{Status: "completed"}, "Completed", false},
	{"Set On-Hold", StatusAction{Status: "on_hold"}, "On-Hold", false},
	{"Set Plan to Watch", StatusAction{Status: "plan_to_watch"}, "Plan to Watch", false},
	{"Set Dropped", StatusAction{Status: "dropped"}, "Dropped", false},
	{"Set Score", StatusAction{}, "", true},
	{"Remove from My List", StatusAction{Remove: true}, "", false},
}

// actionGroupLabels returns the top-level actions menu (Space): "Set Status" and
// "Open Web" always; "Set Score", "Set Episode", and "Remove from My List" only
// when the anime is on the list (they need an entry).
func actionGroupLabels(listStatus string) []string {
	out := []string{"Set Status"}
	if listStatus != "" {
		out = append(out, "Set Score", "Set Episode")
	}
	out = append(out, "Open Web")
	if listStatus != "" {
		out = append(out, "Remove from My List")
	}
	return out
}

// statusActionLabels returns the "Set Status" sub-menu: the five set-status
// options minus the item's current status (a no-op).
func statusActionLabels(listStatus string) []string {
	out := make([]string, 0, 5)
	for _, o := range actionOptions {
		if o.score || o.action.Remove {
			continue
		}
		if o.action.Status == listStatus {
			continue
		}
		out = append(out, o.label)
	}
	return out
}

// actionByLabel resolves an overlay label to its action + display name.
func actionByLabel(label string) (StatusAction, string, bool) {
	for _, o := range actionOptions {
		if o.label == label {
			return o.action, o.display, true
		}
	}
	return StatusAction{}, "", false
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
	case "relevance":
		// Preserve the source's natural order (MAL's search ranking) — no re-sort.
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
	animeOverlayActions    // top-level actions menu (Set Status / Set Score / Remove)
	animeOverlayStatusMenu // "Set Status" sub-menu (Watching/Completed/...)
	animeOverlayConfirm    // centered y/n modal for an actions-menu choice
	animeOverlayScore      // 1-10 / Remove Score picker
	animeOverlayEpisode    // set watched episodes (number input)
	animeOverlayAuth       // MAL account/status modal (read-only + L/o actions)
)

type animeOverlay struct {
	kind          animeOverlayKind
	items         []string
	cursor        int
	text          string       // confirm-modal question
	pendingAction StatusAction // action to apply on confirm
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
	case animeOverlayActions:
		return "Actions"
	case animeOverlayStatusMenu:
		return "Set Status"
	case animeOverlayScore:
		return "Score"
	case animeOverlayEpisode:
		return "Episode"
	case animeOverlayAuth:
		return "Account"
	}
	return ""
}

// ---- model ----

// animePicker is the MAL anime selection screen. Two browse sources (Tab):
// My List (the user's whole list, season filter disabled) and Season (browse one
// season or "Later"/upcoming, where "Not in My List" works). Search is a
// separate query-driven mode.
type animePicker struct {
	items    []mal.Item // loaded (unfiltered) for the current (source, query, season)
	view     []mal.Item // filtered + sorted display slice
	source   AnimeSource
	query    string // "" = browse, non-empty = search
	season   string // "All" (My List/search) | "Later" | "Summer 2026"
	provider string // backend: "torrent" (default) | "anidb" — drives the palette's provider switch
	load     AnimeLoad
	cache    *animeCache
	loading  bool
	loadErr  error // non-nil when the last load failed; rendered in the empty list

	// current real-world season (default + window anchor)
	currentYear   int
	currentSeason mal.Season
	currentLabel  string

	seasonArchive []string // lazily-fetched Jikan archive (nil until first use)

	filter  AnimeFilter
	overlay animeOverlay
	palette Palette // `:` command menu (every action, fuzzy-searchable)

	// Status-overlay state: offline session snapshot + best-effort username.
	authStatus      mal.AuthStatusResult
	authName        string // MAL username (best-effort; "" while pending/unknown)
	authNamePending bool
	authNameFailed  bool

	// applyStatus applies a per-anime status action (set/remove) to MAL. nil in
	// the AnimeTosho fallback (no MAL), where Space is a no-op.
	applyStatus func(malID, watchedEps int, act StatusAction) bool

	// applyScore sets the per-anime score (0 = unrate) on MAL; nil disables.
	applyScore func(malID, score int) bool

	// applyWatched sets the per-anime watched-episode count on MAL; nil disables.
	applyWatched func(malID, watched int) bool

	// latestEpisode returns the latest aired episode for the focused item (nil
	// disables the "watched/aired/total" display). aired is a session-scoped
	// *AiredCache: each anime's count is computed at most once per session (incl.
	// 0/failed — no retry), shared across pickers via app.Run.
	latestEpisode func(item *mal.Item) float64
	aired         *AiredCache

	// latestEpisodePrefetch is the background prefetch variant (fast-only: no
	// Jikan, skips aid-unresolved items). nil ⇒ covers are still paged but no
	// aired prefetch.
	latestEpisodePrefetch func(item *mal.Item) float64

	// prefetchSem bounds concurrent aired-episode fetches. Animetosho times out
	// (~30s) when too many hit it at once — a full season has ~90 airing anime and
	// uncapped dispatch caused ~1/3 to time out. Per-instance so a torn-down
	// picker's orphaned goroutines can't stall a new one.
	prefetchSem chan struct{}

	cursor  int
	topItem int
	// pendingCursor is a session-restored cursor waiting for the first
	// applyLoaded (the list must exist before it can be applied); -1 = none.
	pendingCursor int
	debug         bool

	cover        *CoverCache
	coverText    string
	coverHeights map[int]int // malID → actual rendered cover height (lines), so the placeholder matches on re-focus

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

// AnimeState is the anime picker's session-scoped UI state (options, cursor,
// and the per-(source,query,season) list cache), shared across re-entries so
// backing out of the release picker returns where the user left off.
// Deliberately NOT persisted: a new process starts on defaults.
type AnimeState struct {
	Source AnimeSource
	Season string
	Status string // filter.Status label
	Sort   string // filter.Sort value
	Cursor int
	// restored marks a state that has been populated by saveState. Until the
	// first save, restoreState must not apply anything (the zero values mean
	// "never used", not "use these").
	restored bool
	cache    *animeCache
}

// NewAnimeState returns an empty session state (fresh cache). Source starts at
// the browse default (Season) — AnimeSource's zero value is SourceList, which
// would otherwise hijack restoreState on a fresh process's first open.
func NewAnimeState() *AnimeState {
	return &AnimeState{Source: SourceSeason, cache: &animeCache{m: map[string][]mal.Item{}}}
}

// restoreState seeds a picker from a previous session's state, overriding the
// per-source defaults chosen by newAnimePicker. Unknown/inapplicable values
// (nil state, bogus sort, a status the restored source doesn't offer) keep
// their defaults; the cursor is applied to the first load only (applyLoaded).
func (m *animePicker) restoreState(st *AnimeState) {
	if st == nil || !st.restored {
		return
	}
	if st.cache != nil {
		m.cache = st.cache // re-entry is instant: same items, same order
	}
	if m.query == "" && (st.Source == SourceList || st.Source == SourceSeason) && st.Source != m.source {
		// Mirrors the Tab handler: a source change recomputes its derived
		// defaults (season, status) before the saved values are validated on top.
		m.source = st.Source
		m.season = m.defaultSeason()
		m.filter.Status = m.defaultStatus()
	}
	if m.source == SourceSeason && m.query == "" && st.Season != "" {
		m.season = st.Season
	}
	for _, s := range m.statusOptions() {
		if st.Status == s {
			m.filter.Status = st.Status
			break
		}
	}
	if sortKnown(st.Sort) {
		m.filter.Sort = st.Sort
	}
	m.pendingCursor = st.Cursor
}

// saveState snapshots the picker's options, cursor, and list cache for the
// next restoreState (same session).
func (m *animePicker) saveState(st *AnimeState) {
	if st == nil {
		return
	}
	st.Source = m.source
	st.Season = m.season
	st.Status = m.filter.Status
	st.Sort = m.filter.Sort
	st.Cursor = m.cursor
	st.cache = m.cache
	st.restored = true
}

// sortKnown reports whether value is one of sortOptions' values.
func sortKnown(value string) bool {
	for _, o := range sortOptions {
		if o.value == value {
			return true
		}
	}
	return false
}

func animeCacheKey(source AnimeSource, query, season string) string {
	return fmt.Sprintf("%d|%s|%s", source, query, season)
}

func newAnimePicker(source AnimeSource, query string, load AnimeLoad, applyStatus func(int, int, StatusAction) bool, applyScore func(int, int) bool, applyWatched func(int, int) bool, latestEpisode func(*mal.Item) float64, latestEpisodePrefetch func(*mal.Item) float64, debug bool) *animePicker {
	y, s, label := mal.CurrentSeason()
	ap := &animePicker{
		source:                source,
		query:                 query,
		load:                  load,
		applyStatus:           applyStatus,
		applyScore:            applyScore,
		applyWatched:          applyWatched,
		latestEpisode:         latestEpisode,
		latestEpisodePrefetch: latestEpisodePrefetch,
		aired:                 NewAiredCache(),
		prefetchSem:           make(chan struct{}, prefetchCap),
		coverHeights:          map[int]int{},
		cache:                 &animeCache{m: map[string][]mal.Item{}},
		loading:               true,
		pendingCursor:         -1,
		currentYear:           y,
		currentSeason:         s,
		currentLabel:          label,
		filter:                AnimeFilter{Sort: "popularity", Status: "All"},
		debug:                 debug,
		result:                &Result{},
	}
	ap.season = ap.defaultSeason()
	ap.filter.Status = ap.defaultStatus()
	ap.filter.Sort = ap.defaultSort()
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

// defaultSort is the sort a source opens on: "relevance" for search (keep MAL's
// search ranking), "updated" (last updated) otherwise.
func (m *animePicker) defaultSort() string {
	if m.query != "" {
		return "relevance"
	}
	return "updated"
}

// defaultStatus is the status filter a source opens on: "My List" for Season
// (browse) — your items this season first — and "All" otherwise.
func (m *animePicker) defaultStatus() string {
	if m.source == SourceSeason && m.query == "" {
		return "My List"
	}
	return "All"
}

func (m *animePicker) Init() tea.Cmd { return m.loadCmd(m.source, m.query, m.season) }

// itemsLoadedMsg carries one (source, query, season) load's items; Update
// discards stale results.
type itemsLoadedMsg struct {
	items  []mal.Item
	err    error
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
		items, err := load(source, query, season)
		if err == nil { // don't cache failures — let them retry on revisit
			cache.put(key, items)
		}
		return itemsLoadedMsg{items: items, err: err, source: source, query: query, season: season}
	}
}

// authNameMsg carries the best-effort MAL username for the status overlay. ok=false
// means the fetch failed (e.g. expired token → 401); the overlay then shows the
// offline id/expiry and "(name unavailable)".
type authNameMsg struct {
	name string
	ok   bool
}

// fetchAuthNameCmd calls the MAL API for the logged-in user's name (best-effort).
func (m *animePicker) fetchAuthNameCmd() tea.Cmd {
	debug := m.debug
	return func() tea.Msg {
		u, err := mal.WhoAmI(debug)
		if err != nil || u == nil {
			return authNameMsg{ok: false}
		}
		return authNameMsg{name: u.Name, ok: true}
	}
}

func (m *animePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.recomputeLayout()
		m.fixScroll()
		return m, m.focusCmd()

	case itemsLoadedMsg:
		return m.applyLoaded(msg)

	case statusAppliedMsg:
		return m.applyStatusApplied(msg)

	case scoreAppliedMsg:
		if msg.applied {
			for i := range m.items {
				if m.items[i].MalID == msg.malID {
					m.items[i].Score = msg.score
					break
				}
			}
		}
		return m, nil

	case episodeAppliedMsg:
		if msg.applied {
			for i := range m.items {
				if m.items[i].MalID == msg.malID {
					m.items[i].WatchedEps = msg.watched
					break
				}
			}
			m.applyFilter()
		}
		return m, nil

	case latestEpMsg:
		// Record the result for this anime. Per the once-per-session rule any count
		// (incl. 0/failed) is kept — it won't be re-fetched again this session.
		if m.latestEpisode != nil {
			m.aired.put(msg.malID, msg.aired)
		}
		return m, nil

	case coverReadyMsg:
		return m, m.focusCmd()

	case prefetchPageDoneMsg:
		// Page 1 settling schedules page 2 (the rest); page 2 is the tail.
		if msg.firstPage {
			return m, m.prefetchPageCmd(false)
		}
		return m, nil

	case coverTextMsg:
		m.coverText = msg.text
		// Cache the actual rendered cover height so the blank placeholder matches
		// exactly on re-focus (no text shift).
		if msg.text != "" && msg.key > 0 {
			m.coverHeights[msg.key] = len(strings.Split(msg.text, "\n"))
		}
		return m, nil

	case authNameMsg:
		// Best-effort username for the status overlay; only relevant while it's open.
		m.authNamePending = false
		if msg.ok {
			m.authName, m.authNameFailed = msg.name, false
		} else {
			m.authNameFailed = true
		}
		return m, nil

	case tea.KeyMsg:
		if m.palette.Active() {
			return m.handlePaletteKey(msg)
		}
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
	m.loadErr = msg.err
	m.applyFilter()
	// Session-restored cursor: applied to the first load only (later loads —
	// Tab/season switches — keep the reset-to-0 above).
	if m.pendingCursor >= 0 && len(m.view) > 0 {
		m.cursor = min(m.pendingCursor, len(m.view)-1)
		m.fixScroll()
	}
	m.pendingCursor = -1
	if msg.err != nil {
		// Nothing to prefetch; the empty list renders the error line instead.
		return m, nil
	}
	// Fresh cover cache; prefetchPageCmd drives its downloads page-by-page
	// (first visible page first, then the rest) alongside the aired episodes.
	m.cover = NewCoverCache()
	m.coverText = ""
	return m, m.prefetchPageCmd(true)
}

// prefetchPageDoneMsg is emitted when a prefetch page (covers + aired) settles,
// so the model schedules the next page.
type prefetchPageDoneMsg struct{ firstPage bool }

// prefetchCap bounds concurrent animetosho aired-episode fetches. Uncapped, a full
// season's ~90 airing anime overwhelm animetosho (~1/3 time out at the 30s
// deadline). 16 stays under where timeouts begin (verified: 0 errors at 16).
const prefetchCap = 16

// prefetchPageCmd prefetches one page of covers + aired episodes (see
// selectPrefetchPage for what each page covers). The cover download is batched
// behind a barrier that emits prefetchPageDoneMsg when it settles, so the model
// schedules page 2. Aired cmds run independently of the barrier (capped at
// prefetchCap concurrent; emit latestEpMsg as they finish) so slow aired fetches
// can't gate the next page's covers. An empty page 1 still chains to page 2 (the
// work may be entirely off-screen); an empty page 2 returns nil.
func (m *animePicker) prefetchPageCmd(firstPage bool) tea.Cmd {
	coverURLs, airedItems := m.selectPrefetchPage(firstPage)
	// Debug: confirm the on-list batch (page 1) is dispatched before off-list
	// (page 2). The log order in debug.log shows the sequence.
	page, label := 2, "off-list"
	if firstPage {
		page, label = 1, "on-list (my list)"
	}
	ids := make([]int, len(airedItems))
	for i, it := range airedItems {
		ids[i] = it.MalID
	}
	mal.LogDebug("DEBUG prefetch page %d (%s): %d aired items malIDs=%v\n", page, label, len(airedItems), ids)
	if len(coverURLs) == 0 && len(airedItems) == 0 {
		// Nothing on this page. Page 1 must still chain to page 2 — the work may
		// be entirely off-screen (e.g. the default status filter hides everything
		// on a fresh season), and page 2 is what covers the filtered-out items.
		if firstPage {
			return func() tea.Msg { return prefetchPageDoneMsg{firstPage: true} }
		}
		return nil
	}

	// The barrier tracks ONLY the cover download, so page-2 covers start as soon
	// as page-1 covers settle (fast CDN) — slow aired fetches can't gate them.
	var wg sync.WaitGroup
	wg.Add(1)
	cmds := make([]tea.Cmd, 0, 2+len(airedItems))

	// Covers: download the page's distinct URLs concurrently (CDN, uncapped);
	// wg.Done() when the batch settles.
	coverCmd := m.cover.Download(coverURLs)
	cmds = append(cmds, func() tea.Msg {
		defer wg.Done()
		coverCmd()
		return coverReadyMsg{}
	})

	// Aired episodes: one cmd per airing item, capped at prefetchCap concurrent
	// (animetosho times out under a full season's ~90-at-once load). These run
	// independently of the barrier — they emit latestEpMsg as they finish, so aired
	// counts fill in without delaying the next page's covers. Each cmd only calls
	// the injected fn; the shared *AiredCache is touched solely on the Update
	// goroutine when latestEpMsg lands.
	sem := m.prefetchSem
	for _, it := range airedItems {
		item := it
		fn := m.latestEpisodePrefetch
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			return latestEpMsg{malID: item.MalID, aired: fn(&item)}
		})
	}

	// Barrier: once the page's covers settle, signal the page is done. (Aired
	// cmds are intentionally not waited on — see above.)
	cmds = append(cmds, func() tea.Msg {
		wg.Wait()
		return prefetchPageDoneMsg{firstPage: firstPage}
	})
	return tea.Batch(cmds...)
}

// selectPrefetchPage picks the cover URLs and airing items for one prefetch page.
//
// COVERS page by visibility: page 1 is the visible page (m.view[:pageSize]) so the
// covers the user sees load first; page 2 is the rest (view tail + filtered-out
// items). Covers are gathered for ALL items so a status-filter change always
// reveals cached covers with no re-fetch.
//
// AIRING COUNTS page by list-membership: page 1 = the user's own anime
// (ListStatus != ""), page 2 = off-list anime. Within each page, items are taken
// in DISPLAY (sorted) order — m.view top-down, where the cursor sits — so the
// first visible items get their counts first (then any filtered-out items).
// Page 2 is gated on page 1's covers settling, so off-list aired work starts only
// after the on-list batch is already in flight. Each chosen airing item is marked
// dispatched (AiredCache) so it isn't re-fetched. Pure selection — prefetchPageCmd
// wraps the result into cmds.
func (m *animePicker) selectPrefetchPage(firstPage bool) (coverURLs []string, airedItems []mal.Item) {
	pageSize := m.pageSize()
	// ---- Covers: page by visibility ----
	if firstPage {
		end := min(pageSize, len(m.view))
		for _, it := range m.view[:end] {
			if it.CoverURL != "" {
				coverURLs = append(coverURLs, it.CoverURL)
			}
		}
	} else {
		if pageSize < len(m.view) {
			for _, it := range m.view[pageSize:] {
				if it.CoverURL != "" {
					coverURLs = append(coverURLs, it.CoverURL)
				}
			}
		}
		inView := map[int]bool{}
		for _, it := range m.view {
			inView[it.MalID] = true
		}
		for _, it := range m.items {
			if inView[it.MalID] {
				continue
			}
			if it.CoverURL != "" {
				coverURLs = append(coverURLs, it.CoverURL)
			}
		}
	}

	// ---- Airing counts: page by list-membership (on-list first), and within each
	// page in DISPLAY (sorted) order — m.view top-down, where the cursor is — so the
	// first visible items get their counts first. In-view items first, then any
	// filtered-out items of this membership group. ----
	inView := map[int]bool{}
	for _, it := range m.view {
		inView[it.MalID] = true
		if (it.ListStatus != "") != firstPage {
			continue // page 1 → on-list only; page 2 → off-list only
		}
		airedItems = m.maybeAppendAired(airedItems, it)
	}
	for _, it := range m.items {
		if inView[it.MalID] {
			continue
		}
		if (it.ListStatus != "") != firstPage {
			continue
		}
		airedItems = m.maybeAppendAired(airedItems, it)
	}
	return coverURLs, airedItems
}

// maybeAppendAired appends it to items if it's an airing item whose aired episode
// hasn't been cached or dispatched yet, marking it dispatched. No-op otherwise.
func (m *animePicker) maybeAppendAired(items []mal.Item, it mal.Item) []mal.Item {
	if m.latestEpisodePrefetch == nil || it.MalID == 0 || it.AirStatus != "currently_airing" {
		return items
	}
	if !m.aired.shouldFetch(it.MalID) {
		return items
	}
	m.aired.markDispatched(it.MalID)
	return append(items, it)
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
		m.filter.Status = m.defaultStatus()
		m.loading = true
		m.cursor = 0
		m.topItem = 0
		return m, m.loadCmd(m.source, m.query, m.season)
	case "up", "k":
		if len(m.view) > 0 {
			if m.cursor > 0 {
				m.cursor--
			} else {
				m.cursor = len(m.view) - 1 // wrap to bottom
			}
			m.fixScroll()
			return m, m.focusCmd()
		}
	case "down", "j":
		if len(m.view) > 0 {
			if m.cursor < len(m.view)-1 {
				m.cursor++
			} else {
				m.cursor = 0 // wrap to top
			}
			m.fixScroll()
			return m, m.focusCmd()
		}
	case "ctrl+u":
		if len(m.view) > 0 {
			m.cursor -= m.pageSize()
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.fixScroll()
			return m, m.focusCmd()
		}
	case "ctrl+d":
		if len(m.view) > 0 {
			m.cursor += m.pageSize()
			if m.cursor >= len(m.view) {
				m.cursor = len(m.view) - 1
			}
			m.fixScroll()
			return m, m.focusCmd()
		}
	case "home":
		if len(m.view) > 0 {
			m.cursor = 0
			m.fixScroll()
			return m, m.focusCmd()
		}
	case "end":
		if len(m.view) > 0 {
			m.cursor = len(m.view) - 1
			m.fixScroll()
			return m, m.focusCmd()
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
	case " ":
		// Open the per-anime actions menu (set status / remove). No-op without a
		// MAL item (AnimeTosho fallback) or an injected writer.
		if m.applyStatus == nil {
			return m, nil
		}
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 {
			return m, nil
		}
		// Top-level actions menu (Set Status / Set Score / Remove from My List).
		m.overlay.open(animeOverlayActions, actionGroupLabels(it.ListStatus), "")
		return m, nil
	case "/":
		m.filter.Filtering = true
		m.filter.FuzzyText = ""
		return m, nil
	case ":":
		// Command palette: every anime-picker action, fuzzy-searchable.
		m.palette.Open(m.animeCommands())
		return m, nil
	case "L":
		// Re-run the MAL browser OAuth flow (handled by app.Run after the TUI exits).
		m.result.Relogin = true
		return m, tea.Batch(tea.Quit, m.quitCmd())
	case "a":
		// Account/status overlay: offline snapshot now, best-effort name async.
		return m, m.openAccountModal()
	case "enter":
		if it := m.currentItemCopy(); it != nil {
			// Carry the cached aired count so the release picker reuses it
			// instead of re-fetching on entry.
			it.AiredEps = int(m.aired.value(it.MalID))
			m.result.Anime = it
			return m, tea.Batch(tea.Quit, m.quitCmd())
		}
	}
	return m, nil
}

// openAccountModal opens the read-only account/status overlay: an offline
// snapshot now plus a best-effort username fetch. Shared by the `a` key and the
// palette's "Show account" command.
func (m *animePicker) openAccountModal() tea.Cmd {
	st, _ := mal.AuthStatus()
	m.authStatus = st
	m.authName = ""
	m.authNameFailed = false
	// Only fetch the name when there's a session — otherwise WhoAmI would run
	// the browser OAuth flow (Client() launches it when no token is saved).
	m.authNamePending = st.LoggedIn
	m.overlay.kind = animeOverlayAuth
	m.overlay.items = nil
	m.overlay.cursor = 0
	if st.LoggedIn {
		return m.fetchAuthNameCmd()
	}
	return nil
}

// handlePaletteKey drives the `:` command menu. On a selection it runs the
// chosen intent via applyCommand; Esc/Ctrl-C just close.
func (m *animePicker) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	done, sel := m.palette.HandleKey(msg)
	if done && sel {
		return m.applyCommand(m.palette.Selected().Intent)
	}
	return m, nil
}

// animeCommands builds the palette's command list from live state: sort, the
// status filter, source/provider switching, per-anime actions (on the focused
// item when a writer is wired), account, and quit. Rebuilt each open so it
// reflects the current item's status, the current source, and the provider.
func (m *animePicker) animeCommands() []Command {
	var cmds []Command

	for _, o := range sortOptions {
		cmds = append(cmds, Command{Category: "Sort", Title: "Sort by " + o.label, Intent: "sort:" + o.value, Keywords: "order"})
	}

	cmds = append(cmds, Command{Category: "Filter", Title: "Filter list…", Intent: "filter", Keywords: "search fuzzy"})
	for _, s := range m.statusOptions() {
		cmds = append(cmds, Command{Category: "Filter", Title: "Status: " + s, Intent: "statusfilter:" + s, Keywords: "list"})
	}

	if m.query == "" { // search is query-driven; source/season only apply to browse
		if m.source == SourceList {
			cmds = append(cmds, Command{Category: "Source", Title: "Switch to Season", Intent: "source:season", Keywords: "browse current"})
		} else {
			cmds = append(cmds, Command{Category: "Source", Title: "Switch to My List", Intent: "source:mylist", Keywords: "browse"})
		}
		if m.source == SourceSeason {
			cmds = append(cmds, Command{Category: "Source", Title: "Change season…", Intent: "season", Keywords: "year cour"})
		}
	}

	torrentActive := m.provider != "anidb"
	cmds = append(cmds,
		Command{Category: "Provider", Title: providerLabel("torrent", torrentActive), Intent: "provider:torrent", Keywords: "animetosho backend"},
		Command{Category: "Provider", Title: providerLabel("anidb", !torrentActive), Intent: "provider:anidb", Keywords: "stream backend"},
	)

	// Per-anime actions target the focused row (same as the Space menu); only
	// when a MAL writer is wired (nil in the AnimeTosho fallback).
	if m.applyStatus != nil {
		if it := m.currentItemCopy(); it != nil && it.MalID != 0 {
			for _, o := range actionOptions {
				if o.score || o.action.Remove {
					continue
				}
				if o.action.Status == it.ListStatus { // a no-op: hide
					continue
				}
				cmds = append(cmds, Command{Category: "Actions", Title: "Set status: " + o.display, Intent: "statusset:" + o.action.Status, Keywords: "list"})
			}
			if it.ListStatus != "" {
				for n := 1; n <= 10; n++ {
					cmds = append(cmds, Command{Category: "Actions", Title: fmt.Sprintf("Rate %d", n), Intent: fmt.Sprintf("score:%d", n), Keywords: "rating"})
				}
				cmds = append(cmds,
					Command{Category: "Actions", Title: "Clear score", Intent: "score:0", Keywords: "rating unrate"},
					Command{Category: "Actions", Title: "Set episodes watched…", Intent: "setepisode", Keywords: "progress"},
					Command{Category: "Actions", Title: "Remove from My List…", Intent: "remove", Keywords: "delete"},
				)
			}
			cmds = append(cmds, Command{Category: "Actions", Title: "Open on MAL", Intent: "web", Keywords: "browser url"})
		}
	}

	cmds = append(cmds,
		Command{Category: "Account", Title: "Show account", Intent: "account", Keywords: "login status"},
		Command{Category: "Account", Title: "Re-login (MAL)", Intent: "relogin", Keywords: "auth oauth"},
		Command{Category: "Account", Title: "Log out", Intent: "logout", Keywords: "auth sign out"},
		Command{Category: "View", Title: "Quit", Intent: "quit", Keywords: "exit"},
	)
	return cmds
}

// providerLabel renders a provider menu row, marking the active one.
func providerLabel(name string, active bool) string {
	prefix := "Use "
	if active {
		prefix = "● " // active marker (● = current)
	}
	if name == "anidb" {
		return prefix + "anidb.app (stream)"
	}
	return prefix + "AnimeTosho (torrent)"
}

// applyCommand runs a palette intent, reusing the same mutators/callbacks and
// Result flags as the direct keys and the Space menu. statusset:/score: apply
// directly (the search-and-choose is the confirmation); remove keeps its y/n
// modal. provider: sets Result.SourceSwitch and quits (app.Run re-resolves).
func (m *animePicker) applyCommand(intent string) (tea.Model, tea.Cmd) {
	switch {
	case intent == "quit":
		m.result.Quit = true
		return m, tea.Batch(tea.Quit, m.quitCmd())
	case intent == "relogin":
		m.result.Relogin = true
		return m, tea.Batch(tea.Quit, m.quitCmd())
	case intent == "logout":
		m.result.Logout = true
		return m, tea.Batch(tea.Quit, m.quitCmd())
	case intent == "account":
		return m, m.openAccountModal()
	case intent == "filter":
		m.filter.Filtering = true
		m.filter.FuzzyText = ""
		return m, nil
	case intent == "season":
		if m.query == "" && m.source == SourceSeason {
			m.openSeasonOverlay()
		}
		return m, nil
	case intent == "source:mylist", intent == "source:season":
		if m.query != "" {
			return m, nil
		}
		if intent == "source:mylist" {
			m.source = SourceList
		} else {
			m.source = SourceSeason
		}
		m.season = m.defaultSeason()
		m.filter.Status = m.defaultStatus()
		m.loading = true
		m.cursor = 0
		m.topItem = 0
		return m, m.loadCmd(m.source, m.query, m.season)
	case strings.HasPrefix(intent, "sort:"):
		m.filter.Sort = strings.TrimPrefix(intent, "sort:")
		m.applyFilter()
		return m, nil
	case strings.HasPrefix(intent, "statusfilter:"):
		m.filter.Status = strings.TrimPrefix(intent, "statusfilter:")
		m.applyFilter()
		return m, nil
	case strings.HasPrefix(intent, "statusset:"):
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 || m.applyStatus == nil {
			return m, nil
		}
		return m, m.statusApplyCmd(it.MalID, it.WatchedEps, StatusAction{Status: strings.TrimPrefix(intent, "statusset:")})
	case strings.HasPrefix(intent, "score:"):
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 || m.applyScore == nil {
			return m, nil
		}
		n, err := strconv.Atoi(strings.TrimPrefix(intent, "score:"))
		if err != nil || n < 0 || n > 10 {
			return m, nil
		}
		return m, m.scoreApplyCmd(it.MalID, n)
	case intent == "setepisode":
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 || m.applyWatched == nil {
			return m, nil
		}
		m.openEpisodeOverlay(it.WatchedEps)
		return m, nil
	case intent == "web":
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 {
			return m, nil
		}
		mal.OpenBrowser("https://myanimelist.net/anime/" + strconv.Itoa(it.MalID))
		return m, nil
	case intent == "remove":
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 || m.applyStatus == nil {
			return m, nil
		}
		m.overlay.kind = animeOverlayConfirm
		m.overlay.items = nil
		m.overlay.cursor = 0
		m.overlay.text = fmt.Sprintf("Remove %q from your list?", it.Title)
		m.overlay.pendingAction = StatusAction{Remove: true}
		return m, nil
	case strings.HasPrefix(intent, "provider:"):
		src := strings.TrimPrefix(intent, "provider:")
		if src != "torrent" && src != "anidb" {
			return m, nil
		}
		if src == m.provider { // already active: no-op
			return m, nil
		}
		m.result.SourceSwitch = src
		return m, tea.Batch(tea.Quit, m.quitCmd())
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

// scoreOptions is the score picker list: 1-10 then "Remove Score".
var scoreOptions = func() []string {
	out := make([]string, 0, 11)
	for i := 1; i <= 10; i++ {
		out = append(out, strconv.Itoa(i))
	}
	out = append(out, "Remove Score")
	return out
}()

// openScoreOverlay opens the 1-10 / Remove Score picker, cursor on currentScore.
func (m *animePicker) openScoreOverlay(currentScore int) {
	cur := ""
	if currentScore >= 1 && currentScore <= 10 {
		cur = strconv.Itoa(currentScore)
	}
	m.overlay.open(animeOverlayScore, scoreOptions, cur)
}

// backToActions returns from a sub-menu (Set Status / Set Score / Set Episode)
// to the top-level actions group menu. Used on Esc in those sub-menus.
func (m *animePicker) backToActions() (tea.Model, tea.Cmd) {
	it := m.currentItemCopy()
	if it == nil || it.MalID == 0 {
		m.overlay.close()
		return m, nil
	}
	m.overlay.open(animeOverlayActions, actionGroupLabels(it.ListStatus), "")
	return m, nil
}

// openEpisodeOverlay opens the watched-episode number input, seeded with the
// current watched count.
func (m *animePicker) openEpisodeOverlay(currentWatched int) {
	m.overlay.kind = animeOverlayEpisode
	m.overlay.items = nil
	m.overlay.cursor = 0
	if currentWatched > 0 {
		m.overlay.text = strconv.Itoa(currentWatched)
	} else {
		m.overlay.text = ""
	}
}

// applyEpisodeSelection parses the typed episode number and applies it.
func (m *animePicker) applyEpisodeSelection() (tea.Model, tea.Cmd) {
	text := m.overlay.text
	m.overlay.close()
	it := m.currentItemCopy()
	if it == nil || it.MalID == 0 {
		return m, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 {
		return m, nil
	}
	return m, m.episodeApplyCmd(it.MalID, n)
}

// episodeAppliedMsg carries a finished watched-episode update.
type episodeAppliedMsg struct {
	malID   int
	watched int
	applied bool
}

// episodeApplyCmd runs the watched-episode update in the background.
func (m *animePicker) episodeApplyCmd(malID, watched int) tea.Cmd {
	apply := m.applyWatched
	return func() tea.Msg {
		applied := false
		if apply != nil {
			applied = apply(malID, watched)
		}
		return episodeAppliedMsg{malID: malID, watched: watched, applied: applied}
	}
}

// applyScoreSelection applies the chosen score picker item: a digit sets the
// score, "Remove Score" clears it (0). sel is the selected label; the overlay is
// already closed by the caller.
func (m *animePicker) applyScoreSelection(sel string) (tea.Model, tea.Cmd) {
	it := m.currentItemCopy()
	if it == nil || it.MalID == 0 {
		return m, nil
	}
	score := 0
	if sel != "Remove Score" {
		n, err := strconv.Atoi(sel)
		if err != nil || n < 0 || n > 10 {
			return m, nil
		}
		score = n
	}
	return m, m.scoreApplyCmd(it.MalID, score)
}

// scoreAppliedMsg carries a finished score update.
type scoreAppliedMsg struct {
	malID   int
	score   int
	applied bool
}

// scoreApplyCmd runs the score update in the background.
func (m *animePicker) scoreApplyCmd(malID, score int) tea.Cmd {
	apply := m.applyScore
	return func() tea.Msg {
		applied := false
		if apply != nil {
			applied = apply(malID, score)
		}
		return scoreAppliedMsg{malID: malID, score: score, applied: applied}
	}
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
	// Account/status modal: L re-logs in, o logs out, Esc closes; ignore other keys.
	if m.overlay.kind == animeOverlayAuth {
		switch msg.String() {
		case "L":
			m.result.Relogin = true
			return m, tea.Batch(tea.Quit, m.quitCmd())
		case "o":
			m.result.Logout = true
			return m, tea.Batch(tea.Quit, m.quitCmd())
		case "esc":
			m.overlay.close()
			return m, nil
		}
		return m, nil
	}
	// Confirm modal: y/Enter applies the pending action; anything else cancels.
	if m.overlay.kind == animeOverlayConfirm {
		switch msg.String() {
		case "y", "Y", "enter":
			it := m.currentItemCopy()
			act := m.overlay.pendingAction
			m.overlay.close()
			if it == nil || it.MalID == 0 {
				return m, nil
			}
			// "Completed" implies watched = total episodes (MAL requires it, and
			// it's the intent); fall back to the current count when total is unknown.
			watched := it.WatchedEps
			if act.Status == "completed" && it.TotalEps > 0 {
				watched = it.TotalEps
			}
			return m, m.statusApplyCmd(it.MalID, watched, act)
		default:
			m.overlay.close()
			return m, nil
		}
	}

	// Episode number input: digits build the number; Enter/Space applies; Esc
	// goes back to the group menu.
	if m.overlay.kind == animeOverlayEpisode {
		key := msg.String()
		switch key {
		case "esc", "ctrl+c":
			return m.backToActions()
		case " ", "enter":
			return m.applyEpisodeSelection()
		case "backspace":
			if len(m.overlay.text) > 0 {
				r := []rune(m.overlay.text)
				m.overlay.text = string(r[:len(r)-1])
			}
			return m, nil
		default:
			if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
				m.overlay.text += key
			}
			return m, nil
		}
	}

	// List overlays (status / season / sort / actions).
	switch msg.String() {
	case "esc", "ctrl+c":
		// In a sub-menu opened from the actions group menu, Esc goes back up to
		// the group menu instead of closing entirely.
		if m.overlay.kind == animeOverlayStatusMenu || m.overlay.kind == animeOverlayScore {
			return m.backToActions()
		}
		m.overlay.close()
		return m, nil
	case " ", "enter":
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

// applyOverlaySelection applies the overlay's selection and closes it. The
// actions menu is special: it doesn't apply directly but switches to a confirm
// modal first.
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
		return m, m.focusCmd()
	case animeOverlaySort:
		if v, ok := sortValue(sel); ok {
			m.filter.Sort = v
		}
		m.applyFilter()
		return m, m.focusCmd()
	case animeOverlaySeason:
		if sel == "" {
			return m, nil
		}
		m.season = sel
		m.loading = true
		m.cursor = 0
		m.topItem = 0
		return m, m.loadCmd(m.source, m.query, m.season)
	case animeOverlayActions:
		// Top-level group menu → route to the relevant sub-menu/confirm.
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 {
			return m, nil
		}
		switch sel {
		case "Set Status":
			m.overlay.open(animeOverlayStatusMenu, statusActionLabels(it.ListStatus), "")
			return m, nil
		case "Set Score":
			m.openScoreOverlay(it.Score)
			return m, nil
		case "Set Episode":
			m.openEpisodeOverlay(it.WatchedEps)
			return m, nil
		case "Open Web":
			mal.OpenBrowser("https://myanimelist.net/anime/" + strconv.Itoa(it.MalID))
			return m, nil
		case "Remove from My List":
			m.overlay.kind = animeOverlayConfirm
			m.overlay.items = nil
			m.overlay.cursor = 0
			m.overlay.text = fmt.Sprintf("Remove %q from your list?", it.Title)
			m.overlay.pendingAction = StatusAction{Remove: true}
			return m, nil
		}
		return m, nil
	case animeOverlayStatusMenu:
		it := m.currentItemCopy()
		if it == nil || it.MalID == 0 {
			return m, nil
		}
		act, display, ok := actionByLabel(sel)
		if !ok {
			return m, nil
		}
		m.overlay.kind = animeOverlayConfirm
		m.overlay.items = nil
		m.overlay.cursor = 0
		m.overlay.text = fmt.Sprintf("Set %q to %s?", it.Title, display)
		m.overlay.pendingAction = act
		return m, nil
	case animeOverlayScore:
		return m.applyScoreSelection(sel)
	}
	return m, nil
}

// statusApplyCmd runs the per-anime action (set/remove) in the background.
type statusAppliedMsg struct {
	malID   int
	act     StatusAction
	applied bool
}

func (m *animePicker) statusApplyCmd(malID, watchedEps int, act StatusAction) tea.Cmd {
	apply := m.applyStatus
	return func() tea.Msg {
		applied := false
		if apply != nil {
			applied = apply(malID, watchedEps, act)
		}
		return statusAppliedMsg{malID: malID, act: act, applied: applied}
	}
}

// applyStatusApplied reflects a finished action in the local item set, then
// re-filters (so e.g. a Completed item leaves a Watching filter, or a removed
// item leaves the My List view).
func (m *animePicker) applyStatusApplied(msg statusAppliedMsg) (tea.Model, tea.Cmd) {
	if !msg.applied {
		return m, nil
	}
	for i := range m.items {
		if m.items[i].MalID != msg.malID {
			continue
		}
		if msg.act.Remove {
			if m.query == "" && m.source == SourceList {
				// Removed from the list entirely → drop it from the cached set.
				m.items = append(m.items[:i], m.items[i+1:]...)
			} else {
				m.items[i].ListStatus = ""
			}
			break
		}
		m.items[i].ListStatus = msg.act.Status
		if msg.act.Status == "completed" && m.items[i].TotalEps > 0 {
			m.items[i].WatchedEps = m.items[i].TotalEps
		}
		break
	}
	m.cursor = 0
	m.topItem = 0
	m.applyFilter()
	return m, m.focusCmd()
}

func (m *animePicker) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Esc discards the filter (vim: abort search) and returns to normal mode.
		m.filter.Filtering = false
		m.filter.FuzzyText = ""
		m.applyFilter()
		m.fixScroll()
		return m, m.focusCmd()
	case "enter":
		// Enter accepts the filter (vim: keep the pattern) and returns to normal
		// mode with the filter still applied — it does not select the item.
		m.filter.Filtering = false
		m.fixScroll()
		return m, m.focusCmd()
	case "up":
		if m.cursor > 0 {
			m.cursor--
			m.fixScroll()
			return m, m.focusCmd()
		}
		return m, nil
	case "down":
		if m.cursor < len(m.view)-1 {
			m.cursor++
			m.fixScroll()
			return m, m.focusCmd()
		}
		return m, nil
	case "backspace":
		if len(m.filter.FuzzyText) > 0 {
			r := []rune(m.filter.FuzzyText)
			m.filter.FuzzyText = string(r[:len(r)-1])
			m.applyFilter()
			m.fixScroll()
			return m, m.focusCmd()
		}
		m.filter.Filtering = false
		m.applyFilter()
		return m, m.focusCmd()
	case "ctrl+w":
		m.filter.FuzzyText = dropLastWord(m.filter.FuzzyText)
		m.applyFilter()
		m.fixScroll()
		return m, m.focusCmd()
	case " ", "tab":
		m.filter.FuzzyText += " "
		m.applyFilter()
		m.fixScroll()
		return m, m.focusCmd()
	default:
		if isPrintable(msg) {
			m.filter.FuzzyText += msg.String()
			m.applyFilter()
			m.fixScroll()
			return m, m.focusCmd()
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

// coverTextMsg carries the unicode-placeholder text for a cover. key is the
// anime's malID (anime picker) or the series' aid (series picker), used to cache
// the rendered cover height so the placeholder matches exactly on re-focus.
type coverTextMsg struct {
	text string
	key  int
}

// focusCmd batches the work done when the focused anime changes: load its cover
// and (for airing anime) fetch its latest aired episode.
func (m *animePicker) focusCmd() tea.Cmd {
	return tea.Batch(m.loadCoverCmd(), m.latestEpisodeCmd())
}

// latestEpisodeCmd fetches the latest aired episode for the focused airing anime
// (cached, nil-gated). Returns nil when there's nothing to fetch.
func (m *animePicker) latestEpisodeCmd() tea.Cmd {
	if m.latestEpisode == nil {
		return nil
	}
	cur := m.currentItemCopy()
	if cur == nil || cur.MalID == 0 || cur.AirStatus != "currently_airing" {
		return nil
	}
	if !m.aired.shouldFetch(cur.MalID) {
		return nil // already computed (or in flight) this session
	}
	m.aired.markDispatched(cur.MalID)
	item := cur // stable copy; safe for the background goroutine
	fn := m.latestEpisode
	return func() tea.Msg { return latestEpMsg{malID: item.MalID, aired: fn(item)} }
}

func (m *animePicker) loadCoverCmd() tea.Cmd {
	cur := m.currentItemCopy()
	if cur == nil || cur.CoverURL == "" || m.cover == nil {
		return func() tea.Msg { return coverTextMsg{text: ""} }
	}
	path := m.cover.Get(cur.CoverURL)
	cols, rows := m.coverCols, m.coverRows
	malID := cur.MalID
	if path == "" {
		return func() tea.Msg { return coverTextMsg{text: ""} }
	}
	return func() tea.Msg {
		upload, text, err := RenderCoverPlaceholder(path, cols, rows)
		if err != nil {
			return coverTextMsg{text: ""}
		}
		WriteUpload(upload)
		return coverTextMsg{text: text, key: malID}
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
	switch m.overlay.kind {
	case animeOverlayConfirm:
		return m.renderConfirmModal()
	case animeOverlayAuth:
		return m.renderAuthStatusModal()
	}

	// ---- LEFT pane (list / overlay / palette) ----
	var leftContent string
	if m.palette.Active() {
		leftContent = m.palette.View(m.listWidth-2, m.pageSize())
	} else if m.overlay.active() {
		if m.overlay.kind == animeOverlayEpisode {
			// Number-input overlay (text prompt, not a list).
			title := TitleStyle.Render("Episodes watched (Enter = set, Esc = back)")
			input := SelectedStyle.Render("▶ " + m.overlay.text + "▏")
			leftContent = title + "\n" + input
		} else {
			leftContent = renderListOverlayContent(m.overlay.kind.String(), m.overlay.items, m.overlay.cursor, m.pageSize())
		}
	} else {
		title := TitleStyle.Render("Anime") + FaintStyle.Render(fmt.Sprintf("  (%d)", len(m.view)))
		if m.filter.Filtering || m.filter.FuzzyText != "" {
			title += "  " + FaintStyle.Render("filter: ") + m.filter.FuzzyText
			if m.filter.Filtering {
				title += "▏"
			}
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
	help := HelpStyle.Render("j/k move  Tab source  Enter select  / filter  : command  q quit")
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	return lipgloss.JoinVertical(lipgloss.Left, header, badges, panes, help)
}

// renderConfirmModal draws the centered y/n modal for a per-anime action
// (set status / remove). Replaces the panes while active.
func (m *animePicker) renderConfirmModal() string {
	questionColor := colorAccent
	if m.overlay.pendingAction.Remove {
		questionColor = lipgloss.Color("203") // salmon/red for destructive remove
	}
	question := lipgloss.NewStyle().Bold(true).Foreground(questionColor).Render(m.overlay.text)
	hint := lipgloss.NewStyle().Render("[Y] yes   [n] no  (Esc = no)")
	body := ModalBorderStyle.Render(lipgloss.JoinVertical(lipgloss.Center, question, "", hint))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

// renderAuthStatusModal draws the centered MAL account/status overlay: login state,
// best-effort username + id, token expiry, and the L/o/Esc actions. Offline fields
// render instantly (AuthStatus); the username fills in via fetchAuthNameCmd.
func (m *animePicker) renderAuthStatusModal() string {
	st := m.authStatus
	lines := []string{TitleStyle.Render("Account")}

	status := FaintStyle.Render("not logged in — press L to log in")
	if st.LoggedIn {
		status = ProgressStyle.Render("logged in")
	}
	lines = append(lines, "Status: "+status)

	if st.LoggedIn {
		name := ""
		switch {
		case m.authNamePending:
			name = FaintStyle.Render("…")
		case m.authNameFailed:
			name = FaintStyle.Render("(name unavailable)")
		case m.authName != "":
			name = m.authName
		}
		id := st.Subject
		if id == "" {
			id = "?"
		}
		lines = append(lines, fmt.Sprintf("User:   %s (id %s)", name, id))

		switch {
		case st.ExpiryKnown && st.Expiry.After(time.Now()):
			lines = append(lines, "Token:  "+FaintStyle.Render("valid until "+st.Expiry.Format("2006-01-02")))
		case st.ExpiryKnown:
			lines = append(lines, "Token:  "+ErrorStyle.Render("EXPIRED — press L to re-login"))
		default:
			lines = append(lines, "Token:  "+FaintStyle.Render("expiry unknown"))
		}
	}

	lines = append(lines, "", FaintStyle.Render("[L] re-login   [o] log out   Esc back"))
	body := ModalBorderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
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
	if len(m.view) == 0 && m.loadErr != nil {
		msg := fmt.Sprintf("MAL error: %v", m.loadErr)
		if mal.IsAuthError(m.loadErr) {
			msg += " — press L to log in"
		}
		return ErrorStyle.Render(msg)
	}
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
	// No blank placeholder: show nothing until the cover loads, then show the
	// cover at its exact rendered height. Avoids the height mismatch between a
	// fixed placeholder and the actual cover.
	if m.coverText != "" {
		lines = append(lines, strings.Split(m.coverText+"\x1b[0m", "\n")...)
	}

	if cur == nil {
		return fitPaneHeight(strings.Join(padToHeight(lines, m.paneHeight-2), "\n"), m.paneHeight-2)
	}

	width := m.width - m.previewCol - 2
	if width < 12 {
		width = 12
	}

	lines = append(lines, TitleStyle.Render(wrap(cur.Title, width)))

	progress := ui.FormatProgress(cur.WatchedEps, cur.TotalEps, m.aired.value(cur.MalID), cur.AirStatus == "currently_airing")
	if a := ui.MALAirShort(cur.AirStatus); a != "" {
		progress += "  [" + a + "]"
	}
	// Render the progress core (wrapped), then append the status badge unwrapped
	// so its ANSI doesn't get split by line wrapping.
	progressLine := ProgressStyle.Render(wrap(progress, width))
	if cur.ListStatus != "" {
		if badge := ui.ColoredStatus(cur.ListStatus); badge != "" {
			progressLine += "  " + badge
		}
	} else if cur.WatchedEps > 0 {
		progressLine += "  ·  Watching"
	}
	lines = append(lines, progressLine)

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
