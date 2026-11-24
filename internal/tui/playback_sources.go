package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ChoosePlaybackSource prompts only after Play when no unique provider page
// has been verified. Returns a provider row index, or -1 to keep browsing.
func ChoosePlaybackSource(subject string, sources []string) (int, error) {
	if len(sources) == 0 {
		return -1, nil
	}
	m := &playbackSources{subject: subject, sources: sources, chosen: -1}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return m.chosen, err
}

type playbackSources struct {
	subject string
	sources []string
	cursor  int // row zero is Back; repeated Enter never silently picks the first page
	chosen  int
	height  int
}

func (*playbackSources) Init() tea.Cmd { return nil }

func (m *playbackSources) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(m.sources), m.cursor+1)
		case "esc", "q", "ctrl+c":
			m.chosen = -1
			return m, tea.Quit
		case "enter":
			m.chosen = m.cursor - 1
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *playbackSources) View() string {
	lines := []string{
		HeaderStyle.Render("Choose a FlixHQ playback source"),
		TitleStyle.Render("Trakt selection: " + m.subject),
		"No verified match. Choose a page only if you recognize it.",
		"This choice is for playback only; it does not change your Trakt selection.",
		"",
	}
	visible := max(2, m.height-7)
	first := max(0, m.cursor-visible+1)
	for i := first; i < min(len(m.sources)+1, first+visible); i++ {
		label := "Back without playing"
		if i != 0 {
			label = m.sources[i-1]
		}
		if i == m.cursor {
			lines = append(lines, SelectedStyle.Render(CursorGlyph+label))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	return strings.Join(append(lines, HelpStyle.Render("↑/↓ select · Enter confirm · Esc back")), "\n")
}
