// Package tui is the bubbletea-based terminal UI for ani.
//
// The package exposes two entry points — RunAnimePicker and RunReleasePicker —
// each of which drives one screen to completion and returns a Result. The
// root model is a small state machine that sequences the screens:
//
//	anime picker → release picker → (completed prompt)
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"ani/internal/animetosho"
	"ani/internal/mal"
	"ani/internal/playable"
)

// Result is what a TUI screen returns to its caller. Each Run* function fills
// in the fields relevant to its screen; Quit is set when the user backed out.
type Result struct {
	Quit      bool              // user quit without selecting
	Back      bool              // user wants to return to the previous screen
	Anime     *mal.Item         // selected anime (anime picker)
	Release   *playable.Release // selected release (release picker)
	Action    string            // "play" or "download" (release picker: Enter / d)
	Completed bool              // mark MAL completed (completed prompt)

	// MAL auth actions requested from the anime picker (L / status overlay). The
	// caller (app.Run) performs them after the TUI exits, then re-resolves.
	Relogin bool // re-run the browser OAuth flow
	Logout  bool // forget the saved MAL token

	// SourceSwitch requests a provider change ("torrent"/"anidb") from either
	// picker's `:` palette; app.Run applies it (persist + re-select the
	// provider's filters, then re-resolve — the release picker re-opens on the
	// same anime).
	SourceSwitch string

	// Filter preferences from the release picker (persisted across sessions).
	FilterGroup   string
	FilterQuality string
	FilterSort    string

	// FilterEpisode is the release picker's episode filter at exit (0 = "all").
	// Consumed only on a provider switch: the re-opened picker restores it
	// instead of falling back to the next-unwatched default.
	FilterEpisode int
}

// RunAnimePicker launches the TUI for anime selection. source is the initial
// browse source (SourceList / SourceSeason); query non-empty means search. load
// supplies items per (source, query, season); applyStatus applies a per-anime
// set-status/remove action, applyScore sets the score (nil disables either);
// latestEpisode backs the "watched/aired/total" display for the focused airing
// anime (nil disables); latestEpisodePrefetch is the fast-only background variant
// that pages the aired-episode prefetch (nil disables aired prefetch; covers are
// still paged). state carries the session's picker options/cursor/list cache
// across re-entries (nil = fresh defaults). Returns the selected anime, or
// Quit=true on cancel.
func RunAnimePicker(source AnimeSource, query string, load AnimeLoad, applyStatus func(int, int, StatusAction) bool, applyScore func(int, int) bool, applyWatched func(int, int) bool, latestEpisode func(*mal.Item) float64, latestEpisodePrefetch func(*mal.Item) float64, aired *AiredCache, provider string, state *AnimeState, debug bool) (*Result, error) {
	if load == nil {
		return &Result{Quit: true}, nil
	}
	m := newAnimePicker(source, query, load, applyStatus, applyScore, applyWatched, latestEpisode, latestEpisodePrefetch, debug)
	m.provider = provider // drives the palette's provider switch (● active marker)
	if aired != nil {
		m.aired = aired // reuse the session cache across Esc-from-releases
	}
	m.restoreState(state) // previous options/cursor + list cache (nil-safe)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	if ap, ok := final.(*animePicker); ok {
		ap.saveState(state) // on every exit — keep options/cursor for re-entry
		return ap.result, nil
	}
	return &Result{Quit: true}, nil
}

// RunReleasePicker launches the TUI for release selection. item provides the
// anime info shown in the header; group/quality/sortName seed the initial
// filter. fetch returns the releases for a given episode (cached + scoped by
// the caller) and is invoked on demand: initially for the default episode, and
// again whenever the user changes the episode filter. disableEpisode suppresses
// the episode filter (latest-uploads view). provider is the active backend
// ("torrent"/"anidb"; empty hides the palette's provider switch). copyMagnet
// backs the Space menu's "Copy Magnet URL"; latestEpisode backs the
// "watched/aired/total" header (nil disables each).
func RunReleasePicker(item *mal.Item, group, quality, sortName string, fetch func(int) []*playable.Release, disableEpisode bool, copyMagnet func(string) error, latestEpisode func(*mal.Item) float64, aired *AiredCache, defaultEpisode int, provider string, debug bool) (*Result, error) {
	if item == nil || fetch == nil {
		return &Result{Quit: true}, nil
	}
	m := newReleasePicker(item, group, quality, sortName, fetch, disableEpisode, copyMagnet, latestEpisode, aired, defaultEpisode, debug)
	m.provider = provider // drives the palette's provider switch (● active marker)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	if rp, ok := final.(*releasePicker); ok {
		rp.result.FilterGroup = rp.filter.Group
		rp.result.FilterQuality = rp.filter.Quality
		rp.result.FilterSort = rp.filter.Sort
		rp.result.FilterEpisode = rp.filter.Episode
		return rp.result, nil
	}
	return &Result{Quit: true}, nil
}

// RunSeriesPicker launches the manual AnimeTosho-series fallback: a two-pane
// picker over the given series so the user can choose the matching AniDB entry
// when auto resolution fails. Returns (aid, true) on selection, (0, false) on
// cancel or empty input.
func RunSeriesPicker(header string, series []animetosho.SeriesSummary) (int, bool) {
	if len(series) == 0 {
		return 0, false
	}
	m := newSeriesPicker(header, series)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil || final == nil {
		return 0, false
	}
	sp, ok := final.(*seriesPicker)
	if !ok {
		return 0, false
	}
	sp.Cleanup()
	return sp.result.aid, sp.result.ok
}
