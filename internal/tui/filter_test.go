package tui

import (
	"testing"

	"ani/internal/animetosho"
)

func mkRel(group, res string, ep int, batch bool) *animetosho.Release {
	return &animetosho.Release{
		Entry:      &animetosho.Entry{ReleaseGroup: group, Resolution: res, Series: animetosho.Series{EpisodeNumber: ep}, IsBatch: batch},
		Group:      group,
		Resolution: res,
		Episode:    ep,
		IsBatch:    batch,
	}
}

func TestResolutionHeight(t *testing.T) {
	cases := map[string]string{
		"1920x1080": "1080",
		"1280x720":  "720",
		"1080p":     "1080",
		"3840x2160": "2160",
		"720":       "",
		"":          "",
		"1080":      "",
	}
	for in, want := range cases {
		if got := resolutionHeight(in); got != want {
			t.Errorf("resolutionHeight(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDistinctQualities(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1920x1080", 1, false),
		mkRel("b", "1280x720", 2, false),
		mkRel("c", "3840x2160", 3, false),
		mkRel("d", "1080p", 4, false),
		mkRel("e", "unknown", 5, false),
	}
	got := DistinctQualities(all)
	want := []string{"2160p", "1080p", "720p"}
	if len(got) != len(want) {
		t.Fatalf("DistinctQualities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DistinctQualities[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDistinctQualitiesOrdering(t *testing.T) {
	// Out-of-order inputs still come back highest-first.
	all := []*animetosho.Release{
		mkRel("a", "720p", 1, false),
		mkRel("b", "480p", 2, false),
		mkRel("c", "1080p", 3, false),
	}
	got := DistinctQualities(all)
	want := []string{"1080p", "720p", "480p"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestDefaultQuality(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "720p", 1, false),
		mkRel("b", "1080p", 2, false),
	}
	if q := DefaultQuality(all); q != "1080p" {
		t.Errorf("DefaultQuality = %q, want 1080p", q)
	}
	none := []*animetosho.Release{mkRel("a", "weird", 1, false)}
	if q := DefaultQuality(none); q != "" {
		t.Errorf("DefaultQuality(no recognized) = %q, want \"\"", q)
	}
}

func TestDefaultEpisode(t *testing.T) {
	cases := []struct {
		watched, total, want int
	}{
		{3, 12, 4},   // next unwatched
		{11, 12, 12}, // last episode
		{12, 12, 0},  // finished → all
		{5, 0, 6},    // unknown total → next episode (watched+1)
		{0, 0, 1},    // unknown total, nothing watched → episode 1
		{0, 24, 1},   // just started
		{24, 24, 0},  // finished
	}
	for _, c := range cases {
		if got := DefaultEpisode(c.watched, c.total); got != c.want {
			t.Errorf("DefaultEpisode(%d,%d) = %d, want %d", c.watched, c.total, got, c.want)
		}
	}
}

func TestDistinctGroups(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("Erai-raws", "1080p", 1, false),
		mkRel("SubsPlease", "1080p", 2, false),
		mkRel("Erai-raws", "720p", 3, false),
		mkRel("", "1080p", 4, false),
	}
	got := DistinctGroups(all)
	want := []string{"Erai-raws", "SubsPlease"}
	if len(got) != len(want) {
		t.Fatalf("DistinctGroups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestFilterApplyQuality(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
		mkRel("b", "720p", 2, false),
		mkRel("c", "1080p", 3, false),
	}
	f := Filter{Quality: "1080p"}
	got := f.Apply(all)
	if len(got) != 2 {
		t.Fatalf("quality 1080p kept %d, want 2", len(got))
	}
}

func TestFilterApplyEpisode(t *testing.T) {
	all := []*animetosho.Release{
		mkRel("a", "1080p", 1, false),
		mkRel("b", "1080p", 2, false),
		mkRel("c", "1080p", 0, true), // batch excluded
	}
	f := Filter{Episode: 2}
	got := f.Apply(all)
	if len(got) != 1 || got[0].Episode != 2 {
		t.Fatalf("ep 2 filter = %+v, want only ep2", got)
	}
}

func TestReleaseSortOptions(t *testing.T) {
	if v, ok := releaseSortValue("Oldest"); !ok || v != "oldest" {
		t.Errorf("releaseSortValue(Oldest) = %q,%v want oldest,true", v, ok)
	}
	if releaseSortLabel("smallest") != "Smallest" {
		t.Errorf("releaseSortLabel(smallest) = %q, want Smallest", releaseSortLabel("smallest"))
	}
	if _, ok := releaseSortValue("Nope"); ok {
		t.Errorf("releaseSortValue(Nope) = ok, want false")
	}
	if len(releaseSortOptions) != 4 {
		t.Errorf("len(releaseSortOptions) = %d, want 4", len(releaseSortOptions))
	}
}

func TestOverlayGroupOpenSelect(t *testing.T) {
	var o filterOverlay
	o.openGroup([]string{"Erai-raws", "SubsPlease"}, "")
	if o.kind != overlayGroup {
		t.Fatalf("kind = %v, want overlayGroup", o.kind)
	}
	if len(o.items) != 3 || o.items[0] != "All" {
		t.Errorf("items = %v, want [All Erai-raws SubsPlease]", o.items)
	}
	if o.cursor != 0 {
		t.Errorf("cursor (current empty) = %d, want 0 (All)", o.cursor)
	}

	// Move down to Erai-raws, apply.
	o.move(1)
	f := &Filter{Group: ""}
	o.applySelected(f)
	if f.Group != "Erai-raws" {
		t.Errorf("after move+apply, Group = %q, want Erai-raws", f.Group)
	}

	// "All" maps to "".
	o.cursor = 0
	o.applySelected(f)
	if f.Group != "" {
		t.Errorf("All → Group = %q, want \"\"", f.Group)
	}
}

func TestOverlayGroupOpensOnCurrent(t *testing.T) {
	var o filterOverlay
	o.openGroup([]string{"Erai-raws", "SubsPlease"}, "SubsPlease")
	if o.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (SubsPlease)", o.cursor)
	}
}

func TestOverlayQualityOpen(t *testing.T) {
	var o filterOverlay
	o.openQuality([]string{"1080p", "720p"}, "1080p")
	if o.kind != overlayQuality {
		t.Fatalf("kind = %v", o.kind)
	}
	if len(o.items) != 3 || o.items[0] != "All" || o.items[1] != "1080p" {
		t.Errorf("items = %v", o.items)
	}
	if o.cursor != 1 {
		t.Errorf("cursor (current 1080p) = %d, want 1", o.cursor)
	}
}

func TestOverlayEpisodeInput(t *testing.T) {
	var o filterOverlay
	o.openEpisode(0)
	if o.kind != overlayEpisode {
		t.Fatalf("kind = %v", o.kind)
	}
	if o.text != "" {
		t.Errorf("seeded text = %q, want empty", o.text)
	}
	o.handleEpisodeKey("1")
	o.handleEpisodeKey("2")
	if o.text != "12" {
		t.Errorf("text = %q, want 12", o.text)
	}
	o.handleEpisodeKey("backspace")
	if o.text != "1" {
		t.Errorf("after backspace text = %q, want 1", o.text)
	}
	f := &Filter{}
	o.applySelected(f)
	if f.Episode != 1 {
		t.Errorf("Episode = %d, want 1", f.Episode)
	}
	// Empty → all.
	o.text = ""
	o.applySelected(f)
	if f.Episode != 0 {
		t.Errorf("empty → Episode = %d, want 0", f.Episode)
	}
}

func TestOverlayEpisodeSeededFromCurrent(t *testing.T) {
	var o filterOverlay
	o.openEpisode(11)
	if o.text != "11" {
		t.Errorf("seeded text = %q, want 11", o.text)
	}
}

func TestOverlayClose(t *testing.T) {
	var o filterOverlay
	o.openGroup([]string{"x"}, "")
	o.close()
	if o.active() {
		t.Errorf("close did not deactivate")
	}
}

func TestOverlayMoveWraps(t *testing.T) {
	var o filterOverlay
	o.openGroup([]string{"a", "b", "c"}, "")
	o.move(-1) // wrap from 0 to last
	if o.cursor != 3 {
		t.Errorf("move(-1) from 0 → cursor = %d, want 3", o.cursor)
	}
	o.move(1) // wrap from last to 0
	if o.cursor != 0 {
		t.Errorf("move(+1) from last → cursor = %d, want 0", o.cursor)
	}
}

func TestFuzzyMatch(t *testing.T) {
	if !fuzzyMatch("erai raws 1080p", "er108") {
		t.Errorf("expected match for subsequence")
	}
	if fuzzyMatch("erai", "xyz") {
		t.Errorf("expected no match")
	}
}
