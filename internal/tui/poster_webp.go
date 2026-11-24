package tui

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os/exec"
	"time"
)

// Validate dimensions before handing compressed data to any renderer. Trakt
// normally serves WebP: Kitty can render it natively without a Go decoder.
func posterSize(b []byte) (w, h int, webp bool, err error) {
	bad := errors.New("unsupported poster image")
	if len(b) > maxPosterBytes {
		return 0, 0, false, bad
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		cfg, _, e := image.DecodeConfig(bytes.NewReader(b))
		if e != nil || !validPosterSize(cfg.Width, cfg.Height) {
			return 0, 0, false, bad
		}
		return cfg.Width, cfg.Height, false, nil
	}
	if uint64(binary.LittleEndian.Uint32(b[4:8]))+8 != uint64(len(b)) {
		return 0, 0, true, bad
	}
	var canvasW, canvasH int
	for rest := b[12:]; len(rest) > 0; {
		if len(rest) < 8 {
			return 0, 0, true, bad
		}
		n := uint64(binary.LittleEndian.Uint32(rest[4:8]))
		padded := n + n%2
		if padded > uint64(len(rest)-8) {
			return 0, 0, true, bad
		}
		p := rest[8 : 8+int(n)]
		switch string(rest[:4]) {
		case "VP8X":
			if len(p) != 10 || p[0]&2 != 0 {
				return 0, 0, true, bad
			}
			uint24 := func(v []byte) int { return int(v[0]) | int(v[1])<<8 | int(v[2])<<16 }
			canvasW, canvasH = 1+uint24(p[4:7]), 1+uint24(p[7:10])
			if !validPosterSize(canvasW, canvasH) {
				return 0, 0, true, bad
			}
		case "VP8 ":
			if w != 0 || len(p) < 10 || p[0]&1 != 0 || !bytes.Equal(p[3:6], []byte{0x9d, 1, 0x2a}) {
				return 0, 0, true, bad
			}
			w = int(binary.LittleEndian.Uint16(p[6:8]) & 0x3fff)
			h = int(binary.LittleEndian.Uint16(p[8:10]) & 0x3fff)
		case "VP8L":
			if w != 0 || len(p) < 5 || p[0] != 0x2f {
				return 0, 0, true, bad
			}
			bits := binary.LittleEndian.Uint32(p[1:5])
			w = 1 + int(bits&0x3fff)
			h = 1 + int((bits>>14)&0x3fff)
		case "ANIM", "ANMF":
			return 0, 0, true, bad
		}
		rest = rest[8+int(padded):]
	}
	if !validPosterSize(w, h) || (canvasW != 0 && (w != canvasW || h != canvasH)) {
		return 0, 0, true, bad
	}
	return w, h, true, nil
}
func validPosterSize(w, h int) bool { return w > 0 && h > 0 && w <= 2048 && h <= 2048 }

// Standard-library image decoders cover PNG/JPEG. For terminal text pixels,
// ask the installed ffmpeg to decode just one small WebP frame from stdin;
// it receives no URL, account headers, filenames or external inputs.
func webpThumbnail(ctx context.Context, b []byte, w, h, cols, rows int) (image.Image, error) {
	if cols <= 0 || rows <= 0 {
		return nil, errors.New("no poster area")
	}
	cols = min(80, cols)
	rows = min(40, rows)
	ow := min(cols, max(1, (2*rows*w+h/2)/h))
	oh := min(2*rows, max(1, (ow*h+w/2)/w))
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "pipe", "-f", "webp_pipe", "-threads", "1", "-i", "pipe:0", "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:%d", ow, oh), "-pix_fmt", "rgba", "-threads", "1", "-f", "rawvideo", "pipe:1")
	cmd.Stdin = bytes.NewReader(b)
	buf, err := cmd.Output()
	if err != nil || len(buf) != 4*ow*oh {
		return nil, errors.New("could not decode WebP poster with ffmpeg")
	}
	return &image.RGBA{Pix: buf, Stride: 4 * ow, Rect: image.Rect(0, 0, ow, oh)}, nil
}
