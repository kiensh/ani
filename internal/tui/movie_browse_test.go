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
			f := &movieFixture{recent: []flixhq.Title{{Name: "A new arrival", Kind: "movie"}}}
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
	f := &movieFixture{titles: []flixhq.Title{{Name: "1917", Kind: "movie"}}}
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

func TestMovieWatchlistMatchRequiresProviderChoice(t *testing.T) {
	a := &movieAccountStub{connected: true, shelf: []trakt.Title{{Name: "Another film", Kind: "movie", ID: 10}, {Name: "The Gift", Kind: "movie", Year: 2000, ID: 20}}}
	f := &movieFixture{titles: []flixhq.Title{{Name: "The Gift", Kind: "series", URL: "/show"}, {Name: "The Gift", Kind: "movie", Year: "2015", URL: "/remake"}, {Name: "The Gift", Kind: "movie", Year: "2000", URL: "/original"}}}
	state := &MovieState{}
	m := newMoviePicker("", f, a, nil, state)
	m.width, m.height = 100, 24
	finishMovieLoad(t, m, m.Init())
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.view != movieWatchlist || a.shelfCalls != 1 || f.lastQuery != "" || f.series != "" || m.titles[1].URL != "" || !strings.Contains(m.View(), "[movie] The Gift (2000)") {
		t.Fatalf("watchlist load must use ranked Trakt rows, not provider searches: %s", m.View())
	}
	_, find := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, find)
	if m.play || m.stage != movieTitles || m.view != movieResults || f.lastQuery != "The Gift" || len(m.titles) != 2 || !strings.Contains(m.View(), "FlixHQ match (2000)") {
		t.Fatalf("provider match not presented for confirmation: %s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newMoviePicker("", f, a, nil, state)
	if m.Init() != nil || m.cursor[movieTitles] != 1 || m.titles[1].URL != "/original" {
		t.Fatal("source round trip lost explicit provider selection")
	}
	_, play := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.play || play == nil || m.titles[m.cursor[movieTitles]].URL != "/original" {
		t.Fatal("did not honor the selected remake")
	}
	m.play = false
	_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if back != nil || m.view != movieWatchlist || m.cursor[movieTitles] != 1 || m.titles[1].URL != "" || a.shelfCalls != 1 {
		t.Fatal("did not restore the watchlist and cursor after a provider match")
	}
	// Even a *single* same-kind, same-year result is not an identity match.
	f.titles = f.titles[2:]
	_, find = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finishMovieLoad(t, m, find)
	if m.play || len(m.titles) != 1 || m.stage != movieTitles {
		t.Fatal("lone provider result bypassed confirmation")
	}
}

func TestMovieBrowseFailureRecoveryAndCanceledRequest(t *testing.T) {
	a := &movieAccountStub{connected: true, shelfErr: errors.New("watchlist unavailable")}
	f := &movieFixture{err: errors.New("catalog unavailable")}
	m := newMoviePicker("", f, a, nil, nil)
	m.width, m.height = 90, 24
	finishMovieLoad(t, m, m.Init())
	if m.view != movieWatchlist || f.recentCalls != 0 || !strings.Contains(m.View(), "watchlist unavailable") {
		t.Fatal("failed watchlist should not redirect to Recently Added")
	}
	_, recent := m.Update(movieKey("v"))
	finishMovieLoad(t, m, recent)
	if !strings.Contains(m.View(), "catalog unavailable") || !strings.Contains(m.View(), "r to retry") {
		t.Fatal("missing recently added fallback and retry")
	}
	_, slow := m.Update(movieKey("r"))
	_, list := m.Update(movieKey("v")) // don't wait for FlixHQ to return
	finishMovieLoad(t, m, slow)
	finishMovieLoad(t, m, list)
	if m.view != movieWatchlist || !strings.Contains(m.View(), "watchlist unavailable") {
		t.Fatal("late response replaced the active view or hid its error")
	}
	a.shelfErr = nil
	_, retry := m.Update(movieKey("r"))
	finishMovieLoad(t, m, retry)
	if m.problem != "" || !strings.Contains(m.View(), "No watchlist titles") {
		t.Fatal("empty watchlist not distinguished from a failed request")
	}
	for _, key := range []tea.KeyMsg{movieKey("j"), movieKey("w"), movieKey("m"), {Type: tea.KeyEnter}} {
		if _, cmd := m.Update(key); cmd != nil || m.play || m.cursor[movieTitles] < 0 {
			t.Fatal("empty list allowed row actions")
		}
	}
	f.err = nil
	finishMovieLoad(t, m, chooseMovieCommand(t, m, "view recently added"))
	m.Update(movieKey("/"))
	if m.stage != movieSearch || !m.editing {
		t.Fatal("search inaccessible from empty shelf")
	}
}

func TestMovieWatchlistNeedsAuthorizationAndClearsOnLogout(t *testing.T) {
	a := &movieAccountStub{shelf: []trakt.Title{{Name: "Private title", Kind: "movie", ID: 1}}}
	f := &movieFixture{}
	m := newMoviePicker("", f, a, nil, nil)
	m.width, m.height = 90, 24
	_, list := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.view != movieWatchlist || f.recentCalls != 0 || list != nil || a.shelfCalls != 0 || !strings.Contains(m.View(), "Connect Trakt with L") || !strings.Contains(m.View(), "v for Recently Added") {
		t.Fatal("signed-out watchlist asked Trakt or trapped the user")
	}
	a.login = func(context.Context, func(string, string)) error { a.connected = true; return nil }
	_, login := m.Update(movieKey("L"))
	events := runMovieLogin(t, login)
	_, list = m.Update(nextMovieEvent(t, events))
	// Approval should load the watchlist directly, without switching views.
	m.Update(nextMovieEvent(t, events))
	finishMovieLoad(t, m, list)
	if a.shelfCalls != 1 || !strings.Contains(m.View(), "Private title") {
		t.Fatal("connecting from watchlist didn't load the account's rows")
	}
	chooseMovieCommand(t, m, "disconnect")
	_, logout := m.Update(movieKey("y"))
	finishMovieLoad(t, m, logout)
	if m.connected || len(m.titles) != 0 || strings.Contains(m.View(), "Private title") || a.shelfCalls != 1 {
		t.Fatal("disconnect leaked cached rows or requested account data")
	}
	_, recent := m.Update(movieKey("v"))
	finishMovieLoad(t, m, recent)
	if m.view != movieRecent || a.shelfCalls != 1 {
		t.Fatal("signed-out watchlist had no way to open Recently Added")
	}
	_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if back != nil || m.view != movieWatchlist || f.recentCalls != 1 {
		t.Fatal("Esc from Recently Added should return to My Watchlist")
	}
	if _, quit := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); quit == nil {
		t.Fatal("Esc at home should exit")
	}
}
