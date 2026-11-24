package app

import (
	"context"
	"fmt"
	"io"
	"os"

	"ani/internal/flixhq"
	"ani/internal/playable"
)

type movieSubtitleSource interface {
	Resolve(context.Context, string) (*playable.Release, error)
	ResolveVideoOnly(context.Context, string) (*playable.Release, error)
	FetchEnglishVTT(context.Context, string) ([]byte, error)
}

// Fetch provider captions through the bounded, allowlisted client, then pass
// a local file to mpv. A user-supplied file bypasses subtitle lookup entirely.
func playCaptionedMovie(ctx context.Context, c movieSubtitleSource, t flixhq.Title, ep *flixhq.Episode, local string, out io.Writer, play func(*playable.Release) error) error {
	resolve := c.Resolve
	if local != "" {
		resolve = c.ResolveVideoOnly
	}
	r, err := resolveMovie(ctx, resolve, t, ep)
	if err != nil {
		return err
	}
	track := r.SubtitleURL
	r.SubtitleURL = local
	switch {
	case local != "":
		fmt.Fprintln(out, "Subtitles: using your selected file.")
	case track == "":
		fmt.Fprintln(out, "No external English captions found. Use s in Movies & TV to play with your own subtitle file.")
	default:
		data, err := c.FetchEnglishVTT(ctx, track)
		if err == nil {
			var path string
			path, err = saveEnglishVTT(data)
			if err == nil {
				r.SubtitleURL = path
				defer os.Remove(path)
			}
		}
		if err != nil {
			fmt.Fprintln(out, "Could not load English captions; continuing without an external track. Use s to attach a subtitle file.")
		} else {
			fmt.Fprintln(out, "Subtitles: English (provider).")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return play(r)
}

func saveEnglishVTT(data []byte) (string, error) {
	f, err := os.CreateTemp("", "ani-sub-*.en.vtt")
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(f.Name())
		if writeErr != nil {
			return "", writeErr
		}
		return "", closeErr
	}
	return f.Name(), nil
}
