// Package app wires the ani subcommands together: config + MAL resolve +
// animetosho releases + ui pickers + player launch.
package app

import (
	"errors"
	"fmt"
	"os"

	"ani/internal/animetosho"
	"ani/internal/config"
	"ani/internal/mal"
	"ani/internal/player"
	"ani/internal/tui"
	"ani/internal/ui"

	gomal "github.com/nstratos/go-myanimelist/mal"
)

// errBackToAnime is returned by the release picker flow when the user presses
// Esc to go back to anime selection. Run handles it by re-running the anime
// picker instead of exiting.
var errBackToAnime = errors.New("back to anime selection")

// Run is the main flow: resolve an anime → load releases → pick → play or
// download → write back to MAL, looping until cancelled.
//
// The UI is the bubbletea TUI by default; set Options.UseFzf to use the
// legacy fzf/numbered menus instead. In the TUI, pressing Esc in the release
// picker returns to anime selection (Bug 9).
func Run(opt *Options) error {
	resolveResult, err := resolveForReleases(opt)
	if err != nil {
		return err
	}
	all, item := resolveResult.all, resolveResult.item

	for {
		var pick *animetosho.Release
		if opt.UseFzf {
			pick, err = ui.PickRelease(all, opt.Group, opt.Sort, opt.UseFzf, item, opt.DryRun)
		} else {
			pick, err = pickReleaseTUI(all, item, opt)
		}
		if err != nil {
			if errors.Is(err, errBackToAnime) {
				// User pressed Esc in the release picker → restart at anime
				// selection. (Bug 9)
				resolveResult, err = resolveForReleases(opt)
				if err != nil {
					return err
				}
				all, item = resolveResult.all, resolveResult.item
				continue
			}
			return err // q/cancel → exit
		}

		AnnouncePick(pick)
		action := "play"
		if !opt.DryRun {
			action, err = promptAction(pick.Entry.Title, opt.UseFzf)
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

// resolveOutcome bundles the outputs of the resolve+fetch step so Run can
// re-run it on a "back to anime selection" without duplicating logic.
type resolveOutcome struct {
	all  []*animetosho.Release
	item *mal.Item
}

// resolveForReleases runs the anime picker, fetches releases, and returns the
// full release set plus the selected anime item.
func resolveForReleases(opt *Options) (*resolveOutcome, error) {
	aid, title, item, err := Resolve(opt)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "Loading releases (anidb %d)…\n", aid)
	entries, err := animetosho.FetchSeriesReleases(aid)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no releases found for anidb %d", aid)
	}
	all := animetosho.ToReleases(entries)
	if title == "" {
		if t := entries[0].Series.Title; t != "" {
			title = t
		}
	}
	fmt.Fprintf(os.Stderr, "%s — %d releases\n", title, len(all))
	return &resolveOutcome{all: all, item: item}, nil
}

// pickReleaseTUI drives the bubbletea release picker. dry-run auto-picks the
// first release so the exec commands can still be printed without a TUI.
func pickReleaseTUI(all []*animetosho.Release, item *mal.Item, opt *Options) (*animetosho.Release, error) {
	if opt.DryRun {
		view := ui.SortedReleases(ui.FilterByGroup(all, opt.Group), opt.Sort)
		if len(view) == 0 {
			return nil, fmt.Errorf("no releases for group %q", ui.GroupLabel(opt.Group))
		}
		fmt.Fprintf(os.Stderr, "DRY-RUN: TUI would show %d releases, auto-picking first\n", len(view))
		return view[0], nil
	}
	res, err := tui.RunReleasePicker(all, item, opt.Group, opt.Quality, opt.Sort, opt.Debug)
	if err != nil {
		return nil, err
	}
	// Thread the user's filter choices back into opt and persist them to disk on
	// EVERY exit — including quit and Esc-back — so they survive the post-play
	// loop, back-navigation, and the next session. (Previously this only ran on
	// release selection, so quitting lost any filter changes and config.json
	// was never written.)
	if res != nil {
		opt.Group = res.FilterGroup
		opt.Quality = res.FilterQuality
		opt.Sort = res.FilterSort
		config.SaveFilters(res.FilterGroup, res.FilterQuality, res.FilterSort)
	}
	if res != nil && res.Back {
		// Esc in the release picker → go back to anime selection (Bug 9).
		return nil, errBackToAnime
	}
	if res == nil || res.Quit || res.Release == nil {
		return nil, errors.New("cancelled")
	}
	return res.Release, nil
}

// promptAction asks play or download via the TUI (bubbletea) or the legacy
// readline prompt for the fzf flow.
func promptAction(releaseTitle string, useFzf bool) (string, error) {
	if useFzf {
		return ui.PromptAction()
	}
	return tui.RunActionPrompt(releaseTitle)
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
  --fzf               use the legacy fzf UI instead of the bubbletea TUI

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
