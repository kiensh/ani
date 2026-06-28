package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// runPlay streams the release via webtorrent into the given player.
// --not-on-top stops webtorrent from forcing the player window on top.
// For mpv, the window/media title is set to the release title via a temp
// include file (webtorrent splits --player-args on spaces, so the spaced title
// can't be passed directly).
func runPlay(r *Release, player string) error {
	if player == "" {
		player = "mpv"
	}
	magnet := pickMagnet(r)
	if magnet == "" {
		return fmt.Errorf("release has no magnet URI")
	}
	args := []string{"--not-on-top", "--" + player}
	if player == "mpv" && r.Entry.Title != "" {
		if p, cleanup, ok := writeMpvTitleConfig(r.Entry.Title); ok {
			defer cleanup()
			args = append(args, "--player-args=--include="+p)
		}
	}
	args = append(args, magnet)
	return runWithSignals(exec.Command("webtorrent", args...))
}

// writeMpvTitleConfig writes an mpv include file setting the window and media
// title. Returns the path and a cleanup func.
func writeMpvTitleConfig(title string) (path string, cleanup func(), ok bool) {
	f, err := os.CreateTemp("", "ani-mpv-*.conf")
	if err != nil {
		return "", nil, false
	}
	fmt.Fprintf(f, "title=%s\nforce-media-title=%s\n", title, title)
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, true
}

// runDownload fetches the release via aria2c, exiting after download (--seed-time=0).
func runDownload(r *Release, dir string) error {
	if dir == "" {
		dir = "."
	}
	magnet := pickMagnet(r)
	if magnet == "" {
		return fmt.Errorf("release has no magnet URI")
	}
	cmd := exec.Command("aria2c", "--seed-time=0", "--dir="+dir, "--summary-interval=0", magnet)
	return runWithSignals(cmd)
}

func pickMagnet(r *Release) string {
	return r.Entry.Magnet
}

// runWithSignals runs cmd with inherited stdio and forwards SIGINT/SIGTERM so
// Ctrl-C tears down webtorrent/aria2c cleanly. In debug mode it just prints
// the command and returns without running it.
func runWithSignals(cmd *exec.Cmd) error {
	if debugMode {
		fmt.Fprintf(os.Stderr, "DEBUG exec %s\n", shellQuote(cmd.Args))
		return nil
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cmd.Args[0], err)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case sig := <-sigCh:
		_ = cmd.Process.Signal(sig)
		<-done
		return fmt.Errorf("interrupted: %v", sig)
	case err := <-done:
		return err
	}
}
