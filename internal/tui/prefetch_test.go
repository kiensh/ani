package tui

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ani/internal/mal"
)

// newPrefetchPicker builds a Season picker showing all items in input order
// (Sort=relevance), with a given pageSize. prefetch is the background aired-episode
// fn (nil disables aired prefetch). The focus latestEpisode fn is a no-op.
func newPrefetchPicker(items []mal.Item, prefetch func(*mal.Item) float64, pageSize int) *animePicker {
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil,
		func(*mal.Item) float64 { return 0 }, prefetch, false)
	m.filter.Status = "All"
	m.filter.Sort = "relevance"
	loadAnime(m, items)
	m.height = 50 // non-zero so pageSize() uses paneHeight, not the default
	m.paneHeight = pageSize + 3
	return m
}

func malIDs(items []mal.Item) []int {
	out := make([]int, len(items))
	for i, it := range items {
		out[i] = it.MalID
	}
	return out
}

// TestPrefetchPagingSplit: covers page by visibility (first pageSize); aired is
// scoped to the status filter and dispatched on page 1 only — page 2 is
// covers-only.
func TestPrefetchPagingSplit(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1", ListStatus: "watching"},
		{MalID: 2, AirStatus: "currently_airing", CoverURL: "u2", ListStatus: "watching"},
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3"},
		{MalID: 4, AirStatus: "currently_airing", CoverURL: "u4"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 5 }, 2) // filter "All"

	covers1, aired1 := m.selectPrefetchPage(true)
	if got := malIDs(aired1); len(got) != 4 {
		t.Errorf("page1 aired (filter All) = %v, want [1 2 3 4]", got)
	}
	if len(covers1) != 2 || covers1[0] != "u1" || covers1[1] != "u2" {
		t.Errorf("page1 covers = %v, want [u1 u2]", covers1)
	}

	covers2, aired2 := m.selectPrefetchPage(false)
	if aired2 != nil {
		t.Errorf("page2 aired = %v, want nil (page 2 is covers-only)", malIDs(aired2))
	}
	if len(covers2) != 2 || covers2[0] != "u3" || covers2[1] != "u4" {
		t.Errorf("page2 covers = %v, want [u3 u4]", covers2)
	}
}

// TestPrefetchScopedToStatusFilter: the default Season view's "My List" filter
// fetches only the user's own airing anime; switching the filter to one that
// includes off-list anime ("All") dispatches the rest on demand.
func TestPrefetchScopedToStatusFilter(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1", ListStatus: "watching"},
		{MalID: 2, AirStatus: "currently_airing", CoverURL: "u2"},
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3", ListStatus: "plan_to_watch"},
		{MalID: 4, AirStatus: "currently_airing", CoverURL: "u4"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 5 }, 10)
	m.filter.Status = "My List" // the Season source's default
	m.applyFilter()

	// Load-time prefetch: only the on-list items.
	_, aired := m.selectPrefetchPage(true)
	if got := malIDs(aired); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("page1 aired (My List) = %v, want [1 3]", got)
	}

	// Switching to "All" dispatches the off-list bulk (2, 4) — and only that.
	m.filter.Status = "All"
	m.applyFilter()
	if cmd := m.statusAiredPrefetchCmd(); cmd == nil {
		t.Fatal("filter switch to All: statusAiredPrefetchCmd = nil, want a dispatch cmd")
	}
	// Dispatched via the cmd builder's marking: re-running selects nothing new.
	if items := m.filterKeptAired(); len(items) != 0 {
		t.Errorf("after dispatch, filterKeptAired = %v, want empty", malIDs(items))
	}

	// Re-selecting the same filter is a no-op.
	m.filter.Status = "All"
	if cmd := m.statusAiredPrefetchCmd(); cmd != nil {
		t.Error("re-apply same filter: statusAiredPrefetchCmd non-nil, want nil (nothing new)")
	}
}

// TestPrefetchSkipsCompleted: a completed anime (watched==total — the count
// adds nothing) is never dispatched, at load or on a filter change, and is
// excluded from the progress total.
func TestPrefetchSkipsCompleted(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", ListStatus: "watching"},
		{MalID: 2, AirStatus: "currently_airing", ListStatus: "completed"}, // done: skip
		{MalID: 3, AirStatus: "finished_airing", ListStatus: "watching"},   // not airing: skip
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 5 }, 10)

	_, aired := m.selectPrefetchPage(true)
	if got := malIDs(aired); len(got) != 1 || got[0] != 1 {
		t.Errorf("page1 aired = %v, want [1] (completed and finished skipped)", got)
	}
	m.filter.Status = "All"
	if cmd := m.statusAiredPrefetchCmd(); cmd != nil {
		t.Error("completed item dispatched on filter change, want no-op")
	}
	m.aired.put(1, 5)
	if p := m.airingProgress(80); p != "" {
		t.Errorf("airingProgress = %q, want empty (1/1 done; completed excluded from total)", p)
	}
}

// TestPrefetchDefaultPageSize: with an unknown layout (height 0) the cover page
// uses the default (~20); the rest is the remainder. (Covers page by visibility.)
func TestPrefetchDefaultPageSize(t *testing.T) {
	items := make([]mal.Item, 25)
	for i := range items {
		items[i] = mal.Item{MalID: i + 1, AirStatus: "currently_airing", CoverURL: "u"}
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil,
		func(*mal.Item) float64 { return 1 }, false)
	m.filter.Status = "All"
	m.filter.Sort = "relevance"
	loadAnime(m, items)
	m.height = 0 // unknown layout

	covers1, _ := m.selectPrefetchPage(true)
	if len(covers1) != 20 {
		t.Errorf("page1 (height 0) covers = %v, want 20 (default page size)", len(covers1))
	}
	covers2, _ := m.selectPrefetchPage(false)
	if len(covers2) != 5 {
		t.Errorf("page2 covers = %v, want 5 (remainder)", len(covers2))
	}
}

// TestPrefetchCoversFollowView: page-1 covers follow m.view order, independent of
// m.items load order. (Aired pages by membership, so it's not view-ordered.)
func TestPrefetchCoversFollowView(t *testing.T) {
	items := []mal.Item{
		{MalID: 10, AirStatus: "currently_airing", CoverURL: "u10"},
		{MalID: 20, AirStatus: "currently_airing", CoverURL: "u20"},
		{MalID: 30, AirStatus: "currently_airing", CoverURL: "u30"},
		{MalID: 40, AirStatus: "currently_airing", CoverURL: "u40"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 1 }, 2)
	// Reorder the view (as a sort would) so the top is 40, 30.
	m.view = []mal.Item{items[3], items[2], items[1], items[0]}

	covers1, _ := m.selectPrefetchPage(true)
	if len(covers1) != 2 || covers1[0] != "u40" || covers1[1] != "u30" {
		t.Errorf("page1 covers = %v, want [u40 u30] (m.view order)", covers1)
	}
}

// TestPrefetchAiredFollowsViewOrder: within a page, airing items are taken in
// display (m.view) order so the cursor's top-of-list items are processed first.
func TestPrefetchAiredFollowsViewOrder(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", ListStatus: "watching"},
		{MalID: 2, AirStatus: "currently_airing", ListStatus: "watching"},
		{MalID: 3, AirStatus: "currently_airing", ListStatus: "watching"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 1 }, 10)
	// Reorder the view (as a sort would): top is 3, 1, 2.
	m.view = []mal.Item{items[2], items[0], items[1]}

	_, aired := m.selectPrefetchPage(true)
	if got := malIDs(aired); len(got) != 3 || got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Errorf("page1 aired = %v, want [3 1 2] (m.view order, cursor top first)", got)
	}
}

// TestPrefetchSkipsNonAiring: covers are gathered for every item; aired episodes
// only for currently_airing items (here all on-list, so page 1).
func TestPrefetchSkipsNonAiring(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1", ListStatus: "watching"},
		{MalID: 2, AirStatus: "finished_airing", CoverURL: "u2", ListStatus: "watching"},
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3", ListStatus: "watching"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 1 }, 10)

	covers, aired := m.selectPrefetchPage(true)
	if len(covers) != 3 {
		t.Errorf("covers = %v, want all 3 (covers aren't air-gated)", covers)
	}
	if len(aired) != 2 || aired[0].MalID != 1 || aired[1].MalID != 3 {
		t.Errorf("aired = %v, want [1 3] (non-airing skipped)", malIDs(aired))
	}
}

// TestPrefetchIdempotentAndCacheSkip: already-cached items are skipped, and
// re-selecting a page yields nothing new (the aired cache dedups dispatch).
func TestPrefetchIdempotentAndCacheSkip(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", ListStatus: "watching"},
		{MalID: 2, AirStatus: "currently_airing", ListStatus: "watching"},
		{MalID: 3, AirStatus: "currently_airing", ListStatus: "watching"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 1 }, 10)
	m.aired.put(2, 7) // already cached → skip

	_, aired := m.selectPrefetchPage(true)
	if len(aired) != 2 || aired[0].MalID != 1 || aired[1].MalID != 3 {
		t.Errorf("first select = %v, want [1 3] (2 cached)", malIDs(aired))
	}
	if _, again := m.selectPrefetchPage(true); len(again) != 0 {
		t.Errorf("re-select page1 = %v, want empty (already dispatched)", malIDs(again))
	}
}

// TestPrefetchCoversAllItemsAcrossFilter: page 1 + page 2 together cover EVERY
// item's cover, including ones filtered out of the current view — so changing the
// status filter always reveals cached covers. (Guards the "filter change shows no
// covers" bug from paging only the filtered view.)
func TestPrefetchCoversAllItemsAcrossFilter(t *testing.T) {
	all := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "finished_airing", CoverURL: "u2"}, // filtered out
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3"},
		{MalID: 4, AirStatus: "finished_airing", CoverURL: "u4"}, // filtered out
	}
	m := newPrefetchPicker(all, func(*mal.Item) float64 { return 1 }, 10)
	// Simulate a status filter: the view shows only the airing items.
	m.view = []mal.Item{all[0], all[2]}

	covers1, _ := m.selectPrefetchPage(true)
	covers2, _ := m.selectPrefetchPage(false)
	got := append([]string{}, covers1...)
	got = append(got, covers2...)
	for _, u := range []string{"u1", "u2", "u3", "u4"} {
		if !sliceContains(got, u) {
			t.Errorf("cover %q not prefetched across pages; got %v", u, got)
		}
	}
}

// TestPrefetchAiredAcrossFilter (status-filter scoping edition): filter-kept
// airing items are dispatched even when hidden from m.view by e.g. the fuzzy
// filter — filterKeptAired scans m.items too, so the batch (and the progress
// bar) still reaches everything the status filter keeps.
func TestPrefetchAiredAcrossFilter(t *testing.T) {
	all := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing"}, // in view
		{MalID: 2, AirStatus: "currently_airing"}, // fuzzy-hidden
		{MalID: 3, AirStatus: "currently_airing"}, // in view
		{MalID: 4, AirStatus: "currently_airing"}, // fuzzy-hidden
	}
	m := newPrefetchPicker(all, func(*mal.Item) float64 { return 5 }, 10) // filter "All"
	m.view = []mal.Item{all[0], all[2]}                                   // fuzzy hides 2 and 4

	_, aired := m.selectPrefetchPage(true)
	got := malIDs(aired)
	for _, id := range []int{1, 2, 3, 4} {
		if !containsInt(got, id) {
			t.Errorf("filter-kept airing item %d not prefetched; got %v", id, got)
		}
	}
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestPrefetchEmptyViewChainsToPage2: on a fresh season the default "My List"
// filter keeps nothing (no on-list anime yet) — page 1 selects nothing but must
// still chain to page 2 so every cover loads. The off-list items' aired counts
// wait for a filter change (statusAiredPrefetchCmd), not page 2.
func TestPrefetchEmptyViewChainsToPage2(t *testing.T) {
	all := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "currently_airing", CoverURL: "u2"},
	}
	m := newPrefetchPicker(all, func(*mal.Item) float64 { return 5 }, 10)
	m.filter.Status = "My List" // Season default on a fresh season
	m.view = nil                // filter keeps nothing

	// Page 1 selects nothing…
	covers1, aired1 := m.selectPrefetchPage(true)
	if covers1 != nil || aired1 != nil {
		t.Errorf("page1 (empty view) = %v/%v, want nil/nil", covers1, aired1)
	}
	// …but its cmd still chains to page 2.
	cmd := m.prefetchPageCmd(true)
	if cmd == nil {
		t.Fatal("page1 cmd = nil for empty view, want a chaining cmd")
	}
	if _, ok := cmd().(prefetchPageDoneMsg); !ok {
		t.Fatalf("empty page1 cmd did not return prefetchPageDoneMsg")
	}
	// Page 2 covers everything (all items are filtered-out)…
	covers2, aired2 := m.selectPrefetchPage(false)
	for _, u := range []string{"u1", "u2"} {
		if !sliceContains(covers2, u) {
			t.Errorf("page2 (empty view) missing cover %q; got %v", u, covers2)
		}
	}
	// …but dispatches no aired work (that's the filter change's job now).
	if aired2 != nil {
		t.Errorf("page2 aired = %v, want nil (covers-only page)", malIDs(aired2))
	}
	// Switching the filter to "All" is what fetches the fresh season's counts.
	m.filter.Status = "All"
	if cmd := m.statusAiredPrefetchCmd(); cmd == nil {
		t.Error("filter switch to All: no dispatch cmd, want one for the off-list items")
	}
}

// TestPrefetchCoversEverythingAfterBothPages: after page 1 + page 2, EVERY item's
// cover and EVERY airing item's count has been selected — regardless of the
// status filter. Comprehensive guard for the filter-change bugs.
func TestPrefetchCoversEverythingAfterBothPages(t *testing.T) {
	all := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "finished_airing", CoverURL: "u2"},
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3"},
		{MalID: 4, AirStatus: "currently_airing", CoverURL: "u4"},
		{MalID: 5, AirStatus: "finished_airing", CoverURL: "u5"},
	}
	m := newPrefetchPicker(all, func(*mal.Item) float64 { return 7 }, 3)
	// Status filter: view shows only the finished items (2, 5).
	m.view = []mal.Item{all[1], all[4]}

	covers1, aired1 := m.selectPrefetchPage(true)
	covers2, aired2 := m.selectPrefetchPage(false)
	allCovers := append(append([]string{}, covers1...), covers2...)
	allAired := append(malIDs(aired1), malIDs(aired2)...)

	for _, u := range []string{"u1", "u2", "u3", "u4", "u5"} {
		if !sliceContains(allCovers, u) {
			t.Errorf("cover %q not prefetched across pages; got %v", u, allCovers)
		}
	}
	for _, id := range []int{1, 3, 4} { // the airing items
		if !containsInt(allAired, id) {
			t.Errorf("airing item %d not prefetched across pages; got %v", id, allAired)
		}
	}
	// Non-airing items must never be selected for aired prefetch.
	for _, id := range []int{2, 5} {
		if containsInt(allAired, id) {
			t.Errorf("non-airing item %d selected for aired prefetch; got %v", id, allAired)
		}
	}
}

// TestPrefetchAiredIdempotentAfterBothPages: once both pages have run, re-running
// either selects no new airing items (all dispatched). Covers are re-gathered but
// Download dedups them, so only aired dispatch idempotency is asserted here.
func TestPrefetchAiredIdempotentAfterBothPages(t *testing.T) {
	all := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing"},
		{MalID: 2, AirStatus: "currently_airing"},
		{MalID: 3, AirStatus: "currently_airing"},
	}
	m := newPrefetchPicker(all, func(*mal.Item) float64 { return 5 }, 2)
	m.selectPrefetchPage(true)
	m.selectPrefetchPage(false)

	if _, a1 := m.selectPrefetchPage(true); len(a1) != 0 {
		t.Errorf("re-run page1 aired = %v, want empty", malIDs(a1))
	}
	if _, a2 := m.selectPrefetchPage(false); len(a2) != 0 {
		t.Errorf("re-run page2 aired = %v, want empty (all dispatched)", malIDs(a2))
	}
}

// TestAiredResultSurvivesTeardown: the fetch goroutine records its outcome
// into the session cache itself, so a count completes after its picker was
// torn down (Enter into the release picker mid-prefetch, Esc-back, provider
// switch) still lands — the next picker adopts it instead of re-fetching the
// (rate-limited) request.
func TestAiredResultSurvivesTeardown(t *testing.T) {
	cache := NewAiredCache()
	items := []mal.Item{{MalID: 1, Title: "X", AirStatus: "currently_airing"}}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 7 }, 10)
	m.aired = cache // the session-shared cache

	cmds := m.airedCmds(m.filterKeptAired())
	if len(cmds) != 1 {
		t.Fatalf("airedCmds = %d cmds, want 1", len(cmds))
	}
	msg := cmds[0]() // fetch completes; its latestEpMsg is never delivered (picker gone)
	if lm, ok := msg.(latestEpMsg); !ok || lm.aired != 7 {
		t.Fatalf("cmd msg = %v, want latestEpMsg{aired:7}", msg)
	}
	if n, ok := cache.get(1); !ok || n != 7 {
		t.Fatalf("cache after undelivered msg = (%v, %v), want (7, true)", n, ok)
	}

	// The release picker built afterwards (same session cache) adopts the
	// value — its fetch fn must never run.
	rp := newReleasePicker(&mal.Item{MalID: 1, Title: "X", TotalEps: 12}, "", "", "newest",
		fetchAll(nil), false, nil,
		func(*mal.Item) float64 { t.Error("release picker re-fetched a recorded count"); return 0 },
		cache, 0, false)
	if cmd := rp.airedFetchCmd(); cmd != nil {
		t.Error("release picker issued a re-fetch for an already-recorded count")
	}
	if rp.aired != 7 {
		t.Errorf("release picker aired = %v, want 7 (adopted from the session cache)", rp.aired)
	}
}

// TestAiredCacheConcurrentRecord: Record runs on fetch goroutines while the
// Update goroutine inspects — the mutex keeps it consistent (run with -race).
func TestAiredCacheConcurrentRecord(t *testing.T) {
	c := NewAiredCache()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c.Record(id, float64(id))
			c.shouldFetch(id)
			c.value(id)
		}(i)
	}
	for i := 0; i < 64; i++ { // Update-goroutine side
		c.markDispatched(i + 1000)
		c.get(i)
	}
	wg.Wait()
	for i := 0; i < 64; i++ {
		if n, ok := c.get(i); !ok || n != float64(i) {
			t.Fatalf("cache[%d] = (%v, %v), want (%d, true)", i, n, ok, i)
		}
	}
}

// TestPrefetchDisabledNoAired: with latestEpisodePrefetch == nil, no aired items
// are selected but covers are still returned.
func TestPrefetchDisabledNoAired(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "currently_airing", CoverURL: "u2"},
	}
	m := newPrefetchPicker(items, nil, 10)

	covers, aired := m.selectPrefetchPage(true)
	if len(covers) != 2 {
		t.Errorf("covers = %v, want 2 (covers still paged)", covers)
	}
	if len(aired) != 0 {
		t.Errorf("aired = %v, want empty (prefetch disabled)", malIDs(aired))
	}
}

// TestPrefetchPageCmdEmptyHandling: a populated page 1 returns a real cmd; an
// empty page 1 (no items / nothing selected) still chains to page 2 via a
// prefetchPageDoneMsg cmd; an empty page 2 returns nil.
func TestPrefetchPageCmdEmptyHandling(t *testing.T) {
	nonEmpty := newPrefetchPicker(
		[]mal.Item{{MalID: 1, AirStatus: "currently_airing", CoverURL: "u"}},
		func(*mal.Item) float64 { return 1 }, 10)
	if nonEmpty.prefetchPageCmd(true) == nil {
		t.Errorf("page1 with items = nil, want non-nil")
	}

	empty := newPrefetchPicker(nil, func(*mal.Item) float64 { return 1 }, 10)
	cmd := empty.prefetchPageCmd(true)
	if cmd == nil {
		t.Fatal("empty page1 cmd = nil, want a chaining cmd")
	}
	if _, ok := cmd().(prefetchPageDoneMsg); !ok {
		t.Errorf("empty page1 cmd = %T, want prefetchPageDoneMsg (chain to page 2)", cmd())
	}
	if empty.prefetchPageCmd(false) != nil {
		t.Errorf("empty page2 cmd = non-nil, want nil (nothing left)")
	}
}

// TestPrefetchPageDoneHandler: firstPage done schedules page 2 (covers-only
// now — the items carry cover URLs so page 2 has work); the tail page's done
// is a no-op.
func TestPrefetchPageDoneHandler(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "currently_airing", CoverURL: "u2"},
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3"},
		{MalID: 4, AirStatus: "currently_airing", CoverURL: "u4"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 1 }, 2)

	if _, cmd := m.Update(prefetchPageDoneMsg{firstPage: true}); cmd == nil {
		t.Fatal("firstPage done: cmd = nil, want a page-2 cmd")
	}
	if _, cmd := m.Update(prefetchPageDoneMsg{firstPage: false}); cmd != nil {
		t.Errorf("tail page done: cmd = non-nil, want nil")
	}
}

// TestPrefetchFocusCacheHitAndFallback: after the prefetch fills m.aired,
// focusing that item issues no new fetch; focusing an uncached item invokes the
// full latestEpisode fn (the Jikan-fallback path is preserved for focus).
func TestPrefetchFocusCacheHitAndFallback(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing"},
		{MalID: 2, AirStatus: "currently_airing"},
	}
	calls := 0
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil,
		func(*mal.Item) float64 { calls++; return 9 }, // focus fn (full)
		func(*mal.Item) float64 { return 0 },          // prefetch fn (unused here)
		false)
	m.filter.Status = "All"
	m.filter.Sort = "relevance"
	loadAnime(m, items)
	m.height = 50
	m.paneHeight = 13 // pageSize 10

	// Prefetch fills aired[1] (recorded at the fetch site).
	m.aired.Record(1, 3)
	if n, _ := m.aired.get(1); n != 3 {
		t.Fatalf("aired[1] = %v, want 3", n)
	}

	// Focusing the cached item → no fetch.
	m.cursor = 0
	if cmd := m.latestEpisodeCmd(); cmd != nil {
		t.Errorf("focus on cached item: latestEpisodeCmd non-nil, want nil (cache hit)")
	}

	// Focusing the uncached item → a fetch cmd that invokes the full fn.
	m.cursor = 1
	cmd := m.latestEpisodeCmd()
	if cmd == nil {
		t.Fatal("focus on uncached item: latestEpisodeCmd = nil, want a fetch cmd")
	}
	before := calls
	m.Update(cmd())
	if calls != before+1 {
		t.Errorf("focus fallback: calls = %v, want %v", calls, before+1)
	}
}

// TestPrefetchSemaphoreCap: a picker's semaphore bounds concurrent in-flight
// aired fetches to the torrent prefetch cap (animetosho times out under heavier
// load).
func TestPrefetchSemaphoreCap(t *testing.T) {
	m := newAnimePicker(SourceSeason, "", animeLoadAll(nil), nil, nil, nil, nil, nil, false)
	sem := m.prefetchSem
	const n = 64
	var inFlight, maxInFlight int32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				mm := atomic.LoadInt32(&maxInFlight)
				if cur <= mm || atomic.CompareAndSwapInt32(&maxInFlight, mm, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}()
	}
	wg.Wait()
	if maxInFlight > int32(torrentPrefetchCap) {
		t.Errorf("max concurrent prefetch = %v, want <= %v", maxInFlight, torrentPrefetchCap)
	}
}

// TestAiredFailedFetchRetried: AiredFailed (the fetch itself errored — hianime
// down/blocked) must NOT be cached as a final answer: the id stays retryable, so
// re-focusing fetches it again and a later success caches normally. A genuine 0,
// by contrast, remains a once-per-session answer. (The old behavior cached the
// failure as 0, pinning every aired count to "?" for the rest of the session.)
func TestAiredFailedFetchRetried(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing"},
		{MalID: 2, AirStatus: "currently_airing"},
	}
	calls := 0
	failing := true // focus fn mirrors app.go's hianime closure: AiredFailed on error
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil,
		func(*mal.Item) float64 {
			calls++
			if failing {
				return AiredFailed
			}
			return 7
		},
		nil, false)
	m.filter.Status = "All"
	m.filter.Sort = "relevance"
	loadAnime(m, items)
	m.height = 50
	m.paneHeight = 13 // pageSize 10

	// Focus item 1 while the provider is down: the fetch fails.
	m.cursor = 0
	cmd := m.latestEpisodeCmd()
	if cmd == nil {
		t.Fatal("latestEpisodeCmd = nil, want a fetch cmd")
	}
	m.Update(cmd())
	if _, ok := m.aired.get(1); ok {
		t.Fatal("failed fetch stored a value; want the cache entry absent")
	}
	if !m.aired.shouldFetch(1) {
		t.Fatal("failed fetch left malID 1 un-retryable; want shouldFetch = true")
	}

	// The outage passes; re-focusing the same item retries and now caches.
	failing = false
	cmd = m.latestEpisodeCmd()
	if cmd == nil {
		t.Fatal("re-focus after failure: latestEpisodeCmd = nil, want a retry cmd")
	}
	m.Update(cmd())
	if n, ok := m.aired.get(1); !ok || n != 7 {
		t.Fatalf("aired[1] = %v, %v; want 7, true", n, ok)
	}

	// A genuine 0 is a final answer — focusing that item never re-fetches.
	m.aired.Record(2, 0)
	m.cursor = 1
	if cmd := m.latestEpisodeCmd(); cmd != nil {
		t.Error("focus on genuine-0 item: latestEpisodeCmd non-nil, want nil (cached)")
	}
	if calls != 2 {
		t.Errorf("focus fn calls = %d, want 2 (failed try + one retry)", calls)
	}
}

// TestPrefetchDebugLogsScope verifies the debug log makes the prefetch's
// status-filter scoping observable: page 1 logs the filter it dispatched under
// (carrying only that filter's items), and a later filter change logs its own
// dispatch.
func TestPrefetchDebugLogsScope(t *testing.T) {
	var buf bytes.Buffer
	mal.SetDebugLog(&buf)
	defer mal.SetDebugLog(io.Discard)

	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", ListStatus: "watching"},
		{MalID: 2, AirStatus: "currently_airing", ListStatus: "plan_to_watch"},
		{MalID: 3, AirStatus: "currently_airing"}, // off-list
	}
	m := newPrefetchPicker(items, func(*mal.Item) float64 { return 1 }, 10)
	m.filter.Status = "My List"
	m.applyFilter()
	m.prefetchPageCmd(true) // page 1 → the My List slice

	m.filter.Status = "All"
	m.applyFilter()
	m.statusAiredPrefetchCmd() // filter change → the off-list bulk

	out := buf.String()
	i1 := strings.Index(out, `page 1 (status:My List)`)
	i2 := strings.Index(out, `after filter "All"`)
	if i1 < 0 || !strings.Contains(out, "malIDs=[1 2]") {
		t.Errorf("page-1 log missing or didn't carry only the My List items:\n%s", out)
	}
	if i2 < 0 || !strings.Contains(out, "malIDs=[3]") {
		t.Errorf("filter-change log missing or didn't carry the off-list item:\n%s", out)
	}
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Errorf("expected page-1 log before the filter-change log:\n%s", out)
	}
}
