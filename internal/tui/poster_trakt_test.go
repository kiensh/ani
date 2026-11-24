package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ani/internal/trakt"
)

// A synthetic 16x24 color grid encoded as lossless WebP (no third-party art).
const gridWebP = "UklGRmoAAABXRUJQVlA4TF4AAAAvD8AFAHAVSZJUlTOs4AwrOEtueOc3grZtEyYb05/pz8QG2UYa2pu82ps8Wvs/EVWVklJEpUSIAO8RUQqRUulILiSgglyqqnv4u9KoWV5OJo1OpgxpdfXls+/38iMA"

type posterRT func(*http.Request) (*http.Response, error)

func (f posterRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTraktPosterReadThroughCache(t *testing.T) {
	const u = "https://media.trakt.tv/shows/1388/medium/poster.webp"
	if !trakt.ValidPosterURL(u) {
		t.Fatal("public Trakt poster route not recognized")
	}
	for _, bad := range []string{"https://media.trakt.tv.evil.test/shows/1388/medium/poster.webp", u + "?token=x", "https://media.trakt.tv/shows/1388/medium/../poster.webp"} {
		if trakt.ValidPosterURL(bad) {
			t.Fatalf("invalid poster accepted: %s", bad)
		}
	}
	b, err := base64.StdEncoding.DecodeString(gridWebP)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: posterRT(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != u || r.Header.Get("Authorization") != "" || r.Header.Get("trakt-api-key") != "" || r.Referer() != "" {
			t.Fatal("unexpected artwork request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header), Request: r}, nil
	})}
	dir := t.TempDir()
	for range 2 {
		got, err := cachedMoviePoster(t.Context(), u, client, dir)
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("poster cache: %v", err)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.img"))
	if err != nil || len(files) != 1 || calls != 1 {
		t.Fatalf("cache: requests=%d files=%v err=%v", calls, files, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cachedMoviePoster(ctx, u, client, dir); err == nil {
		t.Fatal("read canceled poster")
	}
}

func TestTraktWebPThumbnail(t *testing.T) {
	b, _ := base64.StdEncoding.DecodeString(gridWebP)
	w, h, webp, err := posterSize(b)
	if err != nil || !webp || w != 16 || h != 24 {
		t.Fatalf("WebP dimensions: %dx%d %v", w, h, err)
	}
	bad := append([]byte(nil), b...)
	bad[21], bad[22] = 0xff, 0xff
	for _, input := range [][]byte{b[:20], bad, []byte("<html>error</html>")} {
		if _, _, _, err := posterSize(input); err == nil {
			t.Fatal("invalid or oversized image accepted")
		}
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TMUX", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("NO_COLOR", "")
	_, art, err := renderMoviePoster(t.Context(), b, 20, 6)
	if err != nil || !strings.Contains(art, "▀") {
		t.Fatalf("no WebP thumbnail: %v", err)
	}
}
