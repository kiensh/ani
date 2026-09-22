package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempConfig points config at a temp file for the test and restores after.
func withTempConfig(t *testing.T) {
	t.Helper()
	old := configFile
	configFile = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { configFile = old })
}

func TestAnidbOverrideRoundTrip(t *testing.T) {
	withTempConfig(t)

	if _, ok := AnidbOverride(12345); ok {
		t.Fatal("AnidbOverride should be absent before any save")
	}
	SaveAnidbOverride(12345, 67890)
	aid, ok := AnidbOverride(12345)
	if !ok || aid != 67890 {
		t.Errorf("AnidbOverride(12345) = (%d, %v), want (67890, true)", aid, ok)
	}

	// A second override persists alongside the first.
	SaveAnidbOverride(222, 333)
	if aid, ok := AnidbOverride(222); !ok || aid != 333 {
		t.Errorf("AnidbOverride(222) = (%d, %v), want (333, true)", aid, ok)
	}
	if aid, ok := AnidbOverride(12345); !ok || aid != 67890 {
		t.Errorf("AnidbOverride(12345) after second save = (%d, %v), want (67890, true)", aid, ok)
	}
}

// TestLegacyAnidbMigration: a config written before the stream provider moved
// from anidb.app to hianime keeps working — source "anidb" loads as "hianime"
// and the anidb_group/anidb_quality filter preferences carry into their
// hianime_* successors (the legacy keys are not re-emitted on the next save).
func TestLegacyAnidbMigration(t *testing.T) {
	withTempConfig(t)
	legacy := `{
		"source": "anidb",
		"anidb_group": "sub",
		"anidb_quality": "1080p",
		"sort": "newest"
	}`
	if err := os.WriteFile(configFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if cfg.Source != "hianime" {
		t.Errorf("Source = %q, want hianime (migrated from anidb)", cfg.Source)
	}
	if cfg.HianimeGroup != "sub" || cfg.HianimeQuality != "1080p" {
		t.Errorf("hianime filters = %q/%q, want sub/1080p (migrated from anidb_*)", cfg.HianimeGroup, cfg.HianimeQuality)
	}

	// The next save drops the legacy keys and keeps the migrated values.
	SaveFilters("dub", "720p", "newest", "hianime")
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); strings.Contains(s, `"anidb_group"`) || strings.Contains(s, `"anidb_quality"`) {
		t.Errorf("legacy filter keys re-emitted after save:\n%s", s)
	}
	cfg = Load()
	if cfg.Source != "hianime" || cfg.HianimeGroup != "dub" || cfg.HianimeQuality != "720p" {
		t.Errorf("after save: %q %q/%q, want hianime dub/720p", cfg.Source, cfg.HianimeGroup, cfg.HianimeQuality)
	}

	// SaveSource also normalizes the legacy spelling so it can't resurrect the
	// dead provider.
	SaveSource("anidb")
	if cfg := Load(); cfg.Source != "hianime" {
		t.Errorf("SaveSource(anidb) then Load = %q, want hianime", cfg.Source)
	}
}
