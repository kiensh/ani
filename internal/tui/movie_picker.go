package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"ani/internal/flixhq"
	tea "github.com/charmbracelet/bubbletea"
)

var movieCatalogCommand = Command{Category: "Catalog", Title: "Search movies & TV (FlixHQ)", Intent: "catalog:flixhq", Keywords: "film series streaming vidmoly provider"}

// MovieCatalog is deliberately separate from MAL: even a numeric film title is a search.
type MovieCatalog interface {
	Search(context.Context, string) ([]flixhq.Title, error)
	Episodes(context.Context, flixhq.Title) ([]flixhq.Episode, error)
}

type movieStage int

const (
	movieSearch movieStage = iota
	movieTitles
	movieSeasons
	movieEpisodes
)

type movieLoaded struct {
	id       int
	titles   []flixhq.Title
	episodes []flixhq.Episode
	err      error
}

type moviePicker struct {
	catalog  MovieCatalog
	stage    movieStage
	query    []rune
	titles   []flixhq.Title
	seasons  []int
	bySeason map[int][]flixhq.Episode
	cursor   [4]int // retain the selected row when backing up one level

	initial       bool
	request       int
	cancel        context.CancelFunc
	problem       string
	notice        string
	tracker       MovieTracker // nil unless this device is connected
	track         *movieTrackForm
	play          bool
	width, height int
}

// RunMoviePicker releases the terminal before resolving and playing a selection.
// Reuse the same list and cursor afterwards, including on a dead embed.
func RunMoviePicker(query string, catalog MovieCatalog, play func(flixhq.Title, *flixhq.Episode) error, tracker MovieTracker) error {
	m := &moviePicker{catalog: catalog, query: []rune(query), initial: query != "", tracker: tracker}
	defer m.stopLoad()
	for {
		if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
			return err
		}
		if !m.play {
			return nil
		}
		m.play = false
		var episode *flixhq.Episode
		if m.stage == movieEpisodes {
			episode = &m.episodes()[m.cursor[movieEpisodes]]
		}
		if err := play(m.titles[m.cursor[movieTitles]], episode); err != nil {
			m.problem = err.Error()
		} else if m.tracker != nil {
			m.notice = "Finished watching? Press m to mark this selection watched on Trakt."
		}
	}
}

func (m *moviePicker) Init() tea.Cmd {
	if m.initial {
		m.initial = false
		return m.load()
	}
	return nil
}

func (m *moviePicker) stopLoad() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *moviePicker) beginRequest() (context.Context, int) {
	m.stopLoad()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.request++
	m.problem, m.notice = "", ""
	return ctx, m.request
}

func (m *moviePicker) load() tea.Cmd {
	ctx, id := m.beginRequest()
	if m.stage == movieSearch {
		query := strings.TrimSpace(string(m.query))
		return func() tea.Msg {
			titles, err := m.catalog.Search(ctx, query)
			return movieLoaded{id: id, titles: titles, err: err}
		}
	}
	title := m.titles[m.cursor[movieTitles]]
	return func() tea.Msg {
		episodes, err := m.catalog.Episodes(ctx, title)
		return movieLoaded{id: id, episodes: episodes, err: err}
	}
}

func (m *moviePicker) loaded(msg movieLoaded) {
	if msg.id != m.request || m.cancel == nil { // escaped or superseded request
		return
	}
	m.stopLoad()
	if msg.err != nil {
		m.problem = msg.err.Error()
		return
	}
	if m.stage == movieSearch {
		if len(msg.titles) == 0 {
			m.problem = "No matching movies or series. Try another title."
			return
		}
		m.titles, m.cursor[movieTitles], m.stage = msg.titles, 0, movieTitles
		return
	}
	if len(msg.episodes) == 0 {
		m.problem = "No episodes listed for this series."
		return
	}
	m.bySeason = make(map[int][]flixhq.Episode)
	m.seasons = nil
	for _, ep := range msg.episodes {
		if _, exists := m.bySeason[ep.Season]; !exists {
			m.seasons = append(m.seasons, ep.Season)
		}
		m.bySeason[ep.Season] = append(m.bySeason[ep.Season], ep)
	}
	slices.Sort(m.seasons)
	for _, eps := range m.bySeason {
		slices.SortStableFunc(eps, func(a, b flixhq.Episode) int { return a.Number - b.Number })
	}
	m.cursor[movieSeasons], m.stage = 0, movieSeasons
}

func (m *moviePicker) episodes() []flixhq.Episode {
	return m.bySeason[m.seasons[m.cursor[movieSeasons]]]
}

func (m *moviePicker) rowCount() int {
	switch m.stage {
	case movieTitles:
		return len(m.titles)
	case movieSeasons:
		return len(m.seasons)
	case movieEpisodes:
		return len(m.episodes())
	}
	return 0
}

func (m *moviePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case movieLoaded:
		m.loaded(msg)
	case traktMatchesMsg:
		m.loadedTrackMatches(msg)
	case traktWriteMsg:
		m.savedTrack(msg)
	case tea.KeyMsg:
		key := msg.String()
		if m.track != nil && key != "ctrl+c" {
			return m, m.trackKey(key)
		}
		if key == "ctrl+c" || (key == "q" && m.stage != movieSearch) {
			m.stopLoad()
			return m, tea.Quit
		}
		if m.cancel != nil {
			if key == "esc" {
				m.stopLoad()
			}
			return m, nil
		}
		m.problem, m.notice = "", ""
		if key == "esc" {
			if m.stage == movieSearch {
				return m, tea.Quit
			}
			m.stage--
			return m, nil
		}
		if m.stage == movieSearch {
			switch key {
			case "enter":
				if strings.TrimSpace(string(m.query)) != "" {
					return m, m.load()
				}
			case "ctrl+u":
				m.query = nil
			case "backspace":
				if len(m.query) > 0 {
					m.query = m.query[:len(m.query)-1]
				}
			default:
				if msg.Type == tea.KeySpace {
					m.query = append(m.query, ' ')
				} else if msg.Type == tea.KeyRunes && !msg.Alt {
					for _, r := range msg.Runes {
						if unicode.IsPrint(r) {
							m.query = append(m.query, r)
						}
					}
				}
			}
			return m, nil
		}
		pos := &m.cursor[m.stage]
		last := m.rowCount() - 1
		switch key {
		case "up", "k":
			*pos = max(0, *pos-1)
		case "down", "j":
			*pos = min(last, *pos+1)
		case "pgup", "ctrl+u":
			*pos = max(0, *pos-max(1, m.height-6))
		case "pgdown", "ctrl+d":
			*pos = min(last, *pos+max(1, m.height-6))
		case "m", "w":
			return m, m.openTrackForm(key == "w")
		case "/":
			m.stage = movieSearch
		case "enter":
			switch m.stage {
			case movieTitles:
				if m.titles[*pos].Kind == "series" {
					return m, m.load()
				}
			case movieSeasons:
				m.cursor[movieEpisodes], m.stage = 0, movieEpisodes
				return m, nil
			}
			m.play = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *moviePicker) View() string {
	if m.track != nil {
		return m.trackView()
	}
	lines := []string{HeaderStyle.Render("Movies & TV") + ProviderStyle.Render("  ·  FlixHQ")}
	if m.stage == movieSearch {
		lines = append(lines, "Search: "+string(m.query)+"▏")
	} else {
		heading := "Results for " + quote(string(m.query))
		if m.stage >= movieSeasons {
			heading = m.titles[m.cursor[movieTitles]].Name
		}
		if m.stage == movieEpisodes {
			heading += fmt.Sprintf("  ·  Season %d", m.seasons[m.cursor[movieSeasons]])
		}
		lines = append(lines, TitleStyle.Render(clipANSI(heading, max(10, m.width-2))))
		pos := m.cursor[m.stage]
		top, n := paletteWindow(pos, max(1, m.height-6), m.rowCount())
		for i := top; i < top+n; i++ {
			var label string
			switch m.stage {
			case movieTitles:
				t := m.titles[i]
				label = fmt.Sprintf("[%s] %s", t.Kind, t.Name)
				if t.Year != "" {
					label += " (" + t.Year + ")"
				}
			case movieSeasons:
				s := m.seasons[i]
				label = fmt.Sprintf("Season %d  ·  %d episodes", s, len(m.bySeason[s]))
			case movieEpisodes:
				ep := m.episodes()[i]
				label = fmt.Sprintf("S%02dE%02d  %s", ep.Season, ep.Number, ep.Name)
			}
			label = clipANSI(label, max(10, m.width-4))
			if i == pos {
				lines = append(lines, SelectedStyle.Render(CursorGlyph+label))
			} else {
				lines = append(lines, "  "+label)
			}
		}
	}
	if m.cancel != nil {
		lines = append(lines, FaintStyle.Render("Loading…  Esc cancels"))
	}
	if m.problem != "" {
		lines = append(lines, ErrorStyle.Render(m.problem))
	}
	if m.notice != "" {
		lines = append(lines, FaintStyle.Render(m.notice))
	}
	help := "↑/↓ select · Enter open/play · w watchlist · m watched · / search · Esc back · q exit"
	if m.stage == movieSearch {
		help = "Type a title · Enter search · Ctrl+U clear · Esc exit"
	}
	return strings.Join(append(lines, HelpStyle.Render(help)), "\n")
}
