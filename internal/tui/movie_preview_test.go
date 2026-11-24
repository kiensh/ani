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

	"ani/internal/flixhq"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type previewCatalog struct {
	movieFixture
	calls []string
}

func (c *previewCatalog) Details(_ context.Context, t flixhq.Title) (flixhq.Details, error) {
	c.calls = append(c.calls, t.URL)
	return flixhq.Details{Description: "Synopsis for " + t.Name, Genres: "Drama", Rating: "8.4", Duration: "92 min"}, nil
}

func TestMoviePreviewFollowsFocusAndCachesDetails(t *testing.T) {
	cat := &previewCatalog{}
	m := &moviePicker{MovieState: &MovieState{stage: movieTitles, titles: []flixhq.Title{{Name: "First", Kind: "movie", URL: "/first"}, {Name: "Second", Kind: "series", Year: "2019", URL: "/second"}}}, catalog: cat}
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
	m := &moviePicker{MovieState: &MovieState{stage: movieTitles, view: movieWatchlist, titles: []flixhq.Title{{Name: "A Private Title", Kind: "movie", Year: "2024"}}}, catalog: cat}
	for _, width := range []int{44, 80, 118} {
		_, cmd := m.Update(tea.WindowSizeMsg{Width: width, Height: 26})
		if cmd != nil {
			t.Fatal("watchlist preview should not resolve a private title through FlixHQ")
		}
		panes := m.browsePanes("My Watchlist")
		lines := strings.Split(panes, "\n")
		if lipgloss.Width(panes) > width {
			t.Fatalf("%d-cell window overflow: %d", width, lipgloss.Width(panes))
		}
		if !strings.Contains(panes, "A Private Title") || !strings.Contains(panes, "2024") {
			t.Fatal("missing immediate title details")
		}
		if side := strings.Count(lines[0], "╭") == 2; side != (width >= 66) {
			t.Fatalf("wrong layout at width %d: %s", width, lines[0])
		}
	}
}

func TestMovieTerminalPoster(t *testing.T) {
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
