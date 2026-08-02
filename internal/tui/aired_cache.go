package tui

// AiredCache memoizes the latest-aired-episode count per MAL id for ONE app
// session. Each anime's count is computed AT MOST ONCE per session (until the app
// exits): a 0/failed result is stored too — it is not retried this session.
//
// It is owned by app.Run and shared across the anime picker and the release
// picker (and across Esc-from-releases, which recreate the picker), so a count
// computed anywhere is never re-fetched.
//
// CONCURRENCY: every method runs on the single bubbletea Update goroutine.
// selectPrefetchPage/maybeAppendAired/latestEpisodeCmd build their cmds
// synchronously on Update; the worker goroutines they spawn only call the
// injected latestEpisode* fn and return a latestEpMsg — they never touch this
// cache. Picker programs run serially, so the shared cache has a single writer
// at a time. No mutex is needed.
type AiredCache struct {
	values   map[int]int  // malID → computed count (incl. 0); presence = "done this session"
	inflight map[int]bool // malID → a fetch is currently in flight (dedup)
}

// NewAiredCache returns an empty session-scoped aired-episode cache.
func NewAiredCache() *AiredCache {
	return &AiredCache{values: map[int]int{}, inflight: map[int]bool{}}
}

// get returns the cached count and whether one is stored for malID.
func (c *AiredCache) get(malID int) (int, bool) { n, ok := c.values[malID]; return n, ok }

// value returns the cached count, or 0 if none (map-like zero semantics, for
// render / carrying into the release picker).
func (c *AiredCache) value(malID int) int { return c.values[malID] }

// shouldFetch is false once a count has been computed for malID OR a fetch is
// already in flight for it; true otherwise.
func (c *AiredCache) shouldFetch(malID int) bool {
	_, done := c.values[malID]
	_, flying := c.inflight[malID]
	return !done && !flying
}

// markDispatched records that a fetch for malID is in flight. Call it
// synchronously on the Update goroutine when dispatching, before returning the
// async cmd, so a second focus/prefetch in the same tick is deduped.
func (c *AiredCache) markDispatched(malID int) { c.inflight[malID] = true }

// put stores the computed count (any value, including 0) and clears the in-flight
// marker. A 0 is kept for the session (no retry) per the once-per-session rule.
func (c *AiredCache) put(malID, count int) {
	c.values[malID] = count
	delete(c.inflight, malID)
}
