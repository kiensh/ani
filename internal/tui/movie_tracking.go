package tui

import (
	"context"
	"fmt"
	"strings"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

// Browsing, opening the action menu and playing a title never write to Trakt.
type MovieTracker interface {
	AddWatchlist(context.Context, trakt.Title) error
	MarkWatched(context.Context, trakt.Title, int, int) error
	MarkEpisodeWatched(context.Context, trakt.Episode) error
}

// Capture the exact row when the action is chosen. The confirmation is not a
// second picker: neither title names nor season/episode numbers resolve an ID.
type movieTrackForm struct {
	title     trakt.Title
	episode   *trakt.Episode // only set when marking an individual episode watched
	watchlist bool
	saving    bool
}

func movieTitleLabel(t trakt.Title) string {
	if t.Year > 0 {
		return fmt.Sprintf("%s (%d)", t.Name, t.Year)
	}
	return t.Name
}

func (f *movieTrackForm) label() string {
	label := movieTitleLabel(f.title)
	if e := f.episode; e != nil {
		label += fmt.Sprintf(" S%02dE%02d", e.Season, e.Number)
	}
	return label
}

type traktWriteMsg struct {
	id  int
	err error
}

func (m *moviePicker) actionTitle() (trakt.Title, bool) {
	if m.cancel != nil || m.subPrompt != nil || m.dialog.mode != movieAccountClosed || m.track != nil || m.play {
		return trakt.Title{}, false
	}
	t, ok := m.focusedMovie()
	return t, ok && m.rowCount() > 0 && t.ID > 0
}

func (m *moviePicker) titleActions() []Command {
	t, ok := m.actionTitle()
	if !ok {
		return nil
	}
	var cmds []Command
	if m.view != movieWatchlist {
		label := "Add to My Watchlist"
		if t.Kind == "series" {
			label = "Add series to My Watchlist"
		}
		cmds = append(cmds, Command{Category: "Actions", Title: label, Intent: "trakt:add-watchlist", Keywords: "save plan to watch trakt status w"})
	}
	if t.Kind == "movie" || m.stage == movieEpisodes {
		label := "Mark movie as watched"
		if m.stage == movieEpisodes {
			label = "Mark episode as watched"
		}
		cmds = append(cmds, Command{Category: "Actions", Title: label, Intent: "trakt:mark-watched", Keywords: "complete seen history trakt status m"})
	}
	return cmds
}

func (m *moviePicker) openTrackForm(watchlist bool) tea.Cmd {
	t, ok := m.actionTitle()
	if !ok {
		return nil
	}
	if watchlist && m.view == movieWatchlist {
		m.notice = t.Name + " is already on your Trakt watchlist."
		return nil
	}
	f := &movieTrackForm{title: t, watchlist: watchlist}
	if !watchlist && t.Kind != "movie" {
		if t.Kind != "series" || m.stage != movieEpisodes {
			m.problem = "Select an episode to mark watched (not the whole series)."
			return nil
		}
		e := m.episodes()[m.cursor[movieEpisodes]]
		if e.ID <= 0 || e.Season < 0 || e.Number <= 0 {
			m.problem = "Cannot mark an episode without a Trakt episode ID, season and number."
			return nil
		}
		f.episode = &e
	}
	if !m.connected {
		m.offerLogin()
		return nil
	}
	m.problem, m.notice = "", ""
	m.track = f
	return nil
}

func (m *moviePicker) trackKey(key string) tea.Cmd {
	f := m.track
	if f.saving {
		return nil // Enter must not submit a write twice
	}
	switch key {
	case "esc", "q":
		m.track = nil
	case "enter":
		f.saving = true
		ctx, id := m.beginRequest()
		return func() tea.Msg {
			var err error
			switch {
			case f.watchlist:
				err = m.account.AddWatchlist(ctx, f.title)
			case f.episode != nil:
				err = m.account.MarkEpisodeWatched(ctx, *f.episode)
			default:
				err = m.account.MarkWatched(ctx, f.title, 0, 0)
			}
			return traktWriteMsg{id, err}
		}
	}
	return nil
}

func (m *moviePicker) savedTrack(msg traktWriteMsg) tea.Cmd {
	f := m.track
	if f == nil || !f.saving || m.cancel == nil || msg.id != m.request {
		return nil
	}
	m.stopLoad()
	m.track = nil
	if msg.err != nil {
		m.problem = msg.err.Error()
		return nil
	}
	m.invalidateAccountShelves()
	var refresh tea.Cmd
	if m.stage == movieTitles && m.shelfStale {
		refresh = m.loadBrowse()
	}
	if f.watchlist {
		m.notice = f.title.Name + " is on your Trakt watchlist."
	} else {
		m.notice = f.label() + " is marked watched on Trakt."
	}
	return refresh
}

func (m *moviePicker) trackView() string {
	f := m.track
	action := "Mark watched on Trakt?"
	if f.watchlist {
		action = "Add to My Watchlist?"
	}
	id := f.title.ID
	if f.episode != nil {
		id = f.episode.ID
	}
	help := "Enter confirm · Esc cancel"
	if f.saving {
		help = "Saving to Trakt…"
	}
	return strings.Join([]string{HeaderStyle.Render(action), TitleStyle.Render(clipANSI(f.label(), max(10, m.width-2))), FaintStyle.Render(fmt.Sprintf("Trakt #%d", id)), HelpStyle.Render(help)}, "\n")
}
