package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"ani/internal/animetosho"
	"ani/internal/mal"
	"ani/internal/tui"
	"ani/internal/ui"

	gomal "github.com/nstratos/go-myanimelist/mal"
)

// Resolve picks an anime via MyAnimeList and resolves the AniDB id.
//   - numeric query  → direct anidb id (no MAL)
//   - empty query     → user's MAL list (filtered by --status)
//   - otherwise       → MAL text search
//
// The AniDB id comes from MAL's external links, falling back to an animetosho
// title search when a MAL anime has no AniDB link. When opt.UseFzf is true the
// legacy fzf picker is used; otherwise the bubbletea TUI is used.
func Resolve(opt *Options) (aid int, title string, item *mal.Item, err error) {
	if n, perr := strconv.Atoi(opt.Query); perr == nil && n > 0 {
		return n, "", nil, nil
	}

	var items []mal.Item
	if opt.Query == "" {
		fmt.Fprintf(os.Stderr, "Loading MAL list (%s)…\n", opt.Status)
		items, err = mal.MyList(mapStatus(opt.Status), opt.Debug)
	} else {
		fmt.Fprintf(os.Stderr, "Searching MAL for %q…\n", opt.Query)
		items, err = mal.Search(opt.Query, opt.Debug)
	}
	if err != nil {
		return 0, "", nil, err
	}
	if len(items) == 0 {
		return 0, "", nil, fmt.Errorf("no anime found")
	}

	if opt.UseFzf {
		item, err = ui.PickMALAnime(items, opt.UseFzf, opt.DryRun)
		if err != nil {
			return 0, "", nil, err
		}
	} else {
		mode := tui.AnimeModeList
		query := ""
		if opt.Query != "" {
			mode = tui.AnimeModeSearch
			query = opt.Query
		}
		res, perr := tui.RunAnimePicker(items, mode, query, opt.Debug)
		if perr != nil {
			return 0, "", nil, perr
		}
		if res == nil || res.Quit || res.Anime == nil {
			return 0, "", nil, fmt.Errorf("cancelled")
		}
		item = res.Anime
	}

	// Prefer the Fribb offline mapping (exact MAL→AniDB, independent of Jikan) —
	// near-complete coverage from a 7.4 MB one-time cached map refreshed weekly.
	if id, ok := mal.AnidbAIDViaFribb(item.MalID, opt.Debug); ok {
		return id, item.Title, item, nil
	}

	aid, aerr := mal.AnidbAID(item.MalID, opt.Debug)
	if aerr != nil {
		fmt.Fprintf(os.Stderr, "%v\n", aerr)
		return 0, "", nil, aerr
	}
	if aid == 0 {
		fmt.Fprintf(os.Stderr, "No AniDB link on MAL for %q — searching animetosho…\n", item.Title)
		aid = ui.FallbackAnidbByTitle(item.Title)
	}
	if aid == 0 {
		return 0, "", nil, fmt.Errorf("could not resolve an AniDB id for %q", item.Title)
	}
	return aid, item.Title, item, nil
}

// MalWriteBack updates MAL progress after a play/download (best-effort).
func MalWriteBack(item *mal.Item, pick *animetosho.Release, opt *Options) {
	if item == nil || pick.IsBatch {
		return
	}
	watched := item.WatchedEps
	if pick.Episode > 0 {
		watched = pick.Episode
	}
	// For movies/specials (TotalEps == 1), the release has no episode number,
	// so just set watched to 1 (watched the whole thing).
	if item.TotalEps == 1 && pick.Episode == 0 {
		watched = 1
	}

	// Determine the status to send. Default: keep current status.
	// Only change to "watching" if not in the list yet or plan_to_watch.
	status := gomal.AnimeStatus(item.ListStatus)
	if status == "" || status == gomal.AnimeStatusPlanToWatch {
		status = gomal.AnimeStatusWatching
	}

	// Prompt to mark completed when the last episode is reached
	// and status is not already "completed" (covers dropped, on_hold, etc).
	if item.TotalEps > 0 && watched >= item.TotalEps && status != gomal.AnimeStatusCompleted {
		if opt.DryRun {
			fmt.Fprintf(os.Stderr, "DRY-RUN: would prompt to mark completed\n")
		} else {
			markCompleted := promptCompleted(item.Title, opt.UseFzf)
			if markCompleted {
				status = gomal.AnimeStatusCompleted
				fmt.Fprintln(os.Stderr, "  Marked as completed on MAL.")
			}
		}
	}

	if err := mal.Update(item.MalID, watched, status, opt.DryRun, opt.Debug); err != nil {
		fmt.Fprintf(os.Stderr, "MAL update failed: %v\n", err)
	}
	mal.RefreshItem(item, opt.DryRun, opt.Debug)
}

// promptCompleted asks the user whether to mark the anime completed. Uses the
// bubbletea prompt by default, or the plain readline prompt for the fzf flow.
func promptCompleted(title string, useFzf bool) bool {
	if useFzf {
		fmt.Print("\n  Mark as completed on MAL? [Y/n] ")
		answer, _ := ui.ReadLine()
		return answer == "" || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
	}
	ok, err := tui.RunCompletedPrompt(title)
	if err != nil {
		return false
	}
	return ok
}
