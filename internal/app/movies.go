package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"ani/internal/flixhq"
	"ani/internal/playable"
	"ani/internal/player"
	"ani/internal/trakt"
	"ani/internal/tui"
)

// browseMovies never changes the anime provider or writes progress to MAL. The
// catalog supplies a movie/episode page; only that selected page is resolved.
func browseMovies(opt *Options, query string) error {
	c := flixhq.New()
	play := func(t flixhq.Title, ep *flixhq.Episode) error {
		if !opt.DryRun {
			if _, err := exec.LookPath("yt-dlp"); err != nil {
				return fmt.Errorf("FlixHQ playback needs yt-dlp on PATH (used by mpv)")
			}
			fmt.Fprintf(os.Stderr, "FlixHQ: finding Vidmoly stream for %s…\n", t.Name)
		}
		r, err := resolveMovie(context.Background(), c.Resolve, t, ep)
		if err != nil {
			return err
		}
		AnnouncePick(r)
		if err := player.RunPlayURL(r.StreamURL, r.Title, r.Referer, r.SubtitleURL, opt.Player, opt.DryRun); err != nil {
			return fmt.Errorf("Vidmoly playback: %w (the link may be unavailable; use mpv with an up-to-date yt-dlp)", err)
		}
		return nil
	}
	if opt.DryRun {
		return dryRunMovies(c, query, play)
	}
	var tracker tui.MovieTracker
	if account := trakt.New(); account.Connected() {
		tracker = account
	}
	return tui.RunMoviePicker(query, c, play, tracker)
}

func resolveMovie(ctx context.Context, resolve func(context.Context, string) (*playable.Release, error), t flixhq.Title, ep *flixhq.Episode) (*playable.Release, error) {
	page, label := t.URL, t.Name
	if ep != nil {
		page, label = ep.URL, fmt.Sprintf("%s S%02dE%02d", t.Name, ep.Season, ep.Number)
		if ep.Name != "" {
			label += " · " + ep.Name
		}
	}
	r, err := resolve(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("FlixHQ: %s: %w", label, err)
	}
	r.Title = label
	return r, nil
}

// As in anime mode, dry-run resolves the first result without starting a TUI
// or executing a player. Numeric titles remain movie searches, not AniDB IDs.
func dryRunMovies(c tui.MovieCatalog, query string, play func(flixhq.Title, *flixhq.Episode) error) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("--movies --dry-run requires a title")
	}
	ctx := context.Background()
	titles, err := c.Search(ctx, query)
	if err != nil {
		return err
	}
	if len(titles) == 0 {
		return fmt.Errorf("no movies or series found for %q", query)
	}
	t := titles[0]
	if t.Kind != "series" {
		return play(t, nil)
	}
	eps, err := c.Episodes(ctx, t)
	if err != nil {
		return err
	}
	if len(eps) == 0 {
		return fmt.Errorf("no episodes listed for %q", t.Name)
	}
	return play(t, &eps[0])
}
