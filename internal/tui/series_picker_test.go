package tui

import (
	"os"
	"testing"

	"ani/internal/animetosho"
	tea "github.com/charmbracelet/bubbletea"
)

func mkSeries(aid int, title string) animetosho.SeriesSummary {
	return animetosho.SeriesSummary{AnidbAID: aid, Title: title, TorrentCount: 5, LatestRelease: "2026-07-12T00:00:00Z"}
}

// TestSeriesPickerSelect drives the model: j moves down, Enter selects the
// highlighted series' aid.
func TestSeriesPickerSelect(t *testing.T) {
	m := newSeriesPicker("Some MAL Title", []animetosho.SeriesSummary{
		mkSeries(100, "Show A"),
		mkSeries(200, "Show B"),
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	m.Update(downMsg()) // cursor → Show B (aid 200)
	m.Update(enterMsg())

	if !m.result.ok {
		t.Fatal("Enter: result.ok = false, want true")
	}
	if m.result.aid != 200 {
		t.Errorf("Enter: result.aid = %d, want 200 (Show B)", m.result.aid)
	}
}

// TestSeriesPickerCancel: Esc cancels (no selection).
func TestSeriesPickerCancel(t *testing.T) {
	m := newSeriesPicker("x", []animetosho.SeriesSummary{mkSeries(100, "Show A")})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(escMsg())

	if m.result.ok {
		t.Errorf("Esc: result.ok = true, want false (cancel)")
	}
	if m.result.aid != 0 {
		t.Errorf("Esc: result.aid = %d, want 0", m.result.aid)
	}
}

// TestSeriesPickerFirstSelectedByDefault: Enter without navigating selects the
// first series.
func TestSeriesPickerFirstSelectedByDefault(t *testing.T) {
	m := newSeriesPicker("x", []animetosho.SeriesSummary{
		mkSeries(111, "First"),
		mkSeries(222, "Second"),
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(enterMsg())

	if !m.result.ok || m.result.aid != 111 {
		t.Errorf("default Enter: result = (%d, %v), want (111, true)", m.result.aid, m.result.ok)
	}
}

// TestSeriesPickerEmpty: an empty list never selects.
func TestSeriesPickerEmpty(t *testing.T) {
	m := newSeriesPicker("x", nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(enterMsg())

	if m.result.ok {
		t.Errorf("empty list Enter: result.ok = true, want false")
	}
}

// TestDownloadCoverFileIntegration verifies downloadCoverFile fetches a real
// animetosho cover. Skipped unless ANI_INTEGRATION=1.
func TestDownloadCoverFileIntegration(t *testing.T) {
	if os.Getenv("ANI_INTEGRATION") == "" {
		t.Skip("skipping network integration test; set ANI_INTEGRATION=1 to run")
	}
	path := downloadCoverFile("https://animetosho.xyz/static/img/anidb_covers/327330.jpg")
	if path == "" {
		t.Fatal("downloadCoverFile returned empty path")
	}
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat downloaded file: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("downloaded cover file is empty")
	}
	t.Logf("downloaded cover: %s (%d bytes)", path, info.Size())
}
