package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"ani/internal/flixhq"
	"ani/internal/playable"
	"ani/internal/trakt"
)

// Deliberately not a browsing catalog. These methods are used only on play.
type moviePlaybackProvider interface {
	movieSubtitleSource
	Search(context.Context, string) ([]flixhq.Title, error)
	Episodes(context.Context, flixhq.Title) ([]flixhq.Episode, error)
}

// A choice is a one-play provider row index, never a new Trakt identity. A
// negative index cancels; a nil chooser means noninteractive, no guessing.
type choosePlaybackSource func(subject string, sources []string) (int, error)

// Normalize case and whitespace only: don't discard punctuation, numbers,
// articles, or qualifiers that may distinguish different works.
func sameMovieName(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
}

func playTraktMovie(ctx context.Context, p moviePlaybackProvider, title trakt.Title, ep *trakt.Episode, choose choosePlaybackSource, local string, out io.Writer, play func(*playable.Release) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if title.ID <= 0 || strings.TrimSpace(title.Name) == "" {
		return errors.New("playback requires an identified Trakt title")
	}
	switch title.Kind {
	case "movie":
		if ep != nil {
			return errors.New("cannot play an episode of a Trakt movie")
		}
	case "series":
		if ep == nil || ep.Season < 0 || ep.Number <= 0 {
			return errors.New("choose a specific Trakt episode to play")
		}
	default:
		return errors.New("playback requires a Trakt movie or series")
	}

	rows, err := p.Search(ctx, title.Name)
	if err != nil {
		return fmt.Errorf("FlixHQ playback search: %w", err)
	}
	raw, err := matchProviderTitle(title, ep, rows, choose)
	if err != nil {
		return err
	}
	var sourceEp *flixhq.Episode
	if ep != nil {
		episodes, err := p.Episodes(ctx, raw)
		if err != nil {
			return fmt.Errorf("FlixHQ episode lookup: %w", err)
		}
		for _, e := range episodes {
			if e.Season != ep.Season || e.Number != ep.Number {
				continue
			}
			if sourceEp != nil || e.URL == "" {
				return fmt.Errorf("FlixHQ: cannot verify a unique playback page for %s S%02dE%02d", title.Name, ep.Season, ep.Number)
			}
			e.Name = ep.Name // mpv uses the selected Trakt episode label.
			sourceEp = &e
		}
		if sourceEp == nil {
			return fmt.Errorf("FlixHQ: %s S%02dE%02d is not available", title.Name, ep.Season, ep.Number)
		}
	}
	raw.Name = title.Name // Provider pages are transport details, not tracking IDs.
	return playCaptionedMovie(ctx, p, raw, sourceEp, local, out, play)
}

func matchProviderTitle(t trakt.Title, ep *trakt.Episode, rows []flixhq.Title, choose choosePlaybackSource) (flixhq.Title, error) {
	// FlixHQ search exposes no IMDb/TMDB IDs. Slugs, poster paths and Trakt
	// numeric IDs are not shared IDs; never use them as cross-catalog evidence.
	var candidates []flixhq.Title
	year := strconv.Itoa(t.Year)
	yearConflict := false
	for _, row := range rows {
		if row.Kind != t.Kind || !sameMovieName(row.Name, t.Name) {
			continue
		}
		if t.Year > 0 && row.Year != "" && row.Year != year {
			yearConflict = true
			continue // A known remake is not even a manual choice for this title.
		}
		if row.URL == "" {
			return flixhq.Title{}, fmt.Errorf("FlixHQ: missing playback page for %q", t.Name)
		}
		candidates = append(candidates, row)
	}
	switch len(candidates) {
	case 0:
		if yearConflict {
			return flixhq.Title{}, fmt.Errorf("FlixHQ: no %s named %q matches the Trakt release year %d", t.Kind, t.Name, t.Year)
		}
		return flixhq.Title{}, fmt.Errorf("FlixHQ: no %s with the exact Trakt title %q", t.Kind, t.Name)
	case 1:
		if t.Year > 0 && candidates[0].Year == year {
			return candidates[0], nil
		}
	}
	if choose == nil {
		if len(candidates) == 1 {
			return flixhq.Title{}, fmt.Errorf("FlixHQ: cannot verify the release year for %q; inspect the source in Movies & TV instead", t.Name)
		}
		return flixhq.Title{}, fmt.Errorf("FlixHQ: %d possible playback sources for %q; choose one in Movies & TV instead", len(candidates), t.Name)
	}
	if t.Year <= 0 {
		year = "year unknown"
	}
	subject := fmt.Sprintf("%s (%s, %s)", t.Name, t.Kind, year)
	if ep != nil {
		subject += fmt.Sprintf(" S%02dE%02d", ep.Season, ep.Number)
	}
	labels := make([]string, len(candidates))
	for i, c := range candidates {
		y := c.Year
		if y == "" {
			y = "year unknown"
		}
		// Use the real provider route to distinguish otherwise identical rows.
		labels[i] = fmt.Sprintf("%s · %s · %s", y, strings.TrimPrefix(c.URL, "https://"), c.Name)
	}
	i, err := choose(subject, labels)
	if err != nil {
		return flixhq.Title{}, err
	}
	if i < 0 {
		return flixhq.Title{}, errors.New("FlixHQ playback canceled")
	}
	if i >= len(candidates) {
		return flixhq.Title{}, errors.New("FlixHQ: invalid playback source choice")
	}
	return candidates[i], nil
}
