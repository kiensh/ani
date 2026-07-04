package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"ani/internal/app"
	"ani/internal/config"
	"ani/internal/ui"
)

// valueFlags consume the following argument when not in --flag=value form,
// so intersperseFlags can reorder flags/positionals (stdlib flag stops at the
// first positional otherwise).
var valueFlags = map[string]bool{
	"group":  true,
	"sort":   true,
	"player": true,
	"dir":    true,
}

func intersperseFlags(args []string) []string {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				name = name[:idx]
			} else if valueFlags[name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, a)
	}
	return append(flags, positionals...)
}

func main() {
	// Internal hidden subcommand: backing the release fzf --preview pane.
	if len(os.Args) >= 4 && os.Args[1] == "preview-release" {
		ui.PreviewRelease(os.Args[2], os.Args[3])
		return
	}
	if len(os.Args) >= 4 && os.Args[1] == "preview-anime" {
		ui.PreviewAnime(os.Args[2], os.Args[3])
		return
	}
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, ui.ErrCancelled) {
			return // user hit q — clean exit
		}
		fmt.Fprintln(os.Stderr, "ani:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("ani", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opt struct {
		group  string
		sort   string
		player string
		dir    string
		status string
		noFzf  bool
		debug  bool
		dryRun bool
	}
	fs.StringVar(&opt.group, "group", "", "pre-filter by release group (e.g. Erai-raws)")
	fs.StringVar(&opt.sort, "sort", "", "newest|oldest|smallest|largest (initial order)")
	fs.StringVar(&opt.player, "player", "", "streaming player for play (mpv default)")
	fs.StringVar(&opt.dir, "dir", "", "download directory (default cwd)")
	fs.StringVar(&opt.status, "status", "watching", "MAL list status: watching|completed|on_hold|dropped|plan_to_watch|all")
	fs.BoolVar(&opt.noFzf, "no-fzf", false, "disable fzf (use numbered menus)")
	fs.BoolVar(&opt.debug, "debug", false, "verbose logging (MAL URLs, raw responses)")
	fs.BoolVar(&opt.dryRun, "dry-run", false, "auto-pick first fzf item and print exec commands without running them")
	if err := fs.Parse(intersperseFlags(args)); err != nil {
		return err
	}

	rest := fs.Args()
	query := ""
	if len(rest) > 0 {
		query = rest[0]
	}

	cfg := config.Load()
	o := &app.Options{
		Debug:  opt.debug,
		DryRun: opt.dryRun,
		Group:  app.OrDefault(opt.group, cfg.Group),
		Sort:   ui.NormalizeSort(app.OrDefault(opt.sort, cfg.Sort)),
		Player: app.OrDefault(opt.player, cfg.Player),
		Dir:    app.OrDefault(opt.dir, cfg.Dir),
		Status: opt.status,
		Query:  query,
		UseFzf: ui.FzfAvailable() && !opt.noFzf,
	}
	if o.Player == "" {
		o.Player = "mpv"
	}

	return app.Run(o)
}
