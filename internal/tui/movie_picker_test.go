package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ani/internal/flixhq"
	tea "github.com/charmbracelet/bubbletea"
)

type movieFixture struct {
	titles      []flixhq.Title
	recent      []flixhq.Title
	recentCalls int
	eps         []flixhq.Episode
	err         error
	lastQuery   string
	series      string
}

func (f *movieFixture) RecentlyAdded(context.Context) ([]flixhq.Title, error) {
	f.recentCalls++
	return f.recent, f.err
}
func (f *movieFixture) Search(ctx context.Context, q string) ([]flixhq.Title, error) {
	f.lastQuery = q
	return f.titles, f.err
}
func (f *movieFixture) Episodes(ctx context.Context, t flixhq.Title) ([]flixhq.Episode, error) {
	f.series = t.URL
	return f.eps, f.err
}

func TestMoviePickerNavigation(t *testing.T) {
	f := &movieFixture{titles: []flixhq.Title{{Name: "Test", Kind: "movie", Year: "2010", URL: "/one"}, {Name: "Test", Kind: "series", Year: "2025", URL: "/two"}}, eps: []flixhq.Episode{{Season: 3, Number: 2, Name: "Other"}, {Season: 1, Number: 2, Name: "Second"}, {Season: 3, Number: 1, Name: "First"}}}
	m := &moviePicker{MovieState: &MovieState{}, catalog: f, width: 80, height: 22}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("사과 Pie")})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if m.stage != movieTitles || f.lastQuery != "사과 Pie" || !strings.Contains(m.View(), "[series] Test (2025)") {
		t.Fatalf("title search: %s", m.View())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.play || cmd == nil || f.series != "" {
		t.Fatal("movie should play immediately without requesting episodes")
	}
	m.play = false
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if f.series != "/two" || m.stage != movieSeasons || m.seasons[0] != 1 || m.seasons[1] != 3 {
		t.Fatalf("seasons: %s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.stage != movieEpisodes || m.episodes()[m.cursor[movieEpisodes]].Name != "Other" || !strings.Contains(m.View(), "S03E02") {
		t.Fatalf("episodes: %s", m.View())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.play || cmd == nil {
		t.Fatal("expected selected episode to play")
	}
	m.play = false
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.stage != movieSeasons || m.cursor[movieSeasons] != 1 {
		t.Fatal("lost season on back")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.stage != movieTitles || m.cursor[movieTitles] != 1 {
		t.Fatal("lost series on back")
	}
}

func TestMovieSearchCancelRetryAndEmpty(t *testing.T) {
	f := &movieFixture{err: errors.New("catalog unavailable")}
	m := &moviePicker{MovieState: &MovieState{initial: true, query: []rune("Test")}, catalog: f}
	first := m.Init()
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(first())
	if m.cancel != nil || m.problem != "" || m.stage != movieSearch {
		t.Fatal("late cancelled response changed picker")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if !strings.Contains(m.View(), "catalog unavailable") {
		t.Fatal("error hidden")
	}
	f.err = nil
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if !strings.Contains(m.View(), "No matching") {
		t.Fatal("missing no-results message")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || len(m.query) != 0 {
		t.Fatal("blank search should make no request")
	}
}

func TestMovieCatalogAccessibleFromLatestUploads(t *testing.T) {
	for _, via := range []string{"Tab", "palette"} {
		t.Run(via, func(t *testing.T) {
			m := newReleasePicker(nil, "", "", "newest", nil, true, nil, nil, nil, 0, false)
			m.provider = "torrent"
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			key := tabMsg()
			if via == "palette" {
				m.Update(keyMsg(':')) // the initial fetch has not even returned yet
				for _, r := range "source movies" {
					m.Update(keyMsg(r))
				}
				if !strings.Contains(m.View(), "Movies & TV") {
					t.Fatal("Movies & TV source not visible on the logged-out landing page")
				}
				key = enterMsg()
			}
			_, cmd := m.Update(key)
			if cmd == nil || *m.result != (Result{MediaSwitch: MediaMovies}) || m.provider != "torrent" {
				t.Fatalf("expected only a media switch, got %+v (provider %q)", *m.result, m.provider)
			}
		})
	}
}
