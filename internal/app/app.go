// Package app wires the ani subcommands together: config + MAL resolve +
// animetosho releases + ui pickers + player launch.
package app

import (
	"fmt"
	"os"

	"ani/internal/animetosho"
	"ani/internal/player"
	"ani/internal/ui"

	gomal "github.com/nstratos/go-myanimelist/mal"
)

// Run is the main flow: resolve an anime → load releases → pick → play or
// download → write back to MAL, looping until cancelled.
func Run(opt *Options) error {
	aid, title, item, err := Resolve(opt.Query, opt.Status, opt.UseFzf, opt.DryRun, opt.Debug)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Loading releases (anidb %d)…\n", aid)
	entries, err := animetosho.FetchSeriesReleases(aid)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no releases found for anidb %d", aid)
	}
	all := animetosho.ToReleases(entries)
	if title == "" {
		if t := entries[0].Series.Title; t != "" {
			title = t
		}
	}

	fmt.Fprintf(os.Stderr, "%s — %d releases\n", title, len(all))
	for {
		pick, err := ui.PickRelease(all, opt.Group, opt.Sort, opt.UseFzf, item, opt.DryRun)
		if err != nil {
			return err // q/cancel → exit
		}

		AnnouncePick(pick)
		action := "play"
		if !opt.DryRun {
			action, err = ui.PromptAction()
			if err != nil {
				return err
			}
		}
		if action == "download" {
			if err := player.RunDownload(pick.Entry.Magnet, opt.Dir, opt.DryRun); err != nil {
				return err
			}
		} else {
			if err := player.RunPlay(pick.Entry.Magnet, pick.Entry.Title, opt.Player, opt.DryRun); err != nil {
				return err
			}
		}
		MalWriteBack(item, pick, opt)
		if opt.DryRun {
			return nil // one iteration: print commands, then exit
		}
		// loop: return to the release picker for the next file
	}
}

// AnnouncePick prints the chosen release to stdout.
func AnnouncePick(r *animetosho.Release) {
	grp := r.Group
	if grp == "" {
		grp = "?"
	}
	fmt.Printf("\n> [%s] %s\n  %s, %d seeders\n", grp, r.Entry.Title, ui.HumanSize(r.Entry.SizeBytes), r.Entry.Seeders)
}

// OrDefault returns v when non-empty, else def.
func OrDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// PrintUsage writes the CLI help text to w.
func PrintUsage(w *os.File) {
	fmt.Fprintln(w, `ani — search Anime Tosho by series, then play or download

Usage:
  ani <query|anidb-id> [flags]

  <query>     anime name (e.g. frieren) -> pick from matching series
  <anidb-id>  numeric AniDB id (e.g. 18886) -> skip straight to releases

Flags:
  --group NAME        filter by release group (Erai-raws, SubsPlease, ...)
  --sort ORDER        newest (default) | oldest | smallest | largest
  --player NAME       streaming player for play (mpv default)
  --dir PATH          download directory (default cwd)
  --fzf / --no-fzf    toggle fzf menus

Flow: pick anime -> browse releases (n/p/g/s/q) -> pick -> play or download

Config: $XDG_CONFIG_HOME/ani/config.json`)
}

// mapStatus maps a --status flag value to a MAL AnimeStatus ("" = no filter).
func mapStatus(s string) gomal.AnimeStatus {
	switch s {
	case "watching":
		return gomal.AnimeStatusWatching
	case "completed":
		return gomal.AnimeStatusCompleted
	case "on_hold":
		return gomal.AnimeStatusOnHold
	case "dropped":
		return gomal.AnimeStatusDropped
	case "plan_to_watch":
		return gomal.AnimeStatusPlanToWatch
	}
	return "" // "all" / unknown → no status filter
}
