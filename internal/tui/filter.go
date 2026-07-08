package tui

import (
	"strconv"
	"strings"

	"ani/internal/animetosho"
	"ani/internal/ui"
)

// Filter holds the release-picker filter/sort state. The zero value is "all
// releases, newest first, no fuzzy filter".
type Filter struct {
	Group     string // "" = all
	Quality   string // "" = all, or "1080p", "720p", "480p", "2160p"
	Episode   int    // 0 = all
	Sort      string // "newest" (default), "oldest", "smallest", "largest"
	FuzzyText string // fuzzy filter text (only meaningful when Filtering)
	Filtering bool   // true when in fuzzy filter input mode (after pressing /)
}

// releaseSortOptions is the release-picker sort overlay list (label → value).
var releaseSortOptions = []struct{ label, value string }{
	{"Newest", "newest"},
	{"Oldest", "oldest"},
	{"Smallest", "smallest"},
	{"Largest", "largest"},
}

func releaseSortLabels() []string {
	out := make([]string, len(releaseSortOptions))
	for i, o := range releaseSortOptions {
		out[i] = o.label
	}
	return out
}

func releaseSortLabel(value string) string {
	for _, o := range releaseSortOptions {
		if o.value == value {
			return o.label
		}
	}
	return value
}

func releaseSortValue(label string) (string, bool) {
	for _, o := range releaseSortOptions {
		if o.label == label {
			return o.value, true
		}
	}
	return "", false
}

// SetEpisode sets the episode filter (0 means all).
func (f *Filter) SetEpisode(n int) {
	if n < 0 {
		n = 0
	}
	f.Episode = n
}

// Apply runs the filter/sort pipeline over a sorted-all release slice and
// returns the visible subset. Order: group → quality → episode → fuzzy → sort.
func (f *Filter) Apply(all []*animetosho.Release) []*animetosho.Release {
	rs := ui.FilterByGroup(all, f.Group)
	if f.Quality != "" {
		filtered := make([]*animetosho.Release, 0, len(rs))
		for _, r := range rs {
			if matchResolution(r.Resolution, f.Quality) {
				filtered = append(filtered, r)
			}
		}
		rs = filtered
	}
	if f.Episode > 0 {
		filtered := make([]*animetosho.Release, 0, len(rs))
		for _, r := range rs {
			if !r.IsBatch && r.Episode == f.Episode {
				filtered = append(filtered, r)
			}
		}
		rs = filtered
	}
	if f.Filtering && f.FuzzyText != "" {
		needle := strings.ToLower(f.FuzzyText)
		filtered := make([]*animetosho.Release, 0, len(rs))
		for _, r := range rs {
			if fuzzyMatch(strings.ToLower(r.Entry.Title), needle) {
				filtered = append(filtered, r)
			}
		}
		rs = filtered
	}
	return ui.SortedReleases(rs, f.Sort)
}

// matchResolution reports whether a release resolution string matches the
// requested quality (e.g. "1080p" matches "1920x1080").
func matchResolution(res, quality string) bool {
	if res == "" {
		return false
	}
	// Quality values are like "1080p"; resolutions like "1920x1080".
	// Match on the height suffix.
	height := strings.TrimSuffix(quality, "p")
	return strings.HasSuffix(res, "x"+height) || res == height+"p"
}

// fuzzyMatch is a simple ordered-subsequence match: every rune of needle must
// appear in haystack in order. Good enough for short release titles and keeps
// the TUI dependency-free.
func fuzzyMatch(haystack, needle string) bool {
	h := []rune(haystack)
	n := []rune(needle)
	j := 0
	for i := 0; i < len(h) && j < len(n); i++ {
		if h[i] == n[j] {
			j++
		}
	}
	return j == len(n)
}

// DistinctGroups returns the ordered, de-duplicated set of non-empty release
// groups present in all (insertion order). Used by the group-filter overlay.
func DistinctGroups(all []*animetosho.Release) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, r := range all {
		g := r.Group
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	return out
}

// DistinctQualities returns the resolution qualities present in all, ordered
// from highest to lowest ("2160p" → "480p"). Only heights that appear are
// returned; used by the quality-filter overlay so it never offers an empty
// option.
func DistinctQualities(all []*animetosho.Release) []string {
	want := []string{"2160p", "1080p", "720p", "480p"}
	present := map[string]bool{}
	for _, r := range all {
		h := resolutionHeight(r.Resolution)
		if h == "" {
			continue
		}
		q := h + "p"
		for _, w := range want {
			if q == w {
				present[q] = true
				break
			}
		}
	}
	out := make([]string, 0, len(want))
	for _, q := range want {
		if present[q] {
			out = append(out, q)
		}
	}
	return out
}

// resolutionHeight extracts the height token ("1080" from "1920x1080" or
// "1080p"); "" if none/unrecognized.
func resolutionHeight(res string) string {
	res = strings.TrimSpace(res)
	if res == "" {
		return ""
	}
	if strings.HasSuffix(res, "p") {
		h := strings.TrimSuffix(res, "p")
		if isDigits(h) {
			return h
		}
	}
	if i := strings.IndexByte(res, 'x'); i >= 0 {
		h := res[i+1:]
		if isDigits(h) {
			return h
		}
	}
	return ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// DefaultQuality returns the highest available quality in all, or "" if none
// recognized. Used as the release picker's initial quality filter.
func DefaultQuality(all []*animetosho.Release) string {
	if qs := DistinctQualities(all); len(qs) > 0 {
		return qs[0]
	}
	return ""
}

// DefaultEpisode returns the next-unwatched episode for an anime item, or 0
// (all) if the series is finished. When the total is unknown we can't tell if
// we're done, so default to the next episode (watched+1) rather than "all".
func DefaultEpisode(watchedEps, totalEps int) int {
	next := watchedEps + 1
	if totalEps > 0 && next > totalEps {
		return 0 // reached/passed the known total → "all"
	}
	return next
}

// ---- filter overlay ----

// overlayKind identifies which filter overlay (if any) is active.
type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayGroup
	overlayQuality
	overlayEpisode
	overlaySort
	overlayActions
)

// filterOverlay is a selectable pop-up used by the group/quality filters and a
// text-input pop-up for the episode filter. When its kind != overlayNone it
// captures all key input in the release picker.
type filterOverlay struct {
	kind        overlayKind
	items       []string // selectable options (group / quality overlays)
	cursor      int
	text        string // episode input
	prevEpisode int     // episode before the overlay opened; Esc restores it (cancel)
}

// active reports whether the overlay is currently capturing input.
func (o *filterOverlay) active() bool { return o.kind != overlayNone }

// openGroup builds the group overlay item list: "All" first, then each group.
func (o *filterOverlay) openGroup(groups []string, current string) {
	items := make([]string, 0, len(groups)+1)
	items = append(items, "All")
	items = append(items, groups...)
	o.kind = overlayGroup
	o.items = items
	o.text = ""
	o.cursor = 0
	for i, g := range items {
		if (g == "All" && current == "") || g == current {
			o.cursor = i
			break
		}
	}
}

// openQuality builds the quality overlay: "All" + every present quality
// (highest first). current selects the cursor; "" maps to "All".
func (o *filterOverlay) openQuality(qualities []string, current string) {
	items := make([]string, 0, len(qualities)+1)
	items = append(items, "All")
	items = append(items, qualities...)
	o.kind = overlayQuality
	o.items = items
	o.text = ""
	o.cursor = 0
	for i, q := range items {
		if (q == "All" && current == "") || q == current {
			o.cursor = i
			break
		}
	}
}

// openEpisode starts the episode text-input overlay, seeded with the current
// episode value (rendered as "" when it's 0/all). The current value is also
// saved as prevEpisode so Esc can cancel (restore) instead of clearing.
func (o *filterOverlay) openEpisode(current int) {
	o.kind = overlayEpisode
	o.items = nil
	o.cursor = 0
	o.prevEpisode = current
	if current > 0 {
		o.text = strconv.Itoa(current)
	} else {
		o.text = ""
	}
}

// close deactivates the overlay without applying anything.
func (o *filterOverlay) close() {
	o.kind = overlayNone
	o.items = nil
	o.text = ""
	o.cursor = 0
}

// move adjusts the cursor by delta, clamped to the item range.
func (o *filterOverlay) move(delta int) {
	n := len(o.items)
	if n == 0 {
		return
	}
	o.cursor = (o.cursor + delta) % n
	if o.cursor < 0 {
		o.cursor += n
	}
}

// selectedItem returns the highlighted item (for group/quality/sort overlays).
func (o *filterOverlay) selectedItem() string {
	if o.kind == overlayGroup || o.kind == overlayQuality || o.kind == overlaySort {
		if o.cursor >= 0 && o.cursor < len(o.items) {
			return o.items[o.cursor]
		}
	}
	return ""
}

// openSort opens the sort overlay (Newest/Oldest/Smallest/Largest), cursor on
// the current sort's label.
func (o *filterOverlay) openSort(currentValue string) {
	o.kind = overlaySort
	o.items = releaseSortLabels()
	o.text = ""
	o.cursor = 0
	cur := releaseSortLabel(currentValue)
	for i, it := range o.items {
		if it == cur {
			o.cursor = i
			break
		}
	}
}

// releaseActions is the fixed actions-menu item list.
var releaseActions = []string{"Play", "Download", "Copy Magnet URL"}

// openActions opens the per-release actions menu (Play / Download / Copy Magnet).
func (o *filterOverlay) openActions() {
	o.kind = overlayActions
	o.items = releaseActions
	o.text = ""
	o.cursor = 0
}

// applySelected writes the overlay's selection back into the filter. For group
// and quality, "All" maps to "". Episode parses the typed text (empty = all).
// Sort maps the chosen label to its value.
func (o *filterOverlay) applySelected(f *Filter) {
	switch o.kind {
	case overlayGroup:
		if s := o.selectedItem(); s == "All" || s == "" {
			f.Group = ""
		} else {
			f.Group = s
		}
	case overlayQuality:
		if s := o.selectedItem(); s == "All" || s == "" {
			f.Quality = ""
		} else {
			f.Quality = s
		}
	case overlayEpisode:
		if o.text == "" {
			f.Episode = 0
		} else if n, err := strconv.Atoi(o.text); err == nil {
			f.SetEpisode(n)
		}
	case overlaySort:
		if s := o.selectedItem(); s != "" {
			if v, ok := releaseSortValue(s); ok {
				f.Sort = v
			}
		}
	}
}

// handleEpisodeKey mutates the episode overlay's text for a key. Returns true
// if the key was consumed.
func (o *filterOverlay) handleEpisodeKey(key string) {
	switch key {
	case "backspace":
		if len(o.text) > 0 {
			r := []rune(o.text)
			o.text = string(r[:len(r)-1])
		}
	}
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		o.text += key
	}
}
