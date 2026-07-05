// Package app wires the ani subcommands together: config + MAL resolve +
// animetosho releases + ui pickers + player launch.
package app

import (
	"errors"
	"fmt"
	"os"
	"sync"

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

// Run is the main flow: resolve an anime → pick releases (fetched on demand,
// scoped to the chosen episode) → play or download → write back to MAL,
// looping until cancelled.
//
// The UI is the bubbletea TUI by default; set Options.UseFzf to use the
// legacy fzf/numbered menus instead. In the TUI, pressing Esc in the release
// picker returns to anime selection (Bug 9).
func Run(opt *Options) error {
	resolveResult, err := resolveForReleases(opt)
	if err != nil {
		return err
	}
	aid, item := resolveResult.aid, resolveResult.item
	cache := &episodeCache{data: map[int][]*animetosho.Release{}}
	fetch := cachedFetch(aid, cache)

	for {
		var pick *animetosho.Release
		if opt.UseFzf {
			// Legacy fzf UI: fetch the default episode's releases (scoped + cached).
			all := fetch(tui.DefaultEpisode(item.WatchedEps, item.TotalEps))
			pick, err = ui.PickRelease(all, opt.Group, opt.Sort, opt.UseFzf, item, opt.DryRun)
		} else {
			pick, err = pickReleaseTUI(item, opt, fetch)
		}
		if err != nil {
			if errors.Is(err, errBackToAnime) {
				// User pressed Esc in the release picker → restart at anime
				// selection with a fresh cache. (Bug 9)
				resolveResult, err = resolveForReleases(opt)
				if err != nil {
					return err
				}
				aid, item = resolveResult.aid, resolveResult.item
				cache = &episodeCache{data: map[int][]*animetosho.Release{}}
				fetch = cachedFetch(aid, cache)
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
		// loop: return to the release picker for the next file. The cache (and
		// its prefetched ep+1) carry over, so the next episode loads instantly.
	}
}

// resolveOutcome bundles the outputs of the resolve step so Run can re-run it
// on a "back to anime selection" without duplicating logic.
type resolveOutcome struct {
	aid  int
	item *mal.Item
}

// resolveForReleases runs the anime picker and returns the AniDB id + selected
// anime item. Releases are NOT fetched here — the release picker fetches them
// on demand, scoped to the chosen episode (fast even for huge series).
func resolveForReleases(opt *Options) (*resolveOutcome, error) {
	aid, _, item, err := Resolve(opt)
	if err != nil {
		return nil, err
	}
	return &resolveOutcome{aid: aid, item: item}, nil
}

// episodeCache memoizes fetched releases per episode for the current anime so
// re-visiting an episode (or the prefetched next one) is instant. Keyed by
// episode number (0 = "all"). Not safe to share across animes — Run allocates
// a fresh one per anime.
type episodeCache struct {
	mu   sync.Mutex
	data map[int][]*animetosho.Release
}

func (c *episodeCache) get(ep int) []*animetosho.Release {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data[ep]
}

func (c *episodeCache) put(ep int, r []*animetosho.Release) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[ep] = r
}

// cachedFetch returns an episode fetch func that serves from the cache, falling
// back to animetosho.FetchReleases(aid, ep) and caching the result.
func cachedFetch(aid int, cache *episodeCache) func(int) []*animetosho.Release {
	return func(ep int) []*animetosho.Release {
		if r := cache.get(ep); r != nil {
			return r
		}
		r, _ := animetosho.FetchReleases(aid, ep)
		cache.put(ep, r)
		return r
	}
}

// pickReleaseTUI drives the bubbletea release picker, fetching releases on
// demand via the cached fetch func. dry-run auto-picks the first release of the
// default episode so the exec commands can still be printed without a TUI.
func pickReleaseTUI(item *mal.Item, opt *Options, fetch func(int) []*animetosho.Release) (*animetosho.Release, error) {
	if opt.DryRun {
		all := fetch(tui.DefaultEpisode(item.WatchedEps, item.TotalEps))
		view := ui.SortedReleases(ui.FilterByGroup(all, opt.Group), opt.Sort)
		if len(view) == 0 {
			return nil, fmt.Errorf("no releases for group %q", ui.GroupLabel(opt.Group))
		}
		fmt.Fprintf(os.Stderr, "DRY-RUN: TUI would show %d releases, auto-picking first\n", len(view))
		return view[0], nil
	}
	res, err := tui.RunReleasePicker(item, opt.Group, opt.Quality, opt.Sort, fetch, opt.Debug)
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
