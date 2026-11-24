package tui

import (
	"errors"
	"strings"
	"testing"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

func TestMovieSearchActionsAndAccountShelfReadback(t *testing.T) {
	// Even the name, kind and year together are not an identity.
	first := trakt.Title{Name: "The Gift", Kind: "movie", Year: 2015, ID: 40}
	second := first
	second.ID = 42
	for _, via := range []string{"space", "palette"} {
		for _, home := range []movieView{movieWatchlist, movieWatched} {
			t.Run(via+"/"+home.label(), func(t *testing.T) {
				a := &movieAccountStub{trackerStub: &trackerStub{}, connected: true}
				f := &movieFixture{titles: []trakt.Title{first, second}}
				m := newMoviePicker("", f, a, nil, nil)
				m.width, m.height = 90, 26
				finishMovieLoad(t, m, m.showView(home)) // cache an empty account shelf
				m.Update(movieKey("/"))
				m.Update(movieKey("The"))
				m.Update(tea.KeyMsg{Type: tea.KeySpace})
				m.Update(movieKey("Gift"))
				if string(m.query) != "The Gift" || m.palette.Active() {
					t.Fatal("Space in a query must type a space")
				}
				_, search := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				finishMovieLoad(t, m, search)
				m.Update(tea.KeyMsg{Type: tea.KeyDown})
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
				if home == movieWatchlist {
					m.Update(movieKey("add to my watchlist"))
				} else {
					m.Update(movieKey("mark movie as watched"))
				}
				_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				if cmd != nil || a.watchlist != 0 || a.watched != 0 || !strings.Contains(m.View(), "Trakt #42") {
					t.Fatal("opening action should just confirm the highlighted Trakt ID")
				}
				m.Update(tea.KeyMsg{Type: tea.KeyUp}) // not another selection list
				_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				finishMovieLoad(t, m, save)
				if a.id != second.ID || m.view != movieResults || f.searchCalls != 1 || a.shelfCalls+a.filmsCalls != 1 {
					t.Fatalf("unexpected identity, extra search or premature refresh: %+v", a)
				}
				if home == movieWatchlist {
					if a.watchlist != 1 || a.watched != 0 {
						t.Fatal("wrong watchlist write")
					}
					a.shelf = []trakt.Title{second}
				} else {
					if a.watched != 1 || a.watchlist != 0 {
						t.Fatal("wrong history write")
					}
					a.films = []trakt.Title{second}
				}
				_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				finishMovieLoad(t, m, back)
				if a.shelfCalls+a.filmsCalls != 2 || m.view != home || len(m.titles) != 1 || m.titles[0].ID != 42 {
					t.Fatal("returning from search restored a stale shelf")
				}
			})
		}
	}
}

func TestSeriesActionsAreEpisodeScoped(t *testing.T) {
	s := &trackerStub{}
	m := trackingPicker(s, trakt.Title{Name: "Series", Kind: "series", ID: 7})
	m.view = movieResults
	m.Update(movieKey(":"))
	if !strings.Contains(m.View(), "Add series to My Watchlist") || strings.Contains(m.View(), "as watched") {
		t.Fatal("whole series must not offer a history write")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.stage = movieEpisodes
	m.seasons = []int{3}
	ep := trakt.Episode{Season: 3, Number: 2, Name: "Chosen", ID: 3002}
	m.bySeason = map[int][]trakt.Episode{3: {ep}}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !strings.Contains(m.View(), "S03E02") {
		t.Fatal("action target is unclear")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || len(s.episodes) > 0 || s.watched != 0 {
		t.Fatal("choosing an episode action issued a premature request")
	}
	_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, save)
	if len(s.episodes) != 1 || s.episodes[0] != ep || s.watchlist != 0 || s.watched != 0 {
		t.Fatalf("wrong history write: %+v", s)
	}
}

func TestMovieActionsUnavailableOrCancelled(t *testing.T) {
	s := &trackerStub{writeErr: errors.New("unavailable")}
	m := trackingPicker(s, trakt.Title{Name: "Film", Kind: "movie", ID: 1})
	m.view = movieResults
	m.home = movieListing{view: movieWatchlist, titles: []trakt.Title{}}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.watchlist != 0 || m.play {
		t.Fatal("browsing the menu had side effects")
	}
	m.Update(movieKey("w"))
	_, save := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, save)
	if m.home.titles == nil || m.problem != "unavailable" {
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
