package main

import (
	"os"
	"strings"
	"testing"
)

func TestWriteMpvTitleConfig(t *testing.T) {
	title := "[Erai-raws] Sousou no Frieren 2nd Season - 06 [1080p]"
	path, cleanup, ok := writeMpvTitleConfig(title)
	if !ok {
		t.Fatal("writeMpvTitleConfig returned ok=false")
	}
	defer cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp config: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "title="+title) {
		t.Errorf("missing title= line; got:\n%s", s)
	}
	if !strings.Contains(s, "force-media-title="+title) {
		t.Errorf("missing force-media-title= line; got:\n%s", s)
	}
	// path must contain no spaces (webtorrent splits --player-args on spaces)
	if strings.Contains(path, " ") {
		t.Errorf("temp path contains a space: %s", path)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove %s", path)
	}
}
