package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type movieDetailSource interface {
	TitleDetails(context.Context, trakt.Title) (trakt.Title, error)
}

// Numeric Trakt IDs are unique per media kind, not across movies and shows.
type previewID struct {
	kind string
	id   int
}

func previewKey(t trakt.Title) previewID { return previewID{t.Kind, t.ID} }

type previewEntry struct {
	title      previewID
	details    trakt.Title
	hasDetails bool
	poster     []byte // compressed image, reused when resizing or revisiting this title
}

type previewFocus struct {
	title      previewID
	cols, rows int
}
type moviePreview struct {
	entries                                    []previewEntry // bounded, recent titles only; mutated on the UI goroutine
	focus                                      previewFocus
	generation                                 int
	ctx                                        context.Context
	cancel                                     context.CancelFunc
	art                                        string
	posterStarted, posterFailed, detailsFailed bool
}

type moviePreviewDue int
type movieDetailMsg struct {
	generation int
	details    trakt.Title
	err        error
}
type movieArtMsg struct {
	generation int
	data       []byte
	upload     [][]byte
	text       string
	err        error
}

func (p *moviePreview) entry() *previewEntry {
	for i := range p.entries {
		if p.entries[i].title == p.focus.title {
			return &p.entries[i]
		}
	}
	const maxEntries = 8
	if len(p.entries) == maxEntries {
		copy(p.entries, p.entries[1:])
		p.entries = p.entries[:maxEntries-1]
	}
	p.entries = append(p.entries, previewEntry{title: p.focus.title})
	return &p.entries[len(p.entries)-1]
}

func (m *moviePicker) stopPreview() {
	p := &m.preview
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.focus = previewFocus{}
	p.art = ""
	p.posterStarted, p.posterFailed, p.detailsFailed = false, false, false
}

func (m *moviePicker) focusedMovie() (trakt.Title, bool) {
	if m.stage == movieSearch || m.cursor[movieTitles] < 0 || m.cursor[movieTitles] >= len(m.titles) {
		return trakt.Title{}, false
	}
	return m.titles[m.cursor[movieTitles]], true
}

func (m *moviePicker) syncPreview() tea.Cmd {
	p := &m.preview
	t, ok := m.focusedMovie()
	if !ok || t.ID <= 0 || m.width < 1 || m.height < 1 || m.play || m.source != "" || m.dialog.mode != movieAccountClosed || m.palette.Active() || m.track != nil || m.subPrompt != nil {
		m.stopPreview()
		return nil
	}
	l := m.movieLayout()
	key := previewFocus{previewKey(t), l.coverCols, l.coverRows}
	if p.focus == key {
		return nil
	}
	m.stopPreview()
	p.focus = key
	_, canLoad := m.catalog.(movieDetailSource)
	if !canLoad && t.PosterURL == "" {
		return nil
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.generation++
	id := p.generation
	// Don't request title pages for every row skipped by held arrow keys.
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return moviePreviewDue(id) })
}

func (m *moviePicker) posterCmd(url string) tea.Cmd {
	p := &m.preview
	if p.posterStarted || p.focus.rows < 1 || !trakt.ValidPosterURL(url) {
		return nil
	}
	p.posterStarted = true
	id, ctx, size, data := p.generation, p.ctx, p.focus, p.entry().poster
	return func() tea.Msg {
		if len(data) == 0 {
			var err error
			data, err = fetchMoviePoster(ctx, url)
			if err != nil {
				return movieArtMsg{generation: id, err: err}
			}
		}
		upload, text, err := renderMoviePoster(ctx, data, size.cols, size.rows)
		return movieArtMsg{id, data, upload, text, err}
	}
}

// Preview messages never use the catalog/account request slot: preview failures
// and rapid focus changes must not interrupt browsing, playback or device login.
func (m *moviePicker) updateWithPreview(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := &m.preview
	valid := func(id int) bool { return p.cancel != nil && id == p.generation }
	switch msg := msg.(type) {
	case moviePreviewDue:
		if !valid(int(msg)) {
			return m, nil
		}
		t, ok := m.focusedMovie()
		if !ok {
			return m, nil
		}
		e := p.entry()
		url := t.PosterURL
		if url == "" {
			url = e.details.PosterURL
		}
		var details tea.Cmd
		if source, ok := m.catalog.(movieDetailSource); ok && !e.hasDetails && (t.Overview == "" || t.PosterURL == "") {
			ctx, id := p.ctx, p.generation
			details = func() tea.Msg { d, err := source.TitleDetails(ctx, t); return movieDetailMsg{id, d, err} }
		}
		return m, tea.Batch(details, m.posterCmd(url))
	case movieDetailMsg:
		if !valid(msg.generation) {
			return m, nil
		}
		if msg.err != nil || previewKey(msg.details) != p.focus.title {
			p.detailsFailed = true
			return m, nil
		}
		e := p.entry()
		e.details, e.hasDetails = msg.details, true
		return m, m.posterCmd(msg.details.PosterURL)
	case movieArtMsg:
		if !valid(msg.generation) {
			return m, nil
		}
		if msg.err != nil {
			p.posterFailed = true
			return m, nil
		}
		p.entry().poster = msg.data
		p.art = msg.text
		// Stale renders must not write to the terminal after Tab, mpv or resize.
		if len(msg.upload) > 0 {
			WriteUpload(msg.upload)
		}
		return m, nil
	}
	model, cmd := m.update(msg)
	return model, tea.Batch(cmd, m.syncPreview())
}

type moviePaneLayout struct {
	side                           bool
	listW, listH, detailW, detailH int
	coverCols, coverRows           int
}

func (m *moviePicker) movieLayout() moviePaneLayout {
	w, h := max(1, m.width), max(1, m.height-5) // source, notices, two help lines
	var l moviePaneLayout
	if w >= 66 {
		l.side = true
		left := min(56, w*47/100)
		l.listW, l.detailW = left-2, w-left-2
		l.listH, l.detailH = max(1, h-2), max(1, h-2)
	} else {
		// On a phone-sized terminal put the preview below the selection. No
		// horizontally hidden pane, and always keep the current row visible.
		l.listW, l.detailW = max(1, w-2), max(1, w-2)
		l.listH = max(2, min(6, (h-4)/2))
		l.detailH = max(2, h-4-l.listH)
	}
	l.coverCols = min(38, l.detailW)
	l.coverRows = min(14, max(0, l.detailH-6))
	return l
}

func (m *moviePicker) browsePanes(heading string) string {
	l := m.movieLayout()
	lines := []string{TitleStyle.Render(clipANSI(heading, l.listW))}
	count := m.rowCount()
	pos := m.cursor[m.stage]
	top, n := paletteWindow(pos, max(1, l.listH-1), count)
	for i := top; i < top+n; i++ {
		var label string
		switch m.stage {
		case movieTitles:
			t := m.titles[i]
			label = fmt.Sprintf("[%s] %s", t.Kind, t.Name)
			if t.Year > 0 {
				label += fmt.Sprintf(" (%d)", t.Year)
			}
		case movieSeasons:
			s := m.seasons[i]
			label = fmt.Sprintf("Season %d  ·  %d episodes", s, len(m.bySeason[s]))
		case movieEpisodes:
			ep := m.episodes()[i]
			label = fmt.Sprintf("S%02dE%02d  %s", ep.Season, ep.Number, ep.Name)
		}
		label = clipANSI(label, max(1, l.listW-2))
		if i == pos {
			lines = append(lines, SelectedStyle.Render(CursorGlyph+label))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	list := ListBorderStyle.Width(l.listW).Render(fitPaneHeight(strings.Join(lines, "\n"), l.listH))
	detail := PreviewBorderStyle.Width(l.detailW).Render(fitPaneHeight(m.previewText(l), l.detailH))
	if l.side {
		return lipgloss.JoinHorizontal(lipgloss.Top, list, detail)
	}
	return list + "\n" + detail
}

func (m *moviePicker) previewText(l moviePaneLayout) string {
	t, ok := m.focusedMovie()
	if !ok {
		return FaintStyle.Render("Title details")
	}
	p := &m.preview
	d := t // full catalog/list rows can render immediately, including private shelves
	ready := d.Overview != "" && d.PosterURL != ""
	if p.focus.title == previewKey(t) && t.ID > 0 {
		for _, e := range p.entries {
			if e.title == p.focus.title && e.hasDetails {
				// List snapshots and summaries can be independently sparse. Keep
				// inline metadata if the focused summary returns only artwork.
				x := e.details
				if x.Overview != "" {
					d.Overview = x.Overview
				}
				if x.PosterURL != "" {
					d.PosterURL = x.PosterURL
				}
				if x.Genres != "" {
					d.Genres = x.Genres
				}
				if x.Runtime > 0 {
					d.Runtime = x.Runtime
				}
				if x.Rating > 0 {
					d.Rating = x.Rating
				}
				ready = true
				break
			}
		}
	}
	var meta []string
	if t.Kind == "series" {
		meta = append(meta, "TV series")
	} else {
		meta = append(meta, "Movie")
	}
	if t.Year > 0 {
		meta = append(meta, fmt.Sprint(t.Year))
	}
	if d.Runtime > 0 {
		meta = append(meta, fmt.Sprintf("%d min", d.Runtime))
	}
	if d.Rating > 0 {
		meta = append(meta, fmt.Sprintf("★ %.1f", d.Rating))
	}
	lines := []string{TitleStyle.Render(clipANSI(wrapTwoLines(t.Name, l.detailW), l.detailW)), FaintStyle.Render(clipANSI(strings.Join(meta, " · "), l.detailW))}
	if m.stage == movieEpisodes && len(m.episodes()) > 0 {
		e := m.episodes()[m.cursor[movieEpisodes]]
		lines = append(lines, ProgressStyle.Render(clipANSI(fmt.Sprintf("S%02dE%02d · %s", e.Season, e.Number, e.Name), l.detailW)))
	}
	if p.focus.title == previewKey(t) && p.art != "" && l.coverRows > 0 {
		lines = append(lines, p.art+"\x1b[0m")
	} else if p.posterFailed || (ready && t.PosterURL == "" && d.PosterURL == "" && l.coverRows > 0) {
		lines = append(lines, FaintStyle.Render("Poster unavailable"))
	}
	if d.Genres != "" {
		lines = append(lines, GenresStyle.Render(clipANSI("Genres: "+d.Genres, l.detailW)))
	}
	if d.Overview != "" {
		lines = append(lines, clipANSI(wrap(d.Overview, l.detailW), l.detailW))
	} else if !ready && p.detailsFailed {
		lines = append(lines, FaintStyle.Render("Details unavailable"))
	} else if !ready {
		lines = append(lines, FaintStyle.Render("Loading details…"))
	}
	return strings.Join(lines, "\n")
}
