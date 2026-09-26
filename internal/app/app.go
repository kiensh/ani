// Package app wires ani's flow: resolve an anime (MAL when logged in, otherwise
// AnimeTosho) → pick releases → play or download → write back to MAL.
package app

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"

	"ani/internal/animetosho"
	"ani/internal/config"
	"ani/internal/hianime"
	"ani/internal/mal"
	"ani/internal/playable"
	"ani/internal/player"
	"ani/internal/tui"
	"ani/internal/ui"
)

// errBackToAnime is returned when the user presses Esc in the release picker.
// Run re-runs anime selection instead of exiting.
var errBackToAnime = errors.New("back to anime selection")

// errRelogin / errLogout signal that the anime picker requested a MAL auth action.
// Run performs it after the TUI exits (so the browser flow runs in the normal
// terminal), then re-resolves — MAL after login, AnimeTosho after logout.
var (
	errRelogin = errors.New("re-login")
	errLogout  = errors.New("logout")
)

// errSourceSwitch signals that a picker requested a provider change via the
// `:` palette. From the anime picker, Run applies it and re-resolves; from the
// release picker, releaseLoop applies it and re-opens the same anime. Carries
// the target source ("torrent"/"hianime") and, from the release picker, the
// selected episode to restore (0 = none → default; tui.DefaultEpisodeAll =
// "all").
type errSourceSwitch struct {
	source  string
	episode int
}

func (e errSourceSwitch) Error() string { return "switch provider to " + e.source }

// ErrCancelled is returned when the user quits a picker without selecting. main
// exits silently on it.
var ErrCancelled = errors.New("cancelled")

// latestUploadsAID is a sentinel AniDB id signalling the no-arg AnimeTosho
// landing screen (the newest uploads, flat list, episode filter disabled).
const latestUploadsAID = -1

// Run is the main flow: resolve an anime, then loop picking releases →
// play/download → write back to MAL. Esc in the release picker returns to
// anime selection.
func Run(opt *Options) error {
	aired := tui.NewAiredCache()       // session-scoped; shared across the anime + release pickers
	relcache := newReleaseCache()      // session-scoped; fetched releases survive Esc-back / re-entry
	health := tui.NewProviderHealth()  // session-scoped; backend reachability for the down warning
	animeState := tui.NewAnimeState()  // session-scoped; anime picker's options/cursor/cache survive Esc-back
	go mal.WarmAidResolvers(opt.Debug) // overlap the one-time Fribb/AniDB map load with the first MAL fetch
	for {
		aid, item, err := resolve(opt, aired, health, animeState)
		if errors.Is(err, errRelogin) {
			if e := mal.Login(opt.Debug); e != nil {
				fmt.Fprintf(os.Stderr, "ani: login failed: %v\n", e)
			}
			continue // re-resolve: MAL if login worked, else AnimeTosho
		}
		if errors.Is(err, errLogout) {
			if e := mal.Logout(); e != nil {
				fmt.Fprintf(os.Stderr, "ani: logout failed: %v\n", e)
			}
			continue // re-resolve: token gone → AnimeTosho
		}
		var srcSwitch errSourceSwitch
		if errors.As(err, &srcSwitch) {
			// Provider change from the anime picker's `:` palette: apply it and
			// re-resolve (the release picker handles its own switches in place).
			applySourceSwitch(opt, srcSwitch.source, health)
			aired.Reset()
			relcache.Reset()
			continue
		}
		if err != nil {
			return err
		}
		if err := releaseLoop(opt, aid, item, aired, health, relcache); err != nil {
			if errors.Is(err, errBackToAnime) {
				continue // Esc in release picker (or a dead switched-to provider) → re-resolve
			}
			return err
		}
		return nil
	}
}

// resolve picks an anime and returns its AniDB id + item. A numeric query is a
// direct AniDB id (no MAL); otherwise MAL when logged in, else AnimeTosho
// (series search by name, or latest uploads when no query).
func resolve(opt *Options, aired *tui.AiredCache, health *tui.ProviderHealth, animeState *tui.AnimeState) (int, *mal.Item, error) {
	if n, perr := strconv.Atoi(opt.Query); perr == nil && n > 0 {
		return resolveAnidb(n)
	}
	if mal.LoggedIn() {
		return resolveMal(opt, aired, health, animeState)
	}
	return resolveAnimetosho(opt, health, animeState)
}

// resolveAnidb builds a minimal item from the series metadata (no MAL).
func resolveAnidb(aid int) (int, *mal.Item, error) {
	title, _, totalEps, _, _ := animetosho.SeriesMeta(aid)
	if title == "" {
		title = fmt.Sprintf("anidb/%d", aid)
	}
	return aid, &mal.Item{Title: title, TotalEps: totalEps}, nil
}

// resolveMal runs the anime picker over MAL and resolves the AniDB id from the
// picked item. Browse opens on Season (current); Tab → My List. A non-empty
// query means search.
func resolveMal(opt *Options, aired *tui.AiredCache, health *tui.ProviderHealth, animeState *tui.AnimeState) (int, *mal.Item, error) {
	query := opt.Query
	source := tui.SourceSeason // default browse source
	load := func(src tui.AnimeSource, q, season string) ([]mal.Item, error) {
		items, err := malLoad(src, q, season, opt.Debug)
		// Track MAL reachability for the down warning (errors here are
		// auth/network — an empty result is NOT an error, so no false mark).
		if err != nil {
			health.MarkDown("mal", downReason(err))
		} else {
			health.MarkUp("mal")
		}
		return items, err
	}
	applyStatus := func(malID, watched int, act tui.StatusAction) bool {
		var err error
		if act.Remove {
			err = mal.RemoveFromList(malID, opt.DryRun, opt.Debug)
		} else {
			err = mal.SetStatus(malID, watched, act.Status, opt.DryRun, opt.Debug)
		}
		return err == nil && !opt.DryRun
	}
	latestEpisode := latestEpisodeFn(opt, health)
	// Favorite studios for the preview's "(favorite)" marker and the filter's
	// "favorite" match — mal.FavoriteStudios memoizes, so every call after the
	// first is free.
	favStudios := func() map[string]bool { return mal.FavoriteStudios(opt.Debug) }
	// Related-anime navigation (h/l): the focused anime's related ring + full
	// items for peeked entries (prequel/sequel/side stories/OVAs/movies/…).
	relatedSrc := &tui.RelatedSource{
		List: func(id int) ([]mal.RelatedEntry, error) { return mal.Related(id, opt.Debug) },
		Item: func(id int) (mal.Item, error) { return mal.AnimeItem(id, opt.Debug) },
	}
	applyScore := func(malID, score int) bool {
		err := mal.SetScore(malID, score, opt.DryRun, opt.Debug)
		return err == nil && !opt.DryRun
	}
	applyWatched := func(malID, watched int) bool {
		err := mal.SetWatched(malID, watched, opt.DryRun, opt.Debug)
		return err == nil && !opt.DryRun
	}
	if opt.DryRun {
		// Dry-run: skip the anime picker, auto-pick the first match so the whole
		// flow is non-interactive (the release picker dry-runs separately).
		return resolveMalDry(opt, source, query, load)
	}
	res, err := tui.RunAnimePicker(source, query, load, applyStatus, applyScore, applyWatched, latestEpisode, latestEpisodePrefetchFn(opt, health), aired, health, opt.Source, animeState, favStudios, relatedSrc, opt.Debug)
	if err != nil {
		return 0, nil, err
	}
	if res != nil && res.Relogin {
		return 0, nil, errRelogin
	}
	if res != nil && res.Logout {
		return 0, nil, errLogout
	}
	if res != nil && res.SourceSwitch != "" {
		return 0, nil, errSourceSwitch{source: res.SourceSwitch}
	}
	if res == nil || res.Quit || res.Anime == nil {
		return 0, nil, ErrCancelled
	}
	item := res.Anime
	// hianime (streaming) resolves by title in streamLoop — no AniDB aid needed.
	if opt.Source == "hianime" {
		return 0, item, nil
	}
	aid := item.AnidbAID
	if aid == 0 {
		aid = resolveAnidbFromMAL(item, opt)
	}
	if aid == 0 {
		// Last resort: manual AnimeTosho-series picker (cached on choice).
		aid = resolveAnidbManual(item, opt)
	}
	if aid == 0 {
		return 0, nil, fmt.Errorf("could not resolve an AniDB id for %q", item.Title)
	}
	item.AnidbAID = aid // carry the resolved aid so the release picker's aired fallback can reuse it
	return aid, item, nil
}

// resolveMalDry is the --dry-run path: skip the anime picker and auto-pick the
// first item from load, so the whole flow is non-interactive.
func resolveMalDry(opt *Options, source tui.AnimeSource, query string, load tui.AnimeLoad) (int, *mal.Item, error) {
	season := mal.SeasonAll
	if source == tui.SourceSeason && query == "" {
		_, _, season = mal.CurrentSeason()
	}
	items, err := load(source, query, season)
	if err != nil {
		return 0, nil, fmt.Errorf("load: %w", err)
	}
	if len(items) == 0 {
		return 0, nil, fmt.Errorf("no anime found for %q", query)
	}
	item := items[0]
	fmt.Fprintf(os.Stderr, "DRY-RUN: auto-picked %q\n", item.Title)
	// hianime resolves by title in streamLoop — no aid needed.
	if opt.Source == "hianime" {
		return 0, &item, nil
	}
	aid := item.AnidbAID
	if aid == 0 {
		aid = resolveAnidbFromMAL(&item, opt)
	}
	if aid == 0 {
		return 0, nil, fmt.Errorf("could not resolve an AniDB id for %q", item.Title)
	}
	item.AnidbAID = aid
	return aid, &item, nil
}

// malLoad fetches the anime list for one picker load — search for a query, else
// My List or a season browse — factored out of resolveMal's load closure so the
// closure can wrap it with health marking. An empty result is not an error.
func malLoad(src tui.AnimeSource, q, season string, debug bool) ([]mal.Item, error) {
	if q != "" {
		return mal.Search(q, debug)
	}
	switch src {
	case tui.SourceList:
		return mal.MyList("", debug)
	default: // SourceSeason
		if season == mal.SeasonLater {
			return mal.Upcoming(debug)
		}
		year, s, ok := mal.ParseSeasonLabel(season)
		if !ok {
			return nil, fmt.Errorf("invalid season %q", season)
		}
		return mal.Seasonal(year, s, debug)
	}
}

// httpCodeRe pulls a status code out of a backend error for the warning line.
var httpCodeRe = regexp.MustCompile(`\b(?:HTTP|returned|status)\s+(\d{3})\b`)

// downReason condenses a backend error for the one-line down warning: the HTTP
// status when the error carries one (a 429 becomes "rate-limited" — the
// backend isn't unreachable, it's refusing our pace and cooling down), else
// the error clipped short.
func downReason(err error) string {
	if m := httpCodeRe.FindStringSubmatch(err.Error()); m != nil {
		if m[1] == "429" {
			return "rate-limited"
		}
		return "HTTP " + m[1]
	}
	s := err.Error()
	if len(s) > 48 {
		s = s[:48] + "…"
	}
	return s
}

// latestEpisodeFn returns the aired-episode lookup both pickers use. AnimeTosho
// is primary: resolve the aid and read the latest episode from its releases (a
// same-day proxy for "aired"). Jikan's episode feed is the fallback —
// authoritative but rate-limited — when the aid can't be resolved or
// AnimeTosho has no releases. nil item → 0.
//
// Every provider path returns tui.AiredFailed when its fetch itself errors
// (site down/blocked/rate-limited) so the pickers don't cache the failure as a
// final 0 — it's retried later. Such an error also marks the provider down on
// health (cleared by any success), which is what drives the pickers' warning
// line. A Jikan failure is NOT marked on health — Jikan mirrors episode data,
// it isn't the MAL list backend, so the "list unavailable" warning would lie.
func latestEpisodeFn(opt *Options, health *tui.ProviderHealth) func(*mal.Item) float64 {
	return func(item *mal.Item) float64 {
		if item == nil {
			return 0
		}
		if opt.Source == "hianime" {
			n, err := hianime.AiredCount(item.Title)
			if err != nil {
				health.MarkDown("hianime", downReason(err))
				return tui.AiredFailed
			}
			health.MarkUp("hianime")
			return n
		}
		if aid := resolveAidFast(item, opt); aid > 0 {
			n, err := animetosho.LatestEpisode(aid, item.TotalEps)
			if err != nil {
				health.MarkDown("torrent", downReason(err)) // drives the warning line
			} else if n > 0 {
				health.MarkUp("torrent")
				return float64(n)
			}
		}
		n, err := mal.LatestEpisode(item.MalID, opt.Debug)
		if err != nil {
			return tui.AiredFailed // both sources failed — unknown, retry later
		}
		return float64(n)
	}
}

// latestEpisodePrefetchFn is the background-prefetch variant of latestEpisodeFn:
// fast-only (no Jikan), and it skips items whose AniDB id can't be resolved from
// the fast sources (override → item aid → Fribb → AniDB titles). Those need the
// manual AnimeTosho selection first, so their aired count isn't available yet —
// and the background prefetch never calls Jikan (rate-limited, errors for some).
// Returns 0 when skipped/unknown; a provider fetch error returns tui.AiredFailed
// (uncached, retried later) and marks it down on health for the warning line.
// The focus path (latestEpisodeFn) still tries the full chain (incl. Jikan) on
// demand for items the prefetch didn't fill.
func latestEpisodePrefetchFn(opt *Options, health *tui.ProviderHealth) func(*mal.Item) float64 {
	return func(item *mal.Item) float64 {
		if item == nil {
			return 0
		}
		if opt.Source == "hianime" {
			n, err := hianime.AiredCount(item.Title)
			if err != nil {
				health.MarkDown("hianime", downReason(err)) // drives the warning line
				return tui.AiredFailed                      // fetch failed — the pickers won't cache it
			}
			health.MarkUp("hianime")
			return n
		}
		aid := resolveAidFast(item, opt)
		if aid <= 0 {
			return 0
		}
		n, err := animetosho.LatestEpisode(aid, item.TotalEps)
		if err != nil {
			health.MarkDown("torrent", downReason(err)) // drives the warning line
			return tui.AiredFailed                      // fetch failed — the pickers won't cache it
		}
		health.MarkUp("torrent")
		return float64(n)
	}
}

// resolveAidFast resolves an AniDB aid for item from the fast/cached sources
// only — the user's manual override, then the item's own aid, Fribb, and the
// AniDB title dump. No Jikan /external call (slow + rate-limited). Returns 0 if
// unresolved.
func resolveAidFast(item *mal.Item, opt *Options) int {
	if id, ok := config.AnidbOverride(item.MalID); ok {
		return id
	}
	if aid := item.AnidbAID; aid > 0 {
		return aid
	}
	if id, ok := mal.AnidbAIDViaFribb(item.MalID, opt.Debug); ok {
		return id
	}
	if id, ok := mal.AnidbAIDByTitle(item.Title, mal.StartYear(item), opt.Debug); ok {
		return id
	}
	return 0
}

// resolveAnidbFromMAL resolves the AniDB id for a MAL item: user override → Fribb
// offline map → AniDB title dump → Jikan external links. Returns 0 if none match
// (the caller then offers the manual animetosho-series fallback).
func resolveAnidbFromMAL(item *mal.Item, opt *Options) int {
	if aid, ok := config.AnidbOverride(item.MalID); ok {
		return aid // user's saved manual choice
	}
	if id, ok := mal.AnidbAIDViaFribb(item.MalID, opt.Debug); ok {
		return id
	}
	if id, ok := mal.AnidbAIDByTitle(item.Title, mal.StartYear(item), opt.Debug); ok {
		return id
	}
	id, err := mal.AnidbAID(item.MalID, opt.Debug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 0
	}
	return id
}

// resolveAnidbManual is the last-resort fallback when auto resolution fails: it
// searches AnimeTosho by title and opens the series picker so the user can pick
// the matching series. The choice is cached (malID → aid) so it resolves
// instantly next time. Returns 0 if the user cancels or nothing is found.
func resolveAnidbManual(item *mal.Item, opt *Options) int {
	series := ui.SearchAnidbSeries(item.Title)
	if len(series) == 0 {
		return 0
	}
	aid, ok := tui.RunSeriesPicker(item.Title, series)
	if !ok || aid <= 0 {
		return 0
	}
	config.SaveAnidbOverride(item.MalID, aid)
	return aid
}

// resolveAnimetosho is the no-MAL path. A text query searches the provider's
// series and lets the user pick; no query returns the latest-uploads sentinel
// (animetosho only — hianime has no equivalent).
func resolveAnimetosho(opt *Options, health *tui.ProviderHealth, animeState *tui.AnimeState) (int, *mal.Item, error) {
	if opt.Source == "hianime" {
		return resolveHianimeNoLogin(opt, health, animeState)
	}
	if opt.Query == "" {
		return latestUploadsAID, &mal.Item{Title: "Latest uploads"}, nil
	}
	series, err := animetosho.SearchSeries(opt.Query)
	if err != nil {
		health.MarkDown("torrent", downReason(err))
		return 0, nil, err
	}
	health.MarkUp("torrent")
	items := seriesToItems(series)
	if len(items) == 0 {
		return 0, nil, fmt.Errorf("no anime found")
	}
	load := func(tui.AnimeSource, string, string) ([]mal.Item, error) { return items, nil }
	if opt.DryRun {
		// Dry-run: skip the anime picker, auto-pick the first series hit.
		item := items[0]
		fmt.Fprintf(os.Stderr, "DRY-RUN: auto-picked %q\n", item.Title)
		if item.AnidbAID == 0 {
			return 0, nil, fmt.Errorf("no AniDB id for %q", item.Title)
		}
		return item.AnidbAID, &item, nil
	}
	res, err := tui.RunAnimePicker(tui.SourceSeason, opt.Query, load, nil, nil, nil, nil, nil, nil, health, opt.Source, animeState, nil, nil, opt.Debug)
	if err != nil {
		return 0, nil, err
	}
	if res != nil && res.SourceSwitch != "" {
		return 0, nil, errSourceSwitch{source: res.SourceSwitch}
	}
	if res == nil || res.Quit || res.Anime == nil {
		return 0, nil, ErrCancelled
	}
	item := res.Anime
	if item.AnidbAID == 0 {
		return 0, nil, fmt.Errorf("no AniDB id for %q", item.Title)
	}
	return item.AnidbAID, item, nil
}

// resolveHianimeNoLogin is the no-MAL hianime path: search hianime by query,
// let the user pick, then return the item (aid=0 — hianime resolves by title in
// streamLoop). Unlike animetosho there's no "latest uploads" landing, so a
// query is required.
func resolveHianimeNoLogin(opt *Options, health *tui.ProviderHealth, animeState *tui.AnimeState) (int, *mal.Item, error) {
	if opt.Query == "" {
		return 0, nil, fmt.Errorf("hianime mode requires a search query (run: ani <title>)")
	}
	shows, err := hianime.Search(opt.Query)
	if err != nil {
		health.MarkDown("hianime", downReason(err))
		return 0, nil, err
	}
	health.MarkUp("hianime")
	items := make([]mal.Item, 0, len(shows))
	for _, s := range shows {
		items = append(items, mal.Item{Title: s.Name})
	}
	if len(items) == 0 {
		return 0, nil, fmt.Errorf("no anime found")
	}
	load := func(tui.AnimeSource, string, string) ([]mal.Item, error) { return items, nil }
	if opt.DryRun {
		item := items[0]
		fmt.Fprintf(os.Stderr, "DRY-RUN: auto-picked %q\n", item.Title)
		return 0, &item, nil
	}
	res, err := tui.RunAnimePicker(tui.SourceSeason, opt.Query, load, nil, nil, nil, nil, nil, nil, health, opt.Source, animeState, nil, nil, opt.Debug)
	if err != nil {
		return 0, nil, err
	}
	if res != nil && res.SourceSwitch != "" {
		return 0, nil, errSourceSwitch{source: res.SourceSwitch}
	}
	if res == nil || res.Quit || res.Anime == nil {
		return 0, nil, ErrCancelled
	}
	return 0, res.Anime, nil
}

// seriesToItems projects AnimeTosho series-search hits into picker items (title
// + AniDB id; no cover — the picker shows a blank cover area).
func seriesToItems(ss []animetosho.SeriesSummary) []mal.Item {
	items := make([]mal.Item, 0, len(ss))
	for _, s := range ss {
		items = append(items, mal.Item{Title: s.Title, AnidbAID: s.AnidbAID})
	}
	return items
}

// applySourceSwitch persists the new provider and re-selects its saved
// group/quality (mirrors main.go's startup selection) so a mid-session switch
// picks up the right filters. The caller resets the aired cache — counts are
// provider-specific (torrent's release proxy vs hianime's episode list — each
// fails on different anime), so the old values and zeros don't apply.
//
// It also probes the switched-to provider and pre-marks its health, so a dead
// one warns IMMEDIATELY in the picker that opens next (the anime picker on
// re-resolve, or the release picker re-opening on the same anime) instead of
// only after its first fetches fail. The non-active provider is never probed —
// its state stays whatever real traffic last reported.
func applySourceSwitch(opt *Options, source string, health *tui.ProviderHealth) {
	opt.Source = source
	cfg := config.Load()
	if source == "hianime" {
		opt.Group, opt.Quality = cfg.HianimeGroup, cfg.HianimeQuality
	} else {
		opt.Group, opt.Quality = cfg.Group, cfg.Quality
	}
	config.SaveSource(source)
	switch source {
	case "hianime":
		if err := hianime.Ping(); err != nil {
			health.MarkDown("hianime", downReason(err))
		} else {
			health.MarkUp("hianime")
		}
	default: // "torrent"
		if err := animetosho.Ping(); err != nil {
			health.MarkDown("torrent", downReason(err))
		} else {
			health.MarkUp("torrent")
		}
	}
}

// releaseLoop runs the pick → play/download → write-back loop for one anime.
// A provider switch from the release picker's `:` palette (errSourceSwitch) is
// applied in place and the loop re-runs for the SAME anime under the new
// provider. The latest-uploads sentinel (aid == latestUploadsAID) fetches the
// newest releases site-wide with the episode filter disabled; that view offers
// no switch (hianime resolves per show). Returns errBackToAnime when the user
// backs out.
func releaseLoop(opt *Options, aid int, item *mal.Item, aired *tui.AiredCache, health *tui.ProviderHealth, relcache *releaseCache) error {
	// Episode to restore after a provider switch: keeps the user's selection
	// when the same anime re-opens on the other backend (0 = none yet).
	reentryEpisode := 0
	for {
		err := runReleaseLoop(opt, aid, item, aired, health, relcache, reentryEpisode)
		var srcSwitch errSourceSwitch
		if !errors.As(err, &srcSwitch) {
			return err
		}
		applySourceSwitch(opt, srcSwitch.source, health)
		aired.Reset()
		relcache.Reset()
		reentryEpisode = srcSwitch.episode
		if health.IsDown(opt.Source) {
			// The switch probe already knows the target provider is dead — don't
			// spend another round-trip failing to resolve the show. Bounce
			// straight back to the anime picker, whose warning line says why.
			return errBackToAnime
		}
		if aid == 0 {
			// Came from the hianime path (it resolves by title): the torrent path
			// needs an AniDB id for this anime.
			aid = item.AnidbAID
			if aid == 0 {
				aid = resolveAnidbFromMAL(item, opt)
			}
			if aid == 0 {
				aid = resolveAnidbManual(item, opt)
			}
			if aid == 0 {
				return fmt.Errorf("could not resolve an AniDB id for %q", item.Title)
			}
			item.AnidbAID = aid
		}
	}
}

// runReleaseLoop dispatches to the active provider's pick → play loop.
// firstEpisode seeds the picker's episode filter for its first run only (the
// provider-switch re-entry restores the user's selection).
func runReleaseLoop(opt *Options, aid int, item *mal.Item, aired *tui.AiredCache, health *tui.ProviderHealth, relcache *releaseCache, firstEpisode int) error {
	if opt.Source == "hianime" {
		return streamLoop(opt, item, aired, health, relcache, firstEpisode)
	}
	if aid == latestUploadsAID {
		return latestLoop(opt, item, aired, health, firstEpisode)
	}
	return playLoop(opt, item, cachedFetch(aid, relcache, health), false, aired, health, firstEpisode)
}

// streamLoop resolves the anime on hianime (by title) and runs the same
// pick → play → write-back loop via playLoop, but with a hianime fetch closure
// that returns audio×resolution stream variants as playable.Release items. The
// release picker's group filter = sub/dub, quality filter = resolution — same
// UI, different backend.
func streamLoop(opt *Options, item *mal.Item, aired *tui.AiredCache, health *tui.ProviderHealth, relcache *releaseCache, firstEpisode int) error {
	show, ok := relcache.show(item.Title)
	if !ok {
		var err error
		show, err = hianime.ResolveShow(item.Title)
		if err != nil {
			// Provider down (marked by the switch probe or earlier fetch failures):
			// bounce back to the anime picker — its warning line says why, and the
			// user can switch back or wait — instead of the app dying at a dead
			// backend. A resolve failure with the site UP is a real error (title
			// mismatch) and keeps the old behavior.
			if health.IsDown("hianime") {
				return errBackToAnime
			}
			return fmt.Errorf("hianime: resolve %q: %w", item.Title, err)
		}
		relcache.putShow(item.Title, show)
	}
	// No default-episode override: the picker advances to watched+1 itself and
	// shows an empty list when hianime doesn't have that episode yet (its
	// per-season numbering maps 1:1 to MAL). ep 0 (the picker's "all" filter,
	// also how a finished series opens) lists every episode's variants. All of
	// it is memoized in the session release cache — re-entering the release
	// picker is instant; stream lists carry no watch state, so they can't go
	// stale, and a provider switch resets the cache wholesale.
	fetch := func(ep int) []*playable.Release {
		if r := relcache.streamGet(show.ID, ep); r != nil {
			return r
		}
		rels, e := hianime.FetchReleases(show.ID, ep)
		if e != nil {
			// Mark reachability for the warning line (a missing episode is an
			// empty list, not an error — only transport/HTTP failures land here).
			health.MarkDown("hianime", downReason(e))
			mal.LogDebug("hianime fetch ep %d: %v\n", ep, e)
			return nil // not cached — the next entry retries it
		}
		health.MarkUp("hianime")
		relcache.streamPut(show.ID, ep, rels)
		return rels
	}
	return playLoop(opt, item, fetch, false, aired, health, firstEpisode)
}

// latestLoop is the no-arg AnimeTosho landing screen: the newest uploads in one
// flat list (episode filter disabled), no MAL write-back (the synthetic item
// has no MAL id).
func latestLoop(opt *Options, item *mal.Item, aired *tui.AiredCache, health *tui.ProviderHealth, firstEpisode int) error {
	var cached []*playable.Release
	fetch := func(int) []*playable.Release {
		if cached == nil {
			r, err := animetosho.LatestReleases(200)
			if err != nil {
				health.MarkDown("torrent", downReason(err))
			} else {
				health.MarkUp("torrent")
			}
			cached = animetosho.ToPlayables(r)
		}
		return cached
	}
	return playLoop(opt, item, fetch, true, aired, health, firstEpisode)
}

// playLoop drives the release picker and the play/download + MAL write-back,
// looping for the next episode until cancelled or backed out of. firstEpisode
// seeds the FIRST picker run's episode filter (the provider-switch re-entry
// restores the user's selection; 0 = compute next-unwatched,
// tui.DefaultEpisodeAll = "all"). Later runs always pass 0: after a play the
// write-back has advanced item.WatchedEps, so the picker computes the next
// episode itself.
func playLoop(opt *Options, item *mal.Item, fetch func(int) []*playable.Release, disableEpisode bool, aired *tui.AiredCache, health *tui.ProviderHealth, firstEpisode int) error {
	first := true
	for {
		ep := 0
		if first {
			ep = firstEpisode
			first = false
		}
		pick, action, err := pickReleaseTUI(item, opt, fetch, disableEpisode, aired, health, ep)
		if err != nil {
			return err // errBackToAnime propagates to Run
		}
		AnnouncePick(pick)
		// action comes from the release picker (Enter = play, d = download).
		if pick.IsStream() {
			// hianime stream: play via mpv+URL (with the stream host's required
			// referer and the subtitle track when present). Download isn't
			// applicable.
			if action == "download" {
				fmt.Fprintln(os.Stderr, "ani: download not supported for streams")
			} else {
				if err := player.RunPlayURL(pick.StreamURL, pick.Title, pick.Referer, pick.SubtitleURL, opt.Player, opt.DryRun); err != nil {
					return err
				}
			}
		} else if action == "download" {
			if err := player.RunDownload(pick.Magnet, opt.Dir, opt.DryRun); err != nil {
				return err
			}
		} else {
			if err := player.RunPlay(pick.Magnet, pick.Title, opt.Player, opt.DryRun); err != nil {
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

// releaseCache memoizes fetched releases for ONE app session, per provider
// (app.Run resets it on a provider switch, alongside the aired cache): each
// (anime, episode) pair is fetched at most once per session, so re-entering
// the release picker — Esc-back, re-select, playing the next episode — serves
// instantly instead of re-paying the provider round-trip (the feeds are slow
// and rate-limited). Torrent rows key on the AniDB id; stream rows on the
// hianime show, plus a title→show resolve memo so re-entry skips the search.
// Fetch failures are NOT cached (retried on the next entry). The fetch
// closures run on picker cmd goroutines, so every method takes the mutex.
type releaseCache struct {
	mu      sync.Mutex
	torrent map[int]map[int][]*playable.Release    // aid → ep → releases
	stream  map[string]map[int][]*playable.Release // show ID → ep → releases
	shows   map[string]hianime.Show                // title → resolved show
}

func newReleaseCache() *releaseCache {
	return &releaseCache{
		torrent: map[int]map[int][]*playable.Release{},
		stream:  map[string]map[int][]*playable.Release{},
		shows:   map[string]hianime.Show{},
	}
}

// Reset clears everything (provider switch: releases are provider-specific).
func (c *releaseCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.torrent = map[int]map[int][]*playable.Release{}
	c.stream = map[string]map[int][]*playable.Release{}
	c.shows = map[string]hianime.Show{}
}

func (c *releaseCache) torrentGet(aid, ep int) []*playable.Release {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.torrent[aid][ep]
}

func (c *releaseCache) torrentPut(aid, ep int, r []*playable.Release) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.torrent[aid] == nil {
		c.torrent[aid] = map[int][]*playable.Release{}
	}
	c.torrent[aid][ep] = r
}

func (c *releaseCache) streamGet(showID string, ep int) []*playable.Release {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stream[showID][ep]
}

func (c *releaseCache) streamPut(showID string, ep int, r []*playable.Release) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream[showID] == nil {
		c.stream[showID] = map[int][]*playable.Release{}
	}
	c.stream[showID][ep] = r
}

func (c *releaseCache) show(title string) (hianime.Show, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.shows[title]
	return s, ok
}

func (c *releaseCache) putShow(title string, s hianime.Show) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shows[title] = s
}

// cachedFetch returns an episode fetch func that serves from the session
// release cache, falling back to animetosho.FetchReleases(aid, ep) and caching
// the result. Fetch errors mark the torrent backend down on health (cleared by
// any success) for the pickers' warning line and are not cached — the next
// entry retries them.
func cachedFetch(aid int, cache *releaseCache, health *tui.ProviderHealth) func(int) []*playable.Release {
	return func(ep int) []*playable.Release {
		if r := cache.torrentGet(aid, ep); r != nil {
			return r
		}
		r, err := animetosho.FetchReleases(aid, ep)
		if err != nil {
			health.MarkDown("torrent", downReason(err))
			return nil
		}
		health.MarkUp("torrent")
		p := animetosho.ToPlayables(r)
		cache.torrentPut(aid, ep, p)
		return p
	}
}

// pickReleaseTUI drives the bubbletea release picker. dry-run auto-picks the
// first release so exec commands can be printed without a TUI. Returns the
// chosen release and action ("play"/"download"); disableEpisode suppresses the
// episode filter (latest-uploads view).
func pickReleaseTUI(item *mal.Item, opt *Options, fetch func(int) []*playable.Release, disableEpisode bool, aired *tui.AiredCache, health *tui.ProviderHealth, defaultEpisode int) (*playable.Release, string, error) {
	if opt.DryRun {
		ep := max(defaultEpisode, 0) // DefaultEpisodeAll → plain "all" fetch
		if ep == 0 && !disableEpisode {
			ep = tui.DefaultEpisode(item.WatchedEps, item.TotalEps)
		}
		all := fetch(ep)
		view := ui.SortedReleases(ui.FilterByGroup(all, opt.Group), opt.Sort)
		if len(view) == 0 {
			return nil, "", fmt.Errorf("no releases for group %q", ui.GroupLabel(opt.Group))
		}
		fmt.Fprintf(os.Stderr, "DRY-RUN: TUI would show %d releases, auto-picking first\n", len(view))
		return view[0], "play", nil
	}
	res, err := tui.RunReleasePicker(item, opt.Group, opt.Quality, opt.Sort, fetch, disableEpisode, player.CopyToClipboard,
		latestEpisodeFn(opt, health), aired, health, defaultEpisode, opt.Source, opt.Debug)
	if err != nil {
		return nil, "", err
	}
	// Persist the user's filter choices on EVERY exit (including quit/back) so
	// they survive the post-play loop, back-navigation, and the next session.
	// The filters belong to the current provider — checked before the switch
	// signal, which is applied (with the new provider's saved filters) upstream.
	if res != nil {
		opt.Group = res.FilterGroup
		opt.Quality = res.FilterQuality
		opt.Sort = res.FilterSort
		config.SaveFilters(res.FilterGroup, res.FilterQuality, res.FilterSort, opt.Source)
	}
	if res != nil && res.SourceSwitch != "" {
		// Carry the selected episode so the re-opened picker restores it.
		// FilterEpisode 0 means "all" here — map it to DefaultEpisodeAll so it
		// isn't confused with "no override".
		ep := res.FilterEpisode
		if ep == 0 {
			ep = tui.DefaultEpisodeAll
		}
		return nil, "", errSourceSwitch{source: res.SourceSwitch, episode: ep}
	}
	if res != nil && res.Back {
		return nil, "", errBackToAnime
	}
	if res == nil || res.Quit || res.Release == nil {
		return nil, "", ErrCancelled
	}
	action := res.Action
	if action == "" {
		action = "play"
	}
	return res.Release, action, nil
}

// AnnouncePick prints the chosen release to stdout.
func AnnouncePick(r *playable.Release) {
	grp := r.Group
	if grp == "" {
		grp = "?"
	}
	if r.IsStream() {
		fmt.Printf("\n> [%s] %s %s\n", grp, r.Title, r.Resolution)
	} else {
		fmt.Printf("\n> [%s] %s\n  %s, %d seeders\n", grp, r.Title, ui.HumanSize(r.SizeBytes), r.Seeders)
	}
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
	fmt.Fprintln(w, `ani — a MyAnimeList TUI that streams anime

Usage:
  ani [query|anidb-id]

  <query>     anime name (e.g. frieren) -> pick from matching series
  <anidb-id>  numeric AniDB id (e.g. 18886) -> skip straight to its releases
  (no arg)    your MAL list (logged in) or the latest uploads (not logged in)

Logged-in flow:  browse My List / This Season / Search  ->  pick a release
                 ->  Enter plays  /  d downloads  ->  MAL progress write-back
Not logged in:   AnimeTosho series search, or the latest uploads.

Provider: set "source" in config.json to "torrent" (default: AnimeTosho torrents
          via webtorrent) or "hianime" (hianime.at streaming via mpv — sub/dub +
          quality selection in the release picker).

Config: $XDG_CONFIG_HOME/ani/config.json  (player, dir, group/quality/sort, source)`)
}
