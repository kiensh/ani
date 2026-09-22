package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"ani/internal/mal"
	"ani/internal/playable"
)

// TestProviderHealthMarking: a backend is down after MarkDown, up again after
// MarkUp, and IsDown/Warning agree about it. The reason is carried into the
// warning line.
func TestProviderHealthMarking(t *testing.T) {
	h := NewProviderHealth()
	if h.IsDown("hianime") {
		t.Fatal("fresh tracker reports hianime down")
	}
	h.MarkDown("hianime", "HTTP 503")
	if !h.IsDown("hianime") {
		t.Fatal("hianime not down after MarkDown")
	}
	w := h.Warning("hianime")
	if !strings.Contains(w, "hianime.at unreachable (HTTP 503)") {
		t.Fatalf("Warning = %q, want the hianime.at down line with reason", w)
	}
	if !strings.Contains(w, "streams unavailable") {
		t.Fatalf("Warning = %q, want the hianime effect text", w)
	}
	h.MarkUp("hianime")
	if h.IsDown("hianime") || h.Warning("hianime") != "" {
		t.Fatal("hianime still down after MarkUp")
	}
}

// TestProviderHealthWarningScoping: the warning line reports only the backends
// the caller passes — the pickers pass the ACTIVE provider (plus MAL on the
// anime picker) — so a down backend the user isn't on never surfaces, and the
// first passed key that is down wins.
func TestProviderHealthWarningScoping(t *testing.T) {
	h := NewProviderHealth()
	h.MarkDown("hianime", "HTTP 503")

	// On the torrent provider: hianime being down must NOT warn.
	if w := h.Warning("torrent"); w != "" {
		t.Fatalf("torrent-only Warning = %q, want empty (hianime is not in use)", w)
	}
	// Switching to hianime: now it warns.
	if w := h.Warning("hianime"); !strings.Contains(w, "hianime.at unreachable") {
		t.Fatalf("hianime Warning = %q, want the down line", w)
	}

	// Provider reachable, MAL down: the anime picker (provider, "mal") warns
	// about MAL instead; the release picker (provider only) stays quiet.
	h.MarkUp("hianime")
	h.MarkDown("mal", "HTTP 502")
	if w := h.Warning("torrent", "mal"); !strings.Contains(w, "MyAnimeList unreachable (HTTP 502)") {
		t.Fatalf("Warning(torrent, mal) = %q, want the MyAnimeList down line", w)
	}
	if w := h.Warning("torrent"); w != "" {
		t.Fatalf("Warning(torrent) = %q, want empty (mal is not a release-picker backend)", w)
	}

	// A rate-limited backend (429 cooldown) reads as cooling down, not dead —
	// the site is up, it's refusing our pace.
	h2 := NewProviderHealth()
	h2.MarkDown("hianime", "rate-limited")
	if w := h2.Warning("hianime"); !strings.Contains(w, "hianime.at rate-limited (cooling down)") {
		t.Fatalf("rate-limited Warning = %q, want the cooling-down line", w)
	}
	if strings.Contains(h2.Warning("hianime"), "unreachable") {
		t.Fatalf("rate-limited Warning should not say unreachable: %q", h2.Warning("hianime"))
	}

	// Empty keys (pickers with no provider set) are skipped, not warned.
	if w := h.Warning("", "torrent"); w != "" {
		t.Fatalf(`Warning("", "torrent") = %q, want empty`, w)
	}
}

// TestAnimePickerDownWarning: the anime picker replaces its help line with the
// down warning for the ACTIVE provider (then MAL), never for a provider out of
// use, and not at all when health tracking is off (nil).
func TestAnimePickerDownWarning(t *testing.T) {
	items := []mal.Item{{MalID: 1, Title: "Re:Zero"}}
	newPicker := func() *animePicker {
		m := newAnimePicker(SourceSeason, "", animeLoadAll(items), nil, nil, nil, nil, nil, false)
		loadAnime(m, items)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
		return m
	}

	// Active provider down → warning replaces the help line.
	m := newPicker()
	m.provider = "hianime"
	m.health = NewProviderHealth()
	m.health.MarkDown("hianime", "HTTP 503")
	if out := m.View(); !strings.Contains(out, "hianime.at unreachable (HTTP 503)") {
		t.Errorf("View with hianime down: warning line missing")
	}

	// Provider NOT in use: hianime down while on torrent must stay silent.
	m = newPicker()
	m.provider = "torrent"
	m.health = NewProviderHealth()
	m.health.MarkDown("hianime", "HTTP 503")
	if out := m.View(); strings.Contains(out, "unreachable") {
		t.Errorf("View on torrent with hianime down: unexpected warning %q", firstLine(out))
	}

	// MAL down while on torrent: the list source warns.
	m = newPicker()
	m.provider = "torrent"
	m.health = NewProviderHealth()
	m.health.MarkDown("mal", "HTTP 502")
	if out := m.View(); !strings.Contains(out, "MyAnimeList unreachable") {
		t.Errorf("View with mal down: warning line missing")
	}

	// nil health (tests, library use): never warns.
	m = newPicker()
	m.provider = "hianime"
	if out := m.View(); strings.Contains(out, "unreachable") {
		t.Errorf("View with nil health: unexpected warning")
	}
}

// TestReleasePickerDownWarning: the release picker warns for the active provider
// only, in the help line's slot, and the transient toast outranks it.
func TestReleasePickerDownWarning(t *testing.T) {
	item := &mal.Item{MalID: 1, Title: "Re:Zero", TotalEps: 12}
	rels := []*playable.Release{{Title: "Re:Zero 01", Group: "sub", Resolution: "1080"}}
	newPicker := func() *releasePicker {
		m := newReleasePicker(item, "", "", "newest", fetchAll(rels), false, nil, nil, nil, 0, false)
		m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
		m.applyLoaded(releasesLoadedMsg{releases: rels, ep: 1})
		return m
	}

	m := newPicker()
	m.provider = "hianime"
	m.health = NewProviderHealth()
	m.health.MarkDown("hianime", "HTTP 503")
	if out := m.View(); !strings.Contains(out, "hianime.at unreachable (HTTP 503)") {
		t.Errorf("View with hianime down: warning line missing")
	}

	// A down provider out of use stays silent.
	m = newPicker()
	m.provider = "torrent"
	m.health = NewProviderHealth()
	m.health.MarkDown("hianime", "HTTP 503")
	if out := m.View(); strings.Contains(out, "unreachable") {
		t.Errorf("View on torrent with hianime down: unexpected warning")
	}

	// The transient toast wins its slot; the warning returns after it clears.
	m = newPicker()
	m.provider = "hianime"
	m.health = NewProviderHealth()
	m.health.MarkDown("hianime", "HTTP 503")
	m.toast = "✓ Magnet copied to clipboard"
	if out := m.View(); !strings.Contains(out, "Magnet copied") || strings.Contains(out, "unreachable") {
		t.Errorf("View with toast over warning: toast should win the help line")
	}
	m.Update(clearToastMsg{})
	if out := m.View(); !strings.Contains(out, "hianime.at unreachable") {
		t.Errorf("View after toast cleared: warning should be back")
	}
}
