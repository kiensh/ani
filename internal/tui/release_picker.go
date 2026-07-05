package tui

import (
	"fmt"
	"strings"

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

	filter  Filter
	overlay filterOverlay

	view    []*animetosho.Release // filter.Apply(all)
	cursor  int
	topItem int

	width, height int

	result *Result
}

func newReleasePicker(all []*animetosho.Release, item *mal.Item, group, quality, sortName string, debug bool) *releasePicker {
	rp := &releasePicker{
		all:       all,
		groups:    DistinctGroups(all),
		qualities: DistinctQualities(all),
		item:      item,
		debug:     debug,
		result:    &Result{},
	}
	rp.filter.Group = group
	rp.filter.Quality = quality
	rp.filter.Sort = ui.NormalizeSort(sortName)
	// Default filter: next-unwatched episode (quality left at "all" — no default).
	if rp.filter.Episode == 0 && item != nil {
		rp.filter.Episode = DefaultEpisode(item.WatchedEps, item.TotalEps)
	}
	rp.applyFilter()
	return rp
}

func (m *releasePicker) Init() tea.Cmd { return nil }

func (m *releasePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.fixScroll()
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
		if m.cursor > 0 {
			m.cursor--
			m.fixScroll()
		}
	case "down", "j":
		if m.cursor < len(m.view)-1 {
			m.cursor++
			m.fixScroll()
		}
	case "g":
		m.overlay.openGroup(m.groups, m.filter.Group)
		return m, nil
	case "r":
		m.overlay.openQuality(m.qualities, m.filter.Quality)
		return m, nil
	case "e":
		m.overlay.openEpisode(m.filter.Episode)
		return m, nil
	case "s":
		m.filter.CycleSort()
		m.applyFilter()
	case "/":
		m.filter.Filtering = true
		m.filter.FuzzyText = ""
		return m, nil
	case "enter":
		if cur := m.currentRelease(); cur != nil {
			m.result.Release = cur
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
			// Esc in the episode overlay clears the filter to "all" (0).
			m.filter.Episode = 0
			m.overlay.close()
			m.applyFilter()
			return m, nil
		case "enter":
			m.overlay.applySelected(&m.filter)
			m.overlay.close()
			m.applyFilter()
			return m, nil
		default:
			m.overlay.handleEpisodeKey(msg.String())
			return m, nil
		}
	}

	// List overlays (group / quality).
	switch msg.String() {
	case "esc", "ctrl+c":
		m.overlay.close()
		return m, nil
	case "enter":
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

func (m *releasePicker) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Esc exits filter mode but keeps the filter applied.
		m.filter.Filtering = false
		m.fixScroll()
		return m, nil
	case "enter":
		m.filter.Filtering = false
		m.applyFilter()
		if len(m.view) > 0 {
			m.cursor = 0
			if cur := m.currentRelease(); cur != nil {
				m.result.Release = cur
				return m, tea.Quit
			}
		}
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
	ps := m.pageSize()
	if m.cursor < m.topItem {
		m.topItem = m.cursor
	}
	if m.cursor >= m.topItem+ps {
		m.topItem = m.cursor - ps + 1
	}
	if m.topItem < 0 {
		m.topItem = 0
	}
}

func (m *releasePicker) View() string {
	if m.width == 0 {
		return "Loading releases…"
	}

	// Header line 1: anime info + count. (FIXED)
	var h1 string
	if info := ui.MALItemHeader(m.item); info != "" {
		h1 = HeaderStyle.Render(info) + "  ·  "
	}
	h1 += FaintStyle.Render(fmt.Sprintf("%d rels", len(m.view)))
	if m.filter.Filtering {
		h1 += "  " + FaintStyle.Render("filter: ") + m.filter.FuzzyText + "▏"
	}

	// Header line 2: filter badges. (FIXED)
	h2 := m.renderBadges()

	// List area: scrolls WITHIN a fixed height, bordered. When an overlay is
	// active it replaces the whole list region (border included) so the fixed
	// header/preview/help stay put and the overlay sits exactly over the list.
	var listArea string
	if m.overlay.active() {
		listArea = OverlayBorderStyle.
			Width(m.width).
			Height(m.listHeight()).
			Render(m.renderOverlayContent())
	} else {
		listArea = ListBorderStyle.
			Width(m.width).
			Height(m.listHeight()).
			Render(m.renderList())
	}

	// Preview of the focused release. (FIXED)
	preview := m.renderPreview()

	help := HelpStyle.Render(rpHelpText)

	return lipgloss.JoinVertical(lipgloss.Left, h1, h2, listArea, preview, help)
}

// renderBadges renders the active-filter badges line: group / quality / episode
// / sort. Active filters are yellow; the rest are dim.
func (m *releasePicker) renderBadges() string {
	group := ui.GroupLabel(m.filter.Group)
	parts := []string{
		conditionalBadge("group:"+group, m.filter.Group != ""),
	}
	qLabel := qualityLabel(m.filter.Quality)
	parts = append(parts, conditionalBadge("q:"+qLabel, m.filter.Quality != ""))
	if m.filter.Episode > 0 {
		parts = append(parts, conditionalBadge(fmt.Sprintf("ep:%d", m.filter.Episode), true))
	} else {
		parts = append(parts, conditionalBadge("ep:all", false))
	}
	parts = append(parts, conditionalBadge("sort:"+ui.NormalizeSort(m.filter.Sort), false))
	return strings.Join(parts, " ")
}

const rpHelpText = "j/k nav  g group  r quality  e episode  s sort  / filter  Enter select  Esc back  q quit"

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
// the caller wraps it in the overlay box). Group/quality show a selectable list
// with a ▶ marker; episode shows a text-input prompt.
func (m *releasePicker) renderOverlayContent() string {
	switch m.overlay.kind {
	case overlayGroup:
		return renderListOverlayContent("Group", m.overlay.items, m.overlay.cursor)
	case overlayQuality:
		return renderListOverlayContent("Quality", m.overlay.items, m.overlay.cursor)
	case overlayEpisode:
		title := TitleStyle.Render("Episode (blank = all, Esc cancel)")
		input := SelectedStyle.Render("▶ " + m.overlay.text + "▏")
		return title + "\n" + input
	}
	return ""
}

// renderListOverlayContent renders the inner content of a list overlay: a title
// line, then one selectable line per item with the highlighted one prefixed by
// ▶ and styled.
func renderListOverlayContent(title string, items []string, cursor int) string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render(title))
	b.WriteByte('\n')
	for i, it := range items {
		if i == cursor {
			b.WriteString(SelectedStyle.Render(CursorGlyph + it))
		} else {
			b.WriteString("  " + it)
		}
		if i < len(items)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
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
