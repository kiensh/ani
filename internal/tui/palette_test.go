package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestPaletteOpenShowsAll(t *testing.T) {
	p := &Palette{}
	p.Open([]Command{
		{Category: "Sort", Title: "Sort by Score", Intent: "sort:score"},
		{Category: "Sort", Title: "Sort by Title", Intent: "sort:title"},
		{Category: "View", Title: "Quit", Intent: "quit"},
	})
	if !p.Active() {
		t.Fatal("palette not active after Open")
	}
	if len(p.filtered) != 3 {
		t.Fatalf("expected 3 filtered, got %d", len(p.filtered))
	}
	if p.cursor != 0 {
		t.Fatalf("cursor %d, want 0", p.cursor)
	}
}

func TestPaletteFilterNarrowsAndSelects(t *testing.T) {
	p := &Palette{}
	p.Open([]Command{
		{Category: "Sort", Title: "Sort by Score", Intent: "sort:score"},
		{Category: "Sort", Title: "Sort by Title", Intent: "sort:title"},
		{Category: "View", Title: "Quit", Intent: "quit"},
	})
	// "sc" → only "Sort by Score" (s..c in "score"); "Sort by Title" has no 'c'.
	p.HandleKey(keyRunes("s"))
	p.HandleKey(keyRunes("c"))
	if len(p.filtered) != 1 || p.filtered[0].Intent != "sort:score" {
		t.Fatalf("expected only sort:score, got %+v", p.filtered)
	}
	done, sel := p.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !done || !sel {
		t.Fatal("enter should select")
	}
	if p.Selected().Intent != "sort:score" {
		t.Fatalf("selected %q, want sort:score", p.Selected().Intent)
	}
	if p.Active() {
		t.Fatal("palette should close after selection")
	}
}

func TestPaletteEscapeCancels(t *testing.T) {
	p := &Palette{}
	p.Open([]Command{{Category: "X", Title: "Quit", Intent: "quit"}})
	done, sel := p.HandleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !done || sel {
		t.Fatal("esc should close without selection")
	}
	if p.Active() {
		t.Fatal("palette should be closed after esc")
	}
	if p.Selected().Intent != "" {
		t.Fatal("no selection expected after esc")
	}
}

func TestPaletteCursorWrapsAndJumps(t *testing.T) {
	p := &Palette{}
	p.Open([]Command{
		{Title: "A", Intent: "a"},
		{Title: "B", Intent: "b"},
		{Title: "C", Intent: "c"},
	})
	p.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // 1
	p.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // 2
	p.HandleKey(tea.KeyMsg{Type: tea.KeyDown}) // wrap → 0
	if p.cursor != 0 {
		t.Fatalf("after 3 downs cursor %d, want 0 (wrap)", p.cursor)
	}
	p.HandleKey(tea.KeyMsg{Type: tea.KeyUp}) // wrap → 2
	if p.cursor != 2 {
		t.Fatalf("up from 0 wrapped to %d, want 2", p.cursor)
	}
	p.HandleKey(tea.KeyMsg{Type: tea.KeyHome})
	if p.cursor != 0 {
		t.Fatalf("home cursor %d, want 0", p.cursor)
	}
	p.HandleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if p.cursor != 2 {
		t.Fatalf("end cursor %d, want 2", p.cursor)
	}
}

func TestPalettePagingClampsNoWrap(t *testing.T) {
	// Ctrl-U/Ctrl-D (paging) must clamp at the ends, NOT wrap — only ↑/↓ cycle.
	p := &Palette{}
	cmds := make([]Command, 20)
	for i := range cmds {
		cmds[i] = Command{Title: string(rune('a' + i)), Intent: string(rune('a' + i))}
	}
	p.Open(cmds)
	p.rows = 6 // pageStep = 5 (set by View normally)

	for i := 0; i < 10; i++ { // ctrl+d well past the end
		p.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlD})
	}
	if p.cursor != 19 {
		t.Fatalf("after ctrl+d past end, cursor %d want 19 (clamp, no wrap)", p.cursor)
	}
	for i := 0; i < 10; i++ { // ctrl+u back well past the top
		p.HandleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	}
	if p.cursor != 0 {
		t.Fatalf("after ctrl+u past top, cursor %d want 0 (clamp, no wrap)", p.cursor)
	}

	// Sanity: arrows still wrap (down from last → first).
	p.HandleKey(tea.KeyMsg{Type: tea.KeyEnd}) // → 19
	p.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	if p.cursor != 0 {
		t.Fatalf("down from last should wrap to 0, got %d", p.cursor)
	}
}

func TestPaletteBackspaceClearsFilter(t *testing.T) {
	p := &Palette{}
	p.Open([]Command{
		{Title: "Sort by Score", Intent: "sort:score"},
		{Title: "Set status Watching", Intent: "statusset:watching"},
	})
	for _, r := range "sort" {
		p.HandleKey(keyRunes(string(r)))
	}
	if len(p.filtered) != 1 {
		t.Fatalf("expected 1 match for 'sort', got %d", len(p.filtered))
	}
	for i := 0; i < 4; i++ { // erase "sort" → "" → full list
		p.HandleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	if len(p.filtered) != 2 {
		t.Fatalf("after clearing input expected 2, got %d", len(p.filtered))
	}
}

func TestPaletteHeaderShowsWhileFiltering(t *testing.T) {
	// Category "Zeta" never appears in any title, so finding it in the filtered
	// view proves the section header renders even while filtering.
	p := &Palette{}
	p.Open([]Command{
		{Category: "Zeta", Title: "Alpha", Intent: "a"},
		{Category: "Sort", Title: "Sort by Score", Intent: "sort:score"},
	})
	p.HandleKey(keyRunes("a")) // matches only "Alpha" (no 'a' in "Sort by Score")
	out := p.View(40, 10)
	if !strings.Contains(out, "Zeta") {
		t.Fatalf("expected 'Zeta' header to persist while filtering, got:\n%s", out)
	}
}

func TestPaletteViewNoMatchHint(t *testing.T) {
	p := &Palette{}
	p.Open([]Command{{Title: "Quit", Intent: "quit"}})
	p.HandleKey(keyRunes("z"))
	if out := p.View(40, 10); !strings.Contains(out, "no matching commands") {
		t.Fatalf("expected no-match hint, got %q", out)
	}
}

func TestPaletteViewWindowsWithHeaders(t *testing.T) {
	// 20 commands across 5 categories → 25 display rows (5 headers + 20 cmds).
	// A small maxRows must window so the block never overflows its box and the
	// cursor row stays visible.
	p := &Palette{}
	var cmds []Command
	for cat := 0; cat < 5; cat++ {
		for n := 0; n < 4; n++ {
			cmds = append(cmds, Command{
				Category: fmt.Sprintf("Cat%d", cat),
				Title:    fmt.Sprintf("cmd%d%d", cat, n),
				Intent:   fmt.Sprintf("i%d%d", cat, n),
			})
		}
	}
	p.Open(cmds)
	for i := 0; i < len(cmds)-1; i++ { // cursor → last command
		p.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	out := p.View(40, 6) // ~5 content rows fit
	lines := strings.Split(out, "\n")
	if len(lines) > 6 {
		t.Fatalf("expected ≤6 lines (1 prompt + ≤5 rows), got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(out, CursorGlyph+"cmd43") {
		t.Fatalf("cursor row must be visible:\n%s", out)
	}
}
