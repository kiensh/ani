package tui

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ani/internal/mal"
)

// newPrefetchPicker builds a Season picker showing all items in input order
// (Sort=relevance), with a given pageSize. prefetch is the background aired-episode
// fn (nil disables aired prefetch). The focus latestEpisode fn is a no-op.
func newPrefetchPicker(items []mal.Item, prefetch func(*mal.Item) int, pageSize int) *animePicker {
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil,
		func(*mal.Item) int { return 0 }, prefetch, false)
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

// TestPrefetchPagingSplit: page 1 is the first pageSize items; the rest only after.
func TestPrefetchPagingSplit(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "currently_airing", CoverURL: "u2"},
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3"},
		{MalID: 4, AirStatus: "currently_airing", CoverURL: "u4"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) int { return 5 }, 2)

	covers1, aired1 := m.selectPrefetchPage(true)
	if len(aired1) != 2 || aired1[0].MalID != 1 || aired1[1].MalID != 2 {
		t.Errorf("page1 aired = %v, want [1 2]", malIDs(aired1))
	}
	if len(covers1) != 2 || covers1[0] != "u1" || covers1[1] != "u2" {
		t.Errorf("page1 covers = %v, want [u1 u2]", covers1)
	}

	covers2, aired2 := m.selectPrefetchPage(false)
	if len(aired2) != 2 || aired2[0].MalID != 3 || aired2[1].MalID != 4 {
		t.Errorf("page2 aired = %v, want [3 4]", malIDs(aired2))
	}
	if len(covers2) != 2 || covers2[0] != "u3" || covers2[1] != "u4" {
		t.Errorf("page2 covers = %v, want [u3 u4]", covers2)
	}
}

// TestPrefetchDefaultPageSize: with an unknown layout (height 0) page 1 uses the
// default (~20), the rest is the remainder.
func TestPrefetchDefaultPageSize(t *testing.T) {
	items := make([]mal.Item, 25)
	for i := range items {
		items[i] = mal.Item{MalID: i + 1, AirStatus: "currently_airing"}
	}
	m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil,
		func(*mal.Item) int { return 1 }, false)
	m.filter.Status = "All"
	m.filter.Sort = "relevance"
	loadAnime(m, items)
	m.height = 0 // unknown layout

	_, aired1 := m.selectPrefetchPage(true)
	if len(aired1) != 20 {
		t.Errorf("page1 (height 0) aired = %d, want 20 (default)", len(aired1))
	}
	_, aired2 := m.selectPrefetchPage(false)
	if len(aired2) != 5 {
		t.Errorf("page2 aired = %d, want 5 (remainder)", len(aired2))
	}
}

// TestPrefetchOrderMatchesView: page 1 follows m.view order, independent of
// m.items load order.
func TestPrefetchOrderMatchesView(t *testing.T) {
	items := []mal.Item{
		{MalID: 10, AirStatus: "currently_airing"},
		{MalID: 20, AirStatus: "currently_airing"},
		{MalID: 30, AirStatus: "currently_airing"},
		{MalID: 40, AirStatus: "currently_airing"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) int { return 1 }, 2)
	// Reorder the view (as a sort would) so the top is 40, 30.
	m.view = []mal.Item{items[3], items[2], items[1], items[0]}

	_, aired1 := m.selectPrefetchPage(true)
	if len(aired1) != 2 || aired1[0].MalID != 40 || aired1[1].MalID != 30 {
		t.Errorf("page1 aired = %v, want [40 30] (m.view order)", malIDs(aired1))
	}
}

// TestPrefetchSkipsNonAiring: covers are gathered for every item; aired episodes
// only for currently_airing items.
func TestPrefetchSkipsNonAiring(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "finished_airing", CoverURL: "u2"},
		{MalID: 3, AirStatus: "currently_airing", CoverURL: "u3"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) int { return 1 }, 10)

	covers, aired := m.selectPrefetchPage(true)
	if len(covers) != 3 {
		t.Errorf("covers = %v, want all 3 (covers aren't air-gated)", covers)
	}
	if len(aired) != 2 || aired[0].MalID != 1 || aired[1].MalID != 3 {
		t.Errorf("aired = %v, want [1 3] (non-airing skipped)", malIDs(aired))
	}
}

// TestPrefetchIdempotentAndCacheSkip: already-cached items are skipped, and
// re-selecting a page yields nothing new (airedPrefetched dedups).
func TestPrefetchIdempotentAndCacheSkip(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing"},
		{MalID: 2, AirStatus: "currently_airing"},
		{MalID: 3, AirStatus: "currently_airing"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) int { return 1 }, 10)
	m.aired[2] = 7 // already cached → skip

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
	m := newPrefetchPicker(all, func(*mal.Item) int { return 1 }, 10)
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

// TestPrefetchAiredAcrossFilter: page 1 + page 2 together prefetch EVERY airing
// item's count, including ones filtered out of the current view — so changing
// the status filter shows aired counts instantly (no focus delay). Symmetric to
// the covers fix.
func TestPrefetchAiredAcrossFilter(t *testing.T) {
	all := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing"}, // in view
		{MalID: 2, AirStatus: "currently_airing"}, // filtered out
		{MalID: 3, AirStatus: "currently_airing"}, // in view
		{MalID: 4, AirStatus: "currently_airing"}, // filtered out
	}
	m := newPrefetchPicker(all, func(*mal.Item) int { return 5 }, 10)
	m.view = []mal.Item{all[0], all[2]} // status filter hides 2 and 4

	_, aired1 := m.selectPrefetchPage(true)
	_, aired2 := m.selectPrefetchPage(false)
	got := append(malIDs(aired1), malIDs(aired2)...)
	for _, id := range []int{1, 2, 3, 4} {
		if !containsInt(got, id) {
			t.Errorf("airing item %d not prefetched across pages; got %v", id, got)
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

// TestPrefetchEmptyViewChainsToPage2: when the initial filter hides everything
// (empty view), page 1 selects nothing but must still chain to page 2 — which is
// where the filtered-out items get covered. Guards the "default My List filter on
// a fresh season prefetches nothing" regression.
func TestPrefetchEmptyViewChainsToPage2(t *testing.T) {
	all := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing", CoverURL: "u1"},
		{MalID: 2, AirStatus: "currently_airing", CoverURL: "u2"},
	}
	m := newPrefetchPicker(all, func(*mal.Item) int { return 5 }, 10)
	m.view = nil // status filter hides everything

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
	// Page 2 covers everything (all items are filtered-out).
	covers2, aired2 := m.selectPrefetchPage(false)
	for _, u := range []string{"u1", "u2"} {
		if !sliceContains(covers2, u) {
			t.Errorf("page2 (empty view) missing cover %q; got %v", u, covers2)
		}
	}
	if len(aired2) != 2 {
		t.Errorf("page2 (empty view) aired = %v, want both items", malIDs(aired2))
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
	m := newPrefetchPicker(all, func(*mal.Item) int { return 7 }, 3)
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
	m := newPrefetchPicker(all, func(*mal.Item) int { return 5 }, 2)
	m.selectPrefetchPage(true)
	m.selectPrefetchPage(false)

	if _, a1 := m.selectPrefetchPage(true); len(a1) != 0 {
		t.Errorf("re-run page1 aired = %v, want empty", malIDs(a1))
	}
	if _, a2 := m.selectPrefetchPage(false); len(a2) != 0 {
		t.Errorf("re-run page2 aired = %v, want empty (all dispatched)", malIDs(a2))
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
		func(*mal.Item) int { return 1 }, 10)
	if nonEmpty.prefetchPageCmd(true) == nil {
		t.Errorf("page1 with items = nil, want non-nil")
	}

	empty := newPrefetchPicker(nil, func(*mal.Item) int { return 1 }, 10)
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

// TestPrefetchPageDoneHandler: firstPage done schedules page 2; the tail page's
// done is a no-op.
func TestPrefetchPageDoneHandler(t *testing.T) {
	items := []mal.Item{
		{MalID: 1, AirStatus: "currently_airing"},
		{MalID: 2, AirStatus: "currently_airing"},
		{MalID: 3, AirStatus: "currently_airing"},
		{MalID: 4, AirStatus: "currently_airing"},
	}
	m := newPrefetchPicker(items, func(*mal.Item) int { return 1 }, 2)

	if _, cmd := m.Update(prefetchPageDoneMsg{firstPage: true}); cmd == nil {
		t.Fatal("firstPage done: cmd = nil, want a page-2 cmd")
	}
	if _, cmd := m.Update(prefetchPageDoneMsg{firstPage: false}); cmd != nil {
		t.Errorf("tail page done: cmd = non-nil, want nil")
	}
}

// TestPrefetchSemaphorePerInstance: each picker gets its own semaphore (cap
// prefetchCap), distinct from other instances — so orphaned goroutines from a
// previous picker (e.g. after going to release and back) can't stall this one.
func TestPrefetchSemaphorePerInstance(t *testing.T) {
	m1 := newAnimePicker(SourceSeason, "", animeLoadAll(nil), nil, nil, nil, nil, nil, false)
	m2 := newAnimePicker(SourceSeason, "", animeLoadAll(nil), nil, nil, nil, nil, nil, false)
	if cap(m1.prefetchSem) != prefetchCap {
		t.Errorf("m1.prefetchSem cap = %d, want %d", cap(m1.prefetchSem), prefetchCap)
	}
	if m1.prefetchSem == nil || m1.prefetchSem == m2.prefetchSem {
		t.Errorf("pickers must have distinct non-nil sems; m1=%p m2=%p", m1.prefetchSem, m2.prefetchSem)
	}
}

// TestPrefetchSemaphoreCap: a picker's semaphore bounds concurrent in-flight
// aired fetches to prefetchCap.
func TestPrefetchSemaphoreCap(t *testing.T) {
	m := newAnimePicker(SourceSeason, "", animeLoadAll(nil), nil, nil, nil, nil, nil, false)
	sem := m.prefetchSem
	const n = 50
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
	if maxInFlight > int32(prefetchCap) {
		t.Errorf("max concurrent prefetch = %d, want <= %d", maxInFlight, prefetchCap)
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
		func(*mal.Item) int { calls++; return 9 }, // focus fn (full)
		func(*mal.Item) int { return 0 },          // prefetch fn (unused here)
		false)
	m.filter.Status = "All"
	m.filter.Sort = "relevance"
	loadAnime(m, items)
	m.height = 50
	m.paneHeight = 13 // pageSize 10

	// Prefetch fills m.aired[1].
	m.Update(latestEpMsg{malID: 1, aired: 3})
	if m.aired[1] != 3 {
		t.Fatalf("aired[1] = %d, want 3", m.aired[1])
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
		t.Errorf("focus fallback: calls = %d, want %d", calls, before+1)
	}
}
