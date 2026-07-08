package ui

import (
	"strings"
	"testing"

	"ani/internal/mal"
)

func TestRenderMALLine(t *testing.T) {
	// The list line is title-only now (details live in the preview pane).
	cases := []struct {
		item mal.Item
		want string
	}{
		{mal.Item{Title: "Frieren", TotalEps: 28, WatchedEps: 3, AirStatus: "currently_airing", Score: 9}, "Frieren"},
		{mal.Item{Title: "X", TotalEps: 12, WatchedEps: 12, AirStatus: "finished_airing"}, "X"},
		{mal.Item{Title: "Y", AirStatus: "not_yet_aired"}, "Y"},
	}
	for _, c := range cases {
		if got := RenderMALLine(c.item); got != c.want {
			t.Errorf("RenderMALLine(%+v) = %q, want %q", c.item, got, c.want)
		}
	}
}

func TestColoredStatus(t *testing.T) {
	if got := ColoredStatus(""); got != "" {
		t.Errorf("ColoredStatus(\"\") = %q, want empty", got)
	}
	// Known statuses render as a badge: the label with badge padding (color
	// itself depends on the terminal profile, so we assert structure not ANSI).
	for _, s := range []string{"watching", "completed", "on_hold", "dropped", "plan_to_watch"} {
		got := ColoredStatus(s)
		label := MALListStatusShort(s)
		if !strings.Contains(got, label) {
			t.Errorf("ColoredStatus(%q) = %q, missing label %q", s, got, label)
		}
		if !strings.HasPrefix(got, " ") || !strings.HasSuffix(got, " ") {
			t.Errorf("ColoredStatus(%q) = %q, want badge padding", s, got)
		}
		if !hasStatusColor(s) {
			t.Errorf("hasStatusColor(%q) = false, want true", s)
		}
	}
	// Unknown status: plain label, no badge padding.
	if got := ColoredStatus("something"); got != "Something" {
		t.Errorf("ColoredStatus(\"something\") = %q, want %q", got, "Something")
	}
	if hasStatusColor("something") {
		t.Errorf("hasStatusColor(\"something\") = true, want false")
	}
}

func TestFormatProgress(t *testing.T) {
	cases := []struct {
		name                  string
		watched, total, aired int
		airing                bool
		want                  string
	}{
		{"airing, 4 aired of 12", 0, 12, 4, true, "ep 0/4/12"},
		{"airing, caught up", 4, 12, 4, true, "ep 4/4/12"},
		{"airing, unknown total", 5, 0, 5, true, "ep 5/5/?"},
		{"airing, aired unknown, total known", 0, 12, 0, true, "ep 0/?/12"},
		{"airing, both unknown", 0, 0, 0, true, "ep 0/?/?"},
		{"not airing, total known", 3, 28, 0, false, "ep 3/28"},
		{"not airing, finished", 12, 12, 0, false, "ep 12/12"},
		{"not airing, nothing known", 0, 0, 0, false, ""},
	}
	for _, c := range cases {
		if got := FormatProgress(c.watched, c.total, c.aired, c.airing); got != c.want {
			t.Errorf("%s: FormatProgress(%d,%d,%d,%v) = %q, want %q", c.name, c.watched, c.total, c.aired, c.airing, got, c.want)
		}
	}
}
