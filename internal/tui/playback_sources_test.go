package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPlaybackSourceChoiceIsExplicitAndCancelable(t *testing.T) {
	if i, err := ChoosePlaybackSource("Movie", nil); err != nil || i != -1 {
		t.Fatalf("empty sources: %d, %v", i, err)
	}
	for _, tc := range []struct {
		keys []tea.KeyType
		want int
	}{
		{[]tea.KeyType{tea.KeyEnter}, -1},
		{[]tea.KeyType{tea.KeyDown, tea.KeyEsc}, -1},
		{[]tea.KeyType{tea.KeyDown, tea.KeyCtrlC}, -1},
		{[]tea.KeyType{tea.KeyDown, tea.KeyDown, tea.KeyEnter}, 1},
	} {
		m := &playbackSources{subject: "Movie (movie, 2001)", sources: []string{"first", "second"}, chosen: -1, height: 24}
		view := m.View()
		for _, piece := range []string{"FlixHQ playback source", "Trakt selection: Movie (movie, 2001)", "playback only", "Back without playing", "first", "second"} {
			if !strings.Contains(view, piece) {
				t.Fatalf("manual choice missing %q: %s", piece, view)
			}
		}
		var cmd tea.Cmd
		for _, key := range tc.keys {
			_, cmd = m.Update(tea.KeyMsg{Type: key})
		}
		if cmd == nil || m.chosen != tc.want {
			t.Fatalf("%v selected %d, want %d", tc.keys, m.chosen, tc.want)
		}
	}
}
