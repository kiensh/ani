package tui

import (
	"errors"
	"strings"
	"testing"

	"ani/internal/flixhq"
	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

func TestMovieSearchActionsAndWatchlistReadback(t *testing.T) {
	for _, via := range []string{"space", "palette"} {
		t.Run(via, func(t *testing.T) {
			match := trakt.Title{Name: "The Gift", Kind: "movie", Year: 2015, ID: 42}
			a := &movieAccountStub{trackerStub: &trackerStub{results: []trakt.Title{match}}, connected: true}
			f := &movieFixture{titles: []flixhq.Title{{Name: "The Gift", Kind: "movie", Year: "2015", URL: "/gift"}}}
			m := newMoviePicker("", f, a, nil, nil)
			m.width, m.height = 90, 26
			finishMovieLoad(t, m, m.Init()) // cache an empty My Watchlist
			m.Update(movieKey("/"))
			m.Update(movieKey("The"))
			m.Update(tea.KeyMsg{Type: tea.KeySpace})
			m.Update(movieKey("Gift"))
			if string(m.query) != "The Gift" || m.palette.Active() {
				t.Fatal("Space in a query must type a space")
			}
			_, search := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			finishMovieLoad(t, m, search)
			if !strings.Contains(m.View(), "Space actions") {
				t.Fatal("search results don't advertise their actions")
			}
			if via == "space" {
				m.Update(tea.KeyMsg{Type: tea.KeySpace})
			} else {
				m.Update(movieKey(":"))
			}
			for _, label := range []string{"Selected: The Gift (2015)", "Add to My Watchlist", "Mark movie as watched"} {
				if !strings.Contains(m.View(), label) {
					t.Fatalf("missing %q from %s menu: %s", label, via, m.View())
				}
			}
			if via == "palette" {
				m.Update(movieKey("add to my watchlist"))
			}
			_, find := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			finishMovieLoad(t, m, find)
			if a.watchlist != 0 || !strings.Contains(m.View(), "Enter confirm") {
				t.Fatal("opening action should match, not write")
			}
			_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			finishMovieLoad(t, m, save)
			if a.watchlist != 1 || a.id != 42 || a.watched != 0 || m.view != movieResults || a.shelfCalls != 1 {
				t.Fatalf("unexpected write or premature refresh: %+v", a)
			}
			a.shelf = []trakt.Title{match} // what the remote account now returns
			_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			finishMovieLoad(t, m, back)
			if a.shelfCalls != 2 || m.view != movieWatchlist || !strings.Contains(m.View(), "The Gift (2015)") {
				t.Fatal("returning from search restored a stale watchlist")
			}
		})
	}
}

func TestSeriesActionsAreEpisodeScoped(t *testing.T) {
	s := &trackerStub{results: []trakt.Title{{Name: "Series", Kind: "series", ID: 7}}}
	m := trackingPicker(s, flixhq.Title{Name: "Series", Kind: "series"})
	m.view = movieResults
	m.Update(movieKey(":"))
	if !strings.Contains(m.View(), "Add series to My Watchlist") || strings.Contains(m.View(), "as watched") {
		t.Fatal("whole series must not offer a history write")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.stage = movieEpisodes
	m.seasons = []int{3}
	m.bySeason = map[int][]flixhq.Episode{3: {{Season: 3, Number: 2, Name: "Chosen"}}}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !strings.Contains(m.View(), "S03E02") {
		t.Fatal("action target is unclear")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, find := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, find)
	if s.watched != 0 {
		t.Fatal("history changed before confirmation")
	}
	_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, save)
	if s.watched != 1 || s.season != 3 || s.episode != 2 || s.watchlist != 0 {
		t.Fatalf("wrong history write: %+v", s)
	}
}

func TestMovieActionsUnavailableOrCancelled(t *testing.T) {
	s := &trackerStub{results: []trakt.Title{{Name: "Film", Kind: "movie", ID: 1}}, writeErr: errors.New("unavailable")}
	m := trackingPicker(s, flixhq.Title{Name: "Film", Kind: "movie"})
	m.view = movieResults
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.query != "" || s.watchlist != 0 || m.play {
		t.Fatal("browsing the menu had side effects")
	}
	_, find := m.Update(movieKey("w"))
	finishMovieLoad(t, m, find)
	_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, save)
	if m.home.stale || m.problem != "unavailable" {
		t.Fatal("failed write dirtied the watchlist or hid the error")
	}
	m.connected = false
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.dialog.mode != movieAccountOffer || m.track != nil {
		t.Fatal("signed-out action must offer in-TUI login before contacting Trakt")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.titles = nil
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.palette.Active() || len(m.titleActions()) != 0 {
		t.Fatal("actions offered without a selected row")
	}
}
