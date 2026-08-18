// Package app wires ani's flow: resolve an anime (MAL when logged in, otherwise
// AnimeTosho) → pick releases → play or download → write back to MAL.
package app

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"ani/internal/anidb"
	"ani/internal/animetosho"
	"ani/internal/config"
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

// errSourceSwitch signals that the anime picker requested a provider change via
// the `:` palette. Run applies it (persist + re-select the provider's saved
// filters) then re-resolves under the new provider. Carries the target source
// ("torrent"/"anidb").
type errSourceSwitch struct{ source string }

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
	animeState := tui.NewAnimeState() // session-scoped; anime picker's options/cursor/cache survive Esc-back
	go mal.WarmAidResolvers(opt.Debug) // overlap the one-time Fribb/AniDB map load with the first MAL fetch
	for {
		aid, item, err := resolve(opt, aired, animeState)
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
			// Provider change from the `:` palette. Persist it and re-select the
			// new provider's saved group/quality (mirrors main.go's startup
			// selection) so a mid-session switch picks up the right filters, then
			// re-resolve under the new provider.
			opt.Source = srcSwitch.source
			cfg := config.Load()
			if srcSwitch.source == "anidb" {
				opt.Group, opt.Quality = cfg.AnidbGroup, cfg.AnidbQuality
			} else {
				opt.Group, opt.Quality = cfg.Group, cfg.Quality
			}
			config.SaveSource(srcSwitch.source)
			continue
		}
		if err != nil {
			return err
		}
		if err := releaseLoop(opt, aid, item, aired); err != nil {
			if errors.Is(err, errBackToAnime) {
				continue // Esc in release picker → re-resolve
			}
			return err
		}
		return nil
	}
}

// resolve picks an anime and returns its AniDB id + item. A numeric query is a
// direct AniDB id (no MAL); otherwise MAL when logged in, else AnimeTosho
// (series search by name, or latest uploads when no query).
func resolve(opt *Options, aired *tui.AiredCache, animeState *tui.AnimeState) (int, *mal.Item, error) {
	if n, perr := strconv.Atoi(opt.Query); perr == nil && n > 0 {
		return resolveAnidb(n)
	}
	if mal.LoggedIn() {
		return resolveMal(opt, aired, animeState)
	}
	return resolveAnimetosho(opt, animeState)
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
func resolveMal(opt *Options, aired *tui.AiredCache, animeState *tui.AnimeState) (int, *mal.Item, error) {
	query := opt.Query
	source := tui.SourceSeason // default browse source
	load := func(src tui.AnimeSource, q, season string) ([]mal.Item, error) {
		if q != "" {
			return mal.Search(q, opt.Debug)
		}
		switch src {
		case tui.SourceList:
			return mal.MyList("", opt.Debug)
		default: // SourceSeason
			if season == mal.SeasonLater {
				return mal.Upcoming(opt.Debug)
			}
			year, s, ok := mal.ParseSeasonLabel(season)
			if !ok {
				return nil, fmt.Errorf("invalid season %q", season)
			}
			return mal.Seasonal(year, s, opt.Debug)
		}
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
	latestEpisode := latestEpisodeFn(opt)
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
	res, err := tui.RunAnimePicker(source, query, load, applyStatus, applyScore, applyWatched, latestEpisode, latestEpisodePrefetchFn(opt), aired, opt.Source, animeState, opt.Debug)
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
	// anidb (streaming) resolves by title in streamLoop — no AniDB aid needed.
	if opt.Source == "anidb" {
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
	// anidb resolves by title in streamLoop — no aid needed.
	if opt.Source == "anidb" {
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

// latestEpisodeFn returns the aired-episode lookup both pickers use. AnimeTosho
// is primary (it has no rate limit, unlike Jikan): resolve the aid and read the
// latest episode from its releases (a same-day proxy for "aired"). Jikan's
// episode feed is the fallback — authoritative but rate-limited — when the aid
// can't be resolved or AnimeTosho has no releases. nil item → 0.
func latestEpisodeFn(opt *Options) func(*mal.Item) float64 {
	return func(item *mal.Item) float64 {
		if item == nil {
			return 0
		}
		if opt.Source == "anidb" {
			return anidb.AiredCount(item.Title)
		}
		if aid := resolveAidFast(item, opt); aid > 0 {
			if n := animetosho.LatestEpisode(aid, item.TotalEps); n > 0 {
				return float64(n)
			}
		}
		n, _ := mal.LatestEpisode(item.MalID, opt.Debug)
		return float64(n)
	}
}

// latestEpisodePrefetchFn is the background-prefetch variant of latestEpisodeFn:
// fast-only (no Jikan), and it skips items whose AniDB id can't be resolved from
// the fast sources (override → item aid → Fribb → AniDB titles). Those need the
// manual AnimeTosho selection first, so their aired count isn't available yet —
// and the background prefetch never calls Jikan (rate-limited, errors for some).
// Returns 0 when skipped/unknown; the focus path (latestEpisodeFn) still tries
// the full chain (incl. Jikan) on demand for items the prefetch didn't fill.
func latestEpisodePrefetchFn(opt *Options) func(*mal.Item) float64 {
	return func(item *mal.Item) float64 {
		if item == nil {
			return 0
		}
		if opt.Source == "anidb" {
			return anidb.AiredCount(item.Title)
		}
		aid := resolveAidFast(item, opt)
		if aid <= 0 {
			return 0
		}
		return float64(animetosho.LatestEpisode(aid, item.TotalEps))
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
// (animetosho only — anidb has no equivalent).
func resolveAnimetosho(opt *Options, animeState *tui.AnimeState) (int, *mal.Item, error) {
	if opt.Source == "anidb" {
		return resolveAnidbNoLogin(opt, animeState)
	}
	if opt.Query == "" {
		return latestUploadsAID, &mal.Item{Title: "Latest uploads"}, nil
	}
	series, err := animetosho.SearchSeries(opt.Query)
	if err != nil {
		return 0, nil, err
	}
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
	res, err := tui.RunAnimePicker(tui.SourceSeason, opt.Query, load, nil, nil, nil, nil, nil, nil, opt.Source, animeState, opt.Debug)
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

// resolveAnidbNoLogin is the no-MAL anidb path: search anidb.app by query, let the
// user pick, then return the item (aid=0 — anidb resolves by title in streamLoop).
// Unlike animetosho there's no "latest uploads" landing, so a query is required.
func resolveAnidbNoLogin(opt *Options, animeState *tui.AnimeState) (int, *mal.Item, error) {
	if opt.Query == "" {
		return 0, nil, fmt.Errorf("anidb mode requires a search query (run: ani <title>)")
	}
	shows, err := anidb.Search(opt.Query)
	if err != nil {
		return 0, nil, err
	}
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
	res, err := tui.RunAnimePicker(tui.SourceSeason, opt.Query, load, nil, nil, nil, nil, nil, nil, opt.Source, animeState, opt.Debug)
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

// releaseLoop runs the pick → play/download → write-back loop for one anime.
// The latest-uploads sentinel (aid == latestUploadsAID) fetches the newest
// releases site-wide with the episode filter disabled. Returns errBackToAnime
// when the user backs out.
func releaseLoop(opt *Options, aid int, item *mal.Item, aired *tui.AiredCache) error {
	if opt.Source == "anidb" {
		return streamLoop(opt, item, aired)
	}
	if aid == latestUploadsAID {
		return latestLoop(opt, item, aired)
	}
	cache := &episodeCache{data: map[int][]*playable.Release{}}
	return playLoop(opt, item, cachedFetch(aid, cache), false, aired, func(*mal.Item) int { return 0 })
}

// streamLoop resolves the anime on anidb.app (by title) and runs the same
// pick → play → write-back loop via playLoop, but with an anidb fetch closure that
// returns audio×resolution stream variants as playable.Release items. The release
// picker's group filter = sub/dub, quality filter = resolution — same UI, different
// backend.
func streamLoop(opt *Options, item *mal.Item, aired *tui.AiredCache) error {
	show, err := anidb.ResolveShow(item.Title)
	if err != nil {
		return fmt.Errorf("anidb: resolve %q: %w", item.Title, err)
	}
	// No default-episode override: the picker advances to watched+1 itself
	// (anidb's cumulative numbering is handled inside FetchReleases) and shows
	// an empty list when anidb doesn't have that episode yet. ep 0 (the
	// picker's "all" filter, also how a finished series opens) lists every
	// episode's variants; the memo keeps re-toggling to "all" instant — that
	// fetch costs one languages+embed+master round per episode (bounded inside
	// anidb). Same lifetime as the torrent path's episodeCache: one anime, one
	// playLoop; stream lists carry no watch state, so it can't go stale.
	var allCached []*playable.Release
	fetch := func(ep int) []*playable.Release {
		if ep == 0 && allCached != nil {
			return allCached
		}
		rels, e := anidb.FetchReleases(show.ID, ep)
		if e != nil {
			mal.LogDebug("anidb fetch ep %d: %v\n", ep, e)
			return nil
		}
		if ep == 0 && len(rels) > 0 {
			allCached = rels
		}
		return rels
	}
	return playLoop(opt, item, fetch, false, aired, func(*mal.Item) int { return 0 })
}

// latestLoop is the no-arg AnimeTosho landing screen: the newest uploads in one
// flat list (episode filter disabled), no MAL write-back (the synthetic item
// has no MAL id).
func latestLoop(opt *Options, item *mal.Item, aired *tui.AiredCache) error {
	var cached []*playable.Release
	fetch := func(int) []*playable.Release {
		if cached == nil {
			r, _ := animetosho.LatestReleases(200)
			cached = animetosho.ToPlayables(r)
		}
		return cached
	}
	return playLoop(opt, item, fetch, true, aired, func(*mal.Item) int { return 0 })
}

// playLoop drives the release picker and the play/download + MAL write-back,
// looping for the next episode until cancelled or backed out of.
func playLoop(opt *Options, item *mal.Item, fetch func(int) []*playable.Release, disableEpisode bool, aired *tui.AiredCache, defaultEpisodeFn func(*mal.Item) int) error {
	for {
		pick, action, err := pickReleaseTUI(item, opt, fetch, disableEpisode, aired, defaultEpisodeFn(item))
		if err != nil {
			return err // errBackToAnime propagates to Run
		}
		AnnouncePick(pick)
		// action comes from the release picker (Enter = play, d = download).
		if pick.IsStream() {
			// anidb stream: play via mpv+URL. Download isn't applicable.
			if action == "download" {
				fmt.Fprintln(os.Stderr, "ani: download not supported for streams")
			} else {
				if err := player.RunPlayURL(pick.StreamURL, pick.Title, opt.Player, opt.DryRun); err != nil {
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

// episodeCache memoizes fetched releases per episode for the current anime so
// re-visiting an episode (or the prefetched next one) is instant.
type episodeCache struct {
	mu   sync.Mutex
	data map[int][]*playable.Release
}

func (c *episodeCache) get(ep int) []*playable.Release {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data[ep]
}

func (c *episodeCache) put(ep int, r []*playable.Release) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[ep] = r
}

// cachedFetch returns an episode fetch func that serves from the cache, falling
// back to animetosho.FetchReleases(aid, ep) and caching the result.
func cachedFetch(aid int, cache *episodeCache) func(int) []*playable.Release {
	return func(ep int) []*playable.Release {
		if r := cache.get(ep); r != nil {
			return r
		}
		r, _ := animetosho.FetchReleases(aid, ep)
		p := animetosho.ToPlayables(r)
		cache.put(ep, p)
		return p
	}
}

// pickReleaseTUI drives the bubbletea release picker. dry-run auto-picks the
// first release so exec commands can be printed without a TUI. Returns the
// chosen release and action ("play"/"download"); disableEpisode suppresses the
// episode filter (latest-uploads view).
func pickReleaseTUI(item *mal.Item, opt *Options, fetch func(int) []*playable.Release, disableEpisode bool, aired *tui.AiredCache, defaultEpisode int) (*playable.Release, string, error) {
	if opt.DryRun {
		ep := defaultEpisode
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
		latestEpisodeFn(opt), aired, defaultEpisode, opt.Debug)
	if err != nil {
		return nil, "", err
	}
	// Persist the user's filter choices on EVERY exit (including quit/back) so
	// they survive the post-play loop, back-navigation, and the next session.
	if res != nil {
		opt.Group = res.FilterGroup
		opt.Quality = res.FilterQuality
		opt.Sort = res.FilterSort
		config.SaveFilters(res.FilterGroup, res.FilterQuality, res.FilterSort, opt.Source)
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
          via webtorrent) or "anidb" (anidb.app streaming via mpv — sub/dub +
          quality selection in the release picker).

Config: $XDG_CONFIG_HOME/ani/config.json  (player, dir, group/quality/sort, source)`)
}
