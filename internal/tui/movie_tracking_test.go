package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ani/internal/flixhq"
	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

type trackerStub struct {
	results                                 []trakt.Title
	searchErr, writeErr                     error
	query, kind                             string
	id, season, episode, watchlist, watched int
}

func (s *trackerStub) Search(_ context.Context, kind, q string) ([]trakt.Title, error) {
	s.kind, s.query = kind, q
	return s.results, s.searchErr
}
func (s *trackerStub) AddWatchlist(_ context.Context, t trakt.Title) error {
	s.id = t.ID
	s.watchlist++
	return s.writeErr
}
func (s *trackerStub) MarkWatched(_ context.Context, t trakt.Title, season, episode int) error {
	s.id, s.season, s.episode = t.ID, season, episode
	s.watched++
	return s.writeErr
}
func movieKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestTraktConfirmExactEpisodeAndAvoidRemake(t *testing.T) {
	s := &trackerStub{results: []trakt.Title{{ID: 9, Kind: "movie", Name: "Dig", Year: 2026}, {ID: 3, Kind: "series", Name: "Dig", Year: 2015}, {ID: 4, Kind: "series", Name: "Dig", Year: 2026}}}
	m := trackingPicker(s, flixhq.Title{Name: "Dig", Year: "2026", Kind: "series"})
	m.Update(movieKey("m"))
	if m.track != nil || !strings.Contains(m.problem, "Select an episode") {
		t.Fatal("whole-series mark was allowed")
	}
	m.stage = movieEpisodes
	m.seasons = []int{2}
	m.bySeason = map[int][]flixhq.Episode{2: {{Season: 2, Number: 7, Name: "Focus"}}}
	_, find := m.Update(movieKey("m"))
	m.Update(find())
	view := m.View()
	if s.kind != "series" || s.query != "Dig" || s.watched != 0 || len(m.track.matches) != 2 || m.track.matches[0].ID != 4 || !strings.Contains(view, "S02E07") || !strings.Contains(view, "Enter confirm") {
		t.Fatalf("unexpected match dialog: %s", view)
	}
	_, write := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, again := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if again != nil {
		t.Fatal("double submit while saving")
	}
	m.Update(write())
	if s.id != 4 || s.season != 2 || s.episode != 7 || s.watched != 1 || s.watchlist != 0 || m.track != nil || !strings.Contains(m.View(), "marked watched") {
		t.Fatalf("bad confirmation: %+v %s", s, m.View())
	}
}

func TestTraktWatchlistCancelFailureAndManualChoice(t *testing.T) {
	s := &trackerStub{results: []trakt.Title{{ID: 3, Kind: "movie", Name: "The Gift", Year: 2000}, {ID: 4, Kind: "movie", Name: "The Gift", Year: 2015}}}
	m := trackingPicker(s, flixhq.Title{Name: "The Gift", Kind: "movie"})
	_, find := m.Update(movieKey("w"))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(find())
	if m.track != nil || s.watchlist != 0 {
		t.Fatal("canceled search reopened or wrote")
	}
	_, find = m.Update(movieKey("w"))
	m.Update(find())
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.watchlist != 0 {
		t.Fatal("cancelled confirmation wrote")
	}
	_, find = m.Update(movieKey("w"))
	m.Update(find())
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, write := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(write())
	if s.id != 4 || s.watchlist != 1 || s.watched != 0 {
		t.Fatalf("wrote wrong title or action: %+v", s)
	}
	s.writeErr = errors.New("watchlist limit")
	_, find = m.Update(movieKey("w"))
	m.Update(find())
	_, write = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(write())
	if m.notice != "" || m.problem != "watchlist limit" {
		t.Fatalf("write failure presented as success: %s", m.View())
	}
	s.results = nil
	_, find = m.Update(movieKey("w"))
	m.Update(find())
	if m.track != nil || !strings.Contains(m.problem, "Nothing was saved") {
		t.Fatal("empty match set not handled")
	}
}

func trackingPicker(s *trackerStub, title flixhq.Title) *moviePicker {
	a := &movieAccountStub{trackerStub: s, connected: true}
	m := newMoviePicker("", nil, a, nil, nil)
	m.stage, m.titles = movieTitles, []flixhq.Title{title}
	m.width, m.height = 80, 20
	return m
}

func TestTraktIsOptionalAndNeverWritesOnPlay(t *testing.T) {
	a := &movieAccountStub{trackerStub: &trackerStub{}}
	m := newMoviePicker("", nil, a, nil, nil)
	m.stage, m.titles = movieTitles, []flixhq.Title{{Name: "Test", Kind: "movie"}}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // skip the welcome offer
	for _, key := range []string{"w", "m"} {
		_, cmd := m.Update(movieKey(key))
		if cmd != nil || m.track != nil || !strings.Contains(m.View(), "Continue without an account") {
			t.Fatalf("%s should offer connection without starting it: %s", key, m.View())
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.play || cmd == nil || m.track != nil || a.query != "" || a.watched != 0 || a.watchlist != 0 {
		t.Fatal("opening mpv should not track a title")
	}
	connected := trackingPicker(a.trackerStub, m.titles[0])
	_, cmd = connected.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !connected.play || connected.track != nil || a.query != "" || a.watched != 0 || a.watchlist != 0 {
		t.Fatal("connecting an account must not enable automatic tracking")
	}
}
