package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"ani/internal/animetosho"
	"ani/internal/mal"
	"ani/internal/ui"

	gomal "github.com/nstratos/go-myanimelist/mal"
)

// Resolve picks an anime via MyAnimeList and resolves the AniDB id.
//   - numeric query  → direct anidb id (no MAL)
//   - empty query     → user's MAL list (filtered by --status)
//   - otherwise       → MAL text search
//
// The AniDB id comes from MAL's external links, falling back to an animetosho
// title search when a MAL anime has no AniDB link.
func Resolve(query, status string, useFzf, dryRun, debug bool) (aid int, title string, item *mal.Item, err error) {
	if n, perr := strconv.Atoi(query); perr == nil && n > 0 {
		return n, "", nil, nil
	}

	var items []mal.Item
	if query == "" {
		fmt.Fprintf(os.Stderr, "Loading MAL list (%s)…\n", status)
		items, err = mal.MyList(mapStatus(status), debug)
	} else {
		fmt.Fprintf(os.Stderr, "Searching MAL for %q…\n", query)
		items, err = mal.Search(query, debug)
	}
	if err != nil {
		return 0, "", nil, err
	}
	if len(items) == 0 {
		return 0, "", nil, fmt.Errorf("no anime found")
	}

	item, err = ui.PickMALAnime(items, useFzf, dryRun)
	if err != nil {
		return 0, "", nil, err
	}

	aid, aerr := mal.AnidbAID(item.MalID, debug)
	if aerr == nil && aid == 0 {
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
			fmt.Print("\n  Mark as completed on MAL? [Y/n] ")
			answer, _ := ui.ReadLine()
			if answer == "" || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
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
