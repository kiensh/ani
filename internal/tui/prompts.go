package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RunCompletedPrompt launches a centered modal asking whether to mark the anime
// completed on MAL. On "yes" it shows a green success flash for ~1.4s before
// closing, so the result is impossible to miss (previously it was a stderr log
// after the prompt exited). Returns true for yes.
func RunCompletedPrompt(animeTitle string) (bool, error) {
	m := &completedPrompt{title: animeTitle, phase: phaseConfirm, result: false}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return false, err
	}
	return m.result, nil
}

// completed-prompt phases.
type completedPhase int

const (
	phaseConfirm completedPhase = iota
	phaseDone // green success flash; auto-closes after SuccessFlashHold
)

// completedPrompt is a two-phase centered modal: confirm, then a green success
// flash. result is the user's decision (yes/no); the success phase is only
// reached on yes.
type completedPrompt struct {
	title  string
	phase  completedPhase
	result bool // true = mark completed

	width, height int
}

// successTickMsg fires after the success flash has held long enough.
type successTickMsg struct{}

func (m *completedPrompt) Init() tea.Cmd { return nil }

func (m *completedPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case successTickMsg:
		// Flash held long enough — close.
		return m, tea.Quit
	case tea.KeyMsg:
		if m.phase == phaseDone {
			// Any key during the flash dismisses it immediately.
			return m, tea.Quit
		}
		switch msg.String() {
		case "y", "Y", "enter":
			m.result = true
			m.phase = phaseDone
			return m, tea.Tick(SuccessFlashHold, func(time.Time) tea.Msg { return successTickMsg{} })
		case "n", "N", "esc", "q", "ctrl+c":
			// No / cancel: do NOT mark completed (cancel ≠ yes here).
			m.result = false
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *completedPrompt) View() string {
	if m.phase == phaseDone {
		body := SuccessStyle.Render("✓ Marked \"" + clip(m.title, 48) + "\" completed on MAL")
		return m.center(body)
	}
	question := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).
		Render("Mark as completed on MAL?")
	subject := lipgloss.NewStyle().Faint(true).Render(clip(m.title, 48))
	opts := lipgloss.NewStyle().Render("[Y] yes    [n] no  (Esc = no)")
	body := ModalBorderStyle.Render(lipgloss.JoinVertical(lipgloss.Center,
		question, "", subject, "", opts))
	return m.center(body)
}

// center places a modal box in the middle of the terminal (best-effort when the
// dimensions aren't known yet).
func (m *completedPrompt) center(box string) string {
	if m.width == 0 || m.height == 0 {
		return lipgloss.NewStyle().MarginTop(2).Render(box)
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
