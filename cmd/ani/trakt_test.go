package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ani/internal/trakt"
)

type accountStub struct {
	connected bool
	calls     []string
	err       error
}

func (s *accountStub) Login(_ context.Context, prompt func(string, string)) error {
	s.calls = append(s.calls, "login")
	prompt("https://trakt.tv/activate", "TESTCODE")
	if s.err == nil {
		s.connected = true
	}
	return s.err
}
func (s *accountStub) Connected() bool { return s.connected }
func (s *accountStub) Logout() error {
	s.calls = append(s.calls, "logout")
	s.connected = false
	return s.err
}
func (s *accountStub) Watchlist(context.Context) ([]trakt.Title, error) {
	s.calls = append(s.calls, "list")
	return []trakt.Title{{Kind: "movie", Name: "Example", Year: 2025}}, s.err
}
func (s *accountStub) History(context.Context) ([]trakt.HistoryItem, error) {
	s.calls = append(s.calls, "history")
	return []trakt.HistoryItem{{Title: trakt.Title{Kind: "series", Name: "Example", Year: 2009}, Season: 2, Episode: 4, WatchedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}}, s.err
}

func TestTraktCommands(t *testing.T) {
	s := &accountStub{}
	var out bytes.Buffer
	run := func(args ...string) error { out.Reset(); return runTraktCommand(t.Context(), args, s, &out) }
	for _, args := range [][]string{{}, {"--help"}, {"status"}} {
		if err := run(args...); err != nil || len(s.calls) != 0 {
			t.Fatalf("%v had side effects: %v %v", args, s.calls, err)
		}
	}
	if err := run("history"); err == nil || !strings.Contains(err.Error(), "ani trakt login") || len(s.calls) != 0 {
		t.Fatalf("disconnected should stay offline: %v", err)
	}
	if err := run("login"); err != nil || !strings.Contains(out.String(), "TESTCODE") || !s.connected {
		t.Fatal("device sign-in prompt missing")
	}
	if err := run("login"); err != nil || len(s.calls) != 2 {
		t.Fatal("explicit login must allow reauthorization of a previously connected account")
	}
	if err := run("history"); err != nil || !strings.Contains(out.String(), "2026-02-03  Example (2009) S02E04") {
		t.Fatalf("history: %s %v", out.String(), err)
	}
	if err := run("watchlist"); err != nil || !strings.Contains(out.String(), "[movie] Example (2025)") {
		t.Fatalf("watchlist: %s %v", out.String(), err)
	}
	if err := run("logout"); err != nil || s.connected || !strings.Contains(out.String(), "unchanged") {
		t.Fatalf("logout: %s %v", out.String(), err)
	}
	if err := run("login", "--dry-run"); err == nil || s.connected {
		t.Fatal("trailing flags must not trigger a real login")
	}
	if err := run("what"); err == nil {
		t.Fatal("unknown command accepted")
	}
	s.err = errors.New("authorization denied")
	if err := run("login"); !errors.Is(err, s.err) || strings.Contains(out.String(), "Trakt connected.") {
		t.Fatalf("login failure: %v %q", err, out.String())
	}
}
