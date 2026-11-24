package tui

import (
	"context"
	"fmt"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

type movieView int

const (
	movieWatchlist movieView = iota // also the return view for an explicit CLI search
	movieRecent
	movieWatched
	movieResults
)

func (v movieView) accountShelf() bool { return v == movieWatchlist || v == movieWatched }

func (v movieView) label() string {
	switch v {
	case movieWatchlist:
		return "My Watchlist"
	case movieRecent:
		return "Trakt Recent Releases"
	case movieWatched:
		return "Watched Movies"
	}
	return "Search"
}

func (v movieView) nextShelf() movieView {
	switch v {
	case movieWatchlist:
		return movieRecent
	case movieRecent:
		return movieWatched
	default:
		return movieWatchlist
	}
}

// A search remembers the shelf it came from. Nil titles means it needs loading.
type movieListing struct {
	view   movieView
	titles []trakt.Title
	cursor int
}

type movieBrowseLoaded struct {
	id     int
	titles []trakt.Title
	err    error
}

func (m *moviePicker) loadBrowse() tea.Cmd {
	if m.view.accountShelf() && !m.connected {
		return nil // offer L to sign in; never request private data while signed out
	}
	var fetch func(context.Context) ([]trakt.Title, error)
	switch m.view {
	case movieWatchlist:
		fetch = m.account.Watchlist
	case movieWatched:
		fetch = m.account.WatchedMovies
	case movieRecent:
		fetch = m.catalog.RecentReleases
	default:
		return nil
	}
	ctx, id := m.beginRequest()
	return func() tea.Msg {
		titles, err := fetch(ctx)
		return movieBrowseLoaded{id, titles, err}
	}
}

func (m *moviePicker) loadedBrowse(msg movieBrowseLoaded) {
	if msg.id != m.request || m.cancel == nil {
		return
	}
	m.stopLoad()
	if msg.err != nil {
		m.problem = msg.err.Error()
		return
	}
	selected, ok := m.focusedMovie()
	m.titles = msg.titles
	if m.titles == nil {
		m.titles = []trakt.Title{} // a valid empty shelf, not an interrupted load
	}
	m.shelfStale = false
	m.cursor[movieTitles] = min(m.cursor[movieTitles], max(0, len(m.titles)-1))
	if ok { // Keep the selected identity when refreshing a reordered shelf.
		for i, t := range m.titles {
			if t.ID == selected.ID && t.Kind == selected.Kind {
				m.cursor[movieTitles] = i
				break
			}
		}
	}
}

func (m *moviePicker) showView(v movieView) tea.Cmd {
	m.stopLoad()
	m.initial, m.editing = false, false
	m.dialog, m.track = movieAccountDialog{}, nil
	m.notice, m.problem = "", ""
	m.seasons, m.bySeason = nil, nil
	if m.view != v {
		m.titles, m.cursor[movieTitles], m.shelfStale = nil, 0, false
	}
	m.stage, m.view, m.home = movieTitles, v, movieListing{}
	m.query = nil
	return m.loadBrowse()
}

func (m *moviePicker) toggleView() tea.Cmd {
	v := m.view
	if v == movieResults {
		v = m.home.view
	}
	return m.showView(v.nextShelf())
}

func (m *moviePicker) startSearch() tea.Cmd {
	m.stopLoad()
	m.initial = false
	m.problem, m.notice = "", ""
	if m.view != movieResults {
		m.home = movieListing{view: m.view, titles: m.titles, cursor: m.cursor[movieTitles]}
		if m.shelfStale {
			m.home.titles = nil
		}
		m.query = nil
	}
	m.seasons, m.bySeason = nil, nil
	m.titles, m.cursor[movieTitles] = nil, 0
	m.stage, m.view, m.editing, m.shelfStale = movieSearch, movieResults, true, false
	return nil
}

func (m *moviePicker) returnHome() tea.Cmd {
	m.stopLoad()
	m.view, m.titles, m.cursor[movieTitles] = m.home.view, m.home.titles, m.home.cursor
	m.stage, m.home = movieTitles, movieListing{}
	m.query, m.editing = nil, false
	m.problem, m.notice = "", ""
	return m.Init() // CLI searches and invalidated private shelves load on return.
}

// Account shelves (including their episodes and a search's return address)
// belong to the previous authorization. Never restore them after an account change.
func (m *moviePicker) forgetAccountShelves() {
	if m.view.accountShelf() {
		m.stage, m.titles, m.cursor = movieTitles, nil, [4]int{}
		m.seasons, m.bySeason, m.shelfStale = nil, nil, false
	}
	if m.home.view.accountShelf() {
		m.home = movieListing{view: m.home.view}
	}
}

// A write can change both watchlist membership and the watched shelf. Only the
// current shelf and a search's return address can be cached. Keep episode rows
// intact until the user backs out of the show; replacing them mid-drilldown
// could silently attach those episodes to a different title.
func (m *moviePicker) invalidateAccountShelves() {
	if m.view.accountShelf() {
		m.shelfStale = true
	}
	if m.home.view.accountShelf() {
		m.home.titles = nil
	}
}

func (m *moviePicker) heading() string {
	if m.stage >= movieSeasons {
		name := m.titles[m.cursor[movieTitles]].Name
		if m.stage == movieEpisodes {
			name += fmt.Sprintf("  ·  Season %d", m.seasons[m.cursor[movieSeasons]])
		}
		return name
	}
	if m.view == movieResults {
		return "Results for " + quote(string(m.query))
	}
	return m.view.label()
}

func (m *moviePicker) emptyBrowseHint() string {
	if m.stage != movieTitles || len(m.titles) != 0 || m.cancel != nil {
		return ""
	}
	if m.view.accountShelf() && !m.connected {
		return "Connect Trakt with L to see " + m.view.label() + ", or press / to search."
	}
	if m.problem != "" {
		return "Press r to retry, / to search Trakt, or v to switch views."
	}
	switch m.view {
	case movieWatchlist:
		return "No watchlist titles to show. Press r to refresh or / to search."
	case movieWatched:
		return "No watched movies to show. Press r to refresh or / to search."
	default:
		return "No recent releases to show. Press r to refresh or / to search."
	}
}
