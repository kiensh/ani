package config

import (
	"path/filepath"
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
