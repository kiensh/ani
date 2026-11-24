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
	m := &moviePicker{tracker: s, stage: movieTitles, width: 80, height: 20, titles: []flixhq.Title{{Name: "Dig", Year: "2026", Kind: "series"}}}
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
	m := &moviePicker{tracker: s, stage: movieTitles, titles: []flixhq.Title{{Name: "The Gift", Kind: "movie"}}, width: 80, height: 20}
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

func TestTraktIsOptionalAndNeverWritesOnPlay(t *testing.T) {
	m := &moviePicker{stage: movieTitles, titles: []flixhq.Title{{Name: "Test", Kind: "movie"}}}
	_, cmd := m.Update(movieKey("w"))
	if cmd != nil || !strings.Contains(m.problem, "ani trakt login") {
		t.Fatal("missing offline login hint")
	}
	s := &trackerStub{}
	m.tracker = s
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.play || cmd == nil || m.track != nil || s.query != "" || s.watched != 0 {
		t.Fatal("opening mpv should not track a title")
	}
}
