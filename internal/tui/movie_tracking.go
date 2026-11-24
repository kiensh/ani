package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

// MovieTracker is optional. Merely searching the playback catalog, selecting
// an episode, or quitting mpv must not send a request to a user's Trakt account.
type MovieTracker interface {
	Search(context.Context, string, string) ([]trakt.Title, error)
	AddWatchlist(context.Context, trakt.Title) error
	MarkWatched(context.Context, trakt.Title, int, int) error
}

type movieTrackForm struct {
	watchlist        bool
	name, kind, year string
	season, episode  int
	matches          []trakt.Title
	cursor           int
	saving           bool
}

func (f *movieTrackForm) label() string {
	label := f.name
	if f.year != "" {
		label += " (" + f.year + ")"
	}
	if f.episode > 0 {
		label += fmt.Sprintf(" S%02dE%02d", f.season, f.episode)
	}
	return label
}

type traktMatchesMsg struct {
	id     int
	titles []trakt.Title
	err    error
}
type traktWriteMsg struct {
	id   int
	name string
	err  error
}

func (m *moviePicker) openTrackForm(watchlist bool) tea.Cmd {
	if m.tracker == nil {
		m.problem = "Trakt is not connected. Run ani trakt login, then reopen Movies & TV."
		return nil
	}
	t := m.titles[m.cursor[movieTitles]]
	f := &movieTrackForm{watchlist: watchlist, name: t.Name, kind: t.Kind, year: t.Year}
	if !watchlist && t.Kind == "series" {
		if m.stage != movieEpisodes {
			m.problem = "Select an episode to mark watched (not the whole series)."
			return nil
		}
		e := m.episodes()[m.cursor[movieEpisodes]]
		f.season, f.episode = e.Season, e.Number
	}
	m.track = f
	ctx, id := m.beginRequest()
	return func() tea.Msg {
		titles, err := m.tracker.Search(ctx, t.Kind, t.Name)
		return traktMatchesMsg{id, titles, err}
	}
}

func (m *moviePicker) loadedTrackMatches(msg traktMatchesMsg) {
	f := m.track
	if f == nil || f.saving || m.cancel == nil || msg.id != m.request {
		return
	}
	m.stopLoad()
	if msg.err != nil {
		m.track = nil
		m.problem = msg.err.Error()
		return
	}
	for _, t := range msg.titles {
		if t.Kind == f.kind && t.ID > 0 {
			f.matches = append(f.matches, t)
		}
	}
	if len(f.matches) == 0 {
		m.track = nil
		m.problem = "No matching Trakt title. Nothing was saved."
		return
	}
	// Keep similarly named remakes visible, but put an exact title/year first.
	// Even a single exact match requires confirmation before a write.
	year, _ := strconv.Atoi(f.year)
	rank := func(t trakt.Title) int {
		n := 0
		if strings.EqualFold(t.Name, f.name) {
			n += 2
		}
		if year > 0 && t.Year == year {
			n++
		}
		return n
	}
	slices.SortStableFunc(f.matches, func(a, b trakt.Title) int { return rank(b) - rank(a) })
}

func (m *moviePicker) trackKey(key string) tea.Cmd {
	f := m.track
	if f.saving {
		return nil // a write is in flight; Enter must not submit twice
	}
	switch key {
	case "esc", "q":
		m.stopLoad()
		m.track = nil
		return nil
	}
	if len(f.matches) == 0 {
		return nil
	}
	switch key {
	case "up", "k":
		f.cursor = max(0, f.cursor-1)
	case "down", "j":
		f.cursor = min(len(f.matches)-1, f.cursor+1)
	case "enter":
		t := f.matches[f.cursor]
		f.saving = true
		ctx, id := m.beginRequest()
		return func() tea.Msg {
			var err error
			if f.watchlist {
				err = m.tracker.AddWatchlist(ctx, t)
			} else {
				err = m.tracker.MarkWatched(ctx, t, f.season, f.episode)
			}
			return traktWriteMsg{id, t.Name, err}
		}
	}
	return nil
}

func (m *moviePicker) savedTrack(msg traktWriteMsg) {
	f := m.track
	if f == nil || !f.saving || msg.id != m.request {
		return
	}
	m.stopLoad()
	m.track = nil
	if msg.err != nil {
		m.problem = msg.err.Error()
		return
	}
	if f.watchlist {
		m.notice = msg.name + " is on your Trakt watchlist."
	} else if f.episode > 0 {
		m.notice = fmt.Sprintf("%s S%02dE%02d is marked watched on Trakt.", msg.name, f.season, f.episode)
	} else {
		m.notice = msg.name + " is marked watched on Trakt."
	}
}

func (m *moviePicker) trackView() string {
	f := m.track
	action := "Mark watched on Trakt"
	if f.watchlist {
		action = "Add to Trakt watchlist"
	}
	lines := []string{HeaderStyle.Render(action), TitleStyle.Render(clipANSI(f.label(), max(10, m.width-2)))}
	if len(f.matches) == 0 {
		return strings.Join(append(lines, FaintStyle.Render("Finding matching titles…  Esc cancels")), "\n")
	}
	lines = append(lines, "Choose the matching Trakt title:")
	top, n := paletteWindow(f.cursor, max(1, m.height-5), len(f.matches))
	for i := top; i < top+n; i++ {
		t := f.matches[i]
		label := t.Name
		if t.Year > 0 {
			label += fmt.Sprintf(" (%d)", t.Year)
		}
		label += fmt.Sprintf("  ·  Trakt #%d", t.ID)
		label = clipANSI(label, max(10, m.width-4))
		if i == f.cursor {
			lines = append(lines, SelectedStyle.Render(CursorGlyph+label))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	help := "↑/↓ choose · Enter confirm · Esc cancel"
	if f.saving {
		help = "Saving to Trakt…"
	}
	return strings.Join(append(lines, HelpStyle.Render(help)), "\n")
}
