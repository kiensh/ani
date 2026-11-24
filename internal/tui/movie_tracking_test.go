package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

type trackerStub struct {
	writeErr               error
	id, watchlist, watched int
	episodes               []trakt.Episode
}

func (s *trackerStub) AddWatchlist(_ context.Context, t trakt.Title) error {
	s.id = t.ID
	s.watchlist++
	return s.writeErr
}
func (s *trackerStub) MarkWatched(_ context.Context, t trakt.Title, season, episode int) error {
	if t.Kind != "movie" || season != 0 || episode != 0 {
		panic("only movie IDs may be sent to this method")
	}
	s.id = t.ID
	s.watched++
	return s.writeErr
}
func (s *trackerStub) MarkEpisodeWatched(_ context.Context, e trakt.Episode) error {
	s.episodes = append(s.episodes, e)
	return s.writeErr
}
func movieKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func trackingPicker(s *trackerStub, title trakt.Title) *moviePicker {
	a := &movieAccountStub{trackerStub: s, connected: true}
	m := newMoviePicker("", &movieFixture{}, a, nil, nil)
	m.stage, m.view, m.titles = movieTitles, movieRecent, []trakt.Title{title}
	m.width, m.height = 80, 20
	return m
}

func TestTraktConfirmExactEpisode(t *testing.T) {
	s := &trackerStub{}
	m := trackingPicker(s, trakt.Title{Name: "Dig", Year: 2026, Kind: "series", ID: 4})
	for _, stage := range []movieStage{movieTitles, movieSeasons} {
		m.stage, m.seasons = stage, []int{2}
		m.Update(movieKey("m"))
		if m.track != nil || !strings.Contains(m.problem, "Select an episode") {
			t.Fatal("whole-series or season mark was allowed")
		}
	}
	m.stage = movieEpisodes
	ep := trakt.Episode{Season: 2, Number: 7, Name: "Focus", ID: 907}
	m.bySeason = map[int][]trakt.Episode{2: {ep}}
	_, cmd := m.Update(movieKey("m"))
	view := m.View()
	if cmd != nil || !strings.Contains(view, "S02E07") || !strings.Contains(view, "Trakt #907") || !strings.Contains(view, "Enter confirm") {
		t.Fatalf("expected a local confirmation for the selected episode: %s", view)
	}
	_, write := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, again := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if again != nil {
		t.Fatal("double submit while saving")
	}
	finishMovieLoad(t, m, write)
	if len(s.episodes) != 1 || s.episodes[0] != ep || s.watched != 0 || s.watchlist != 0 || m.track != nil || !strings.Contains(m.View(), "marked watched") {
		t.Fatalf("bad confirmation: %+v %s", s, m.View())
	}
}

func TestTraktWatchlistCancelAndWriteFailure(t *testing.T) {
	s := &trackerStub{}
	m := trackingPicker(s, trakt.Title{Name: "The Gift", Kind: "movie", ID: 4})
	if _, cmd := m.Update(movieKey("w")); cmd != nil {
		t.Fatal("confirming an exact identity should not require a request")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.track != nil || s.watchlist != 0 {
		t.Fatal("canceled confirmation wrote")
	}
	m.Update(movieKey("w"))
	_, write := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, write)
	if s.id != 4 || s.watchlist != 1 || s.watched != 0 {
		t.Fatalf("wrote wrong title or action: %+v", s)
	}
	s.writeErr = errors.New("watchlist limit")
	m.Update(movieKey("w"))
	_, write = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, write)
	if m.notice != "" || m.problem != "watchlist limit" {
		t.Fatalf("write failure presented as success: %s", m.View())
	}
}

func TestTraktIsOptionalAndNeverWritesOnPlay(t *testing.T) {
	a := &movieAccountStub{trackerStub: &trackerStub{}}
	m := newMoviePicker("", &movieFixture{}, a, nil, nil)
	m.stage, m.view, m.titles = movieTitles, movieRecent, []trakt.Title{{Name: "Test", Kind: "movie", ID: 42}}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // skip the welcome offer
	for _, key := range []string{"w", "m"} {
		_, cmd := m.Update(movieKey(key))
		if cmd != nil || m.track != nil || !strings.Contains(m.View(), "Continue without an account") {
			t.Fatalf("%s should offer connection without starting it: %s", key, m.View())
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	}
	for _, picker := range []*moviePicker{m, trackingPicker(a.trackerStub, m.titles[0])} {
		_, cmd := picker.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if !picker.play || cmd == nil || picker.track != nil || a.watched != 0 || a.watchlist != 0 || len(a.episodes) > 0 {
			t.Fatal("opening mpv should not track a title, regardless of account status")
		}
	}
}
