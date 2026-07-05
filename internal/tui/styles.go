package tui

import "github.com/charmbracelet/lipgloss"

// Color palette — mirrors the ANSI codes used by the fzf preview panes
// (internal/ui/preview.go) so the two UIs read as the same app.
//
//	1;36 bold cyan   — title
//	  33 yellow      — progress / status
//	  35 magenta     — score
//	  32 green       — genres
//	  34 blue        — studios
//	   2 dim/faint   — season, rank, help
var (
	colorTitle    = lipgloss.Color("51")  // bright cyan
	colorProgress = lipgloss.Color("220") // yellow
	colorScore    = lipgloss.Color("213") // magenta
	colorGenres   = lipgloss.Color("46")  // green
	colorStudios  = lipgloss.Color("39")  // blue
	colorSelected = lipgloss.Color("51")  // cyan
	colorBadgeFg  = lipgloss.Color("0")   // black
	colorBadgeBg  = lipgloss.Color("220") // yellow
	colorFaint    = lipgloss.Color("245") // grey
	colorAccent   = lipgloss.Color("213") // magenta accent for headers
)

// TitleStyle styles the main title of a pane.
var TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorTitle)

// ProgressStyle styles progress/status text (ep X/Y, list status).
var ProgressStyle = lipgloss.NewStyle().Foreground(colorProgress)

// ScoreStyle styles score text.
var ScoreStyle = lipgloss.NewStyle().Foreground(colorScore)

// GenresStyle styles the genres line.
var GenresStyle = lipgloss.NewStyle().Foreground(colorGenres)

// StudiosStyle styles the studios line.
var StudiosStyle = lipgloss.NewStyle().Foreground(colorStudios)

// FaintStyle styles dim/secondary text (season, rank, help bar).
var FaintStyle = lipgloss.NewStyle().Foreground(colorFaint)

// SelectedStyle styles the currently focused list row. The explicit SetString
// ensures the rendered output wraps the whole line (full reset at both ends),
// so the highlight never leaks into adjacent unstyled lines.
var SelectedStyle = lipgloss.NewStyle().Foreground(colorSelected).Bold(true)

// ListBorderStyle draws a rounded border around the list pane.
var ListBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("240"))

// PreviewBorderStyle draws a rounded border around the preview pane.
var PreviewBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("240"))

// OverlayBorderStyle draws the border around a filter overlay (group/quality/
// episode pop-up). Uses a distinct color so it stands apart from the list box.
var OverlayBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("213"))

// BadgeStyle styles filter badges (yellow background, black text).
var BadgeStyle = lipgloss.NewStyle().Background(colorBadgeBg).Foreground(colorBadgeFg).Bold(true)

// InactiveBadgeStyle styles inactive filter badges (dim, no background).
var InactiveBadgeStyle = lipgloss.NewStyle().Foreground(colorFaint)

// HeaderStyle styles the anime info header above the release list.
var HeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

// HelpStyle styles the bottom help/keybinding bar.
var HelpStyle = lipgloss.NewStyle().Faint(true).PaddingLeft(1)

// CursorGlyph is the marker drawn before the focused row.
const CursorGlyph = "▶ "

// CoverBlankStyle renders plain spaces for the cover area.
var CoverBlankStyle = lipgloss.NewStyle()
