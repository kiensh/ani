package tui

import (
	"fmt"
	"sync"
)

// ProviderHealth tracks reachability of the app's backends for ONE app
// session: MyAnimeList ("mal" — the anime list), AnimeTosho ("torrent") and
// hianime.at ("hianime"). A backend is marked down when a real request to it
// fails at the transport/HTTP level, and any later success marks it back up —
// so the warning follows the provider's actual state, including recovery.
//
// The pickers render the warning for the ACTIVE backend only (never for a
// down backend the user isn't using), and app.Run pre-marks a backend on a
// provider switch by probing it, so switching to a dead provider shows the
// error immediately instead of after the first failed fetches.
//
// CONCURRENCY: unlike AiredCache (Update-goroutine only), the markers are
// also called from the aired-prefetch / release-fetch worker goroutines via
// the injected closures, so every method takes the mutex. Warning runs per
// render frame but only reads a small map under the same lock.
type ProviderHealth struct {
	mu   sync.Mutex
	down map[string]string // backend key → short reason; absent = healthy/unknown
}

// NewProviderHealth returns a tracker with every backend presumed reachable.
func NewProviderHealth() *ProviderHealth {
	return &ProviderHealth{down: map[string]string{}}
}

// MarkDown records backend as unreachable with a short reason for the warning
// line. Idempotent; a later failure overwrites the reason.
func (h *ProviderHealth) MarkDown(backend, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down[backend] = reason
}

// MarkUp clears a down marker — any successful request means reachable again.
func (h *ProviderHealth) MarkUp(backend string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.down, backend)
}

// IsDown reports whether backend is currently marked unreachable.
func (h *ProviderHealth) IsDown(backend string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, down := h.down[backend]
	return down
}

// Warning returns the "⚠ <name> unreachable (<reason>) — <effect>" line for
// the first DOWN backend among keys, or "" when none is down. A "rate-limited"
// reason (a 429 cooldown) drops the "unreachable" wording — the backend is
// up, it's refusing our pace. Callers pass only the backends actually in use —
// the active provider, plus the list source (MAL) for the anime picker — so a
// down backend the user isn't using never surfaces. Empty keys are skipped.
func (h *ProviderHealth) Warning(keys ...string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range keys {
		if k == "" {
			continue
		}
		reason, down := h.down[k]
		if !down {
			continue
		}
		if reason == "rate-limited" {
			return fmt.Sprintf("⚠ %s rate-limited (cooling down) — %s", backendName(k), backendEffect(k))
		}
		return fmt.Sprintf("⚠ %s unreachable (%s) — %s", backendName(k), reason, backendEffect(k))
	}
	return ""
}

// backendName is the display name for a backend key in the warning line.
func backendName(backend string) string {
	switch backend {
	case "hianime":
		return "hianime.at"
	case "mal":
		return "MyAnimeList"
	default: // "torrent"
		return "AnimeTosho"
	}
}

// backendEffect is what losing the backend costs, for the warning line.
func backendEffect(backend string) string {
	switch backend {
	case "hianime":
		return "aired counts and streams unavailable"
	case "mal":
		return "anime list unavailable"
	default: // "torrent"
		return "releases unavailable"
	}
}
