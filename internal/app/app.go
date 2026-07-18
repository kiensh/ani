// Package app wires ani's flow: resolve an anime (MAL when logged in, otherwise
// AnimeTosho) → pick releases → play or download → write back to MAL.
package app

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"ani/internal/animetosho"
	"ani/internal/config"
	"ani/internal/mal"
	"ani/internal/player"
	"ani/internal/tui"
	"ani/internal/ui"
)

// errBackToAnime is returned when the user presses Esc in the release picker.
// Run re-runs anime selection instead of exiting.
var errBackToAnime = errors.New("back to anime selection")

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
	for {
		aid, item, err := resolve(opt)
		if err != nil {
			return err
		}
		if err := releaseLoop(opt, aid, item); err != nil {
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
func resolve(opt *Options) (int, *mal.Item, error) {
	if n, perr := strconv.Atoi(opt.Query); perr == nil && n > 0 {
		return resolveAnidb(n)
	}
	if mal.LoggedIn() {
		return resolveMal(opt)
	}
	return resolveAnimetosho(opt)
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
func resolveMal(opt *Options) (int, *mal.Item, error) {
	query := opt.Query
	source := tui.SourceSeason // default browse source
	load := func(src tui.AnimeSource, q, season string) []mal.Item {
		if q != "" {
			items, _ := mal.Search(q, opt.Debug)
			return items
		}
		switch src {
		case tui.SourceList:
			items, _ := mal.MyList("", opt.Debug)
			return items
		default: // SourceSeason
			if season == mal.SeasonLater {
				items, _ := mal.Upcoming(opt.Debug)
				return items
			}
			year, s, ok := mal.ParseSeasonLabel(season)
			if !ok {
				return nil
			}
			items, _ := mal.Seasonal(year, s, opt.Debug)
			return items
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
	res, err := tui.RunAnimePicker(source, query, load, applyStatus, applyScore, applyWatched, latestEpisode, latestEpisodePrefetchFn(opt), opt.Debug)
	if err != nil {
		return 0, nil, err
	}
	if res == nil || res.Quit || res.Anime == nil {
		return 0, nil, ErrCancelled
	}
	item := res.Anime
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
	items := load(source, query, season)
	if len(items) == 0 {
		return 0, nil, fmt.Errorf("no anime found for %q", query)
	}
	item := items[0]
	fmt.Fprintf(os.Stderr, "DRY-RUN: auto-picked %q\n", item.Title)
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
func latestEpisodeFn(opt *Options) func(*mal.Item) int {
	return func(item *mal.Item) int {
		if item == nil {
			return 0
		}
		if aid := resolveAidFast(item, opt); aid > 0 {
			if n := animetosho.LatestEpisode(aid); n > 0 {
				return n
			}
		}
		n, _ := mal.LatestEpisode(item.MalID, opt.Debug)
		return n
	}
}

// latestEpisodePrefetchFn is the background-prefetch variant of latestEpisodeFn:
// fast-only (no Jikan), and it skips items whose AniDB id can't be resolved from
// the fast sources (override → item aid → Fribb → AniDB titles). Those need the
// manual AnimeTosho selection first, so their aired count isn't available yet —
// and the background prefetch never calls Jikan (rate-limited, errors for some).
// Returns 0 when skipped/unknown; the focus path (latestEpisodeFn) still tries
// the full chain (incl. Jikan) on demand for items the prefetch didn't fill.
func latestEpisodePrefetchFn(opt *Options) func(*mal.Item) int {
	return func(item *mal.Item) int {
		if item == nil {
			return 0
		}
		aid := resolveAidFast(item, opt)
		if aid <= 0 {
			return 0
		}
		return animetosho.LatestEpisode(aid)
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

// resolveAnimetosho is the no-MAL path. A text query searches AnimeTosho series
// and lets the user pick; no query returns the latest-uploads sentinel.
func resolveAnimetosho(opt *Options) (int, *mal.Item, error) {
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
	load := func(tui.AnimeSource, string, string) []mal.Item { return items }
	if opt.DryRun {
		// Dry-run: skip the anime picker, auto-pick the first series hit.
		item := items[0]
		fmt.Fprintf(os.Stderr, "DRY-RUN: auto-picked %q\n", item.Title)
		if item.AnidbAID == 0 {
			return 0, nil, fmt.Errorf("no AniDB id for %q", item.Title)
		}
		return item.AnidbAID, &item, nil
	}
	res, err := tui.RunAnimePicker(tui.SourceSeason, opt.Query, load, nil, nil, nil, nil, nil, opt.Debug)
	if err != nil {
		return 0, nil, err
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
func releaseLoop(opt *Options, aid int, item *mal.Item) error {
	if aid == latestUploadsAID {
		return latestLoop(opt, item)
	}
	cache := &episodeCache{data: map[int][]*animetosho.Release{}}
	return playLoop(opt, item, cachedFetch(aid, cache), false)
}

// latestLoop is the no-arg AnimeTosho landing screen: the newest uploads in one
// flat list (episode filter disabled), no MAL write-back (the synthetic item
// has no MAL id).
func latestLoop(opt *Options, item *mal.Item) error {
	var cached []*animetosho.Release
	fetch := func(int) []*animetosho.Release {
		if cached == nil {
			cached, _ = animetosho.LatestReleases(200)
		}
		return cached
	}
	return playLoop(opt, item, fetch, true)
}

// playLoop drives the release picker and the play/download + MAL write-back,
// looping for the next episode until cancelled or backed out of.
func playLoop(opt *Options, item *mal.Item, fetch func(int) []*animetosho.Release, disableEpisode bool) error {
	for {
		pick, action, err := pickReleaseTUI(item, opt, fetch, disableEpisode)
		if err != nil {
			return err // errBackToAnime propagates to Run
		}
		AnnouncePick(pick)
		// action comes from the release picker (Enter = play, d = download).
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

// episodeCache memoizes fetched releases per episode for the current anime so
// re-visiting an episode (or the prefetched next one) is instant.
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

// pickReleaseTUI drives the bubbletea release picker. dry-run auto-picks the
// first release so exec commands can be printed without a TUI. Returns the
// chosen release and action ("play"/"download"); disableEpisode suppresses the
// episode filter (latest-uploads view).
func pickReleaseTUI(item *mal.Item, opt *Options, fetch func(int) []*animetosho.Release, disableEpisode bool) (*animetosho.Release, string, error) {
	if opt.DryRun {
		ep := 0
		if !disableEpisode {
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
		latestEpisodeFn(opt), opt.Debug)
	if err != nil {
		return nil, "", err
	}
	// Persist the user's filter choices on EVERY exit (including quit/back) so
	// they survive the post-play loop, back-navigation, and the next session.
	if res != nil {
		opt.Group = res.FilterGroup
		opt.Quality = res.FilterQuality
		opt.Sort = res.FilterSort
		config.SaveFilters(res.FilterGroup, res.FilterQuality, res.FilterSort)
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
	fmt.Fprintln(w, `ani — a MyAnimeList TUI that streams from Anime Tosho

Usage:
  ani [query|anidb-id]

  <query>     anime name (e.g. frieren) -> pick from matching series
  <anidb-id>  numeric AniDB id (e.g. 18886) -> skip straight to its releases
  (no arg)    your MAL list (logged in) or the latest uploads (not logged in)

Logged-in flow:  browse My List / This Season / Search  ->  pick a release
                 ->  Enter plays  /  d downloads  ->  MAL progress write-back
Not logged in:   AnimeTosho series search, or the latest uploads.

Config: $XDG_CONFIG_HOME/ani/config.json  (player, dir, group/quality/sort)`)
}
