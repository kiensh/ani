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
	if h.IsDown("anidb") {
		t.Fatal("fresh tracker reports anidb down")
	}
	h.MarkDown("anidb", "HTTP 503")
	if !h.IsDown("anidb") {
		t.Fatal("anidb not down after MarkDown")
	}
	w := h.Warning("anidb")
	if !strings.Contains(w, "anidb.app unreachable (HTTP 503)") {
		t.Fatalf("Warning = %q, want the anidb.app down line with reason", w)
	}
	if !strings.Contains(w, "streams unavailable") {
		t.Fatalf("Warning = %q, want the anidb effect text", w)
	}
	h.MarkUp("anidb")
	if h.IsDown("anidb") || h.Warning("anidb") != "" {
		t.Fatal("anidb still down after MarkUp")
	}
}

// TestProviderHealthWarningScoping: the warning line reports only the backends
// the caller passes — the pickers pass the ACTIVE provider (plus MAL on the
// anime picker) — so a down backend the user isn't on never surfaces, and the
// first passed key that is down wins.
func TestProviderHealthWarningScoping(t *testing.T) {
	h := NewProviderHealth()
	h.MarkDown("anidb", "HTTP 503")

	// On the torrent provider: anidb being down must NOT warn.
	if w := h.Warning("torrent"); w != "" {
		t.Fatalf("torrent-only Warning = %q, want empty (anidb is not in use)", w)
	}
	// Switching to anidb: now it warns.
	if w := h.Warning("anidb"); !strings.Contains(w, "anidb.app unreachable") {
		t.Fatalf("anidb Warning = %q, want the down line", w)
	}

	// Provider reachable, MAL down: the anime picker (provider, "mal") warns
	// about MAL instead; the release picker (provider only) stays quiet.
	h.MarkUp("anidb")
	h.MarkDown("mal", "HTTP 502")
	if w := h.Warning("torrent", "mal"); !strings.Contains(w, "MyAnimeList unreachable (HTTP 502)") {
		t.Fatalf("Warning(torrent, mal) = %q, want the MyAnimeList down line", w)
	}
	if w := h.Warning("torrent"); w != "" {
		t.Fatalf("Warning(torrent) = %q, want empty (mal is not a release-picker backend)", w)
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
	m.provider = "anidb"
	m.health = NewProviderHealth()
	m.health.MarkDown("anidb", "HTTP 503")
	if out := m.View(); !strings.Contains(out, "anidb.app unreachable (HTTP 503)") {
		t.Errorf("View with anidb down: warning line missing")
	}

	// Provider NOT in use: anidb down while on torrent must stay silent.
	m = newPicker()
	m.provider = "torrent"
	m.health = NewProviderHealth()
	m.health.MarkDown("anidb", "HTTP 503")
	if out := m.View(); strings.Contains(out, "unreachable") {
		t.Errorf("View on torrent with anidb down: unexpected warning %q", firstLine(out))
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
	m.provider = "anidb"
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
	m.provider = "anidb"
	m.health = NewProviderHealth()
	m.health.MarkDown("anidb", "HTTP 503")
	if out := m.View(); !strings.Contains(out, "anidb.app unreachable (HTTP 503)") {
		t.Errorf("View with anidb down: warning line missing")
	}

	// A down provider out of use stays silent.
	m = newPicker()
	m.provider = "torrent"
	m.health = NewProviderHealth()
	m.health.MarkDown("anidb", "HTTP 503")
	if out := m.View(); strings.Contains(out, "unreachable") {
		t.Errorf("View on torrent with anidb down: unexpected warning")
	}

	// The transient toast wins its slot; the warning returns after it clears.
	m = newPicker()
	m.provider = "anidb"
	m.health = NewProviderHealth()
	m.health.MarkDown("anidb", "HTTP 503")
	m.toast = "✓ Magnet copied to clipboard"
	if out := m.View(); !strings.Contains(out, "Magnet copied") || strings.Contains(out, "unreachable") {
		t.Errorf("View with toast over warning: toast should win the help line")
	}
	m.Update(clearToastMsg{})
	if out := m.View(); !strings.Contains(out, "anidb.app unreachable") {
		t.Errorf("View after toast cleared: warning should be back")
	}
}
