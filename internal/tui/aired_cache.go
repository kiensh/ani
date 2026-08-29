package tui

import "time"

// inflightTTL is how long an in-flight marker blocks a re-dispatch. A fetch
// whose picker quits before the result lands (provider switch, Esc-back, plain
// quit) never clears its marker; after the TTL the id becomes retryable again
// instead of staying dead for the rest of the session.
const inflightTTL = 10 * time.Second

// AiredCache memoizes the latest-aired-episode count per MAL id for ONE app
// session. Each anime's count is computed AT MOST ONCE per session (until the app
// exits): a 0/failed result is stored too — it is not retried this session.
//
// It is owned by app.Run and shared across the anime picker and the release
// picker (and across Esc-from-releases, which recreate the picker), so a count
// computed anywhere is never re-fetched. app.Run resets the cache on a provider
// switch (counts are provider-specific).
//
// CONCURRENCY: every method runs on the single bubbletea Update goroutine.
// selectPrefetchPage/maybeAppendAired/latestEpisodeCmd build their cmds
// synchronously on Update; the worker goroutines they spawn only call the
// injected latestEpisode* fn and return a latestEpMsg — they never touch this
// cache. Picker programs run serially, so the shared cache has a single writer
// at a time. No mutex is needed.
type AiredCache struct {
	values   map[int]float64   // malID → computed count (incl. 0); presence = "done this session"
	inflight map[int]time.Time // malID → dispatch time of the in-flight fetch (dedup)
}

// NewAiredCache returns an empty session-scoped aired-episode cache.
func NewAiredCache() *AiredCache {
	return &AiredCache{values: map[int]float64{}, inflight: map[int]time.Time{}}
}

// Reset clears all cached counts and in-flight markers. app.Run calls it on a
// provider switch: counts are provider-specific, so the old values and zeros
// don't apply. Resetting in place (vs replacing the cache) keeps every holder
// of the pointer — the release picker mid-switch — on the fresh cache.
func (c *AiredCache) Reset() {
	c.values = map[int]float64{}
	c.inflight = map[int]time.Time{}
}

// clearInflight drops every in-flight marker without touching stored counts.
// applyLoaded calls it when a fresh list lands: markers left behind by a
// torn-down picker (Esc into the release picker and back, provider switch)
// would otherwise block this picker's prefetch for the inflightTTL even though
// nothing is flying. A duplicate dispatch against a still-running orphaned
// fetch is harmless — both write the same count.
func (c *AiredCache) clearInflight() {
	c.inflight = map[int]time.Time{}
}

// get returns the cached count and whether one is stored for malID.
func (c *AiredCache) get(malID int) (float64, bool) { n, ok := c.values[malID]; return n, ok }

// value returns the cached count, or 0 if none (map-like zero semantics, for
// render / carrying into the release picker).
func (c *AiredCache) value(malID int) float64 { return c.values[malID] }

// shouldFetch is false once a count has been computed for malID OR a fetch went
// out for it less than inflightTTL ago; true otherwise. An older in-flight
// marker means the fetch died with its picker — the id is retryable.
func (c *AiredCache) shouldFetch(malID int) bool {
	_, done := c.values[malID]
	if done {
		return false
	}
	t, flying := c.inflight[malID]
	return !flying || time.Since(t) >= inflightTTL
}

// markDispatched records that a fetch for malID is in flight. Call it
// synchronously on the Update goroutine when dispatching, before returning the
// async cmd, so a second focus/prefetch in the same tick is deduped.
func (c *AiredCache) markDispatched(malID int) { c.inflight[malID] = time.Now() }

// put stores the computed count (any value, including 0) and clears the in-flight
// marker. A 0 is kept for the session (no retry) per the once-per-session rule.
func (c *AiredCache) put(malID int, count float64) {
	c.values[malID] = count
	delete(c.inflight, malID)
}
