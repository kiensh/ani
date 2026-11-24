package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"ani/internal/flixhq"
	"ani/internal/playable"
	"ani/internal/trakt"
)

type providerStub struct {
	movieSubStub
	rows                  []flixhq.Title
	eps                   []flixhq.Episode
	query, opened         string
	searchErr, episodeErr error
}

func (p *providerStub) Search(_ context.Context, q string) ([]flixhq.Title, error) {
	p.query = q
	return p.rows, p.searchErr
}
func (p *providerStub) Episodes(_ context.Context, t flixhq.Title) ([]flixhq.Episode, error) {
	p.opened = t.URL
	return p.eps, p.episodeErr
}

func TestProviderTitleMatchRequiresCorroboration(t *testing.T) {
	traktTitle := trakt.Title{Name: "The Fly", Kind: "movie", Year: 1986, ID: 45, IMDb: "tt0000045", TMDB: 78}
	movie := func(kind, name, year, path string) flixhq.Title {
		return flixhq.Title{Kind: kind, Name: name, Year: year, URL: path}
	}
	exact := movie("movie", "the   FLY", "1986", "right")
	remake := movie("movie", "The Fly", "1958", "remake")
	unknownYear := movie("movie", "The Fly", "", "unknown")
	// These provider IDs/slugs can look like external IDs but are not evidence.
	wrongKind := movie("series", "The Fly", "1986", "tt0000045")
	wrongTitle := movie("movie", "The Fly II", "1986", "78")
	for _, tc := range []struct {
		name  string
		tYear int
		rows  []flixhq.Title
		want  string
	}{
		{"exact across kind and remake", 1986, []flixhq.Title{wrongKind, remake, wrongTitle, exact}, "right"},
		{"unrelated singleton", 1986, []flixhq.Title{wrongTitle}, ""},
		{"conflicting kind", 1986, []flixhq.Title{wrongKind}, ""},
		{"conflicting year", 1986, []flixhq.Title{remake}, ""},
		{"missing provider year", 1986, []flixhq.Title{unknownYear}, ""},
		{"missing Trakt year", 0, []flixhq.Title{exact}, ""},
		{"matched and unknown years", 1986, []flixhq.Title{exact, unknownYear}, ""},
		{"ambiguous same year", 1986, []flixhq.Title{exact, movie("movie", "The Fly", "1986", "other")}, ""},
		{"missing page", 1986, []flixhq.Title{movie("movie", "The Fly", "1986", "")}, ""},
		{"no candidates", 1986, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title := traktTitle
			title.Year = tc.tYear
			got, err := matchProviderTitle(title, nil, tc.rows, nil)
			if got.URL != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("resolved %q, %v; want %q", got.URL, err, tc.want)
			}
		})
	}
}

func TestManualPlaybackChoiceOnlySelectsProviderPage(t *testing.T) {
	title := trakt.Title{Name: "Night Shift", Kind: "series", Year: 2018, ID: 307}
	ep := &trakt.Episode{Season: 0, Number: 2, Name: "Special", ID: 504}
	p := &providerStub{
		rows: []flixhq.Title{
			{Name: "Night Shift", Kind: "movie", Year: "2018", URL: "wrong-kind"},
			{Name: "Night Shift", Kind: "series", Year: "1978", URL: "remake"},
			{Name: "Night Shift", Kind: "series", Year: "2018", URL: "first"},
			{Name: "NIGHT SHIFT", Kind: "series", URL: "alternate"},
		},
		eps: []flixhq.Episode{{Season: 1, Number: 2, URL: "main-series"}, {Season: 0, Number: 2, URL: "special", Name: "Provider episode label"}},
	}
	local := "my subtitles.srt"
	calls := 0
	choose := func(subject string, sources []string) (int, error) {
		if !strings.Contains(subject, "Night Shift (series, 2018) S00E02") || len(sources) != 2 || !strings.Contains(sources[0], "first") || !strings.Contains(sources[1], "year unknown · alternate") {
			t.Fatalf("choice wasn't limited to plausible sources for this selection: %q %+v", subject, sources)
		}
		calls++
		return 1, nil
	}
	playerErr := errors.New("mpv exited")
	err := playTraktMovie(t.Context(), p, title, ep, choose, local, io.Discard, func(r *playable.Release) error {
		if r.Referer != "special" || r.Title != "Night Shift S00E02 · Special" || r.SubtitleURL != local || p.opened != "alternate" {
			t.Fatalf("playback changed the selection: %+v, %+v", r, p)
		}
		return playerErr
	})
	if !errors.Is(err, playerErr) || calls != 1 || p.auto != 0 || p.video != 1 || p.fetch != 0 || ep.ID != 504 || ep.Name != "Special" {
		t.Fatalf("provider choice, captions, or return to picker: %v, %+v, %+v", err, p, ep)
	}
}

func TestProviderSelectionFailuresNeverStartPlayback(t *testing.T) {
	title := trakt.Title{Name: "Show", Kind: "series", Year: 2024, ID: 90}
	ep := &trakt.Episode{Season: 1, Number: 2, ID: 29}
	row := flixhq.Title{Name: "Show", Kind: "series", Year: "2024", URL: "show"}
	uncertain := row
	uncertain.Year = ""
	offline := errors.New("provider offline")
	neverChoose := func(string, []string) (int, error) { t.Fatal("offered an unsafe choice"); return -1, nil }
	for _, tc := range []struct {
		name         string
		title        trakt.Title
		ep           *trakt.Episode
		p            *providerStub
		choose       choosePlaybackSource
		beforeSearch bool
	}{
		{"unidentified Trakt title", trakt.Title{Kind: "series", Name: "Show"}, ep, &providerStub{}, neverChoose, true},
		{"series without episode", title, nil, &providerStub{}, neverChoose, true},
		{"series with invalid season", title, &trakt.Episode{Season: -1, Number: 1}, &providerStub{}, neverChoose, true},
		{"series with invalid number", title, &trakt.Episode{Season: 1, Number: 0}, &providerStub{}, neverChoose, true},
		{"movie with episode", trakt.Title{Name: "Show", Kind: "movie", ID: 8}, ep, &providerStub{}, neverChoose, true},
		{"search error", title, ep, &providerStub{searchErr: offline}, neverChoose, false},
		{"wrong Trakt year", title, ep, &providerStub{rows: []flixhq.Title{{Name: "Show", Kind: "series", Year: "1994", URL: "old-show"}}}, neverChoose, false},
		{"episode error", title, ep, &providerStub{rows: []flixhq.Title{row}, episodeErr: offline}, neverChoose, false},
		{"episode unavailable", title, ep, &providerStub{rows: []flixhq.Title{row}, eps: []flixhq.Episode{{Season: 2, Number: 2, URL: "wrong-season"}, {Season: 1, Number: 3, URL: "wrong-number"}}}, neverChoose, false},
		{"episode ambiguous", title, ep, &providerStub{rows: []flixhq.Title{row}, eps: []flixhq.Episode{{Season: 1, Number: 2, URL: "first"}, {Season: 1, Number: 2, URL: "other"}}}, neverChoose, false},
		{"episode without page", title, ep, &providerStub{rows: []flixhq.Title{row}, eps: []flixhq.Episode{{Season: 1, Number: 2}}}, neverChoose, false},
		{"ambiguous dry-run", title, ep, &providerStub{rows: []flixhq.Title{row, uncertain}}, nil, false},
		{"canceled manual selection", title, ep, &providerStub{rows: []flixhq.Title{uncertain}}, func(string, []string) (int, error) { return -1, nil }, false},
		{"invalid manual selection", title, ep, &providerStub{rows: []flixhq.Title{uncertain}}, func(string, []string) (int, error) { return 3, nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := playTraktMovie(t.Context(), tc.p, tc.title, tc.ep, tc.choose, "", io.Discard, func(*playable.Release) error {
				t.Fatal("player invoked after failed provider/episode selection")
				return nil
			})
			if err == nil || tc.p.auto != 0 || tc.p.video != 0 || (tc.p.query == "") != tc.beforeSearch {
				t.Fatalf("expected a safe failure: %v, %+v", err, tc.p)
			}
		})
	}
}
