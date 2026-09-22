// Package hostgate paces requests to rate-limiting hosts: request starts to a
// host are spaced at least Pace apart (serialized per host), and an HTTP 429
// trips a Cooldown window during which Wait fails fast without touching the
// network — so a burst-heavy background prefetch doesn't pile onto a host that
// just refused it. hianime.at and feed.animetosho.xyz both answer bursts with
// 429 while accepting sequential traffic at a steady pace (verified on each).
//
// Gates are keyed by the request URL's host: a provider constructs one Gates
// value and routes its requests through it, so each host it talks to (feed,
// embeds, CDNs) gets its own state and one host's cooldown never affects
// another.
package hostgate

import (
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// ErrLimited marks a 429 (or a request skipped because of one): the host
// isn't down, it's refusing our pace. Consumers map it to a "rate-limited,
// cooling down" message rather than "unreachable" (the error text carries
// "HTTP 429" so an HTTP-code extractor still finds it).
var ErrLimited = errors.New("rate limited (HTTP 429)")

// Gates paces and rate-limits requests, per host. Pace and Cooldown are
// mutable so tests can shorten them — configure before use, not concurrently
// with in-flight requests.
type Gates struct {
	Pace     time.Duration // minimum spacing between request starts (per host)
	Cooldown time.Duration // fail-fast window after a 429 (Retry-After overrides)

	m sync.Map // host → *gate
}

// gate is one host's pacing state.
type gate struct {
	mu        sync.Mutex
	last      time.Time // last request start
	holdUntil time.Time // 429 cooldown deadline
}

// Wait reserves the next request slot for u's host, sleeping out the pace gap
// (under the host's lock, so concurrent callers queue up in order). It fails
// fast with ErrLimited while a tripped cooldown is active — checked before AND
// after the sleep, so a 429 that lands mid-queue stops the requests queued
// behind it.
func (s *Gates) Wait(u string) error {
	g := s.gateFor(u)
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Now().Before(g.holdUntil) {
		return ErrLimited
	}
	if d := g.last.Add(s.Pace).Sub(time.Now()); d > 0 {
		time.Sleep(d)
	}
	if time.Now().Before(g.holdUntil) {
		return ErrLimited
	}
	g.last = time.Now()
	return nil
}

// Trip records a 429 for u's host: requests fail fast until the cooldown
// passes. retryAfter is the response's Retry-After in seconds (0 = use
// Cooldown); a header value is clamped to [5s, 2min] so a hostile 0 can't
// disable the backoff and a stale huge value can't sideline the provider. A
// trip only extends, never shortens, an active cooldown.
func (s *Gates) Trip(u string, retryAfter int) {
	g := s.gateFor(u)
	d := s.Cooldown
	if retryAfter > 0 {
		d = time.Duration(retryAfter) * time.Second
		if d < 5*time.Second {
			d = 5 * time.Second
		}
		if d > 2*time.Minute {
			d = 2 * time.Minute
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := time.Now().Add(d); until.After(g.holdUntil) {
		g.holdUntil = until
	}
}

// gateFor returns u's host's gate (created on first use; "" host gets a
// throwaway so unparseable URLs never panic or gate real traffic).
func (s *Gates) gateFor(u string) *gate {
	host := ""
	if p, err := url.Parse(u); err == nil {
		host = p.Host
	}
	if host == "" {
		return &gate{}
	}
	g, _ := s.m.LoadOrStore(host, &gate{})
	return g.(*gate)
}

// ParseRetryAfter reads a Retry-After header value as seconds (0 when absent
// or not a plain integer).
func ParseRetryAfter(v string) int {
	n, _ := strconv.Atoi(v)
	return n
}
