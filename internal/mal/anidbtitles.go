package mal

// AniDB title→aid resolution from AniDB's public anime-titles dump.
//
// AniDB publishes anime-titles.xml.gz (~2 MB, no auth): every anime as its own
// <anime aid="N"> element with multiple <title> children (main/official/synonym/
// short) in various languages. We download it once, distill it to a
// {normalized_title: aid} map, cache that map on disk next to the MAL token, and
// refresh it weekly — mirroring fribb.go. Lookups are then a fast local read with
// no network, so it works even during a Jikan/MyAnimeList outage for any anime
// Fribb's id-map doesn't yet cover.

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// anidbTitlesURL is the anime-titles dump source (overridable in tests).
	anidbTitlesURL = "https://anidb.net/api/anime-titles.xml.gz"
	// anidbTitlesCache overrides the on-disk cache path (used by tests); empty
	// means derive it from os.UserConfigDir() like the MAL token.
	anidbTitlesCache = ""
	// anidbTitlesMaxAge is how long the cached map is served before a refresh.
	anidbTitlesMaxAge = 7 * 24 * time.Hour
)

// AnidbAIDByTitle resolves an AniDB id from the anime's title via AniDB's public
// anime-titles dump (one-time ~2 MB download, cached at
// <configDir>/ani/anidb-titles.json, refreshed weekly). It first tries an exact
// normalized match, then — when startYear > 0 — a year-variant "base (YYYY)" to
// bridge MAL season naming ("… 2nd Season") onto AniDB's year-suffixed entries
// ("… (2026)", which has zero "2nd Season" variant). "(YYYY)" is a unique key in
// AniDB, so the year-variant is unambiguous. Returns (aid, true) on a hit;
// (0, false) otherwise — never a hard error, so the caller falls through.
func AnidbAIDByTitle(title string, startYear int, debug bool) (int, bool) {
	if strings.TrimSpace(title) == "" {
		return 0, false
	}
	m, err := anidbTitlesMap(debug)
	if err != nil || len(m) == 0 {
		if err != nil {
			dbg(debug, "DEBUG anidb-titles: %v\n", err)
		}
		return 0, false
	}
	if aid, ok := m[normalizeTitle(title)]; ok && aid > 0 {
		return aid, true
	}
	// Year-variant: strip the season suffix and look up "base (YYYY)".
	if startYear > 0 {
		if base := StripSeasonSuffix(title); base != "" && base != title {
			yv := normalizeTitle(fmt.Sprintf("%s (%d)", base, startYear))
			if aid, ok := m[yv]; ok && aid > 0 {
				return aid, true
			}
		}
	}
	return 0, false
}

// anidbTitlesMap returns the title→aid map, serving the on-disk cache when fresh
// and rebuilding (re-downloading) it when missing or stale. A failed rebuild
// falls back to a stale cache if present. Mirrors fribbMap.
func anidbTitlesMap(debug bool) (map[string]int, error) {
	p, err := anidbTitlesCachePath()
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(p)
	if statErr == nil && time.Since(info.ModTime()) < anidbTitlesMaxAge {
		if m, e := readAnidbTitlesCache(p); e == nil {
			return m, nil
		}
	}

	firstBuild := statErr != nil
	if firstBuild {
		fmt.Fprintln(os.Stderr, "Building AniDB title index (one-time, ~2 MB)…")
	} else {
		dbg(debug, "DEBUG anidb-titles: refreshing stale cache (>7d)…\n")
	}

	m, err := downloadAndBuildAnidbTitles(debug)
	if err != nil {
		if statErr == nil {
			if m2, e := readAnidbTitlesCache(p); e == nil {
				dbg(debug, "DEBUG anidb-titles: download failed (%v); serving stale cache (%d entries)\n", err, len(m2))
				return m2, nil
			}
		}
		return nil, err
	}

	if werr := writeAnidbTitlesCache(p, m); werr != nil {
		dbg(debug, "DEBUG anidb-titles: cache write failed (%v)\n", werr)
	}
	dbg(debug, "DEBUG anidb-titles: built map with %d titles\n", len(m))
	return m, nil
}

// anidbTitlesCachePath returns the cache location, mirroring malTokenPath()
// (os.UserConfigDir()/ani/…). It honors anidbTitlesCache for tests.
func anidbTitlesCachePath() (string, error) {
	if anidbTitlesCache != "" {
		return anidbTitlesCache, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ani", "anidb-titles.json"), nil
}

// downloadAndBuildAnidbTitles fetches the gzipped anime-titles dump and distills
// it to a {normalized_title: aid} map, indexing every title except type=="short"
// (shorts like "CB"/"SnM" are ambiguous collisions).
func downloadAndBuildAnidbTitles(debug bool) (map[string]int, error) {
	dbg(debug, "DEBUG anidb-titles GET %s\n", anidbTitlesURL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anidbTitlesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ani/0.1 (+https://animetosho.xyz)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anidb-titles: HTTP %s", resp.Status)
	}

	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anidb-titles: gunzip: %w", err)
	}
	defer zr.Close()

	m := make(map[string]int, 140000)
	dec := xml.NewDecoder(zr)
	currentAID := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("anidb-titles: parse: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "anime":
				currentAID, _ = strconv.Atoi(attrValue(t.Attr, "aid"))
			case "title":
				if currentAID <= 0 {
					continue
				}
				if attrValue(t.Attr, "type") == "short" {
					continue
				}
				// The title text is the next token (CharData inline after the
				// start tag). Read it; the matching </title> end tag is picked up
				// by the next loop iteration and ignored.
				next, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("anidb-titles: parse: %w", err)
				}
				if cd, ok := next.(xml.CharData); ok {
					if s := normalizeTitle(string(cd)); s != "" {
						m[s] = currentAID
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "anime" {
				currentAID = 0
			}
		}
	}
	return m, nil
}

// attrValue returns the value of the first attribute named local, else "".
func attrValue(attrs []xml.Attr, local string) string {
	for _, a := range attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// normalizeTitle lowercases, trims, and collapses internal whitespace runs to a
// single space. Punctuation/digits are kept ("3×3 Eyes" must survive).
func normalizeTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// StartYear returns the 4-digit start year from item.StartDate ("YYYY-MM-DD"),
// or 0 if absent/unparseable. Used to build the year-variant lookup key.
func StartYear(item *Item) int {
	if item == nil || len(item.StartDate) < 4 {
		return 0
	}
	y, err := strconv.Atoi(item.StartDate[:4])
	if err != nil || y < 1900 || y > 2100 {
		return 0
	}
	return y
}

// seasonSuffixRes matches trailing season/part markers MAL uses that AniDB often
// doesn't ("… 2nd Season", "… Season 2", "… Part 2").
var seasonSuffixRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\s+\d+(?:st|nd|rd|th)\s+Season$`),
	regexp.MustCompile(`(?i)\s+Season\s+\d+$`),
	regexp.MustCompile(`(?i)\s+Part\s+\d+$`),
}

// StripSeasonSuffix removes trailing season/part markers from title, repeating
// until none remain so e.g. "Foo Season 2 Part 2" → "Foo" (not "Foo Season 2").
func StripSeasonSuffix(title string) string {
	for {
		next := strings.TrimSpace(title)
		for _, re := range seasonSuffixRes {
			next = re.ReplaceAllString(next, "")
		}
		next = strings.TrimSpace(next)
		if next == title {
			return next
		}
		title = next
	}
}

func readAnidbTitlesCache(path string) (map[string]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]int
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("anidb-titles: parse cache: %w", err)
	}
	return m, nil
}

func writeAnidbTitlesCache(path string, m map[string]int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
