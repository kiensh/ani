package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ani/internal/animetosho"
	"ani/internal/app"
	"ani/internal/config"
	"ani/internal/hianime"
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

// run dispatches an anime query or the separate --movies catalog. The hidden
// --debug and --dry-run flags work with either mode:
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
	if len(args) > 0 && args[0] == "trakt" {
		return traktCommand(args[1:])
	}
	cli := parseArgs(args)
	debug, dryRun := cli.Debug, cli.DryRun
	if cli.Help {
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
			animetosho.SetDebugLog(f)
			hianime.SetDebugLog(f)
			if debug {
				animetosho.SetDebugEcho(true)
				fmt.Fprintf(os.Stderr, "ani debug log → %s\n", logPath)
			}
		}
	}

	cfg := config.Load()
	// Select the per-provider group/quality so switching providers doesn't
	// clobber each other's filter preferences.
	group, quality := cfg.Group, cfg.Quality
	if cfg.Source == "hianime" {
		group, quality = cfg.HianimeGroup, cfg.HianimeQuality
	}
	o := &app.Options{
		Debug:   debug,
		DryRun:  dryRun,
		Group:   group,
		Quality: quality,
		Sort:    ui.NormalizeSort(cfg.Sort),
		Player:  app.OrDefault(cfg.Player, "mpv"),
		Dir:     cfg.Dir,
		Query:   cli.Query,
		Movies:  cli.Movies,
		Source:  cfg.Source,
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

type cliArgs struct {
	Debug, DryRun, Help, Movies bool
	Query                       string
}

func parseArgs(args []string) cliArgs {
	var flags cliArgs
	var words []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			flags.Help = true
		case "--debug", "-debug":
			flags.Debug = true
		case "--dry-run", "-dry-run":
			flags.DryRun = true
		case "--movies":
			flags.Movies = true
		default:
			words = append(words, a)
		}
	}
	if flags.Movies {
		flags.Query = strings.Join(words, " ")
	} else if len(words) > 0 {
		flags.Query = words[0] // preserve the anime CLI's single positional argument
	}
	return flags
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
