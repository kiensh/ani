package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The tmux pane may inherit allow-passthrough; querying only local options
// gave an empty result and silently reduced even Kitty to half-block pixels.
func TestMoviePosterEffectivePane(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX tmux stub")
	}
	dir := t.TempDir()
	stub := `#!/bin/sh
[ "$1" = display-message ] && [ "$2" = -p ] && [ "$3" = -t ] && [ "$4" = %42 ] && [ "$5" = '#{client_termname}	#{allow-passthrough}' ] || exit 1
printf '%s\t%s\n' "$TEST_OUTER" "$TEST_PASS"
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TMUX", "test")
	t.Setenv("TMUX_PANE", "%42")
	t.Setenv("TERM_PROGRAM", "tmux")
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("NO_COLOR", "")
	for _, tc := range []struct {
		outer, pass, kitty string
		want               bool
	}{
		{"xterm-kitty", "on", "", true}, {"xterm-256color", "all", "1", true}, {"xterm-kitty", "off", "1", false}, {"xterm-256color", "on", "", false}, {"xterm-kitty", "", "1", false},
	} {
		t.Setenv("TEST_OUTER", tc.outer)
		t.Setenv("TEST_PASS", tc.pass)
		t.Setenv("KITTY_WINDOW_ID", tc.kitty)
		if got := useNativeMoviePoster(t.Context()); got != tc.want {
			t.Errorf("outer=%s pane=%q kitty=%q: native=%v, want %v", tc.outer, tc.pass, tc.kitty, got, tc.want)
		}
	}
}
