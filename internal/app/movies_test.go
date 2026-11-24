package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ani/internal/flixhq"
	"ani/internal/playable"
)

type catalogStub struct {
	titles           []flixhq.Title
	episodes         []flixhq.Episode
	searched, opened string
}

func (c *catalogStub) RecentlyAdded(context.Context) ([]flixhq.Title, error) {
	c.searched = "<recent>"
	return c.titles, nil
}

func (c *catalogStub) Search(_ context.Context, q string) ([]flixhq.Title, error) {
	c.searched = q
	return c.titles, nil
}
func (c *catalogStub) Episodes(_ context.Context, t flixhq.Title) ([]flixhq.Episode, error) {
	c.opened = t.URL
	return c.episodes, nil
}

func TestMovieDryRunSelectionAndPlayback(t *testing.T) {
	for _, tc := range []struct{ kind, page, label string }{
		{"movie", "movie-page", "1917"}, {"series", "ep-page", "1917 S02E01 · Pilot"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			c := &catalogStub{titles: []flixhq.Title{{Name: "1917", Kind: tc.kind, URL: "movie-page"}}, episodes: []flixhq.Episode{{Season: 2, Number: 1, Name: "Pilot", URL: "ep-page"}}}
			played := false
			err := dryRunMovies(c, "1917", func(title flixhq.Title, ep *flixhq.Episode) error {
				r, err := resolveMovie(context.Background(), func(_ context.Context, p string) (*playable.Release, error) {
					if p != tc.page {
						t.Fatalf("resolved %q not %q", p, tc.page)
					}
					return &playable.Release{StreamURL: "embed", Referer: p, Group: "Vidmoly"}, nil
				}, title, ep)
				if err != nil {
					return err
				}
				played = true
				if r.Title != tc.label || r.Referer != tc.page || r.StreamURL != "embed" {
					t.Fatalf("unexpected player inputs: %+v", r)
				}
				return nil
			})
			if err != nil || !played || c.searched != "1917" || (tc.kind == "movie" && c.opened != "") {
				t.Fatalf("wrong flow: %+v %v", c, err)
			}
		})
	}
}

func TestMovieDryRunErrors(t *testing.T) {
	c := &catalogStub{}
	never := func(flixhq.Title, *flixhq.Episode) error { t.Fatal("unexpected playback"); return nil }
	if err := dryRunMovies(c, "", never); err == nil || c.searched != "<recent>" || !strings.Contains(err.Error(), "Recently Added") {
		t.Fatalf("empty Recently Added: %v", err)
	}
	if err := dryRunMovies(c, "absent", never); err == nil {
		t.Fatal("expected empty search error")
	}
	c.titles = []flixhq.Title{{Name: "Series", Kind: "series"}}
	if err := dryRunMovies(c, "series", never); err == nil {
		t.Fatal("expected empty episode error")
	}
	dead := errors.New("no Vidmoly server")
	_, err := resolveMovie(context.Background(), func(context.Context, string) (*playable.Release, error) { return nil, dead }, c.titles[0], nil)
	if !errors.Is(err, dead) {
		t.Fatalf("resolver error lost: %v", err)
	}
}
