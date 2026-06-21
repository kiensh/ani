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
func runPlay(r *Release, player string) error {
	if player == "" {
		player = "mpv"
	}
	magnet := pickMagnet(r)
	if magnet == "" {
		return fmt.Errorf("release has no magnet URI")
	}
	cmd := exec.Command("webtorrent", "--not-on-top", "--"+player, magnet)
	return runWithSignals(cmd)
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
// Ctrl-C tears down webtorrent/aria2c cleanly.
func runWithSignals(cmd *exec.Cmd) error {
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
