package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RunActionPrompt launches a small full-screen prompt asking the user to play
// or download the selected release. Returns the chosen action ("play" or
// "download"). The default (Enter) is play.
func RunActionPrompt(releaseTitle string) (string, error) {
	m := &actionPrompt{title: releaseTitle, result: "play"}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return "", err
	}
	return m.result, nil
}

// RunCompletedPrompt launches a small full-screen prompt asking whether to mark
// the anime completed on MAL. Returns true for yes (default).
func RunCompletedPrompt(animeTitle string) (bool, error) {
	m := &completedPrompt{title: animeTitle, result: true}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return false, err
	}
	return m.result, nil
}

// ---- action prompt ----

type actionPrompt struct {
	title  string
	result string
}

func (m *actionPrompt) Init() tea.Cmd { return nil }

func (m *actionPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "p", "P":
		m.result = "play"
		return m, tea.Quit
	case "d", "D":
		m.result = "download"
		return m, tea.Quit
	case "enter":
		m.result = "play" // default
		return m, tea.Quit
	case "ctrl+c", "q":
		m.result = "play"
		return m, tea.Quit
	}
	return m, nil
}

func (m *actionPrompt) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(colorTitle).Render("  Selected:")
	subject := lipgloss.NewStyle().Faint(true).Render("  " + clip(m.title, 60))
	opts := lipgloss.NewStyle().Render("\n  [p] play in mpv   [d] download via aria2c  (default p)")
	hint := HelpStyle.Render("\n  press p or d, Enter to confirm")
	return lipgloss.JoinVertical(lipgloss.Left, title, subject, opts, hint)
}

// ---- completed prompt ----

type completedPrompt struct {
	title  string
	result bool // true = mark completed (default)
}

func (m *completedPrompt) Init() tea.Cmd { return nil }

func (m *completedPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "y", "Y":
		m.result = true
		return m, tea.Quit
	case "n", "N":
		m.result = false
		return m, tea.Quit
	case "enter":
		m.result = true // default Y
		return m, tea.Quit
	case "ctrl+c", "q":
		m.result = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *completedPrompt) View() string {
	question := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).
		Render("  Mark as completed on MAL?")
	subject := lipgloss.NewStyle().Faint(true).Render("  " + clip(m.title, 60))
	opts := lipgloss.NewStyle().Render("\n  [Y] yes   [n] no  (default Y)")
	hint := HelpStyle.Render("\n  press y or n, Enter to confirm")
	return lipgloss.JoinVertical(lipgloss.Left, question, subject, opts, hint)
}
