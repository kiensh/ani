package app

import (
	"context"
	"errors"
	"io"
	"testing"

	"ani/internal/flixhq"
	"ani/internal/playable"
	"ani/internal/trakt"
)

type catalogStub struct {
	titles []trakt.Title
	eps    []trakt.Episode
	query  string
	opened int
}

func (c *catalogStub) RecentReleases(context.Context) ([]trakt.Title, error) {
	panic("dry-run must not browse a shelf")
}
func (c *catalogStub) SearchTitles(_ context.Context, q string) ([]trakt.Title, error) {
	if q == "" {
		panic("dry-run without a title must not query any catalog")
	}
	c.query = q
	return c.titles, nil
}
func (c *catalogStub) Episodes(_ context.Context, t trakt.Title) ([]trakt.Episode, error) {
	c.opened = t.ID
	return c.eps, nil
}

func TestMovieDryRunFromCatalogToPlayback(t *testing.T) {
	for _, kind := range []string{"movie", "series"} {
		t.Run(kind, func(t *testing.T) {
			title := trakt.Title{Name: "1917", Kind: kind, Year: 2019, ID: 57, IMDb: "tt0000057", TMDB: 234}
			catalog := &catalogStub{
				titles: []trakt.Title{{Name: "1917: Beyond", Kind: kind, Year: 2020, ID: 74}, title},
				eps:    []trakt.Episode{{Season: 0, Number: 1, ID: 98}, {Season: 2, Number: 1, ID: 65}, {Season: 1, Number: 1, ID: 34, Name: "Premiere"}},
			}
			provider := &providerStub{
				rows: []flixhq.Title{{Name: "1917", Kind: kind, Year: "1994", URL: "remake"}, {Name: "1917", Kind: kind, Year: "2019", URL: "matching-page"}},
				eps:  []flixhq.Episode{{Season: 1, Number: 2, URL: "next"}, {Season: 2, Number: 1, URL: "next-season"}, {Season: 1, Number: 1, Name: "Provider label", URL: "premiere"}},
			}
			played := 0
			err := dryRunMovies(catalog, " 1917 ", func(pick trakt.Title, ep *trakt.Episode, local string) error {
				if catalog.query != "1917" || (kind == "series" && catalog.opened != title.ID) || (kind == "movie" && catalog.opened != 0) {
					t.Fatalf("search/series selection did not use Trakt: %+v", catalog)
				}
				if provider.query != "" || provider.opened != "" || provider.auto != 0 || provider.video != 0 {
					t.Fatalf("provider used before playback: %+v", provider)
				}
				if pick.ID != title.ID || pick.IMDb != title.IMDb || pick.TMDB != title.TMDB || pick.Kind != kind || local != "" {
					t.Fatalf("Trakt identity lost: %+v", pick)
				}
				if (kind == "movie" && ep != nil) || (kind == "series" && (ep == nil || ep.ID != 34)) {
					t.Fatalf("wrong catalog episode: %+v", ep)
				}
				return playTraktMovie(t.Context(), provider, pick, ep, nil, local, io.Discard, func(r *playable.Release) error {
					played++
					page, label := "matching-page", "1917"
					if kind == "series" {
						page, label = "premiere", "1917 S01E01 · Premiere"
					}
					if r.Title != label || r.Referer != page || r.StreamURL != "example-video" {
						t.Fatalf("wrong stream, referrer or label: %+v", r)
					}
					return nil
				})
			})
			if err != nil || played != 1 || provider.query != "1917" || (kind == "series" && provider.opened != "matching-page") || (kind == "movie" && provider.opened != "") {
				t.Fatalf("playback flow: %+v, played %d, %v", provider, played, err)
			}
		})
	}
}

func TestMovieDryRunNeverGuesses(t *testing.T) {
	movie := trakt.Title{Name: "1917", Kind: "movie", Year: 2019, ID: 57}
	remake := trakt.Title{Name: "1917", Kind: "movie", Year: 1989, ID: 84}
	series := trakt.Title{Name: "1917", Kind: "series", Year: 2019, ID: 35}
	for _, tc := range []struct {
		name, query string
		titles      []trakt.Title
		eps         []trakt.Episode
	}{
		{"no query", "", []trakt.Title{movie}, nil},
		{"no match", "1917", nil, nil},
		{"non-exact singleton", "191", []trakt.Title{movie}, nil},
		{"remakes", "1917", []trakt.Title{remake, movie}, nil},
		{"movie and series", "1917", []trakt.Title{series, movie}, nil},
		{"missing premiere", "1917", []trakt.Title{series}, []trakt.Episode{{Season: 0, Number: 1}, {Season: 1, Number: 2}}},
		{"duplicate premiere", "1917", []trakt.Title{series}, []trakt.Episode{{Season: 1, Number: 1, ID: 56}, {Season: 1, Number: 1, ID: 34}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &catalogStub{titles: tc.titles, eps: tc.eps}
			err := dryRunMovies(c, tc.query, func(trakt.Title, *trakt.Episode, string) error {
				t.Fatal("provider playback attempted without a unique Trakt selection")
				return nil
			})
			if (err == nil) != (tc.query == "") || c.query != tc.query {
				t.Fatalf("error = %v, searched %q", err, c.query)
			}
		})
	}
}

func TestMovieResolverReturnsPlaybackError(t *testing.T) {
	dead := errors.New("no Vidmoly server")
	_, err := resolveMovie(t.Context(), func(context.Context, string) (*playable.Release, error) { return nil, dead }, flixhq.Title{Name: "Example", URL: "page"}, nil)
	if !errors.Is(err, dead) {
		t.Fatalf("resolver error lost: %v", err)
	}
}
