package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

func TestLocalSubtitlePaths(t *testing.T) {
	dir := t.TempDir()
	name := "My cut.EN.SRT"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Chdir(dir)
	for _, in := range []string{p, `"` + p + `"`, "~/" + name, name} {
		got, err := localSubtitlePath(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		a, _ := os.Stat(p)
		b, e := os.Stat(got)
		if e != nil || !filepath.IsAbs(got) || !os.SameFile(a, b) {
			t.Fatalf("wrong subtitle path %q", got)
		}
	}
	folder := filepath.Join(dir, "directory.srt")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"", "https://example.test/sub.srt", "file:///tmp/test.srt", "not-there.vtt", "movie.mp4", folder, "bad\nsrt.srt"} {
		if _, err := localSubtitlePath(in); err == nil {
			t.Fatalf("accepted %q", in)
		}
	}
}

func TestMovieFileSubtitleAction(t *testing.T) {
	p := filepath.Join(t.TempDir(), "My S01E02 English.vtt")
	if err := os.WriteFile(p, []byte("WEBVTT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &moviePicker{MovieState: &MovieState{stage: movieTitles, view: movieRecent, titles: []trakt.Title{{Kind: "series", Name: "Example", ID: 5}}}}
	m.Update(movieKey("s"))
	if m.subPrompt != nil || m.play {
		t.Fatal("cannot attach to the entire series")
	}
	m.stage = movieEpisodes
	m.seasons = []int{1}
	m.bySeason = map[int][]trakt.Episode{1: {{Season: 1, Number: 2, ID: 22}}}
	m.Update(movieKey("s"))
	if !strings.Contains(m.View(), "Play with subtitle file") {
		t.Fatal("no file prompt")
	}
	m.Update(movieKey("/tmp/not-a-subtitle.mp4"))
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.subPrompt == nil || m.play || !strings.Contains(m.View(), "Choose an .srt") {
		t.Fatal("invalid path should not start playback")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.subPrompt != nil || m.subtitleFile != "" || m.play || m.stage != movieEpisodes {
		t.Fatal("cancel lost selection or started playback")
	}
	m.applyMovieCommand("play:subtitle")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(p), Paste: true})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.play || m.subtitleFile != p || m.subPrompt != nil {
		t.Fatalf("file not selected: %q", m.subtitleFile)
	}
}

func TestSubtitleShortcutContext(t *testing.T) {
	m := &moviePicker{MovieState: &MovieState{stage: movieTitles, titles: []trakt.Title{{Name: "Movie", Kind: "movie", ID: 123}}}}
	for _, v := range []movieView{movieRecent, movieWatchlist, movieWatched, movieResults} {
		m.view = v
		if !m.canAttachSubtitle() {
			t.Fatalf("no subtitles on %s", v.label())
		}
		m.Update(movieKey("s"))
		if m.subPrompt == nil || m.play {
			t.Fatal("movie should accept a local subtitle without provider selection")
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	}
	m.stage = movieSearch
	m.editing = true
	m.Update(movieKey("s"))
	if m.subPrompt != nil || string(m.query) != "s" {
		t.Fatal("s should type into search")
	}
}
