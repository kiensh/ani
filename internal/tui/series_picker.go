package tui

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ani/internal/animetosho"
)

// seriesMetaMsg carries the focused series' detail (year, episode count) and the
// local path of its downloaded cover ("" if none).
type seriesMetaMsg struct {
	aid       int
	year      string
	episodes  int
	coverPath string
}

// seriesPicker is the manual AnimeTosho-series fallback: a two-pane screen (list
// + preview) shown when auto AniDB-id resolution fails, so the user can pick the
// right series. The choice is cached by the caller (malID → aid).
type seriesPicker struct {
	header string // MAL title being resolved (header context)
	series []animetosho.SeriesSummary

	cursor  int
	topItem int

	width, height int
	listWidth     int
	paneHeight    int
	coverCols     int
	coverRows     int

	// per-aid cached detail + downloaded cover file path ("" = fetched, no cover).
	year       map[int]string
	episodes   map[int]int
	coverFiles   map[int]string // aid → temp file path (or "" once fetched)
	coverText    string         // rendered cover placeholder for the focused series
	coverHeights map[int]int    // aid → actual rendered cover height, so placeholder matches

	result struct {
		aid int
		ok  bool
	}
}

func newSeriesPicker(header string, series []animetosho.SeriesSummary) *seriesPicker {
	return &seriesPicker{
		header:      header,
		series:      series,
		year:        map[int]string{},
		episodes:    map[int]int{},
		coverFiles:   map[int]string{},
		coverHeights: map[int]int{},
	}
}

func (m *seriesPicker) Init() tea.Cmd { return m.focusCmd() }

func (m *seriesPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.recomputeLayout()
		m.fixScroll()
		return m, m.focusCmd()
	case seriesMetaMsg:
		m.year[msg.aid] = msg.year
		m.episodes[msg.aid] = msg.episodes
		m.coverFiles[msg.aid] = msg.coverPath
		if len(m.series) > 0 && msg.aid == m.series[m.cursor].AnidbAID {
			return m, m.renderCoverCmd()
		}
		return m, nil
	case coverTextMsg:
		m.coverText = msg.text
		if msg.text != "" && msg.key > 0 {
			m.coverHeights[msg.key] = len(strings.Split(msg.text, "\n"))
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if len(m.series) > 0 {
				m.result.aid = m.series[m.cursor].AnidbAID
				m.result.ok = true
			}
			return m, tea.Quit
		case "esc", "ctrl+c", "q":
			m.result.ok = false
			return m, tea.Quit
		case "down", "j":
			if m.cursor < len(m.series)-1 {
				m.cursor++
				m.fixScroll()
				return m, m.focusCmd()
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.fixScroll()
				return m, m.focusCmd()
			}
		}
	}
	return m, nil
}

// focusCmd fetches the focused series' detail + cover once (cached per aid), then
// renders the cover.
func (m *seriesPicker) focusCmd() tea.Cmd {
	if len(m.series) == 0 {
		return nil
	}
	aid := m.series[m.cursor].AnidbAID
	if _, ok := m.coverFiles[aid]; ok {
		return m.renderCoverCmd() // already fetched (cover or none) → render
	}
	return func() tea.Msg {
		_, year, eps, pic, _ := animetosho.SeriesMeta(aid)
		path := ""
		if pic != "" {
			path = downloadCoverFile(pic) // pic is already the full URL (SeriesMeta prepends CoverBase)
		}
		return seriesMetaMsg{aid: aid, year: year, episodes: eps, coverPath: path}
	}
}

// renderCoverCmd renders the focused series' cached cover file (re-runs kitten so
// the image re-displays after other covers were shown).
func (m *seriesPicker) renderCoverCmd() tea.Cmd {
	if len(m.series) == 0 {
		return nil
	}
	aid := m.series[m.cursor].AnidbAID
	path := m.coverFiles[aid]
	cols, rows := m.coverCols, m.coverRows
	if path == "" {
		return func() tea.Msg { return coverTextMsg{text: ""} }
	}
	return func() tea.Msg {
		upload, text, err := RenderCoverPlaceholder(path, cols, rows)
		if err != nil {
			return coverTextMsg{text: ""}
		}
		WriteUpload(upload)
		return coverTextMsg{text: text, key: aid}
	}
}

func (m *seriesPicker) View() string {
	if m.width == 0 {
		return "Loading…"
	}
	if len(m.series) == 0 {
		return FaintStyle.Render("No AnimeTosho series found for " + quote(m.header))
	}

	// ---- LEFT pane: series list ----
	avail := m.listWidth - 2 - len(CursorGlyph)
	if avail < 4 {
		avail = 4
	}
	ps := m.pageSize()
	end := m.topItem + ps
	if end > len(m.series) {
		end = len(m.series)
	}
	listLines := make([]string, 0, ps)
	for i := m.topItem; i < end; i++ {
		text := clip(m.series[i].Title, avail)
		if i == m.cursor {
			listLines = append(listLines, SelectedStyle.Render(CursorGlyph+text))
		} else {
			listLines = append(listLines, "  "+text)
		}
	}
	for len(listLines) < ps {
		listLines = append(listLines, "")
	}
	title := TitleStyle.Render("AnimeTosho series") + FaintStyle.Render(fmt.Sprintf("  (%d)", len(m.series)))
	leftPane := ListBorderStyle.
		Width(m.listWidth).
		Height(m.paneHeight - 2).
		Render(title + "\n" + strings.Join(listLines, "\n"))

	// ---- RIGHT pane: preview (cover + full title + disambiguators) ----
	rightPane := PreviewBorderStyle.
		Width(m.width - m.listWidth).
		Height(m.paneHeight - 2).
		Render(m.renderPreview())

	help := HelpStyle.Render("Resolve AniDB id — pick the matching series   j/k nav   Enter select   Esc cancel")
	header := HeaderStyle.Render("Could not auto-resolve " + quote(m.header) + " — choose from AnimeTosho:")
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	return lipgloss.JoinVertical(lipgloss.Left, header, panes, help)
}

// renderPreview builds the focused series' detail: cover placeholder on top, then
// the full title and the disambiguators (aid, torrent count, latest-release date,
// year, episode count).
func (m *seriesPicker) renderPreview() string {
	cur := m.series[m.cursor]
	width := m.width - m.listWidth - 2
	if width < 12 {
		width = 12
	}

	lines := make([]string, 0, m.coverRows+8)
	if m.coverText != "" {
		lines = append(lines, strings.Split(m.coverText+"\x1b[0m", "\n")...)
	}

	lines = append(lines, TitleStyle.Render(wrap(cur.Title, width)))

	meta := []string{fmt.Sprintf("aid %d", cur.AnidbAID)}
	if cur.TorrentCount > 0 {
		meta = append(meta, fmt.Sprintf("%d torrents", cur.TorrentCount))
	}
	if lr := strings.TrimSpace(cur.LatestRelease); lr != "" {
		meta = append(meta, "latest "+strings.Fields(lr)[0])
	}
	lines = append(lines, FaintStyle.Render(strings.Join(meta, "  ·  ")))

	if year, ok := m.year[cur.AnidbAID]; ok && year != "" {
		info := year
		if eps, ok := m.episodes[cur.AnidbAID]; ok && eps > 0 {
			info += fmt.Sprintf("  ·  %d episodes", eps)
		}
		lines = append(lines, FaintStyle.Render(info))
	}

	return fitPaneHeight(strings.Join(padToHeight(lines, m.paneHeight-2), "\n"), m.paneHeight-2)
}

// ---- layout / scroll ----

func (m *seriesPicker) recomputeLayout() {
	listW := m.width * 42 / 100
	if listW < 24 {
		listW = 24
	}
	if m.width-listW < 30 {
		listW = max(10, m.width-30)
	}
	m.listWidth = listW
	m.paneHeight = m.height - 3 // header (1) + help (1) + margin
	if m.paneHeight < 3 {
		m.paneHeight = 3
	}
	previewContentW := m.width - listW - 2
	m.coverCols = clamp(previewContentW, 8, 36)
	contentH := m.paneHeight - 2
	cr := contentH - 8
	if cr > 14 {
		cr = 14
	}
	if cr < 1 {
		cr = 1
	}
	m.coverRows = cr
}

func (m *seriesPicker) pageSize() int {
	if m.height == 0 {
		return 20
	}
	ps := m.paneHeight - 3 // border (2) + title line (1)
	if ps < 1 {
		ps = 1
	}
	return ps
}

func (m *seriesPicker) fixScroll() {
	m.topItem = clampTop(m.cursor, m.topItem, m.pageSize(), len(m.series))
}

// Cleanup removes downloaded cover temp files.
func (m *seriesPicker) Cleanup() {
	for _, p := range m.coverFiles {
		if p != "" {
			os.Remove(p)
		}
	}
}

// downloadCoverFile fetches a cover URL into a temp file and returns its path
// ("" on any failure).
func downloadCoverFile(url string) string {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	tmp, err := os.CreateTemp("", "ani-seriescover-*"+coverExt(url))
	if err != nil {
		return ""
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return ""
	}
	tmp.Close()
	return tmp.Name()
}

func quote(s string) string { return "\"" + s + "\"" }
