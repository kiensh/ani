package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ani/internal/flixhq"
	"ani/internal/playable"
)

const testVTT = "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nAn English caption.\n"

type movieSubStub struct {
	auto, video, fetch int
	page, track        string
	err                error
}

func (s *movieSubStub) Resolve(_ context.Context, p string) (*playable.Release, error) {
	s.auto++
	s.page = p
	return &playable.Release{StreamURL: "example-video", Referer: p, SubtitleURL: s.track}, nil
}
func (s *movieSubStub) ResolveVideoOnly(_ context.Context, p string) (*playable.Release, error) {
	s.video++
	s.page = p
	return &playable.Release{StreamURL: "example-video", Referer: p}, nil
}
func (s *movieSubStub) FetchEnglishVTT(_ context.Context, url string) ([]byte, error) {
	s.fetch++
	if url != s.track {
		return nil, errors.New("wrong track")
	}
	return []byte(testVTT), s.err
}

func TestProviderCaptionsLiveOnlyDuringPlayback(t *testing.T) {
	for _, playerErr := range []error{nil, errors.New("player failed")} {
		c := &movieSubStub{track: "fixture-english"}
		var path string
		var out bytes.Buffer
		err := playCaptionedMovie(t.Context(), c, flixhq.Title{Name: "Show"}, &flixhq.Episode{Season: 1, Number: 2, URL: "episode-page"}, "", &out, func(r *playable.Release) error {
			path = r.SubtitleURL
			data, err := os.ReadFile(path)
			if err != nil || string(data) != testVTT || !strings.HasSuffix(path, ".en.vtt") {
				t.Fatalf("missing English file: %s, %v", path, err)
			}
			info, err := os.Stat(path)
			if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
				t.Fatalf("temporary caption permissions: %v, %v", info, err)
			}
			if r.Referer != "episode-page" || r.Title != "Show S01E02" {
				t.Fatalf("lost video identity: %+v", r)
			}
			return playerErr
		})
		if !errors.Is(err, playerErr) || c.auto != 1 || c.video != 0 || c.fetch != 1 || !strings.Contains(out.String(), "English (provider)") {
			t.Fatalf("unexpected flow: %+v %s %v", c, out.String(), err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("left caption file after player exit: %v", err)
		}
	}
}

func TestUserSubtitleOverridesAndIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my english.srt")
	if err := os.WriteFile(path, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &movieSubStub{track: "fixture"}
	err := playCaptionedMovie(t.Context(), c, flixhq.Title{Name: "Movie", URL: "movie-page"}, nil, path, &bytes.Buffer{}, func(r *playable.Release) error {
		if r.SubtitleURL != path {
			t.Fatal("user selection was not passed to player")
		}
		return nil
	})
	if err != nil || c.auto != 0 || c.fetch != 0 || c.video != 1 {
		t.Fatalf("manual mode should not look up provider tracks: %+v %v", c, err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "user file" {
		t.Fatalf("altered user subtitle: %v", err)
	}
}

func TestMissingOrFailedCaptionsDoNotBlockMovie(t *testing.T) {
	for _, track := range []string{"", "fixture"} {
		c := &movieSubStub{track: track, err: errors.New("subtitle server offline")}
		played := false
		var out bytes.Buffer
		err := playCaptionedMovie(t.Context(), c, flixhq.Title{Name: "Movie", URL: "movie-page"}, nil, "", &out, func(r *playable.Release) error {
			played = true
			if r.SubtitleURL != "" || r.StreamURL == "" {
				t.Fatalf("unusable caption was passed to mpv: %+v", r)
			}
			return nil
		})
		if err != nil || !played || !strings.Contains(out.String(), "Use s") {
			t.Fatalf("fallback: %v, %v, %q", err, played, out.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := playCaptionedMovie(ctx, &movieSubStub{}, flixhq.Title{}, nil, "", &bytes.Buffer{}, func(*playable.Release) error { t.Fatal("started canceled playback"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
