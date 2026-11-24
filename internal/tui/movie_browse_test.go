package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

func finishMovieLoad(t *testing.T, m *moviePicker, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a load command")
	}
	m.Update(cmd())
}

func TestMovieLandingMyWatchlist(t *testing.T) {
	for _, mode := range []string{"connected", "skip", "authorize"} {
		t.Run(mode, func(t *testing.T) {
			a := &movieAccountStub{connected: mode == "connected", shelf: []trakt.Title{{Name: "Saved film", Kind: "movie", ID: 10}}}
			f := &movieFixture{recent: []trakt.Title{{Name: "A new arrival", Kind: "movie"}}}
			m := newMoviePicker("", f, a, nil, nil)
			m.width, m.height = 100, 24
			cmd := m.Init()
			if mode != "connected" {
				if cmd != nil || m.dialog.mode != movieAccountOffer || f.recentCalls != 0 || a.shelfCalls != 0 {
					t.Fatal("optional welcome must precede the first request")
				}
				if mode == "skip" {
					_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				} else {
					a.login = func(context.Context, func(string, string)) error { a.connected = true; return nil }
					_, login := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
					events := runMovieLogin(t, login)
					_, cmd = m.Update(nextMovieEvent(t, events))
					m.Update(nextMovieEvent(t, events)) // canceled prompt listener
				}
			}
			if mode == "skip" {
				if cmd != nil || a.shelfCalls != 0 || !strings.Contains(m.View(), "Connect Trakt with L") {
					t.Fatal("signed-out landing must stay on watchlist without fetching it")
				}
			} else {
				finishMovieLoad(t, m, cmd)
				if a.shelfCalls != 1 || !strings.Contains(m.View(), "Saved film") {
					t.Fatal("missing saved watchlist")
				}
			}
			if m.stage != movieTitles || m.view != movieWatchlist || f.recentCalls != 0 || f.lastQuery != "" || !strings.Contains(m.View(), "My Watchlist") {
				t.Fatalf("not a watchlist-first landing: %s", m.View())
			}
			if cmd := m.Init(); cmd != nil {
				t.Fatal("resuming picker should keep the loaded shelf")
			}
		})
	}
}

func TestMovieSearchBackToHome(t *testing.T) {
	a := &movieAccountStub{connected: true, shelf: []trakt.Title{{Name: "One", Kind: "movie", ID: 1}, {Name: "Two", Kind: "movie", ID: 2}}}
	f := &movieFixture{titles: []trakt.Title{{Name: "1917", Kind: "movie", ID: 381917}}}
	m := newMoviePicker("1917", f, a, nil, nil)
	finishMovieLoad(t, m, m.Init())
	if m.view != movieResults || a.shelfCalls != 0 || f.recentCalls != 0 || f.lastQuery != "1917" {
		t.Fatal("explicit query must bypass both landing views")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	finishMovieLoad(t, m, cmd)
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	for _, submit := range []bool{false, true} {
		m.Update(movieKey("/"))
		m.Update(movieKey("different"))
		if submit {
			_, search := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			finishMovieLoad(t, m, search)
		}
		_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if back != nil || m.view != movieWatchlist || m.stage != movieTitles || m.cursor[movieTitles] != 1 || f.recentCalls != 0 || a.shelfCalls != 1 {
			t.Fatal("search must return to the same home row without a reload")
		}
	}
}

func TestWatchlistPlayAndEpisodesUseExactSelectedIdentity(t *testing.T) {
	first := trakt.Title{Name: "The Gift", Kind: "movie", Year: 2000, ID: 10}
	second := first
	second.ID, second.TMDB, second.IMDb, second.Overview = 20, 9020, "tt0090200", "Selected edition"
	series := second // a Trakt ID is unique within a kind, not across kinds
	series.Kind = "series"
	a := &movieAccountStub{trackerStub: &trackerStub{}, connected: true, shelf: []trakt.Title{first, second, series}}
	ep := trakt.Episode{Season: 1, Number: 1, Name: "Pilot", ID: 2021}
	f := &movieFixture{eps: []trakt.Episode{ep}}
	state := &MovieState{}
	m := newMoviePicker("", f, a, nil, state)
	m.width, m.height = 100, 24
	finishMovieLoad(t, m, m.Init())
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, play := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.play || play == nil || m.view != movieWatchlist || m.titles[m.cursor[movieTitles]] != second || f.searchCalls != 0 || f.seriesID != 0 {
		t.Fatal("Enter from watchlist should play the selected movie without another lookup")
	}
	m.play = false
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, load := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, load)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.play || m.stage != movieEpisodes || m.episodes()[0] != ep || f.seriesID != 20 || f.searchCalls != 0 {
		t.Fatal("watchlist show did not open its own episodes")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newMoviePicker("", f, a, nil, state)
	if m.Init() != nil || m.stage != movieEpisodes || m.cursor[movieTitles] != 2 || m.episodes()[0] != ep || a.shelfCalls != 1 {
		t.Fatal("source round trip lost the watchlist's selected episode")
	}
	// Trakt can remove a watched series from the shelf. Do not replace the
	// selected show's parent with someone else's while browsing its episodes.
	m.Update(movieKey("m"))
	_, write := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a.shelf = []trakt.Title{first}
	_, refresh := m.Update(write())
	if refresh != nil || m.titles[m.cursor[movieTitles]].Kind != "series" || m.episodes()[0].ID != ep.ID {
		t.Fatal("episode history write lost its parent row")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	_, refresh = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	finishMovieLoad(t, m, refresh)
	if len(m.titles) != 1 || m.titles[0].ID != first.ID || m.cursor[movieTitles] != 0 || a.shelfCalls != 2 || f.searchCalls != 0 {
		t.Fatal("back from episodes did not refresh account membership")
	}
}

func TestMovieBrowseCycleFailureRecoveryAndCanceledRequest(t *testing.T) {
	a := &movieAccountStub{connected: true, shelfErr: errors.New("watchlist unavailable"), filmsErr: errors.New("watched movies unavailable")}
	f := &movieFixture{err: errors.New("catalog unavailable")}
	m := newMoviePicker("", f, a, nil, nil)
	m.width, m.height = 90, 24
	finishMovieLoad(t, m, m.Init())
	if m.view != movieWatchlist || f.recentCalls != 0 || !strings.Contains(m.View(), "watchlist unavailable") || !strings.Contains(m.View(), "Watched Movies") {
		t.Fatal("failed watchlist should offer the other two shelves")
	}
	_, recent := m.Update(movieKey("v"))
	finishMovieLoad(t, m, recent)
	if m.view != movieRecent || !strings.Contains(m.View(), "catalog unavailable") || !strings.Contains(m.View(), "r to retry") || !strings.Contains(m.View(), "Trakt Recent Releases") {
		t.Fatal("missing recent releases error and retry")
	}
	_, slow := m.Update(movieKey("r"))
	_, watched := m.Update(movieKey("v")) // switch while the public shelf is loading
	finishMovieLoad(t, m, slow)
	finishMovieLoad(t, m, watched)
	if m.view != movieWatched || a.filmsCalls != 1 || !strings.Contains(m.View(), "watched movies unavailable") {
		t.Fatal("late response replaced the active view or hid its error")
	}
	a.filmsErr = nil
	_, retry := m.Update(movieKey("r"))
	finishMovieLoad(t, m, retry)
	if m.problem != "" || !strings.Contains(m.View(), "No watched movies") {
		t.Fatal("empty watched shelf not distinguished from a failed request")
	}
	a.shelfErr = nil
	_, list := m.Update(movieKey("v"))
	finishMovieLoad(t, m, list)
	if m.view != movieWatchlist || !strings.Contains(m.View(), "No watchlist titles") {
		t.Fatal("view cycle did not end on My Watchlist")
	}
	for _, key := range []tea.KeyMsg{movieKey("j"), movieKey("w"), movieKey("m"), {Type: tea.KeyEnter}} {
		if _, cmd := m.Update(key); cmd != nil || m.play || m.cursor[movieTitles] < 0 {
			t.Fatal("empty list allowed row actions")
		}
	}
	f.err = nil
	finishMovieLoad(t, m, chooseMovieCommand(t, m, "view recent releases"))
	m.Update(movieKey("/"))
	if m.stage != movieSearch || !m.editing {
		t.Fatal("search inaccessible from empty shelf")
	}
}

func TestMovieAccountShelvesNeedAuthorizationAndClearOnLogout(t *testing.T) {
	for _, view := range []movieView{movieWatchlist, movieWatched} {
		t.Run(view.label(), func(t *testing.T) {
			rows := []trakt.Title{{Name: "Private title", Kind: "movie", ID: 1}}
			a := &movieAccountStub{shelf: rows, films: rows}
			f := &movieFixture{}
			m := newMoviePicker("", f, a, nil, nil)
			m.width, m.height = 90, 24
			_, list := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if view == movieWatched {
				list = chooseMovieCommand(t, m, "view watched movies")
			}
			if m.view != view || list != nil || a.shelfCalls+a.filmsCalls+f.recentCalls != 0 || !strings.Contains(m.View(), "Connect Trakt with L") {
				t.Fatal("signed-out account shelf should be visible without contacting Trakt")
			}
			a.login = func(context.Context, func(string, string)) error { a.connected = true; return nil }
			_, login := m.Update(movieKey("L"))
			events := runMovieLogin(t, login)
			_, list = m.Update(nextMovieEvent(t, events))
			m.Update(nextMovieEvent(t, events))
			finishMovieLoad(t, m, list)
			if a.shelfCalls+a.filmsCalls != 1 || !strings.Contains(m.View(), "Private title") || m.view != view {
				t.Fatal("connecting from an account shelf didn't load its rows")
			}
			chooseMovieCommand(t, m, "disconnect")
			_, logout := m.Update(movieKey("y"))
			finishMovieLoad(t, m, logout)
			if m.connected || len(m.titles) != 0 || strings.Contains(m.View(), "Private title") || a.shelfCalls+a.filmsCalls != 1 {
				t.Fatal("disconnect leaked cached rows or requested account data")
			}
			finishMovieLoad(t, m, chooseMovieCommand(t, m, "view recent releases"))
			if m.view != movieRecent || a.shelfCalls+a.filmsCalls != 1 {
				t.Fatal("signed-out shelves had no way to open Trakt Recent Releases")
			}
			_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if back != nil || m.view != movieWatchlist || f.recentCalls != 1 {
				t.Fatal("Esc from Trakt Recent Releases should return to My Watchlist")
			}
			if _, quit := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); quit == nil {
				t.Fatal("Esc at home should exit")
			}
		})
	}
}

func TestMovieWatchedShelfDirectPlaybackRefreshAndSourceSwitch(t *testing.T) {
	// Larger than the recent history page, with distinct rows sharing a label.
	var films []trakt.Title
	for id := 1; id <= 32; id++ {
		films = append(films, trakt.Title{Name: "Same title", Kind: "movie", Year: 2020, ID: id})
	}
	state := &MovieState{}
	a := &movieAccountStub{trackerStub: &trackerStub{}, connected: true, films: films}
	f := &movieFixture{}
	m := newMoviePicker("", f, a, nil, state)
	finishMovieLoad(t, m, chooseMovieCommand(t, m, "view watched movies"))
	if len(m.titles) != 32 || a.filmsCalls != 1 || a.shelfCalls != 0 || f.searchCalls+f.recentCalls != 0 {
		t.Fatal("Watched Movies must use the full watched film shelf")
	}
	for range 30 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	_, play := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if play == nil || !m.play || m.titles[m.cursor[movieTitles]].ID != 31 || a.watched != 0 || f.searchCalls != 0 {
		t.Fatal("watched title didn't play directly")
	}
	m.play = false
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newMoviePicker("", f, a, nil, state)
	if m.Init() != nil || m.view != movieWatched || m.cursor[movieTitles] != 30 {
		t.Fatal("switching sources lost the watched shelf or its selected row")
	}
	m.Update(movieKey("w"))
	_, write := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a.films = []trakt.Title{films[30], films[3]} // changed rank on Trakt
	_, refresh := m.Update(write())
	finishMovieLoad(t, m, refresh)
	if a.filmsCalls != 2 || len(m.titles) != 2 || m.cursor[movieTitles] != 0 || a.id != 31 || a.watchlist != 1 || f.searchCalls != 0 {
		t.Fatal("write didn't refresh active shelf and keep the exact selected Trakt row")
	}
	m.Update(movieKey("/")) // private shelf is now a saved search return address
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	a.connected = false
	m = newMoviePicker("", f, a, nil, state)
	_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if back != nil || m.view != movieWatched || len(m.titles) != 0 || a.filmsCalls != 2 {
		t.Fatal("source round trip after logout restored private watched rows")
	}
}
