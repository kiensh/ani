package hostgate

import (
	"errors"
	"testing"
	"time"
)

// TestPaceSpacesStarts: Wait serializes callers to one host and spaces request
// starts at least Pace apart.
func TestPaceSpacesStarts(t *testing.T) {
	s := &Gates{Pace: 30 * time.Millisecond}
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.Wait("http://feed.example/x"); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	if d := time.Since(start); d < 2*s.Pace {
		t.Errorf("3 Waits finished in %v, want >= %v (spaced starts)", d, 2*s.Pace)
	}
}

// TestPacePerHost: different hosts gate independently — feed's pace (next
// slot an hour out) doesn't delay a request to another host.
func TestPacePerHost(t *testing.T) {
	s := &Gates{Pace: time.Hour} // feed.example's next slot is far away
	if err := s.Wait("http://feed.example/x"); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	start := time.Now()
	if err := s.Wait("http://cdn.example/y"); err != nil {
		t.Fatalf("cdn Wait: %v (hosts must not share a gate)", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("cdn wait took %v — gated by feed's pace", d)
	}
}

// TestCooldownFailsFastAndRecovers: after Trip, Wait returns ErrLimited
// without hitting the network, and passes again once the configured cooldown
// expires (the configured Cooldown is honored as-is; only Retry-After values
// get the minimum clamp).
func TestCooldownFailsFastAndRecovers(t *testing.T) {
	s := &Gates{Pace: time.Hour, Cooldown: 40 * time.Millisecond} // pace never elapses naturally
	s.Trip("http://feed.example/x", 0)
	if err := s.Wait("http://feed.example/x"); !errors.Is(err, ErrLimited) {
		t.Fatalf("Wait during cooldown = %v, want ErrLimited", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := s.Wait("http://feed.example/x"); err != nil {
		t.Fatalf("Wait after cooldown = %v, want nil", err)
	}
	// The cooldown was for one host only.
	if err := s.Wait("http://cdn.example/y"); err != nil {
		t.Fatalf("other host gated by feed's cooldown: %v", err)
	}
}

// TestTripRetryAfterClamps: a Retry-After override is clamped to [5s, 2min].
// Trips only extend, never shorten, an active cooldown.
func TestTripRetryAfterClamps(t *testing.T) {
	s := &Gates{Cooldown: 20 * time.Second}
	const u = "http://feed.example/x"
	s.Trip(u, 0)
	if d := time.Until(s.gateFor(u).holdUntil); d < 4*time.Second {
		t.Errorf("Retry-After 0 cooled down for %v, want the 5s clamp", d)
	}
	s.Trip(u, 1000)
	if d := time.Until(s.gateFor(u).holdUntil); d > 2*time.Minute {
		t.Errorf("Retry-After 1000 cooled down for %v, want the 2min clamp", d)
	}
	// A shorter later trip must not shorten the active cooldown.
	long := time.Until(s.gateFor(u).holdUntil)
	s.Trip(u, 0)
	if now := time.Until(s.gateFor(u).holdUntil); now < long-time.Second {
		t.Errorf("second trip shortened the cooldown (%v → %v)", long, now)
	}
}

// TestParseRetryAfter: plain seconds parse; anything else is 0 (use the
// default cooldown).
func TestParseRetryAfter(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"30", 30}, {"", 0}, {"soon", 0},
	} {
		if got := ParseRetryAfter(c.in); got != c.want {
			t.Errorf("ParseRetryAfter(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
