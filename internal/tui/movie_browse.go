package tui

import (
	"fmt"
	"strconv"

	"ani/internal/flixhq"
	tea "github.com/charmbracelet/bubbletea"
)

type movieView int

const (
	movieRecent movieView = iota
	movieWatchlist
	movieResults
)

// A search remembers the shelf it came from. Returning doesn't fetch it again
// or lose the selected row. A nil titles slice means the shelf hasn't loaded.
type movieListing struct {
	view   movieView
	titles []flixhq.Title
	cursor int
}

type movieBrowseLoaded struct {
	id     int
	titles []flixhq.Title
	err    error
}

func (m *moviePicker) loadBrowse() tea.Cmd {
	if m.view == movieWatchlist && !m.connected {
		return nil // the empty view explains both L to sign in and v to leave
	}
	ctx, id := m.beginRequest()
	if m.view == movieWatchlist {
		return func() tea.Msg {
			rows, err := m.account.Watchlist(ctx)
			titles := make([]flixhq.Title, 0, len(rows))
			for _, t := range rows {
				year := ""
				if t.Year > 0 {
					year = strconv.Itoa(t.Year)
				}
				// Display metadata only. No Trakt ID or name-derived URL can be
				// used as a FlixHQ identity for playback or for provider previews.
				titles = append(titles, flixhq.Title{Name: t.Name, Kind: t.Kind, Year: year})
			}
			return movieBrowseLoaded{id, titles, err}
		}
	}
	return func() tea.Msg {
		titles, err := m.catalog.RecentlyAdded(ctx)
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
	m.titles = msg.titles
	if m.titles == nil {
		m.titles = []flixhq.Title{} // a valid empty shelf, not an interrupted load
	}
	m.cursor[movieTitles] = min(m.cursor[movieTitles], max(0, len(m.titles)-1))
}

func (m *moviePicker) showView(v movieView) tea.Cmd {
	m.stopLoad()
	m.initial, m.editing, m.matching = false, false, false
	m.dialog, m.track = movieAccountDialog{}, nil
	m.notice, m.problem = "", ""
	if m.view != v {
		m.titles, m.cursor[movieTitles] = nil, 0
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
	if v == movieWatchlist {
		return m.showView(movieRecent)
	}
	return m.showView(movieWatchlist)
}

func (m *moviePicker) startSearch(match bool) tea.Cmd {
	m.stopLoad()
	m.initial = false
	m.problem, m.notice = "", ""
	if m.view != movieResults {
		m.home = movieListing{m.view, m.titles, m.cursor[movieTitles]}
		m.query = nil
	}
	m.titles, m.cursor[movieTitles] = nil, 0
	m.stage, m.view, m.editing, m.matching = movieSearch, movieResults, !match, match
	if match {
		m.query = []rune(m.home.titles[m.home.cursor].Name)
		return m.load()
	}
	return nil
}

func (m *moviePicker) returnHome() tea.Cmd {
	m.stopLoad()
	m.view, m.titles, m.cursor[movieTitles] = m.home.view, m.home.titles, m.home.cursor
	m.stage, m.home = movieTitles, movieListing{}
	m.query, m.matching, m.editing = nil, false, false
	m.problem, m.notice = "", ""
	return m.Init() // --movies <query> might not have visited home yet
}

func (m *moviePicker) forgetWatchlist() {
	if m.view == movieWatchlist {
		m.titles, m.cursor[movieTitles] = nil, 0
	}
	if m.home.view == movieWatchlist {
		m.home = movieListing{view: movieWatchlist}
		m.matching = false
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
	switch m.view {
	case movieRecent:
		return "Recently Added"
	case movieWatchlist:
		return "My Watchlist"
	}
	if m.matching {
		t := m.home.titles[m.home.cursor]
		prefix := "FlixHQ match"
		if t.Year != "" {
			prefix += " (" + t.Year + ")"
		}
		return prefix + ": " + t.Name
	}
	return "Results for " + quote(string(m.query))
}

func (m *moviePicker) emptyBrowseHint() string {
	if m.stage != movieTitles || len(m.titles) != 0 || m.cancel != nil {
		return ""
	}
	if m.view == movieWatchlist && !m.connected {
		return "Connect Trakt with L to see your watchlist, or press v for Recently Added."
	}
	if m.problem != "" {
		return "Press r to retry, / to search FlixHQ, or v to switch views."
	}
	if m.view == movieWatchlist {
		return "No watchlist titles to show. Press r to refresh or v for Recently Added."
	}
	return "No recently added titles to show. Press r to refresh or / to search FlixHQ."
}
