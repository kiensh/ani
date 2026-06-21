package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
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
		fzf    bool
		noFzf  bool
	}
	fs.StringVar(&opt.group, "group", "", "filter by release group (e.g. Erai-raws)")
	fs.StringVar(&opt.sort, "sort", "", "newest|oldest|smallest|largest")
	fs.StringVar(&opt.player, "player", "", "streaming player for play (mpv default)")
	fs.StringVar(&opt.dir, "dir", "", "download directory (default cwd)")
	fs.BoolVar(&opt.fzf, "fzf", false, "use fzf for menus")
	fs.BoolVar(&opt.noFzf, "no-fzf", false, "disable fzf")
	if err := fs.Parse(intersperseFlags(args)); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(os.Stdout)
		return nil
	}
	query := rest[0]
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("query is empty")
	}

	cfg := loadConfig()
	group := orDefault(opt.group, cfg.Group)
	sortName := normalizeSort(orDefault(opt.sort, cfg.Sort))
	player := orDefault(opt.player, cfg.Player)
	if player == "" {
		player = "mpv"
	}
	dir := orDefault(opt.dir, cfg.Dir)
	useFzf := !opt.noFzf && (opt.fzf || cfg.Fzf) && fzfAvailable()

	aid, title, err := resolveAnime(query, useFzf)
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
	if title == "" || strings.HasPrefix(title, "anidb:") {
		if t := entries[0].Series.Title; t != "" {
			title = t
		}
	}

	fmt.Printf("\n%s — %d releases\n", title, len(all))
	st := &browseState{group: group, sort: normalizeSort(sortName)}
	for {
		pick, err := browseReleases(all, st, useFzf)
		if err != nil {
			return err // q/cancel → exit
		}

		announcePick(pick)
		action, err := promptAction()
		if err != nil {
			return err
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
		// loop: return to the release picker for the next file
	}
}

// resolveAnime returns the chosen AniDB id + title. A numeric query is treated
// as a direct anidb id (skips the picker). Otherwise it searches, dedups by
// anidb id, enriches each candidate with clean title/year/episodes, and sorts
// by year desc. If the raw query yields no distinct anime it retries once with
// a shortened query (long official titles often return 0).
func resolveAnime(query string, useFzf bool) (aid int, title string, err error) {
	if n, perr := strconv.Atoi(query); perr == nil && n > 0 {
		return n, "", nil
	}

	series, err := searchSeries(query)
	if err != nil {
		return 0, "", err
	}
	series = dedupSeries(series)
	if len(series) == 0 {
		if short := shortenQuery(query); short != "" && short != query {
			series, err = searchSeries(short)
			if err != nil {
				return 0, "", err
			}
			series = dedupSeries(series)
		}
	}
	if len(series) == 0 {
		return 0, "", fmt.Errorf("no anime found for %q", query)
	}

	fmt.Fprintf(os.Stderr, "Resolving %d anime…\n", len(series))
	cands := enrichAnime(series)
	sortCandidatesByYear(cands)

	lines := make([]string, len(cands))
	for i, c := range cands {
		lines[i] = renderAnimeLine(c)
	}
	idx, err := pickIndex(lines, fmt.Sprintf("Anime (%d)", len(cands)), "Select anime", useFzf)
	if err != nil {
		return 0, "", err
	}
	return cands[idx].Aid, cands[idx].CleanTitle, nil
}

// shortenQuery reduces an over-long official title to its core, for the
// retry-on-empty fallback. Tries the part before the first ':'/'-', else the
// first three whitespace words.
func shortenQuery(q string) string {
	q = strings.TrimSpace(q)
	for _, sep := range []string{":", " - ", " – "} {
		if i := strings.Index(q, sep); i > 1 {
			return strings.TrimSpace(q[:i])
		}
	}
	words := strings.Fields(q)
	if len(words) > 3 {
		return strings.Join(words[:3], " ")
	}
	return q
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
