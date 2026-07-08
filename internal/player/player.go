// Package player runs the streaming player (webtorrent) and downloader (aria2c).
package player

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

// RunPlay streams the release via webtorrent into the given player.
// --not-on-top stops webtorrent from forcing the player window on top.
// For mpv, the window/media title is set to the release title via a temp
// include file (webtorrent splits --player-args on spaces, so the spaced title
// can't be passed directly).
func RunPlay(magnet, title, player string, dryRun bool) error {
	if player == "" {
		player = "mpv"
	}
	if magnet == "" {
		return fmt.Errorf("release has no magnet URI")
	}
	args := []string{"--not-on-top", "--" + player}
	if player == "mpv" && title != "" {
		if p, cleanup, ok := WriteMpvTitleConfig(title); ok {
			defer cleanup()
			args = append(args, "--player-args=--include="+p)
		}
	}
	args = append(args, magnet)
	return RunWithSignals(exec.Command("webtorrent", args...), dryRun)
}

// WriteMpvTitleConfig writes an mpv include file setting the window and media
// title. Returns the path and a cleanup func.
func WriteMpvTitleConfig(title string) (path string, cleanup func(), ok bool) {
	f, err := os.CreateTemp("", "ani-mpv-*.conf")
	if err != nil {
		return "", nil, false
	}
	fmt.Fprintf(f, "title=%s\nforce-media-title=%s\n", title, title)
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, true
}

// RunDownload fetches the release via aria2c, exiting after download (--seed-time=0).
func RunDownload(magnet, dir string, dryRun bool) error {
	if dir == "" {
		dir = "."
	}
	if magnet == "" {
		return fmt.Errorf("release has no magnet URI")
	}
	cmd := exec.Command("aria2c", "--seed-time=0", "--dir="+dir, "--summary-interval=0", magnet)
	return RunWithSignals(cmd, dryRun)
}

// CopyToClipboard copies text to the system clipboard via the platform tool
// (pbcopy on macOS, clip on Windows, xclip on Linux). Returns an error if the
// tool is missing or the copy fails.
func CopyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip")
	default:
		cmd = exec.Command("xclip", "-selection", "clipboard")
	}
	w, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("clipboard: start %s: %w", cmd.Args[0], err)
	}
	if _, err := w.Write([]byte(text)); err != nil {
		return fmt.Errorf("clipboard: write: %w", err)
	}
	w.Close()
	return cmd.Wait()
}

// RunWithSignals runs cmd with inherited stdio and forwards SIGINT/SIGTERM so
// Ctrl-C tears down webtorrent/aria2c cleanly. When dryRun is true it prints
// the command (quoted) and returns without running it.
func RunWithSignals(cmd *exec.Cmd, dryRun bool) error {
	if dryRun {
		fmt.Fprintf(os.Stderr, "DEBUG exec %s\n", ShellQuote(cmd.Args))
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

// ShellQuote joins args into a single shell-quoted command line (each arg in
// single quotes, inner ' escaped) so printed commands are copy-paste-runnable.
func ShellQuote(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
}
