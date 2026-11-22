package tui

import (
	"sync"
	"time"

	"ani/internal/mal"
)

// inflightTTL is how long an in-flight marker blocks a re-dispatch. A fetch
// whose picker quits before the result lands (provider switch, Esc-back, plain
// quit) never clears its marker; after the TTL the id becomes retryable again
// instead of staying dead for the rest of the session.
const inflightTTL = 10 * time.Second

// AiredCache is shared by both pickers for one provider. Focused lookups
// (including a genuine zero) last the session; listing hints can be replaced
// by a later snapshot. Neither a failed request nor a missing listing row is
// a count. Commands record hits here even when their picker has exited.
type AiredCache struct {
	mu       sync.Mutex
	values   map[int]float64   // malID → computed count (incl. 0); presence = "done this session"
	listed   map[int]float64   // snapshot hints; never override a focused response
	inflight map[int]time.Time // malID → dispatch time of the in-flight fetch (dedup)
}

// NewAiredCache returns an empty session-scoped aired-episode cache.
func NewAiredCache() *AiredCache {
	return &AiredCache{values: map[int]float64{}, listed: map[int]float64{}, inflight: map[int]time.Time{}}
}

// seed replaces hints for the requested rows only. A show leaving the feed
// becomes eligible for a focused lookup again.
func (c *AiredCache) seed(items []mal.Item, counts map[int]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, it := range items {
		if n := counts[it.MalID]; n > 0 {
			c.listed[it.MalID] = n
		} else {
			delete(c.listed, it.MalID)
		}
	}
}

func (c *AiredCache) hasExact(id int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.values[id]
	return ok
}

// clearInflight drops every in-flight marker without touching stored counts.
// applyLoaded calls it when a fresh list lands: markers left behind by a
// torn-down picker (Esc into the release picker and back, provider switch)
// would otherwise block this picker's prefetch for the inflightTTL even though
// nothing is flying. A duplicate dispatch against a still-running orphaned
// fetch is harmless — both write the same count.
func (c *AiredCache) clearInflight() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight = map[int]time.Time{}
}

// get returns the cached count and whether one is stored for malID.
func (c *AiredCache) get(malID int) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.values[malID]; ok {
		return n, true
	}
	n, ok := c.listed[malID]
	return n, ok
}

// value returns the cached count, or 0 if none (map-like zero semantics, for
// render / carrying into the release picker).
func (c *AiredCache) value(malID int) float64 {
	n, _ := c.get(malID)
	return n
}

// shouldFetch is false once a count has been computed for malID OR a fetch went
// out for it less than inflightTTL ago; true otherwise. An older in-flight
// marker means the fetch died with its picker — the id is retryable.
func (c *AiredCache) shouldFetch(malID int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, done := c.values[malID]; done || c.listed[malID] > 0 {
		return false
	}
	t, flying := c.inflight[malID]
	return !flying || time.Since(t) >= inflightTTL
}

// markDispatched records that a fetch for malID is in flight. Call it
// synchronously on the Update goroutine when dispatching, before returning the
// async cmd, so a second focus/prefetch in the same tick is deduped.
func (c *AiredCache) markDispatched(malID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight[malID] = time.Now()
}

// Record stores a fetch's outcome from the goroutine the fetch ran on:
// AiredFailed records nothing (the id stays retryable), any real answer
// (including a genuine 0) is kept for the session. Recording here — not in the
// Update-loop msg handler — is what makes a count survive its picker being
// torn down mid-flight (Enter into releases, Esc-back, provider switch).
func (c *AiredCache) Record(malID int, aired float64) {
	if aired == AiredFailed {
		c.fail(malID)
		return
	}
	c.put(malID, aired)
}

// put stores the computed count (any value, including 0) and clears the in-flight
// marker. A 0 is kept for the session (no retry) per the once-per-session rule.
func (c *AiredCache) put(malID int, count float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[malID] = count
	delete(c.inflight, malID)
}

// fail records that the fetch for malID itself failed (provider down/blocked): it
// clears the in-flight marker WITHOUT storing a value, so the id stays retryable —
// the next focus or picker entry fetches it again. A failed fetch is not an answer,
// unlike a computed 0.
func (c *AiredCache) fail(malID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inflight, malID)
}
