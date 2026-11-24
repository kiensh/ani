package app

import (
	"errors"
	"reflect"
	"testing"

	"ani/internal/mal"
	"ani/internal/tui"
)

func TestMediaSourceRoundTrip(t *testing.T) {
	for _, startMovies := range []bool{false, true} {
		opt := &Options{Movies: startMovies, Query: "1917", Source: "hianime", Group: "dub"}
		var calls []tui.MediaSource
		open := func(which tui.MediaSource) func(*Options) (tui.MediaSource, error) {
			return func(o *Options) (tui.MediaSource, error) {
				calls = append(calls, which)
				wantQuery := ""
				if startMovies == (which == tui.MediaMovies) {
					wantQuery = "1917"
				}
				if o.Query != wantQuery || o.Movies != (which == tui.MediaMovies) || o.Source != "hianime" || o.Group != "dub" {
					t.Fatalf("%s: options %+v", which, *o)
				}
				if len(calls) == 3 {
					return "", nil
				}
				if which == tui.MediaAnime {
					return tui.MediaMovies, nil
				}
				return tui.MediaAnime, nil
			}
		}
		if err := runMediaSources(opt, open(tui.MediaAnime), open(tui.MediaMovies)); err != nil {
			t.Fatal(err)
		}
		want := []tui.MediaSource{tui.MediaAnime, tui.MediaMovies, tui.MediaAnime}
		if startMovies {
			want = []tui.MediaSource{tui.MediaMovies, tui.MediaAnime, tui.MediaMovies}
		}
		if !reflect.DeepEqual(want, calls) {
			t.Errorf("entered %v, want %v", calls, want)
		}
	}
}

func TestAnimePickerIntentsAreDistinct(t *testing.T) {
	var toMovies openMovies
	var backend errSourceSwitch
	if _, err := animeSelection(&tui.Result{MediaSwitch: tui.MediaMovies}); !errors.As(err, &toMovies) || errors.As(err, &backend) {
		t.Fatalf("media switch: %v", err)
	}
	if _, err := animeSelection(&tui.Result{SourceSwitch: "hianime"}); !errors.As(err, &backend) || errors.As(err, &toMovies) {
		t.Fatalf("provider switch: %v", err)
	}
	for _, tc := range []struct {
		res *tui.Result
		err error
	}{{&tui.Result{Relogin: true}, errRelogin}, {&tui.Result{Logout: true}, errLogout}, {nil, ErrCancelled}} {
		if _, err := animeSelection(tc.res); !errors.Is(err, tc.err) {
			t.Fatalf("%+v: %v", tc.res, err)
		}
	}
	item := &mal.Item{Title: "Example"}
	if got, err := animeSelection(&tui.Result{Anime: item}); err != nil || got != item {
		t.Fatal("normal anime selection was lost")
	}
}
