package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"ani/internal/trakt"
)

const maxPosterBytes = 2 << 20

var moviePosterHTTP = &http.Client{
	Timeout: 6 * time.Second,
	CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !trakt.ValidPosterURL(r.URL.String()) {
			return errors.New("unexpected poster redirect")
		}
		return nil
	},
}

// The poster CDN is not a media provider: never attach a page referrer, account
// headers, or an app key. Bound both the compressed download and decoded image.
func fetchMoviePoster(ctx context.Context, url string) ([]byte, error) {
	return cachedMoviePoster(ctx, url, moviePosterHTTP, posterCacheDir())
}

func cachedMoviePoster(ctx context.Context, url string, client *http.Client, dir string) ([]byte, error) {
	if !trakt.ValidPosterURL(url) {
		return nil, errors.New("invalid poster URL")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b := readCachedPoster(dir, url); b != nil {
		return b, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poster HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxPosterBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPosterBytes {
		return nil, errors.New("poster too large")
	}
	if _, _, _, err := posterSize(b); err != nil {
		return nil, err
	}
	cachePoster(dir, url, b)
	return b, nil
}

func renderMoviePoster(ctx context.Context, b []byte, cols, rows int) ([][]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	w, h, webp, err := posterSize(b)
	if err != nil {
		return nil, "", err
	}
	// Use Anime's full-resolution Kitty renderer where supported. Other terminals
	// still show an actual thumbnail rather than blank Kitty placeholder cells.
	if useNativeMoviePoster(ctx) {
		upload, text, e := coverPlaceholder(ctx, "", bytes.NewReader(b), cols, rows)
		if e == nil && len(upload) > 0 && strings.TrimSpace(text) != "" {
			return upload, text, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if webp {
		img, err := webpThumbnail(ctx, b, w, h, cols, rows)
		if err != nil {
			return nil, "", err
		}
		return nil, blockPoster(img, cols, rows, os.Getenv("NO_COLOR") == ""), nil
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, "", err
	}
	return nil, blockPoster(img, cols, rows, os.Getenv("NO_COLOR") == ""), nil
}

// A Kitty-capable outer terminal doesn't guarantee image passthrough in
// tmux. Use text pixels when tmux would silently discard the graphics upload.
func useNativeMoviePoster(ctx context.Context) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	program := os.Getenv("TERM_PROGRAM")
	native := os.Getenv("KITTY_WINDOW_ID") != "" || graphicsTerm(os.Getenv("TERM")) || program == "ghostty" || program == "WezTerm"
	if os.Getenv("TMUX") == "" {
		return native
	}
	// TERM_PROGRAM usually describes tmux, not the outer terminal. And a
	// plain `show-options` reports nothing for an inherited pane option.
	// Resolve both in the pane that will actually receive the upload.
	args := []string{"display-message", "-p"}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		args = append(args, "-t", pane)
	}
	args = append(args, "#{client_termname}\t#{allow-passthrough}")
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	v, err := exec.CommandContext(ctx, "tmux", args...).Output()
	outer, pass, ok := strings.Cut(strings.TrimRight(string(v), "\r\n"), "\t")
	return err == nil && ok && (pass == "on" || pass == "all") && (native || graphicsTerm(outer))
}

func graphicsTerm(term string) bool {
	switch term {
	case "xterm-kitty", "xterm-ghostty", "wezterm":
		return true
	}
	return false
}

// Each terminal cell is about two pixels tall. Use its foreground and background
// as the upper/lower pixels, preserving the poster's aspect ratio. NO_COLOR gets
// a luminance thumbnail instead of color escapes.
func blockPoster(img image.Image, cols, rows int, color bool) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return ""
	}
	cols = min(cols, max(1, (2*rows*w+h/2)/h))
	rows = min(rows, max(1, (cols*h+w)/(2*w)))
	var out strings.Builder
	const ramp = " .:-=+*#%@"
	pixel := func(x, y int) (int, int, int) {
		// Average four samples instead of dropping thin high-contrast detail.
		var r, g, b uint32
		for _, dy := range []int{1, 3} {
			for _, dx := range []int{1, 3} {
				cr, cg, cb, _ := img.At(bounds.Min.X+(4*x+dx)*w/(4*cols), bounds.Min.Y+(4*y+dy)*h/(8*rows)).RGBA()
				r += cr
				g += cg
				b += cb
			}
		}
		return int(r >> 10), int(g >> 10), int(b >> 10)
	}
	for y := 0; y < rows; y++ {
		if y != 0 {
			out.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			r, g, b := pixel(x, 2*y)
			rr, gg, bb := pixel(x, 2*y+1)
			if color {
				fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", r, g, b, rr, gg, bb)
			} else {
				l := (30*(r+rr) + 59*(g+gg) + 11*(b+bb)) / 200
				out.WriteByte(ramp[l*(len(ramp)-1)/255])
			}
		}
		if color {
			out.WriteString("\x1b[0m")
		}
	}
	return out.String()
}
