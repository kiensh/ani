package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ani/internal/mal"
	"ani/internal/ui"
)

// AnimeMode selects which header the anime picker shows.
type AnimeMode int

const (
	AnimeModeList   AnimeMode = iota // user's MAL list (no query)
	AnimeModeSearch                  // MAL text search
)

// animePicker is the MAL anime selection screen. Left pane: scrollable list
// (title, ep X/Y, status) inside a rounded border. Right pane: cover image
// (kitten icat via /dev/tty, painted over a pure-blank region) above colored
// metadata text. Supports `/` fuzzy filter, j/k + arrow navigation, Enter to
// select, q to quit.
//
// Cover images are pre-downloaded to temp files on load (CoverCache) so renders
// read a local file — instant, no network delay, no blank flash on navigation.
type animePicker struct {
	items    []mal.Item
	mode     AnimeMode
	query    string // for AnimeModeSearch header
	filtered []int  // indices into items matching the fuzzy filter
	cursor   int    // index into filtered
	topItem  int    // first visible row in the list
	debug    bool

	cover *CoverCache

	width, height int

	// Layout, recomputed on WindowSizeMsg. All in terminal cells.
	listWidth  int // outer width of the LEFT pane (incl. border)
	paneHeight int // height of both panes (= height - header - help)
	previewCol int // column where the RIGHT pane (incl. its left border) starts
	coverCol   int // column where the cover image is placed (inside right border)
	coverRow   int // row where the cover image is placed (inside top border)
	coverCols  int // cell width of the cover area (matches --place W)
	coverRows  int // cell height of the cover area (matches --place H)

	filterText string
	filtering  bool

	result *Result
}

func newAnimePicker(items []mal.Item, mode AnimeMode, query string, debug bool) *animePicker {
	ap := &animePicker{
		items:  items,
		mode:   mode,
		query:  query,
		debug:  debug,
		result: &Result{},
	}
	ap.applyFilter()
	return ap
}

func (m *animePicker) Init() tea.Cmd {
	// Pre-download every cover to a temp file so on-screen renders are instant.
	urls := make([]string, 0, len(m.items))
	for _, it := range m.items {
		if it.CoverURL != "" {
			urls = append(urls, it.CoverURL)
		}
	}
	cmd, cache := NewCoverCache(urls)
	m.cover = cache
	return cmd
}

func (m *animePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.recomputeLayout()
		m.fixScroll()
		return m, m.renderCoverCmd()

	case coverReadyMsg:
		// All downloads finished — re-render so any skipped cover now draws.
		return m, m.renderCoverCmd()

	case coverRenderedMsg:
		return m, nil

	case tea.KeyMsg:
		if m.filtering {
			return m.handleFilterKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *animePicker) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.result.Quit = true
		// ClearCover on exit so the graphics-layer image doesn't persist on
		// the real screen after the alt-screen is torn down.
		return m, tea.Batch(tea.Quit, m.quitCmd())
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.fixScroll()
			return m, m.renderCoverCmd()
		}
	case "down", "j":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
			m.fixScroll()
			return m, m.renderCoverCmd()
		}
	case "/":
		m.filtering = true
		m.filterText = ""
		return m, nil
	case "enter":
		if idx := m.selectedIndex(); idx >= 0 {
			item := m.items[idx]
			m.result.Anime = &item
			return m, tea.Batch(tea.Quit, m.quitCmd())
		}
	}
	return m, nil
}

// quitCmd clears the placed cover and the temp dir. Used on every screen
// transition out of the anime picker (quit/select).
func (m *animePicker) quitCmd() tea.Cmd {
	cache := m.cover
	return tea.Batch(func() tea.Msg {
		if cache != nil {
			cache.Cleanup()
		}
		return coverRenderedMsg{}
	}, ClearCover(m.coverPlace()))
}

func (m *animePicker) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Esc exits filter mode but keeps the filter applied.
		m.filtering = false
		m.fixScroll()
		return m, nil
	case "enter":
		m.filtering = false
		m.applyFilter()
		m.fixScroll()
		if len(m.filtered) > 0 {
			m.cursor = 0
			if idx := m.selectedIndex(); idx >= 0 {
				item := m.items[idx]
				m.result.Anime = &item
				return m, tea.Batch(tea.Quit, m.quitCmd())
			}
		}
		return m, m.renderCoverCmd()
	case "up":
		if m.cursor > 0 {
			m.cursor--
			m.fixScroll()
			return m, m.renderCoverCmd()
		}
		return m, nil
	case "down":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
			m.fixScroll()
			return m, m.renderCoverCmd()
		}
		return m, nil
	case "backspace":
		if len(m.filterText) > 0 {
			r := []rune(m.filterText)
			m.filterText = string(r[:len(r)-1])
			m.applyFilter()
			m.fixScroll()
			return m, m.renderCoverCmd()
		}
		m.filtering = false
		return m, m.renderCoverCmd()
	case " ", "tab":
		m.filterText += " "
		m.applyFilter()
		m.fixScroll()
		return m, m.renderCoverCmd()
	default:
		if isPrintable(msg) {
			m.filterText += msg.String()
			m.applyFilter()
			m.fixScroll()
			return m, m.renderCoverCmd()
		}
	}
	return m, nil
}

// applyFilter rebuilds m.filtered from the current filterText (substring,
// case-insensitive). Empty filter shows everything; the cursor is clamped.
func (m *animePicker) applyFilter() {
	needle := strings.ToLower(m.filterText)
	m.filtered = m.filtered[:0]
	for i, it := range m.items {
		if needle == "" || strings.Contains(strings.ToLower(it.Title), needle) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

// selectedIndex maps the cursor (into filtered) back to an items index.
func (m *animePicker) selectedIndex() int {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return -1
	}
	return m.filtered[m.cursor]
}

func (m *animePicker) currentItem() *mal.Item {
	idx := m.selectedIndex()
	if idx < 0 {
		return nil
	}
	return &m.items[idx]
}

// recomputeLayout sets the pane sizes and the cover/preview anchor cells from
// the ACTUAL layout, so the kitten icat --place argument lines up exactly with
// the blank cover area rendered by renderMetadata().
//
// Layout (top→bottom): header (1), panes (height-2), help (1).
// Left pane ≈ 60% width (clamped 35–60); right pane gets the rest (min 25).
// Both panes have a 1-cell border on every side and no inner padding, so the
// cover area sits flush inside the right pane's border.
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
	m.paneHeight = h - 2 // header (1) + help (1)
	if m.paneHeight < 3 {
		m.paneHeight = 3
	}

	// RIGHT pane starts where the LEFT pane ends (borders are shared via
	// JoinHorizontal, so the right pane's left border sits at listWidth).
	m.previewCol = listW
	// Cover area lives INSIDE the right pane border: +1 col, +1 row (header)
	// +1 row (right pane top border). No inner padding.
	m.coverCol = m.previewCol + 1
	m.coverRow = 1 /* header */ + 1 /* right pane top border */

	// Cover width = right pane content width (full width inside the borders).
	// Cover width = right pane CONTENT width (previewWidth minus 2 borders).
	// The blank cover lines must be exactly this wide to sit flush inside the
	// border (no wrap/clip) and to match the --place width.
	previewContentW := w - m.previewCol - 2 /* left+right border */
	m.coverCols = clamp(previewContentW, 8, 40)

	// Cover height = right pane content height (paneHeight minus its 2 border
	// rows), capped to 14 so tall terminals leave room for metadata below it.
	// Never exceed the content height, or metadata would overflow the border.
	contentH := m.paneHeight - 2 /* right pane top+bottom border */
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

// coverPlace returns the current kitten --place string for the cover image.
func (m *animePicker) coverPlace() string {
	return CoverPlace(m.coverCols, m.coverRows, m.coverCol, m.coverRow)
}

// pageSize is the number of list rows that fit inside the LEFT pane (its
// content height, minus title and border rows). The left pane renders a title
// line at its top, so reserve 1 for the title.
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

// fixScroll keeps the cursor within the visible window, scrolling topItem as
// needed.
func (m *animePicker) fixScroll() {
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

func (m *animePicker) renderCoverCmd() tea.Cmd {
	cur := m.currentItem()
	if cur == nil || cur.CoverURL == "" {
		// No cover to draw. Don't clear on every navigation (ClearCover runs
		// only on exit/select); the blank cover area in View() prevents stale
		// text from leaking through.
		return nil
	}
	place := CoverPlace(m.coverCols, m.coverRows, m.coverCol, m.coverRow)
	return RenderCover(m.cover, cur.CoverURL, place)
}

// headerText renders the 1-line header for the current mode.
func (m *animePicker) headerText() string {
	switch m.mode {
	case AnimeModeSearch:
		return HeaderStyle.Render(fmt.Sprintf("Search: %q — %d results", m.query, len(m.filtered)))
	default:
		noun := "anime"
		if len(m.filtered) == 1 {
			noun = "anime"
		}
		return HeaderStyle.Render(fmt.Sprintf("Watching List — %d %s", len(m.filtered), noun))
	}
}

func (m *animePicker) View() string {
	if m.width == 0 {
		return "Loading anime…"
	}

	// ---- LEFT pane (list) ----
	title := TitleStyle.Render("Anime") + FaintStyle.Render(fmt.Sprintf("  (%d)", len(m.filtered)))
	if m.filtering {
		title += "  " + FaintStyle.Render("filter: ") + m.filterText + "▏"
	}
	leftContent := title + "\n" + m.renderList()
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
	help := HelpStyle.Render("j/k nav  / filter  Enter select  q quit")
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	return lipgloss.JoinVertical(lipgloss.Left, header, panes, help)
}

// renderList draws the visible slice of the filtered list with a cursor glyph.
// Each line is rendered independently with a full style reset so the selected
// highlight never leaks into adjacent rows. The selected row gets the full
// "▶ " prefix + SelectedStyle; other rows are plain strings (no style), which
// reset naturally.
func (m *animePicker) renderList() string {
	ps := m.pageSize()
	end := m.topItem + ps
	if end > len(m.filtered) {
		end = len(m.filtered)
	}
	// Content width inside the list border: -2 (left+right border) -2 (glyph).
	avail := m.listWidth - 2 - len(CursorGlyph)
	if avail < 4 {
		avail = 4
	}
	var b strings.Builder
	for i := m.topItem; i < end; i++ {
		idx := m.filtered[i]
		text := clip(ui.RenderMALLine(m.items[idx]), avail)
		var line string
		if i == m.cursor {
			line = SelectedStyle.Render(CursorGlyph + text)
		} else {
			line = "  " + text
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// renderMetadata builds the right pane content. The TOP region is exactly
// `coverRows` lines of `coverCols` blank spaces — this is where kitten icat
// paints the cover, and its dimensions MUST match the RenderCover --place
// argument (same X/Y/W/H). Below the cover region comes the colored metadata
// text. No text may live inside the cover region, otherwise the image overlaps
// the text.
func (m *animePicker) renderMetadata() string {
	cur := m.currentItem()

	// Always emit the cover area first. Use explicit background color so the
	// text layer covers any stale kitty graphics image from a previous item.
	// The new image is placed AFTER View() via a tea.Cmd, so it renders on top.
	coverRows := m.coverRows
	coverCols := m.coverCols
	blank := strings.Repeat(" ", coverCols)
	// Background style to cover the old image (kitty renders images behind text bg).
	coverBlank := CoverBlankStyle.Render(blank)
	lines := make([]string, 0, coverRows+8)
	for i := 0; i < coverRows; i++ {
		lines = append(lines, coverBlank)
	}

	if cur == nil {
		// Pad remaining pane height with blanks so the border is uniform.
		return fitPaneHeight(strings.Join(padToHeight(lines, m.paneHeight-2), "\n"), m.paneHeight-2)
	}

	// Metadata content width = preview content width (matches cover width).
	width := m.width - m.previewCol - 2 /* left+right border */
	if width < 12 {
		width = 12
	}

	lines = append(lines, TitleStyle.Render(wrap(cur.Title, width)))

	// progress / status
	progress := ""
	switch {
	case cur.TotalEps > 0:
		progress = fmt.Sprintf("ep %d/%d", cur.WatchedEps, cur.TotalEps)
	default:
		progress = fmt.Sprintf("ep %d", cur.WatchedEps)
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

	// score
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
		parts := []string{}
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

// fitPaneHeight ensures the joined content occupies exactly maxLines rows: it
// splits on newlines, truncates to maxLines (preserving the leading coverRows
// blank lines so the cover --place region stays valid), and pads short content
// with blanks. maxLines <= 0 returns s unchanged.
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

// padToHeight appends blank lines (each width = the last line's width if
// present, else 1) until the slice has target lines. Used so the right pane
// fills its border height uniformly when there's no metadata to show.
func padToHeight(lines []string, target int) []string {
	for len(lines) < target {
		lines = append(lines, "")
	}
	return lines
}

// ---- small text helpers shared by the pickers ----

// clip truncates s to n runes (no ellipsis; lipgloss clips per-style anyway).
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

// wrap breaks s into newline-separated pieces no wider than width runes,
// preferring to break after a space. (Mirrors ui.WrapLine but returns a single
// joined string.)
func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ui.WrapLine(s, width)
}

// isPrintable reports whether a key event is a single printable rune.
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

// clamp limits v to the inclusive [lo, hi] range.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
