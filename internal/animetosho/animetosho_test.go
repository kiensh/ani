package animetosho

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// withToshoServer serves body for every request and points toshoBase at the
// server. Returns a cleanup that restores toshoBase.
func withToshoServer(t *testing.T, body string, status int) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			http.Error(w, body, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	old := toshoBase
	toshoBase = srv.URL
	return func() {
		srv.Close()
		toshoBase = old
	}
}

// toshoBody builds a seriesDetailResponse body: each episode maps to the release
// groups that released it (a group listed twice = two resolutions, which must
// count as one distinct group).
func toshoBody(eps map[int][]string) string {
	var b strings.Builder
	b.WriteString(`{"data":{"releases":[`)
	first := true
	for ep, groups := range eps {
		for _, g := range groups {
			if !first {
				b.WriteString(",")
			}
			first = false
			fmt.Fprintf(&b, `{"release_group":%q,"series":{"episode_number":%d}}`, g, ep)
		}
	}
	b.WriteString(`]}}`)
	return b.String()
}

// TestLatestEpisodeAgreement: the highest episode with ≥ minGroups distinct
// groups wins.
func TestLatestEpisodeAgreement(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		5: {"Erai-raws", "SubsPlease", "ASW", "Judas"},
		4: {"Erai-raws", "SubsPlease", "ASW"},
	}), 0)()

	if got := LatestEpisode(123); got != 5 {
		t.Errorf("LatestEpisode = %d, want 5", got)
	}
}

// TestLatestEpisodeCumulative: Re:Zero-style — ep 11 has many groups, ep 77
// (numbered across seasons) has only 2 distinct groups (SubsPlease's 3
// resolutions + ASW collapse to 2). 77 must not win.
func TestLatestEpisodeCumulative(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		11: {"Erai-raws", "SubsPlease", "ASW", "Judas"},
		77: {"SubsPlease", "SubsPlease", "SubsPlease", "ASW"},
	}), 0)()

	if got := LatestEpisode(19242); got != 11 {
		t.Errorf("LatestEpisode (cumulative) = %d, want 11 (not 77)", got)
	}
}

// TestLatestEpisodePreview: Super-no-Ura-style — ep 1 has many groups; the
// preview/pre-release eps (5/4/3) come from one group each and must not win.
func TestLatestEpisodePreview(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		1: {"Erai-raws", "SubsPlease", "ASW", "Judas", "ToonsHub"},
		5: {"FrixySubs"},
		4: {"FrixySubs"},
		3: {"FrixySubs"},
	}), 0)()

	if got := LatestEpisode(19479); got != 1 {
		t.Errorf("LatestEpisode (preview) = %d, want 1 (not 5)", got)
	}
}

// TestLatestEpisodeLong: 4-digit episodes (One Piece 1168) aren't truncated —
// no title regex is involved.
func TestLatestEpisodeLong(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		1168: {"Erai-raws", "SubsPlease", "Judas", "VARYG"},
		1161: {"Erai-raws", "SubsPlease", "Judas"},
	}), 0)()

	if got := LatestEpisode(69); got != 1168 {
		t.Errorf("LatestEpisode (long show) = %d, want 1168 (no truncation)", got)
	}
}

// TestLatestEpisodeLowAgreement: no episode reaches minGroups → 0 (caller falls
// back to Jikan).
func TestLatestEpisodeLowAgreement(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		5: {"NicheGrp", "OtherGrp"},
		4: {"NicheGrp"},
	}), 0)()

	if got := LatestEpisode(123); got != 0 {
		t.Errorf("LatestEpisode (low agreement) = %d, want 0 (→ Jikan)", got)
	}
}

func TestLatestEpisodeEmpty(t *testing.T) {
	defer withToshoServer(t, `{"data":{"title":"X","releases":[]}}`, 0)()
	if got := LatestEpisode(123); got != 0 {
		t.Errorf("LatestEpisode (no releases) = %d, want 0", got)
	}
}

func TestLatestEpisodeError(t *testing.T) {
	defer withToshoServer(t, "boom", http.StatusInternalServerError)()
	if got := LatestEpisode(123); got != 0 {
		t.Errorf("LatestEpisode (HTTP 500) = %d, want 0", got)
	}
}

// TestLatestEpisodeRealRezero hits the live AnimeTosho feed for Re:Zero S4
// (aid 19242, total 19 eps) and asserts the aired count is within the season
// (≤ 19) — i.e. the cumulative-numbering bug (77) is gone. Skipped unless
// ANI_INTEGRATION=1.
func TestLatestEpisodeRealRezero(t *testing.T) {
	if os.Getenv("ANI_INTEGRATION") == "" {
		t.Skip("skipping network integration test; set ANI_INTEGRATION=1 to run")
	}
	got := LatestEpisode(19242)
	if got <= 0 || got > 19 {
		t.Errorf("LatestEpisode(19242 Re:Zero S4) = %d, want 1..19 (total 19); cumulative bug?", got)
	}
	t.Logf("Re:Zero S4 latest episode via real feed: %d", got)
}

// TestLatestEpisodeReal100nin hits the live feed for 100-nin Kanojo S3
// (aid 19663) — the mixed-numbering case: the same aired episode is released as
// per-season "2" (6 groups) and cumulative "26" (3 groups) on the same day.
// Asserts the per-season number wins (not a cumulative 25+). Skipped unless
// ANI_INTEGRATION=1.
func TestLatestEpisodeReal100nin(t *testing.T) {
	if os.Getenv("ANI_INTEGRATION") == "" {
		t.Skip("skipping network integration test; set ANI_INTEGRATION=1 to run")
	}
	got := LatestEpisode(19663)
	// Season 3 is a single cour (≤ ~12 per-season eps); a cumulative number would
	// be 25+. A correct per-season result stays well under the cumulative floor.
	if got <= 0 || got >= 25 {
		t.Errorf("LatestEpisode(19663 100-nin S3) = %d, want a small per-season number (<25); cumulative bug?", got)
	}
	t.Logf("100-nin S3 latest episode via real feed: %d", got)
}

// TestLatestEpisodeRealSlimeS4 hits the live feed for Slime S4 (aid 18884) — the
// re-upload case: ep 12 was re-uploaded after ep 15 aired, and other old eps were
// re-upped too. Asserts the re-up doesn't win (≥ 15). Skipped unless
// ANI_INTEGRATION=1.
func TestLatestEpisodeRealSlimeS4(t *testing.T) {
	if os.Getenv("ANI_INTEGRATION") == "" {
		t.Skip("skipping network integration test; set ANI_INTEGRATION=1 to run")
	}
	got := LatestEpisode(18884)
	// Ep 15 has aired; the re-up of ep 12 must not drag the result down to 12.
	if got < 15 {
		t.Errorf("LatestEpisode(18884 Slime S4) = %d, want >= 15 (re-up of ep 12 shouldn't win)", got)
	}
	t.Logf("Slime S4 latest episode via real feed: %d", got)
}

// TestLatestEpisodeMixedNumbering: 100-nin Kanojo S3 shape — per-season "2"
// (many groups) and cumulative "26" (≥3 groups) for the same episodes. The big
// gap (2 → 25) splits the clusters; the per-season max (2) wins.
func TestLatestEpisodeMixedNumbering(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		1:  {"DKB", "Erai-raws", "Judas", "Onalrie", "ToonsHub", "Trix"},
		2:  {"DKB", "Erai-raws", "Judas", "Onalrie", "ToonsHub", "Trix"},
		25: {"ASW", "SubsPlease", "VARYG"},
		26: {"ASW", "SubsPlease", "VARYG"},
	}), 0)()
	if got := LatestEpisode(19663); got != 2 {
		t.Errorf("LatestEpisode (mixed numbering) = %d, want 2 (per-season, not cumulative 26)", got)
	}
}

// TestLatestEpisodeCumulativeCluster: a high cumulative cluster (26) sits alongside
// the per-season run (1–5). The gap (5 → 26) splits them; the per-season max (5)
// wins, not the global max (26).
func TestLatestEpisodeCumulativeCluster(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		1:  {"A", "B", "C"},
		2:  {"A", "B", "C"},
		3:  {"A", "B", "C"},
		4:  {"A", "B", "C"},
		5:  {"A", "B", "C"},
		26: {"ASW", "SubsPlease", "VARYG"}, // cumulative outlier
	}), 0)()
	if got := LatestEpisode(19663); got != 5 {
		t.Errorf("LatestEpisode (cumulative cluster) = %d, want 5 (per-season max, not 26)", got)
	}
}

// TestLatestEpisodeCumulativeOnly: only cumulative numbering (25, 26) reaches
// minGroups. With no per-season cluster there's no gap, so the walk returns the
// cumulative max (26). Documents the limitation (can't recover per-season from a
// cumulative-only feed).
func TestLatestEpisodeCumulativeOnly(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		25: {"ASW", "SubsPlease", "VARYG"},
		26: {"ASW", "SubsPlease", "VARYG"},
	}), 0)()
	if got := LatestEpisode(19663); got != 26 {
		t.Errorf("LatestEpisode (cumulative only) = %d, want 26 (limitation)", got)
	}
}

// TestLatestEpisodeReupOfOldEpisode: Slime S4 shape — ep 12 was re-uploaded many
// times (12 distinct groups from originals + re-ups), 13/14 a handful, 15 only 3
// groups. The real latest is 15: re-ups inflate an old episode's group count but
// add no new episode number, so the contiguous run's max still wins. Guards the
// "ep 12 re-up looks newest" bug.
func TestLatestEpisodeReupOfOldEpisode(t *testing.T) {
	defer withToshoServer(t, toshoBody(map[int][]string{
		12: {"ASW", "Asakura", "DKB", "Erai-raws", "FoundYears", "FrixySubs", "Ironclad", "Judas", "ToonsHub", "Tsundere-Raws", "VARYG", "Yameii"},
		13: {"ASW", "DKB", "Erai-raws", "SubsPlease"},
		14: {"ASW", "DKB", "Erai-raws", "SubsPlease"},
		15: {"ASW", "DKB", "Erai-raws"}, // real latest, fewest groups
	}), 0)()
	if got := LatestEpisode(18884); got != 15 {
		t.Errorf("LatestEpisode (re-up of old ep) = %d, want 15 (re-ups must not win)", got)
	}
}
