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
