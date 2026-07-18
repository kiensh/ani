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

// datedRel is one release: its group and the date_added day ("YYYY-MM-DD"; ""
// omits date_added, like the older toshoBody helper).
type datedRel struct {
	group string
	day   string
}

// toshoBodyDated builds a seriesDetailResponse where each episode maps to dated
// releases — used to exercise LatestEpisode's same-day mixed-numbering
// detection, which toshoBody (no dates) cannot reach.
func toshoBodyDated(eps map[int][]datedRel) string {
	var b strings.Builder
	b.WriteString(`{"data":{"releases":[`)
	first := true
	for ep, rels := range eps {
		for _, r := range rels {
			if !first {
				b.WriteString(",")
			}
			first = false
			if r.day != "" {
				fmt.Fprintf(&b, `{"release_group":%q,"date_added":%q,"series":{"episode_number":%d}}`, r.group, r.day+"T00:00:00Z", ep)
			} else {
				fmt.Fprintf(&b, `{"release_group":%q,"series":{"episode_number":%d}}`, r.group, ep)
			}
		}
	}
	b.WriteString(`]}}`)
	return b.String()
}

// TestLatestEpisodeMixedNumbering: 100-nin Kanojo S3 shape — the same aired
// episode (S03E02) is released as per-season "2" (6 groups) and cumulative "26"
// (3 groups) on the same day. The per-season number must win.
func TestLatestEpisodeMixedNumbering(t *testing.T) {
	defer withToshoServer(t, toshoBodyDated(map[int][]datedRel{
		1:  {{"DKB", "2026-07-05"}, {"Erai-raws", "2026-07-05"}, {"Judas", "2026-07-05"}, {"Onalrie", "2026-07-05"}, {"ToonsHub", "2026-07-05"}, {"Trix", "2026-07-05"}},
		2:  {{"DKB", "2026-07-12"}, {"Erai-raws", "2026-07-12"}, {"Judas", "2026-07-12"}, {"Onalrie", "2026-07-12"}, {"ToonsHub", "2026-07-12"}, {"Trix", "2026-07-12"}},
		25: {{"ASW", "2026-07-05"}, {"SubsPlease", "2026-07-05"}, {"VARYG", "2026-07-05"}},
		26: {{"ASW", "2026-07-12"}, {"SubsPlease", "2026-07-12"}, {"VARYG", "2026-07-12"}},
	}), 0)()
	if got := LatestEpisode(19663); got != 2 {
		t.Errorf("LatestEpisode (mixed numbering) = %d, want 2", got)
	}
}

// TestLatestEpisodeSameDayDoubleEpisode: eps 11 and 12 both aired the same day —
// consecutive (spread 1), not mixed numbering → return the max (12).
func TestLatestEpisodeSameDayDoubleEpisode(t *testing.T) {
	defer withToshoServer(t, toshoBodyDated(map[int][]datedRel{
		11: {{"Erai-raws", "2026-07-12"}, {"SubsPlease", "2026-07-12"}, {"ASW", "2026-07-12"}},
		12: {{"Erai-raws", "2026-07-12"}, {"SubsPlease", "2026-07-12"}, {"ASW", "2026-07-12"}},
	}), 0)()
	if got := LatestEpisode(123); got != 12 {
		t.Errorf("LatestEpisode (same-day double ep) = %d, want 12", got)
	}
}

// TestLatestEpisodeCumulativeOnly: no per-season cluster — only cumulative
// numbering (25, 26) reaches minGroups. Unrecoverable from AnimeTosho alone;
// returns the cumulative max (26). Documents the limitation.
func TestLatestEpisodeCumulativeOnly(t *testing.T) {
	defer withToshoServer(t, toshoBodyDated(map[int][]datedRel{
		25: {{"ASW", "2026-07-05"}, {"SubsPlease", "2026-07-05"}, {"VARYG", "2026-07-05"}},
		26: {{"ASW", "2026-07-12"}, {"SubsPlease", "2026-07-12"}, {"VARYG", "2026-07-12"}},
	}), 0)()
	if got := LatestEpisode(19663); got != 26 {
		t.Errorf("LatestEpisode (cumulative only) = %d, want 26 (limitation)", got)
	}
}

// TestLatestEpisodeContiguousSeasonDated: a normal weekly 12-ep season — ep 12
// newest, the rest progressively older. No mixed numbering → 12.
func TestLatestEpisodeContiguousSeasonDated(t *testing.T) {
	eps := map[int][]datedRel{}
	for ep := 1; ep <= 12; ep++ {
		day := fmt.Sprintf("2026-01-%02d", ep)
		eps[ep] = []datedRel{{"Erai-raws", day}, {"SubsPlease", day}, {"ASW", day}}
	}
	defer withToshoServer(t, toshoBodyDated(eps), 0)()
	if got := LatestEpisode(123); got != 12 {
		t.Errorf("LatestEpisode (contiguous season) = %d, want 12", got)
	}
}

// TestLatestEpisodeLongDated: One Piece shape — recent eps on a weekly cadence;
// only the newest is inside dayWindow → return it (no false mixed split).
func TestLatestEpisodeLongDated(t *testing.T) {
	defer withToshoServer(t, toshoBodyDated(map[int][]datedRel{
		1165: {{"Erai-raws", "2026-06-21"}, {"SubsPlease", "2026-06-21"}, {"Judas", "2026-06-21"}},
		1166: {{"Erai-raws", "2026-06-28"}, {"SubsPlease", "2026-06-28"}, {"Judas", "2026-06-28"}},
		1167: {{"Erai-raws", "2026-07-05"}, {"SubsPlease", "2026-07-05"}, {"Judas", "2026-07-05"}},
		1168: {{"Erai-raws", "2026-07-12"}, {"SubsPlease", "2026-07-12"}, {"Judas", "2026-07-12"}},
	}), 0)()
	if got := LatestEpisode(69); got != 1168 {
		t.Errorf("LatestEpisode (long show, dated) = %d, want 1168", got)
	}
}
