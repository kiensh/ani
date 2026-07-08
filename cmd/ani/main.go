package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"ani/internal/app"
	"ani/internal/config"
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
// dev flags) and dispatches into app.Run. There are no user-facing flags: just
//   ani               -> MAL list (logged in) or latest uploads (not logged in)
//   ani <name>        -> search by name
//   ani <anidb-id>    -> that series' releases directly
func run(args []string) error {
	fs := flag.NewFlagSet("ani", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { app.PrintUsage(os.Stderr) }

	// Hidden dev/test flags — not shown in usage.
	debug := fs.Bool("debug", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil // -h/-help: usage already printed
		}
		return err
	}

	query := ""
	if rest := fs.Args(); len(rest) > 0 {
		query = rest[0]
	}

	cfg := config.Load()
	o := &app.Options{
		Debug:   *debug,
		DryRun:  *dryRun,
		Group:   cfg.Group,
		Quality: cfg.Quality,
		Sort:    ui.NormalizeSort(cfg.Sort),
		Player:  app.OrDefault(cfg.Player, "mpv"),
		Dir:     cfg.Dir,
		Query:   query,
	}
	return app.Run(o)
}
