package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"os"
	"os/exec"
)

// thumbHeightPx is the downscale target; ~12 terminal cells tall at the usual
// font size. Override not exposed yet — easy to make a flag if needed.
const thumbHeightPx = 240

// kittyActive reports whether we're running inside kitty with the kitten CLI
// available and stdout attached to a terminal.
func kittyActive() bool {
	if os.Getenv("KITTY_WINDOW_ID") == "" && os.Getenv("KITTY_PID") == "" {
		return false
	}
	if _, err := exec.LookPath("kitten"); err != nil {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// printCover fetches the cover at picURL, downscales it, and renders it inline
// at the cursor via `kitten icat` (kitty's own tool — clears correctly across
// tmux pane/window switches, unlike raw kitty-graphics APC).
func printCover(picURL string) error {
	req, err := http.NewRequest(http.MethodGet, picURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ani/0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cover: %s", resp.Status)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return err
	}
	small := downscaleNN(img, thumbHeightPx)

	var buf bytes.Buffer
	if err := png.Encode(&buf, small); err != nil {
		return err
	}
	cmd := exec.Command("kitten", "icat")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = &buf, os.Stdout, os.Stderr
	return cmd.Run()
}

// downscaleNN nearest-neighbor downscales src to newH pixels tall, preserving
// aspect ratio. Good enough for a small thumbnail.
func downscaleNN(src image.Image, newH int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sh == 0 {
		return src
	}
	newW := sw * newH / sh
	if newW < 1 {
		newW = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, newW, newH))
	for dy := 0; dy < newH; dy++ {
		sy := b.Min.Y + dy*sh/newH
		for dx := 0; dx < newW; dx++ {
			sx := b.Min.X + dx*sw/newW
			dst.Set(dx, dy, src.At(sx, sy))
		}
	}
	return dst
}
