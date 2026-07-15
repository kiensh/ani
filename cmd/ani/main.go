package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"ani/internal/app"
	"ani/internal/config"
	"ani/internal/mal"
	"ani/internal/ui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// A clean cancel (user hit q / Esc at the top level) exits silently.
		if errors.Is(err, app.ErrCancelled) {
			return
		}
		fmt.Fprintln(os.Stderr, "ani:", err)
		os.Exit(1)
	}
}

// run parses the single positional argument (plus the hidden --debug/--dry-run
// dev flags) and dispatches into app.Run. No user-facing flags:
//
//	ani               -> MAL list (logged in) or latest uploads (not logged in)
//	ani <name>        -> search by name
//	ani <anidb-id>    -> that series' releases directly
//
// Flags are pulled out manually so they work in any position/combination
// (Go's flag package would otherwise stop at the first positional and ignore
// `ani <query> --debug`). A debug log file is always written; --debug additionally
// echoes DEBUG lines to the terminal (which the TUI's alt screen would otherwise
// hide).
func run(args []string) error {
	debug, dryRun, help, query := parseArgs(args)
	if help {
		app.PrintUsage(os.Stderr)
		return nil
	}

	// Always-on debug log: capture DEBUG … lines to <configdir>/ani/debug.log so
	// any run can be inspected after.
	logPath, _ := debugLogPath()
	var logFile *os.File
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			logFile = f
			mal.SetDebugLog(f)
			if debug {
				fmt.Fprintf(os.Stderr, "ani debug log → %s\n", logPath)
			}
		}
	}

	cfg := config.Load()
	o := &app.Options{
		Debug:   debug,
		DryRun:  dryRun,
		Group:   cfg.Group,
		Quality: cfg.Quality,
		Sort:    ui.NormalizeSort(cfg.Sort),
		Player:  app.OrDefault(cfg.Player, "mpv"),
		Dir:     cfg.Dir,
		Query:   query,
	}
	runErr := app.Run(o)

	// The TUI's alt screen hides (and discards) the DEBUG lines that --debug
	// echoed inline. --dry-run has no TUI so they were already visible; for a TUI
	// run, echo the captured log now that the screen has been restored.
	if logFile != nil {
		logFile.Close()
	}
	if debug && !dryRun && logPath != "" {
		if data, err := os.ReadFile(logPath); err == nil {
			os.Stderr.Write(data)
		}
	}
	return runErr
}

// parseArgs extracts the hidden dev flags from anywhere in args and returns the
// first positional as query. Flags combine freely in any order.
func parseArgs(args []string) (debug, dryRun, help bool, query string) {
	for _, a := range args {
		switch a {
		case "-h", "--help":
			help = true
		case "--debug", "-debug":
			debug = true
		case "--dry-run", "-dry-run":
			dryRun = true
		default:
			if query == "" {
				query = a
			}
		}
	}
	return debug, dryRun, help, query
}

// debugLogPath returns the always-on debug log location, mirroring malTokenPath
// (os.UserConfigDir()/ani/…).
func debugLogPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ani", "debug.log"), nil
}
