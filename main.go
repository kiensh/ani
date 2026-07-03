package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nstratos/go-myanimelist/mal"
)

// debugMode is set by --debug. When true, verbose logs are printed (MAL request
// URLs, PKCE info, raw responses). Tools still run normally.
var debugMode bool

// dryRunMode is set by --dry-run. When true, fzf/exec are not run; their
// commands and input data are printed to stderr instead (see runFzf,
// runWithSignals). The first fzf item is auto-picked and the loop runs once.
var dryRunMode bool

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
		previewRelease(os.Args[2], os.Args[3])
		return
	}
	if len(os.Args) >= 4 && os.Args[1] == "preview-anime" {
		previewAnime(os.Args[2], os.Args[3])
		return
	}
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errCancelled) {
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
	}
	fs.StringVar(&opt.group, "group", "", "pre-filter by release group (e.g. Erai-raws)")
	fs.StringVar(&opt.sort, "sort", "", "newest|oldest|smallest|largest (initial order)")
	fs.StringVar(&opt.player, "player", "", "streaming player for play (mpv default)")
	fs.StringVar(&opt.dir, "dir", "", "download directory (default cwd)")
	fs.StringVar(&opt.status, "status", "watching", "MAL list status: watching|completed|on_hold|dropped|plan_to_watch|all")
	fs.BoolVar(&opt.noFzf, "no-fzf", false, "disable fzf (use numbered menus)")
	fs.BoolVar(&debugMode, "debug", false, "verbose logging (MAL URLs, raw responses)")
	fs.BoolVar(&dryRunMode, "dry-run", false, "auto-pick first fzf item and print exec commands without running them")
	if err := fs.Parse(intersperseFlags(args)); err != nil {
		return err
	}

	rest := fs.Args()
	query := ""
	if len(rest) > 0 {
		query = rest[0]
	}

	cfg := loadConfig()
	group := orDefault(opt.group, cfg.Group)
	sortName := normalizeSort(orDefault(opt.sort, cfg.Sort))
	player := orDefault(opt.player, cfg.Player)
	if player == "" {
		player = "mpv"
	}
	dir := orDefault(opt.dir, cfg.Dir)
	useFzf := fzfAvailable() && !opt.noFzf

	aid, title, item, err := resolve(query, opt.status, useFzf)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Loading releases (anidb %d)…\n", aid)
	entries, err := fetchSeriesReleases(aid)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no releases found for anidb %d", aid)
	}
	all := toReleases(entries)
	if title == "" {
		if t := entries[0].Series.Title; t != "" {
			title = t
		}
	}

	fmt.Fprintf(os.Stderr, "%s — %d releases\n", title, len(all))
	for {
		pick, err := pickRelease(all, group, sortName, useFzf, item)
		if err != nil {
			return err // q/cancel → exit
		}

		announcePick(pick)
		action := "play"
		if !dryRunMode {
			action, err = promptAction()
			if err != nil {
				return err
			}
		}
		if action == "download" {
			if err := runDownload(pick, dir); err != nil {
				return err
			}
		} else {
			if err := runPlay(pick, player); err != nil {
				return err
			}
		}
		malWriteBack(item, pick)
		if dryRunMode {
			return nil // one iteration: print commands, then exit
		}
		// loop: return to the release picker for the next file
	}
}

// resolve picks an anime via MyAnimeList and resolves the AniDB id.
//   - numeric query  → direct anidb id (no MAL)
//   - empty query     → user's MAL list (filtered by --status)
//   - otherwise       → MAL text search
// The AniDB id comes from MAL's external links, falling back to an animetosho
// title search when a MAL anime has no AniDB link.
func resolve(query, status string, useFzf bool) (aid int, title string, item *MALItem, err error) {
	if n, perr := strconv.Atoi(query); perr == nil && n > 0 {
		return n, "", nil, nil
	}

	var items []MALItem
	if query == "" {
		fmt.Fprintf(os.Stderr, "Loading MAL list (%s)…\n", status)
		items, err = malMyList(mapStatus(status))
	} else {
		fmt.Fprintf(os.Stderr, "Searching MAL for %q…\n", query)
		items, err = malSearch(query)
	}
	if err != nil {
		return 0, "", nil, err
	}
	if len(items) == 0 {
		return 0, "", nil, fmt.Errorf("no anime found")
	}

	item, err = pickMALAnime(items, useFzf)
	if err != nil {
		return 0, "", nil, err
	}

	aid, aerr := malAnidbAid(item.MalID)
	if aerr == nil && aid == 0 {
		fmt.Fprintf(os.Stderr, "No AniDB link on MAL for %q — searching animetosho…\n", item.Title)
		aid = fallbackAnidbByTitle(item.Title)
	}
	if aid == 0 {
		return 0, "", nil, fmt.Errorf("could not resolve an AniDB id for %q", item.Title)
	}
	return aid, item.Title, item, nil
}

func mapStatus(s string) mal.AnimeStatus {
	switch s {
	case "watching":
		return mal.AnimeStatusWatching
	case "completed":
		return mal.AnimeStatusCompleted
	case "on_hold":
		return mal.AnimeStatusOnHold
	case "dropped":
		return mal.AnimeStatusDropped
	case "plan_to_watch":
		return mal.AnimeStatusPlanToWatch
	}
	return "" // "all" / unknown → no status filter
}

// malWriteBack updates MAL progress after a play/download (best-effort).
func malWriteBack(item *MALItem, pick *Release) {
	if item == nil || pick.IsBatch {
		return
	}
	watched := item.WatchedEps
	if pick.Episode > 0 {
		watched = pick.Episode
	}

	// Determine the status to send. Default: keep current status.
	// Only change to "watching" if not in the list yet or plan_to_watch.
	status := mal.AnimeStatus(item.ListStatus)
	if status == "" || status == mal.AnimeStatusPlanToWatch {
		status = mal.AnimeStatusWatching
	}

	// Prompt to mark completed when the last episode is reached
	// and status is not already "completed" (covers dropped, on_hold, etc).
	if item.TotalEps > 0 && watched >= item.TotalEps && status != mal.AnimeStatusCompleted {
		if dryRunMode {
			fmt.Fprintf(os.Stderr, "DRY-RUN: would prompt to mark completed\n")
		} else {
			fmt.Print("\n  Mark as completed on MAL? [Y/n] ")
			answer, _ := readLine()
			if answer == "" || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
				status = mal.AnimeStatusCompleted
				fmt.Fprintln(os.Stderr, "  Marked as completed on MAL.")
			}
		}
	}

	if err := malUpdate(item.MalID, watched, status); err != nil {
		fmt.Fprintf(os.Stderr, "MAL update failed: %v\n", err)
	}
	malRefreshItem(item)
}

func announcePick(r *Release) {
	grp := r.Group
	if grp == "" {
		grp = "?"
	}
	fmt.Printf("\n> [%s] %s\n  %s, %d seeders\n", grp, r.Entry.Title, humanSize(r.Entry.SizeBytes), r.Entry.Seeders)
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func printUsage(w *os.File) {
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
