package main

import (
	"os"
	"os/exec"
)

// kittyActive reports whether we're running inside kitty with the kitten CLI
// available and stdout attached to a terminal. Used to gate fzf's image
// preview (kitten icat) in the anime picker.
func kittyActive() bool {
	if os.Getenv("KITTY_WINDOW_ID") == "" && os.Getenv("KITTY_PID") == "" {
		return false
	}
	if _, err := exec.LookPath("kitten"); err != nil {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
