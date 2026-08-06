package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Command is one searchable action in the `:` command palette. Intent is an
// opaque key the owning picker interprets in its applyCommand switch (e.g.
// "sort:score", "play", "provider:anidb"); Title + Keywords drive the fuzzy
// filter; Category groups rows when the filter is empty.
type Command struct {
	Category string
	Title    string
	Keywords string
	Intent   string
}

// Palette is the screen-agnostic `:` fuzzy command menu. A picker rebuilds its
// Command list from live state on each Open (so it reflects the current filters,
// the focused item, login state, provider, etc.) and hands it over; the Palette
// owns the input text, the filtered view, and the cursor. It never knows what an
// action does — HandleKey returns the chosen Intent for the picker to run.
//
// Reuses the package's existing primitives: fuzzyMatch for filtering,
// dropLastWord for ctrl-w, isPrintable for text input, and the OverlayBorderStyle
// / SelectedStyle / CursorGlyph / FaintStyle look for rendering (via the owning
// picker's border, the same way the filter overlays are drawn).
type Palette struct {
	all      []Command
	filtered []Command
	input    []rune
	cursor   int
	open     bool
	rows     int // last known available command-rows (set by View; used for pgup/dn)

	selected Command
}

// Open seeds the palette with a command list and resets input/cursor. The list
// is shown grouped by Category (insertion order) until the user types.
func (p *Palette) Open(all []Command) {
	p.all = all
	p.filtered = all
	p.input = nil
	p.cursor = 0
	p.open = true
	p.selected = Command{}
}

// Active reports whether the palette is capturing input.
func (p *Palette) Active() bool { return p.open }

// Close deactivates the palette without a selection.
func (p *Palette) Close() { p.open = false }

// Selected returns the chosen command after HandleKey signals a selection.
func (p *Palette) Selected() Command { return p.selected }

// HandleKey processes one key. done=true means the palette should close: if
// selected is also true, the caller runs Selected().Intent (then closes). Esc /
// Ctrl-C close without a selection.
func (p *Palette) HandleKey(msg tea.KeyMsg) (done, selected bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		p.Close()
		return true, false
	case "enter":
		if len(p.filtered) > 0 {
			p.selected = p.filtered[p.cursor]
			p.Close()
			return true, true
		}
	case "up":
		p.move(-1, true) // arrows cycle (wrap)
	case "down":
		p.move(1, true)
	case "home":
		p.cursor = 0
	case "end":
		if n := len(p.filtered); n > 0 {
			p.cursor = n - 1
		}
	case "ctrl+u", "pgup":
		for i := 0; i < p.pageStep(); i++ {
			p.move(-1, false) // paging clamps at the ends (no wrap)
		}
	case "ctrl+d", "pgdown":
		for i := 0; i < p.pageStep(); i++ {
			p.move(1, false)
		}
	case "backspace":
		if len(p.input) > 0 {
			p.input = p.input[:len(p.input)-1]
			p.refilter()
		}
	case "ctrl+w":
		p.input = []rune(dropLastWord(string(p.input)))
		p.refilter()
	default:
		if isPrintable(msg) {
			p.input = append(p.input, []rune(msg.String())...)
			p.refilter()
		}
	}
	return false, false
}

// pageStep is how many rows ctrl-u/d jumps — one screenful minus the overlap.
func (p *Palette) pageStep() int {
	if p.rows > 1 {
		return p.rows - 1
	}
	return 1
}

// move advances the cursor by delta. wrap=true cycles past the ends (used by
// ↑/↓); wrap=false clamps at the first/last item (used by Ctrl-U/D paging).
func (p *Palette) move(delta int, wrap bool) {
	n := len(p.filtered)
	if n == 0 {
		p.cursor = 0
		return
	}
	p.cursor += delta
	if wrap {
		for p.cursor < 0 {
			p.cursor += n
		}
		p.cursor %= n
		return
	}
	if p.cursor < 0 {
		p.cursor = 0
	} else if p.cursor >= n {
		p.cursor = n - 1
	}
}

// refilter recomputes the visible commands via fuzzyMatch over Category+Title+
// Keywords. Empty input restores the full grouped list (cursor to top).
func (p *Palette) refilter() {
	needle := strings.ToLower(strings.TrimSpace(string(p.input)))
	if needle == "" {
		p.filtered = p.all
		p.cursor = 0
		return
	}
	out := make([]Command, 0, len(p.all))
	for _, c := range p.all {
		hay := strings.ToLower(c.Category + " " + c.Title + " " + c.Keywords)
		if fuzzyMatch(hay, needle) {
			out = append(out, c)
		}
	}
	p.filtered = out
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}
}

// View renders the palette's inner content — the input line plus the visible
// commands, windowed to the cursor. The owning picker wraps this in its usual
// overlay/list border (the same way it draws its filter overlays). width
// truncates long rows; maxRows is the inner content height available (the input
// line consumes one row, the rest show commands + category headers).
//
// Category headers always render (even while filtering), so each section keeps
// its title; they are themselves display rows, so windowing is done over the
// combined header+command line list — otherwise many categories would push the
// block past its fixed-height box.
func (p *Palette) View(width, maxRows int) string {
	if maxRows < 3 {
		maxRows = 3
	}
	p.rows = maxRows - 1 // reserve one row for the input line

	prompt := TitleStyle.Render(":") + " " + string(p.input) +
		lipgloss.NewStyle().Foreground(colorSelected).Render("▏")
	lines := []string{prompt}

	if len(p.filtered) == 0 {
		lines = append(lines, FaintStyle.Render("  no matching commands"))
		return strings.Join(lines, "\n")
	}

	type paletteRow struct {
		header   bool
		text     string
		selected bool
	}
	var rows []paletteRow
	cmdRow := make([]int, len(p.filtered)) // command index → row index
	lastCat := ""
	for i, c := range p.filtered {
		if c.Category != "" && c.Category != lastCat {
			lastCat = c.Category
			rows = append(rows, paletteRow{header: true, text: c.Category})
		}
		cmdRow[i] = len(rows)
		title := c.Title
		if width > 0 {
			title = paletteTruncate(title, width-4)
		}
		rows = append(rows, paletteRow{text: title, selected: i == p.cursor})
	}

	curRow := 0
	if p.cursor >= 0 && p.cursor < len(cmdRow) {
		curRow = cmdRow[p.cursor]
	}
	top, vis := paletteWindow(curRow, p.rows, len(rows))
	for ri := top; ri < top+vis; ri++ {
		r := rows[ri]
		switch {
		case r.header:
			lines = append(lines, FaintStyle.Render(" "+r.text))
		case r.selected:
			lines = append(lines, SelectedStyle.Render(CursorGlyph+r.text))
		default:
			lines = append(lines, "  "+r.text)
		}
	}
	return strings.Join(lines, "\n")
}

// paletteWindow returns the top index and visible count for a cursor within
// total, keeping the cursor on-screen (mirrors the lists' fixScroll behavior).
func paletteWindow(cursor, maxRows, total int) (top, vis int) {
	if total <= maxRows {
		return 0, total
	}
	top = cursor - maxRows/2
	if top < 0 {
		top = 0
	}
	if top+maxRows > total {
		top = total - maxRows
	}
	if top < 0 {
		top = 0
	}
	return top, maxRows
}

// paletteTruncate crops a plain (unstyled) string to width display cells.
func paletteTruncate(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}
