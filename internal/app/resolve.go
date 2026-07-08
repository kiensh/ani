package app

import (
	"fmt"
	"os"

	"ani/internal/animetosho"
	"ani/internal/mal"
	"ani/internal/tui"

	gomal "github.com/nstratos/go-myanimelist/mal"
)

// MalWriteBack updates MAL progress after a play/download (best-effort, no-op
// when the item has no MAL id — e.g. a direct AniDB id or the latest-uploads
// view). Prompts to mark the series completed when the last episode is reached.
func MalWriteBack(item *mal.Item, pick *animetosho.Release, opt *Options) {
	if item == nil || item.MalID == 0 || pick.IsBatch {
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

	// Prompt to mark completed when the last episode is reached and the status
	// is not already "completed" (covers dropped, on_hold, etc).
	if item.TotalEps > 0 && watched >= item.TotalEps && status != gomal.AnimeStatusCompleted {
		if opt.DryRun {
			fmt.Fprintf(os.Stderr, "DRY-RUN: would prompt to mark completed\n")
		} else {
			// RunCompletedPrompt shows a green success flash on yes, so no
			// separate log line is needed here.
			if promptCompleted(item.Title) {
				status = gomal.AnimeStatusCompleted
			}
		}
	}

	if err := mal.Update(item.MalID, watched, status, opt.DryRun, opt.Debug); err != nil {
		fmt.Fprintf(os.Stderr, "MAL update failed: %v\n", err)
	}
	mal.RefreshItem(item, opt.DryRun, opt.Debug)
}

// promptCompleted asks whether to mark the anime completed via the green-flash
// modal. Returns true for yes.
func promptCompleted(title string) bool {
	ok, _ := tui.RunCompletedPrompt(title)
	return ok
}
