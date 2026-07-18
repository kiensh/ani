package mal

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// anidbTitlesSample mirrors the real "Seihantai na Kimi to Boku" entries in
// AniDB's dump: season 1 (aid 19010) and the 2026 sequel (aid 19983), the latter
// titled "(2026)" with zero "2nd Season" variant — the mismatch we bridge.
const anidbTitlesSample = `<?xml version="1.0" encoding="UTF-8"?>
<animetitles>
	<anime aid="19010">
		<title type="main" xml:lang="x-jat">Seihantai na Kimi to Boku</title>
		<title type="official" xml:lang="en">You and I Are Polar Opposites</title>
		<title type="short" xml:lang="en">SatKtB</title>
	</anime>
	<anime aid="19983">
		<title type="main" xml:lang="x-jat">Seihantai na Kimi to Boku (2026)</title>
		<title type="official" xml:lang="en">You and I Are Polar Opposites (2026)</title>
	</anime>
	<anime aid="55555">
		<title type="main" xml:lang="x-jat">Hana-darake na Show</title>
	</anime>
	<anime aid="66666">
		<title type="main" xml:lang="x-jat">Saijo no Osewa wo Kagenagara Suru Koto ni Narimashita</title>
	</anime>
	<anime aid="77777">
		<title type="main" xml:lang="x-jat">Akira</title>
	</anime>
</animetitles>`

func gzipString(s string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(s))
	zw.Close()
	return buf.Bytes()
}

// setupAnidbTitlesServer serves gzipped xml at anidbTitlesURL and isolates the
// cache to a temp file. Returns a pointer to the server's hit count and a
// cleanup func that restores the package vars.
func setupAnidbTitlesServer(t *testing.T, xml string) (*int, func()) {
	t.Helper()
	gz := gzipString(xml)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(gz)
	}))
	oldURL, oldCache := anidbTitlesURL, anidbTitlesCache
	anidbTitlesURL = srv.URL
	anidbTitlesCache = filepath.Join(t.TempDir(), "anidb-titles.json")
	return &hits, func() {
		srv.Close()
		anidbTitlesURL = oldURL
		anidbTitlesCache = oldCache
	}
}

// TestAnidbAIDByTitle covers the user's case: the season/year mismatch.
func TestAnidbAIDByTitle(t *testing.T) {
	_, cleanup := setupAnidbTitlesServer(t, anidbTitlesSample)
	defer cleanup()

	tests := []struct {
		name      string
		title     string
		startYear int
		wantAid   int
		wantOK    bool
	}{
		{"season 1 exact", "Seihantai na Kimi to Boku", 0, 19010, true},
		{"sequel via year-variant", "Seihantai na Kimi to Boku 2nd Season", 2026, 19983, true},
		{"sequel without year cannot bridge", "Seihantai na Kimi to Boku 2nd Season", 0, 0, false},
		{"short title not indexed", "SatKtB", 0, 0, false},
		{"case/whitespace insensitive", "  SEIHANTAI  na KIMI to BOKU ", 0, 19010, true},
		{"absent title", "No Such Anime", 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			aid, ok := AnidbAIDByTitle(tc.title, tc.startYear, false)
			if aid != tc.wantAid || ok != tc.wantOK {
				t.Errorf("AnidbAIDByTitle(%q, %d) = (%d, %v), want (%d, %v)",
					tc.title, tc.startYear, aid, ok, tc.wantAid, tc.wantOK)
			}
		})
	}
}

// TestAnidbAIDByTitleCacheReused asserts the first lookup downloads + caches, and
// a second lookup is served from cache (no second download).
func TestAnidbAIDByTitleCacheReused(t *testing.T) {
	hits, cleanup := setupAnidbTitlesServer(t, anidbTitlesSample)
	defer cleanup()

	if _, ok := AnidbAIDByTitle("Seihantai na Kimi to Boku", 0, false); !ok {
		t.Fatal("first lookup: want (19010,true)")
	}
	if *hits != 1 {
		t.Fatalf("after first lookup: server hits = %d, want 1", *hits)
	}
	// Different title, same cached map — must not re-download.
	if aid, ok := AnidbAIDByTitle("Seihantai na Kimi to Boku (2026)", 0, false); !ok || aid != 19983 {
		t.Errorf("second lookup = (%d, %v), want (19983, true)", aid, ok)
	}
	if *hits != 1 {
		t.Errorf("after second lookup: server hits = %d, want 1 (cache reused)", *hits)
	}
}

func TestStartYear(t *testing.T) {
	tests := []struct {
		start string
		want  int
	}{
		{"2026-07-05", 2026},
		{"2025", 2025},
		{"", 0},
		{"abc", 0},
		{"1899-01-01", 0}, // out of plausible range
	}
	for _, tc := range tests {
		got := StartYear(&Item{StartDate: tc.start})
		if got != tc.want {
			t.Errorf("StartYear(%q) = %d, want %d", tc.start, got, tc.want)
		}
	}
	if got := StartYear(nil); got != 0 {
		t.Errorf("StartYear(nil) = %d, want 0", got)
	}
}

func TestStripSeasonSuffix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Seihantai na Kimi to Boku 2nd Season", "Seihantai na Kimi to Boku"},
		{"Foo Season 2", "Foo"},
		{"Bar Part 3", "Bar"},
		{"No Suffix Here", "No Suffix Here"},
	}
	for _, tc := range tests {
		if got := StripSeasonSuffix(tc.in); got != tc.want {
			t.Errorf("StripSeasonSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	cases := map[string]string{
		"A: B-C!":         "abc",
		"Hana-darake":     "hanadarake",
		"Dai Dai Dai":     "daidaidai",
		"Foo (2026)":      "foo2026",
		"  SEIHANTAI  ":   "seihantai",
		"":                "",
	}
	for in, want := range cases {
		if got := normalizeTitle(in); got != want {
			t.Errorf("normalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAnidbAIDByTitleTolerant covers the MAL↔AniDB format-diff cases the offline
// dump should now bridge: hyphen differences (exact, via normalizeTitle) and small
// romanization diffs (fuzzy, long titles only).
func TestAnidbAIDByTitleTolerant(t *testing.T) {
	_, cleanup := setupAnidbTitlesServer(t, anidbTitlesSample)
	defer cleanup()

	// Hyphen diff: indexed "Hana-darake na Show", queried "Hanadarake na Show".
	if aid, ok := AnidbAIDByTitle("Hanadarake na Show", 0, false); !ok || aid != 55555 {
		t.Errorf("hyphen diff: AnidbAIDByTitle = (%d, %v), want (55555, true)", aid, ok)
	}

	// Fuzzy: indexed "...Osewa wo Kagenagara...", queried "...Osewa o Kagenagara..."
	// (the を particle wo→o); long title, edit distance 1.
	if aid, ok := AnidbAIDByTitle("Saijo no Osewa o Kagenagara Suru Koto ni Narimashita", 0, false); !ok || aid != 66666 {
		t.Errorf("fuzzy (wo/o): AnidbAIDByTitle = (%d, %v), want (66666, true)", aid, ok)
	}

	// Short title, 1-char diff: must NOT fuzzy-match (would be a false positive).
	// Indexed "Akira" (len 5 < 30); "Akura" is distance 1 but fuzzy is gated off.
	if aid, ok := AnidbAIDByTitle("Akura", 0, false); ok || aid != 0 {
		t.Errorf("short fuzzy: AnidbAIDByTitle = (%d, %v), want (0, false) — no false match", aid, ok)
	}
}

// TestAnidbAIDByTitleRealDump resolves the user's anime against the live AniDB
// dump (1.84 MB) — guards the real parser/token-walk end-to-end. Skipped unless
// ANI_INTEGRATION=1 so normal `go test` stays offline + fast.
func TestAnidbAIDByTitleRealDump(t *testing.T) {
	if os.Getenv("ANI_INTEGRATION") == "" {
		t.Skip("skipping network integration test; set ANI_INTEGRATION=1 to run")
	}
	oldURL, oldCache := anidbTitlesURL, anidbTitlesCache
	anidbTitlesURL = "https://anidb.net/api/anime-titles.xml.gz"
	anidbTitlesCache = filepath.Join(t.TempDir(), "anidb-titles.json")
	defer func() { anidbTitlesURL, anidbTitlesCache = oldURL, oldCache }()

	aid, ok := AnidbAIDByTitle("Seihantai na Kimi to Boku 2nd Season", 2026, true)
	if !ok || aid != 19983 {
		t.Fatalf(`real dump: "Seihantai na Kimi to Boku 2nd Season"+2026 = (%d, %v), want (19983, true)`, aid, ok)
	}
	t.Logf("resolved aid %d via the real AniDB dump", aid)
}

// TestAnidbAIDByTitleRealSaijo resolves the long-titled "Saijo no Osewa" anime
// (MAL title) against the real dump — it's indexed as aid 19690 with hyphen +
// "wo"/"o" differences, so this guards the concatenate + fuzzy path end-to-end.
// Skipped unless ANI_INTEGRATION=1.
func TestAnidbAIDByTitleRealSaijo(t *testing.T) {
	if os.Getenv("ANI_INTEGRATION") == "" {
		t.Skip("skipping network integration test; set ANI_INTEGRATION=1 to run")
	}
	oldURL, oldCache := anidbTitlesURL, anidbTitlesCache
	anidbTitlesURL = "https://anidb.net/api/anime-titles.xml.gz"
	anidbTitlesCache = filepath.Join(t.TempDir(), "anidb-titles.json")
	defer func() { anidbTitlesURL, anidbTitlesCache = oldURL, oldCache }()

	q := "Saijo no Osewa: Takane no Hanadarake na Meimonkou de, Gakuin Ichi no Ojousama (Seikatsu Nouryoku Kaimu) wo Kagenagara Osewa suru Koto ni Narimashita"
	aid, ok := AnidbAIDByTitle(q, 0, true)
	if !ok || aid != 19690 {
		t.Fatalf(`real dump: Saijo... = (%d, %v), want (19690, true)`, aid, ok)
	}
	t.Logf("resolved Saijo aid %d via the real AniDB dump (concatenate + fuzzy)", aid)
}