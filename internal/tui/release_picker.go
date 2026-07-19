package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ani/internal/animetosho"
	"ani/internal/mal"
	"ani/internal/ui"
)

// releasePicker is the release selection screen. Top: header (anime info +
// count) and filter badges. Middle: scrollable release list inside a bordered
// box. Bottom: preview of the focused release (title + magnet) and the help
// bar. All sections are FIXED height except the list, which scrolls internally
// — so the header, preview, and help never get pushed off-screen.
//
// Keys (when no overlay is active): j/k navigate, g group overlay, r quality
// overlay, e episode overlay, s cycle sort, / fuzzy filter, Enter select, Esc
// back to anime selection, q quit.
type releasePicker struct {
	all       []*animetosho.Release
	groups    []string // distinct groups, for the group overlay
	qualities []string // distinct qualities present, for the quality overlay
	item      *mal.Item
	debug     bool

	// fetch returns the releases for a given episode (cached + scoped by the
	// caller). Invoked on demand: initially for the default episode, and again
	// whenever the user changes the episode filter.
	fetch    func(int) []*animetosho.Release
	fetching bool // a fetch is in flight; show "Loading…" in the list area

	// episodeDisabled suppresses the episode filter (the 'e' overlay and the
	// default-episode seed). Used for the latest-uploads landing screen, where
	// the list spans many series and per-episode filtering is meaningless.
	episodeDisabled bool

	filter  Filter
	overlay filterOverlay

	// copyMagnet copies a magnet URI to the clipboard (nil disables Copy Magnet).
	copyMagnet func(string) error
	toast      string // transient confirmation line (e.g. "✓ Magnet copied")

	// latestEpisode returns the latest aired episode for m.item (nil disables
	// the "watched/aired/total" header). aired caches the result for m.item.
	latestEpisode func(*mal.Item) int
	aired         int

	view    []*animetosho.Release // filter.Apply(all)
	cursor  int
	topItem int

	width, height int

	result *Result
}

func newReleasePicker(item *mal.Item, group, quality, sortName string, fetch func(int) []*animetosho.Release, disableEpisode bool, copyMagnet func(string) error, latestEpisode func(*mal.Item) int, debug bool) *releasePicker {
	rp := &releasePicker{
		item:            item,
		debug:           debug,
		fetch:           fetch,
		fetching:        true, // the initial episode fetch is kicked off by Init
		episodeDisabled: disableEpisode,
		copyMagnet:      copyMagnet,
		latestEpisode:   latestEpisode,
		result:          &Result{},
	}
	// Reuse the aired count cached by the anime picker (if any), so the header
	// shows immediately and Init doesn't re-fetch it.
	if item != nil {
		rp.aired = item.AiredEps
	}
	rp.filter.Group = group
	rp.filter.Quality = quality
	rp.filter.Sort = ui.NormalizeSort(sortName)
	// Default filter: next-unwatched episode (quality left at "all" — no default).
	// Skipped when the episode filter is disabled (latest-uploads view).
	if !disableEpisode && rp.filter.Episode == 0 && item != nil {
		rp.filter.Episode = DefaultEpisode(item.WatchedEps, item.TotalEps)
	}
	return rp
}

// releasesLoadedMsg carries the releases for one episode fetch. ep lets Update
// discard stale results when the user changed the episode again mid-fetch.
type releasesLoadedMsg struct {
	releases []*animetosho.Release
	ep       int
}

// fetchCmd returns a tea.Cmd that fetches the given episode's releases.
func (m *releasePicker) fetchCmd(ep int) tea.Cmd {
	fetch := m.fetch
	return func() tea.Msg {
		return releasesLoadedMsg{releases: fetch(ep), ep: ep}
	}
}

// latestEpMsg carries the latest aired episode for the header anime.
type latestEpMsg struct {
	malID int
	aired int
}

func (m *releasePicker) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(m.filter.Episode), m.airedFetchCmd())
}

// airedFetchCmd fetches the latest aired episode for the "watched/aired/total"
// header — unless the anime picker already cached it on the item (m.aired), in
// which case it returns nil so the picker reuses the cached value.
func (m *releasePicker) airedFetchCmd() tea.Cmd {
	if m.aired != 0 || m.item == nil || m.item.MalID == 0 || m.latestEpisode == nil {
		return nil
	}
	item := m.item
	fn := m.latestEpisode
	return func() tea.Msg { return latestEpMsg{malID: item.MalID, aired: fn(item)} }
}

func (m *releasePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.fixScroll()
		return m, nil

	case releasesLoadedMsg:
		return m.applyLoaded(msg)

	case clearToastMsg:
		m.toast = ""
		return m, nil

	case latestEpMsg:
		if m.item != nil && msg.malID == m.item.MalID {
			m.aired = msg.aired
		}
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

// applyLoaded ingests a fetched episode's releases: populate all/groups/
// qualities, clear the loading state, and kick off a background prefetch of the
// next episode (so the post-play loop is instant). Stale results (the user
// changed the episode again before this fetch returned) are discarded.
func (m *releasePicker) applyLoaded(msg releasesLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.ep != m.filter.Episode {
		return m, nil
	}
	m.all = msg.releases
	m.groups = DistinctGroups(m.all)
	m.qualities = DistinctQualities(m.all)
	m.fetching = false
	m.cursor = 0
	m.topItem = 0
	m.applyFilter()
	// Prefetch ep+1 into the session cache (no UI effect) so advancing is instant.
	if msg.ep > 0 {
		fetch := m.fetch
		next := msg.ep + 1
		return m, func() tea.Msg { fetch(next); return nil }
	}
	return m, nil
}

func (m *releasePicker) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.result.Quit = true
		return m, tea.Quit
	case "esc":
		// Esc (outside filter/overlay mode) goes back to anime selection.
		// Quit is NOT set — Back distinguishes "back" from "quit".
		m.result.Back = true
		return m, tea.Quit
	case "home":
		m.cursor = 0
		m.fixScroll()
	case "end":
		m.cursor = max(0, len(m.view)-1)
		m.fixScroll()
	case "ctrl+u":
		m.cursor -= m.pageSize()
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.fixScroll()
	case "ctrl+d":
		m.cursor += m.pageSize()
		if m.cursor >= len(m.view) {
			m.cursor = max(0, len(m.view)-1)
		}
		m.fixScroll()
	case "up", "k":
		if len(m.view) > 0 {
			if m.cursor > 0 {
				m.cursor--
			} else {
				m.cursor = len(m.view) - 1 // wrap to bottom
			}
			m.fixScroll()
		}
	case "down", "j":
		if len(m.view) > 0 {
			if m.cursor < len(m.view)-1 {
				m.cursor++
			} else {
				m.cursor = 0 // wrap to top
			}
			m.fixScroll()
		}
	case "g":
		m.overlay.openGroup(m.groups, m.filter.Group)
		return m, nil
	case "r":
		m.overlay.openQuality(m.qualities, m.filter.Quality)
		return m, nil
	case "e":
		if m.episodeDisabled {
			return m, nil // episode filter N/A for the latest-uploads view
		}
		m.overlay.openEpisode(m.filter.Episode)
		return m, nil
	case "s":
		m.overlay.openSort(m.filter.Sort)
		return m, nil
	case "/":
		m.filter.Filtering = true
		m.filter.FuzzyText = ""
		return m, nil
	case " ":
		// Open the per-release actions menu (Play / Download / Copy Magnet).
		if m.currentRelease() != nil {
			m.overlay.openActions()
			return m, nil
		}
	case "enter":
		// Enter plays immediately (no separate play/download prompt).
		if cur := m.currentRelease(); cur != nil {
			m.result.Release = cur
			m.result.Action = "play"
			return m, tea.Quit
		}
	case "d":
		// d downloads immediately.
		if cur := m.currentRelease(); cur != nil {
			m.result.Release = cur
			m.result.Action = "download"
			return m, tea.Quit
		}
	}
	return m, nil
}

// handleOverlayKey routes keys to the active overlay (group/quality list or
// episode input). Enter applies, Esc cancels.
func (m *releasePicker) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay.kind {
	case overlayEpisode:
		switch msg.String() {
		case "esc", "ctrl+c":
			// Esc cancels: restore the episode to its pre-overlay value. The
			// loaded set already matches it, so no re-fetch is needed.
			m.filter.Episode = m.overlay.prevEpisode
			m.overlay.close()
			m.applyFilter()
			return m, nil
		case "enter":
			m.overlay.applySelected(&m.filter)
			m.overlay.close()
			m.fetching = true
			return m, m.fetchCmd(m.filter.Episode)
		default:
			m.overlay.handleEpisodeKey(msg.String())
			return m, nil
		}
	}

	// List overlays (group / quality / sort / actions).
	switch msg.String() {
	case "esc", "ctrl+c":
		m.overlay.close()
		return m, nil
	case " ", "enter":
		// The actions menu doesn't go through applySelected — it picks an action.
		if m.overlay.kind == overlayActions {
			return m.applyAction()
		}
		m.overlay.applySelected(&m.filter)
		m.overlay.close()
		m.applyFilter()
		return m, nil
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

// toastHold is how long the copy-magnet confirmation stays on screen.
const toastHold = 1200 * time.Millisecond

// clearToastMsg clears the transient toast line.
type clearToastMsg struct{}

// applyAction runs the chosen actions-menu item. Play/Download select the release
// and quit (same path as Enter/d); Copy Magnet copies to the clipboard and shows
// a brief toast, then returns to the list.
func (m *releasePicker) applyAction() (tea.Model, tea.Cmd) {
	sel := ""
	if m.overlay.cursor >= 0 && m.overlay.cursor < len(m.overlay.items) {
		sel = m.overlay.items[m.overlay.cursor]
	}
	cur := m.currentRelease()
	m.overlay.close()
	if cur == nil {
		return m, nil
	}
	switch sel {
	case "Play":
		m.result.Release = cur
		m.result.Action = "play"
		return m, tea.Quit
	case "Download":
		m.result.Release = cur
		m.result.Action = "download"
		return m, tea.Quit
	case "Copy Magnet URL":
		if m.copyMagnet != nil && cur.Entry.Magnet != "" {
			m.copyMagnet(cur.Entry.Magnet)
			m.toast = "✓ Magnet copied to clipboard"
			return m, tea.Tick(toastHold, func(time.Time) tea.Msg { return clearToastMsg{} })
		}
	}
	return m, nil
}

func (m *releasePicker) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Esc discards the filter (vim: abort search) and returns to normal mode.
		m.filter.Filtering = false
		m.filter.FuzzyText = ""
		m.applyFilter()
		m.fixScroll()
		return m, nil
	case "enter":
		// Enter accepts the filter (vim: keep the pattern) and returns to normal
		// mode with the filter still applied — it does not select the release.
		m.filter.Filtering = false
		m.fixScroll()
		return m, nil
	case "up":
		if m.cursor > 0 {
			m.cursor--
			m.fixScroll()
		}
		return m, nil
	case "down":
		if m.cursor < len(m.view)-1 {
			m.cursor++
			m.fixScroll()
		}
		return m, nil
	case "backspace":
		if len(m.filter.FuzzyText) > 0 {
			r := []rune(m.filter.FuzzyText)
			m.filter.FuzzyText = string(r[:len(r)-1])
			m.applyFilter()
		} else {
			m.filter.Filtering = false
			m.applyFilter()
		}
		return m, nil
	case "ctrl+w":
		m.filter.FuzzyText = dropLastWord(m.filter.FuzzyText)
		m.applyFilter()
		return m, nil
	default:
		if isPrintable(msg) {
			m.filter.FuzzyText += msg.String()
			m.applyFilter()
			return m, nil
		}
	}
	return m, nil
}

// applyFilter recomputes the visible slice from the current filter state and
// clamps the cursor.
func (m *releasePicker) applyFilter() {
	m.view = m.filter.Apply(m.all)
	if m.cursor >= len(m.view) {
		m.cursor = max(0, len(m.view)-1)
	}
	m.fixScroll()
}

func (m *releasePicker) currentRelease() *animetosho.Release {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return nil
	}
	return m.view[m.cursor]
}

// Fixed section heights. The list fills everything these leave behind.
const (
	rpHeaderLines  = 2 // anime info + count, then filter badges
	rpPreviewLines = 3 // title + detail + magnet
	rpHelpLines    = 1
	rpListBorders  = 2 // bordered box top + bottom
)

// listHeight is the FIXED number of rows the scrolling list area may occupy.
// Header, badges, preview, and help are always visible (Bug 6).
func (m *releasePicker) listHeight() int {
	if m.height == 0 {
		return 20
	}
	h := m.height - rpHeaderLines - rpPreviewLines - rpListBorders - rpHelpLines
	if h < 1 {
		h = 1
	}
	return h
}

// pageSize returns the number of list items visible inside the bordered box.
// listHeight includes borders; content area is listHeight - 2.
func (m *releasePicker) pageSize() int {
	ps := m.listHeight() - 2 // subtract top+bottom border
	if ps < 1 {
		ps = 1
	}
	return ps
}

func (m *releasePicker) fixScroll() {
	m.topItem = clampTop(m.cursor, m.topItem, m.pageSize(), len(m.view))
}

func (m *releasePicker) View() string {
	if m.width == 0 {
		return "Loading releases…"
	}

	// Header line 1: anime info + count. (FIXED)
	var h1 string
	if info := ui.MALItemHeader(m.item, m.aired); info != "" {
		h1 = HeaderStyle.Render(info) + "  ·  "
	}
	h1 += FaintStyle.Render(fmt.Sprintf("%d rels", len(m.view)))
	if m.filter.Filtering || m.filter.FuzzyText != "" {
		h1 += "  " + FaintStyle.Render("filter: ") + m.filter.FuzzyText
		if m.filter.Filtering {
			h1 += "▏"
		}
	}

	// Header line 2: filter badges. (FIXED)
	h2 := m.renderBadges()

	// List area: scrolls WITHIN a fixed height, bordered. While a fetch is in
	// flight show a "Loading…" state; an overlay replaces the whole list region
	// (border included) so the fixed header/preview/help stay put.
	var listArea string
	switch {
	case m.fetching:
		listArea = ListBorderStyle.
			Width(m.width).
			Height(m.listHeight()).
			Render(m.loadingText())
	case m.overlay.active():
		listArea = OverlayBorderStyle.
			Width(m.width).
			Height(m.listHeight()).
			Render(m.renderOverlayContent())
	default:
		listArea = ListBorderStyle.
			Width(m.width).
			Height(m.listHeight()).
			Render(m.renderList())
	}

	// Preview of the focused release. (FIXED)
	preview := m.renderPreview()

	help := HelpStyle.Render(rpHelpText)
	if m.toast != "" {
		// Transient confirmation (e.g. "✓ Magnet copied") in place of the help line.
		help = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render(m.toast)
	}

	return lipgloss.JoinVertical(lipgloss.Left, h1, h2, listArea, preview, help)
}

// loadingText is the message shown in the list area while an episode fetch is
// in flight.
func (m *releasePicker) loadingText() string {
	if m.filter.Episode > 0 {
		return FaintStyle.Render(fmt.Sprintf("Loading episode %d…", m.filter.Episode))
	}
	return FaintStyle.Render("Loading releases…")
}

// renderBadges renders the active-filter badges line: group / quality / episode
// / sort. Active filters are yellow; the rest are dim.
func (m *releasePicker) renderBadges() string {
	group := ui.GroupLabel(m.filter.Group)
	parts := []string{
		conditionalBadge("group:"+group, m.filter.Group != ""),
	}
	qLabel := qualityLabel(m.filter.Quality)
	parts = append(parts, conditionalBadge("resolution:"+qLabel, m.filter.Quality != ""))
	if m.filter.Episode > 0 {
		parts = append(parts, conditionalBadge(fmt.Sprintf("ep:%d", m.filter.Episode), true))
	} else {
		parts = append(parts, conditionalBadge("ep:all", false))
	}
	parts = append(parts, conditionalBadge("sort:"+ui.NormalizeSort(m.filter.Sort), false))
	return strings.Join(parts, " ")
}

const rpHelpText = "j/k nav  g group  r quality  e episode  s sort  Space act  / filter  Enter play  d download  Esc back  q quit"

// renderList draws the visible slice of the filtered list with a cursor glyph.
// Each line is rendered independently with a full style reset so the selected
// highlight never leaks into adjacent rows. The selected row gets the full
// glyph + SelectedStyle; other rows are plain strings.
func (m *releasePicker) renderList() string {
	ps := m.pageSize()
	end := m.topItem + ps
	if end > len(m.view) {
		end = len(m.view)
	}
	// Content width inside the list border: -2 (left+right border) -2 (glyph).
	avail := m.width - 2 - len(CursorGlyph)
	if avail < 4 {
		avail = 4
	}
	var b strings.Builder
	for i := m.topItem; i < end; i++ {
		text := clip(ui.RenderReleaseLine(m.view[i]), avail)
		var line string
		if i == m.cursor {
			line = SelectedStyle.Render(CursorGlyph + text)
		} else {
			line = "  " + text
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	// Pad with blank lines so the list ALWAYS fills the content area exactly.
	// This prevents lipgloss.JoinVertical from collapsing the fixed layout.
	for i := end - m.topItem; i < ps; i++ {
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *releasePicker) renderPreview() string {
	cur := m.currentRelease()
	width := m.width
	if width < 20 {
		width = 20
	}
	// Always exactly rpPreviewLines (3) lines — truncate, never wrap.
	lines := make([]string, 0, rpPreviewLines)
	if cur == nil {
		lines = append(lines, FaintStyle.Render("(no releases match)"))
	} else {
		lines = append(lines, TitleStyle.Render(ui.Truncate(cur.Entry.Title, width)))
		detail := ui.RenderReleaseLine(cur)
		lines = append(lines, ProgressStyle.Render(ui.Truncate(detail, width)))
		if magnet := cur.Entry.Magnet; magnet != "" {
			short := magnet
			if len([]rune(short)) > width-9 {
				short = string([]rune(short)[:width-10]) + "…"
			}
			lines = append(lines, FaintStyle.Render(ui.Truncate("magnet: "+short, width)))
		} else {
			lines = append(lines, "")
		}
	}
	// Pad to exactly rpPreviewLines.
	for len(lines) < rpPreviewLines {
		lines = append(lines, "")
	}
	return strings.Join(lines[:rpPreviewLines], "\n")
}

// renderOverlayContent renders the active overlay's inner content (no border —
// the caller wraps it in the overlay box). Group/quality show a selectable,
// scrolling list; episode shows a text-input prompt.
func (m *releasePicker) renderOverlayContent() string {
	switch m.overlay.kind {
	case overlayGroup:
		return renderListOverlayContent("Group", m.overlay.items, m.overlay.cursor, max(1, m.pageSize()-1))
	case overlayQuality:
		return renderListOverlayContent("Quality", m.overlay.items, m.overlay.cursor, max(1, m.pageSize()-1))
	case overlaySort:
		return renderListOverlayContent("Sort", m.overlay.items, m.overlay.cursor, max(1, m.pageSize()-1))
	case overlayActions:
		return renderListOverlayContent("Actions", m.overlay.items, m.overlay.cursor, max(1, m.pageSize()-1))
	case overlayEpisode:
		title := TitleStyle.Render("Episode (blank = all, Esc cancel)")
		input := SelectedStyle.Render("▶ " + m.overlay.text + "▏")
		return title + "\n" + input
	}
	return ""
}

// renderListOverlayContent renders the inner content of a list overlay: a title
// line, then a windowed slice of items centered on the cursor so a long list
// scrolls inside the pane instead of overflowing. maxItems is the available item
// rows; ↑/↓ hints mark when more items exist off-screen.
func renderListOverlayContent(title string, items []string, cursor, maxItems int) string {
	n := len(items)
	if n > 0 {
		title = fmt.Sprintf("%s (%d)", title, n)
	}
	lines := []string{TitleStyle.Render(title)}

	top, end := 0, n
	if maxItems > 0 && n > maxItems {
		avail := maxItems - 2 // reserve room for the ↑/↓ hint lines
		if avail < 1 {
			avail = 1
		}
		top = cursor - avail/2
		if top < 0 {
			top = 0
		}
		if top > n-avail {
			top = n - avail
		}
		end = top + avail
		if top > 0 {
			lines = append(lines, FaintStyle.Render("  ↑ more"))
		}
	}
	for i := top; i < end; i++ {
		if i == cursor {
			lines = append(lines, SelectedStyle.Render(CursorGlyph+items[i]))
		} else {
			lines = append(lines, "  "+items[i])
		}
	}
	if end < n {
		lines = append(lines, FaintStyle.Render("  ↓ more"))
	}
	return strings.Join(lines, "\n")
}

// conditionalBadge renders active when on, otherwise dim.
func conditionalBadge(text string, on bool) string {
	if on {
		return BadgeStyle.Render(" " + text + " ")
	}
	return InactiveBadgeStyle.Render(" " + text + " ")
}

func qualityLabel(q string) string {
	if q == "" {
		return "all"
	}
	return q
}
