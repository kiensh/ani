package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type movieSubtitleInput struct {
	path    []rune
	problem string
}

func (m *moviePicker) canAttachSubtitle() bool {
	if m.cancel != nil || m.dialog.mode != movieAccountClosed || m.track != nil {
		return false
	}
	switch m.stage {
	case movieTitles:
		t, ok := m.focusedMovie()
		return ok && t.Kind == "movie"
	case movieEpisodes:
		return m.rowCount() > 0
	}
	return false
}

func (m *moviePicker) openSubtitleInput() {
	if !m.canAttachSubtitle() {
		m.problem = "Select a movie or a specific episode first."
		return
	}
	m.problem, m.notice = "", ""
	m.subPrompt = &movieSubtitleInput{}
}

func (m *moviePicker) subtitleKey(msg tea.KeyMsg) tea.Cmd {
	p := m.subPrompt
	switch msg.Type {
	case tea.KeyCtrlC:
		m.subPrompt = nil
		return m.leave("")
	case tea.KeyEsc:
		m.subPrompt = nil
		return nil
	case tea.KeyCtrlU:
		p.path = nil
	case tea.KeyBackspace:
		if len(p.path) > 0 {
			p.path = p.path[:len(p.path)-1]
		}
	case tea.KeyEnter:
		path, err := localSubtitlePath(string(p.path))
		if err != nil {
			p.problem = err.Error()
			return nil
		}
		m.subPrompt = nil
		m.subtitleFile = path
		m.play = true
		return tea.Quit
	case tea.KeySpace:
		if len(p.path) < 4096 {
			p.path = append(p.path, ' ')
		}
	case tea.KeyRunes:
		if !msg.Alt {
			for _, r := range msg.Runes {
				if unicode.IsPrint(r) && len(p.path) < 4096 {
					p.path = append(p.path, r)
				}
			}
		}
	}
	p.problem = ""
	return nil
}

// Treat pasted input only as a file path, never a command or a remote URL. A
// sidecar with spaces needs no shell escaping. Also accept wrapping quotes and ~/.
func localSubtitlePath(input string) (string, error) {
	p := strings.TrimSpace(input)
	if len(p) >= 2 && (p[0] == '\'' || p[0] == '"') && p[len(p)-1] == p[0] {
		p = p[1 : len(p)-1]
	}
	if p == "" || strings.Contains(p, "://") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return "", errors.New("Enter a local subtitle file path, not a URL.")
	}
	if strings.HasPrefix(p, "~/") || (os.PathSeparator == '\\' && strings.HasPrefix(p, `~\`)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("Cannot locate your home directory.")
		}
		p = filepath.Join(home, p[2:])
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".srt", ".vtt", ".ass", ".ssa":
	default:
		return "", errors.New("Choose an .srt, .vtt, .ass, or .ssa subtitle file.")
	}
	if os.PathSeparator == '/' && strings.Contains(p, `\ `) {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			p = strings.ReplaceAll(p, `\ `, " ")
		}
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", errors.New("Invalid subtitle file path.")
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("Subtitle file not found or not a regular file.")
	}
	f, err := os.Open(p)
	if err != nil {
		return "", errors.New("Subtitle file is not readable.")
	}
	_ = f.Close()
	return p, nil
}

func (m *moviePicker) subtitleView() string {
	p := m.subPrompt
	lines := []string{TitleStyle.Render("Play with subtitle file"), "Paste a local .srt, .vtt, .ass, or .ssa path for this playback only.", "File: " + string(p.path) + "▏"}
	if p.problem != "" {
		lines = append(lines, ErrorStyle.Render(p.problem))
	}
	return strings.Join(append(lines, HelpStyle.Render("Enter play with file · Ctrl+U clear · Esc cancel")), "\n")
}
