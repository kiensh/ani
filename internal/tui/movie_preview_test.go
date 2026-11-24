package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type previewCatalog struct {
	movieFixture
	calls []int
}

func (c *previewCatalog) TitleDetails(_ context.Context, t trakt.Title) (trakt.Title, error) {
	c.calls = append(c.calls, t.ID)
	t.Overview, t.Genres, t.Rating, t.Runtime = "Synopsis for "+t.Name, "Drama", 8.4, 92
	return t, nil
}

func TestMoviePreviewFollowsFocusAndCachesDetails(t *testing.T) {
	cat := &previewCatalog{}
	m := &moviePicker{MovieState: &MovieState{stage: movieTitles, titles: []trakt.Title{{Name: "First", Kind: "movie", ID: 15}, {Name: "Second", Kind: "series", Year: 2019, ID: 15}}}, catalog: cat}
	t.Cleanup(m.stopPreview)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	firstID := m.preview.generation
	firstCtx := m.preview.ctx
	_, fetch := m.Update(moviePreviewDue(firstID))
	if fetch == nil {
		t.Fatal("missing detail request")
	}
	firstResult := fetch()
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if firstCtx.Err() == nil {
		t.Fatal("old preview not canceled")
	}
	m.Update(firstResult)
	m.Update(movieArtMsg{generation: firstID, text: "STALE POSTER"})
	if strings.Contains(m.View(), "Synopsis for First") || strings.Contains(m.View(), "STALE POSTER") {
		t.Fatal("stale preview visible after cursor movement")
	}
	id := m.preview.generation
	_, cmd := m.Update(moviePreviewDue(id))
	m.Update(cmd())
	m.Update(movieArtMsg{generation: id, text: "NEW POSTER"})
	view := m.View()
	for _, want := range []string{"NEW POSTER", "Synopsis for Second", "Drama", "TV series", "2019", "92 min", "8.4"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, again := m.Update(moviePreviewDue(m.preview.generation))
	if again != nil || len(cat.calls) != 2 {
		t.Fatal("freshly cached details were requested again")
	}
	m.Update(movieArtMsg{generation: m.preview.generation, err: errors.New("image unavailable")})
	if !strings.Contains(m.View(), "Synopsis for Second") || !strings.Contains(m.View(), "Poster unavailable") {
		t.Fatal("poster failure obscured text details")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.preview.cancel != nil {
		t.Fatal("leaving Movies & TV kept preview work alive")
	}
}

func TestMoviePreviewLayoutsAndWatchlistPrivacy(t *testing.T) {
	cat := &previewCatalog{}
	m := &moviePicker{MovieState: &MovieState{stage: movieTitles, view: movieWatchlist, titles: []trakt.Title{{Name: "A Private Title", Kind: "movie", Year: 2024, ID: 42, Overview: "Trakt overview", PosterURL: "https://media.trakt.tv/images/movies/000/000/042/posters/thumb/abc.jpg.webp"}}}, catalog: cat}
	t.Cleanup(m.stopPreview)
	for _, width := range []int{44, 80, 118} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 26})
		if m.preview.focus.title != (previewID{"movie", 42}) {
			t.Fatal("watchlist preview must use the existing Trakt identity")
		}
		_, cmd := m.Update(moviePreviewDue(m.preview.generation))
		if (cmd == nil && m.preview.art == "") || !m.preview.posterStarted || len(cat.calls) > 0 {
			t.Fatal("full watchlist row should render its poster without another catalog lookup")
		}
		m.Update(movieArtMsg{generation: m.preview.generation, text: "WATCHLIST POSTER"})
		panes := m.browsePanes("My Watchlist")
		lines := strings.Split(panes, "\n")
		if lipgloss.Width(panes) > width {
			t.Fatalf("%d-cell window overflow: %d", width, lipgloss.Width(panes))
		}
		if !strings.Contains(panes, "A Private Title") || !strings.Contains(panes, "2024") || !strings.Contains(panes, "WATCHLIST POSTER") || !strings.Contains(panes, "Trakt overview") {
			t.Fatal("missing immediate title details")
		}
		if side := strings.Count(lines[0], "╭") == 2; side != (width >= 66) {
			t.Fatalf("wrong layout at width %d: %s", width, lines[0])
		}
	}
}

func TestSparseWatchlistRowKeepsInlineDetailsWhenArtArrives(t *testing.T) {
	item := trakt.Title{ID: 1388, Kind: "series", Name: "Example", Year: 2008, Overview: "Overview from the watchlist row"}
	m := &moviePicker{MovieState: &MovieState{stage: movieTitles, view: movieWatchlist, titles: []trakt.Title{item}}, catalog: &previewCatalog{}}
	t.Cleanup(m.stopPreview)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	if m.preview.cancel == nil {
		t.Fatal("a watchlist row missing art needs its Trakt summary")
	}
	_, art := m.Update(movieDetailMsg{generation: m.preview.generation, details: trakt.Title{ID: item.ID, Kind: item.Kind, PosterURL: "https://media.trakt.tv/shows/1388/medium/poster.webp"}})
	if art == nil || !m.preview.posterStarted || !strings.Contains(m.View(), item.Overview) {
		t.Fatal("did not hydrate the watchlist poster while preserving its synopsis")
	}
}

func TestMovieTerminalPoster(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	img := image.NewRGBA(image.Rect(0, 0, 4, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: uint8(y * 30), G: 200, B: 20, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	u, text, err := renderMoviePoster(t.Context(), buf.Bytes(), 10, 4)
	if err != nil || len(u) != 0 || !strings.Contains(text, "▀") || !strings.Contains(text, "\x1b[38;2;") || lipgloss.Width(text) != 4 || lipgloss.Height(text) != 4 {
		t.Fatalf("half-block thumbnail: %q, %v", text, err)
	}
	t.Setenv("NO_COLOR", "1")
	_, text, err = renderMoviePoster(t.Context(), buf.Bytes(), 10, 4)
	if err != nil || strings.Contains(text, "\x1b") || strings.TrimSpace(text) == "" {
		t.Fatalf("plain thumbnail: %q, %v", text, err)
	}
	if _, _, err := renderMoviePoster(t.Context(), []byte("<html>not an image</html>"), 12, 6); err == nil {
		t.Fatal("accepted non-image data")
	}
	bad := "https://example.test/thumbnail.jpg"
	if _, err := fetchMoviePoster(t.Context(), bad); err == nil {
		t.Fatal("accepted unknown initial poster host")
	}
	u2, _ := url.Parse(bad)
	if err := moviePosterHTTP.CheckRedirect(&http.Request{URL: u2}, nil); err == nil {
		t.Fatal("accepted redirect to unknown host")
	}
}
