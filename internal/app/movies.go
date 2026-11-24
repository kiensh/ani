package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"ani/internal/browser"
	"ani/internal/flixhq"
	"ani/internal/playable"
	"ani/internal/player"
	"ani/internal/trakt"
	"ani/internal/tui"
)

// The picker only sees Trakt. The playback callback creates the FlixHQ client
// after the picker releases the terminal for an explicit movie or episode play.
func browseMovies(opt *Options, state *tui.MovieState) (tui.MediaSource, error) {
	catalog := trakt.New()
	play := func(t trakt.Title, ep *trakt.Episode, subtitleFile string) error {
		var choose choosePlaybackSource // A dry-run must never open a picker.
		if !opt.DryRun {
			if _, err := exec.LookPath("yt-dlp"); err != nil {
				return fmt.Errorf("FlixHQ playback needs yt-dlp on PATH (used by mpv)")
			}
			choose = tui.ChoosePlaybackSource
			fmt.Fprintf(os.Stderr, "FlixHQ: looking for a playback source for %s…\n", t.Name)
		}
		return playTraktMovie(context.Background(), flixhq.New(), t, ep, choose, subtitleFile, os.Stderr, func(r *playable.Release) error {
			AnnouncePick(r)
			if err := player.RunPlayURL(r.StreamURL, r.Title, r.Referer, r.SubtitleURL, opt.Player, opt.DryRun); err != nil {
				return fmt.Errorf("Vidmoly playback: %w (the link may be unavailable; use mpv with an up-to-date yt-dlp)", err)
			}
			return nil
		})
	}
	if opt.DryRun {
		return "", dryRunMovies(catalog, opt.Query, play)
	}
	return tui.RunMoviePicker(opt.Query, catalog, play, catalog, browser.Open, state)
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

// Dry-run has no selection UI. Never guess a Trakt identity from its search
// rank, or a series premiere from the order of the returned episodes.
func dryRunMovies(c tui.MovieCatalog, query string, play func(trakt.Title, *trakt.Episode, string) error) error {
	ctx := context.Background()
	query = strings.TrimSpace(query)
	if query == "" {
		fmt.Fprintln(os.Stderr, "DRY-RUN: Movies & TV opens on My Watchlist. Pass an exact title to preview a player command.")
		return nil
	}
	titles, err := c.SearchTitles(ctx, query)
	if err != nil {
		return err
	}
	var title *trakt.Title
	for i := range titles {
		if !sameMovieName(query, titles[i].Name) {
			continue
		}
		if title != nil {
			return fmt.Errorf("Trakt: multiple exact matches for %q; select the title in Movies & TV instead", query)
		}
		title = &titles[i]
	}
	if title == nil || title.ID <= 0 {
		return fmt.Errorf("Trakt: no identified exact match for %q; select a title in Movies & TV instead", query)
	}
	switch title.Kind {
	case "movie":
		return play(*title, nil, "")
	case "series":
		eps, err := c.Episodes(ctx, *title)
		if err != nil {
			return err
		}
		var premiere *trakt.Episode
		for i := range eps {
			if eps[i].Season == 1 && eps[i].Number == 1 {
				if premiere != nil {
					return fmt.Errorf("Trakt: ambiguous S01E01 for %q", title.Name)
				}
				premiere = &eps[i]
			}
		}
		if premiere == nil {
			return fmt.Errorf("Trakt: no S01E01 for %q; select an episode in Movies & TV instead", title.Name)
		}
		return play(*title, premiere, "")
	default:
		return fmt.Errorf("Trakt: expected a movie or series for %q", title.Name)
	}
}
