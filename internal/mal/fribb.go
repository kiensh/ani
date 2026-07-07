package mal

// Fribb-based MAL→AniDB resolution, independent of Jikan/MAL uptime.
//
// anime-lists (Fribb) publishes a flat JSON cross-platform ID map where each
// entry carries mal_id and anidb_id (among ~14 platforms). We download the full
// file once (~7 MB), distill it to a {mal_id: anidb_id} map, cache that tiny map
// on disk next to the MAL token, and refresh it weekly. Lookups are then a fast
// local read with no network — so a Jikan/MyAnimeList outage can't block AniDB
// resolution for the ~15.7k anime the map covers.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

var (
	// fribbFullURL is the anime-list-full.json source (overridable in tests).
	fribbFullURL = "https://raw.githubusercontent.com/Fribb/anime-lists/master/anime-list-full.json"
	// fribbCacheFile overrides the on-disk cache path (used by tests); empty
	// means derive it from os.UserConfigDir() like the MAL token.
	fribbCacheFile = ""
	// fribbMaxAge is how long the cached map is served before a refresh.
	fribbMaxAge = 7 * 24 * time.Hour
)

// AnidbAIDViaFribb resolves the AniDB id for a MAL anime from the Fribb
// anime-list offline mapping (one-time ~7 MB download, cached at
// <configDir>/ani/fribb-anidb.json, refreshed weekly). Returns (aid, true) on a
// known mapping; (0, false) if the anime isn't mapped or the map couldn't be
// built — it never surfaces a hard error, so the caller can simply fall through
// to the next resolver. On a download failure it serves a stale cache if one
// exists.
func AnidbAIDViaFribb(malID int, debug bool) (int, bool) {
	m, err := fribbMap(debug)
	if err != nil || len(m) == 0 {
		if debug && err != nil {
			fmt.Fprintf(os.Stderr, "DEBUG fribb: %v\n", err)
		}
		return 0, false
	}
	aid, ok := m[strconv.Itoa(malID)]
	if !ok || aid <= 0 {
		return 0, false
	}
	return aid, true
}

// fribbMap returns the mal→anidb map, serving the on-disk cache when fresh and
// rebuilding (re-downloading) it when missing or stale. A failed rebuild falls
// back to a stale cache if present.
func fribbMap(debug bool) (map[string]int, error) {
	p, err := fribbCachePath()
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(p)
	if statErr == nil && time.Since(info.ModTime()) < fribbMaxAge {
		if m, e := readFribbCache(p); e == nil {
			return m, nil
		}
	}

	firstBuild := statErr != nil
	if firstBuild {
		fmt.Fprintln(os.Stderr, "Building AniDB mapping from Fribb (one-time, ~7 MB)…")
	} else if debug {
		fmt.Fprintln(os.Stderr, "DEBUG fribb: refreshing stale cache (>7d)…")
	}

	m, err := downloadAndBuildFribb(debug)
	if err != nil {
		// Fall back to whatever stale cache we have rather than failing hard.
		if statErr == nil {
			if m2, e := readFribbCache(p); e == nil {
				if debug {
					fmt.Fprintf(os.Stderr, "DEBUG fribb: download failed (%v); serving stale cache (%d entries)\n", err, len(m2))
				}
				return m2, nil
			}
		}
		return nil, err
	}

	if werr := writeFribbCache(p, m); werr != nil && debug {
		fmt.Fprintf(os.Stderr, "DEBUG fribb: cache write failed (%v)\n", werr)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG fribb: built map with %d MAL→AniDB pairs\n", len(m))
	}
	return m, nil
}

// fribbCachePath returns the cache location, mirroring malTokenPath()
// (os.UserConfigDir()/ani/…). It honors fribbCacheFile for tests.
func fribbCachePath() (string, error) {
	if fribbCacheFile != "" {
		return fribbCacheFile, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ani", "fribb-anidb.json"), nil
}

// downloadAndBuildFribb fetches the full anime-list JSON and distills it to a
// {mal_id: anidb_id} map, skipping entries lacking either id.
func downloadAndBuildFribb(debug bool) (map[string]int, error) {
	if debug {
		fmt.Fprintf(os.Stderr, "DEBUG fribb GET %s\n", fribbFullURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fribbFullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ani/0.1 (+https://animetosho.xyz)")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fribb: HTTP %s", resp.Status)
	}

	var arr []struct {
		MalID   int `json:"mal_id"`
		AnidbID int `json:"anidb_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return nil, fmt.Errorf("fribb: decode: %w", err)
	}

	m := make(map[string]int, 16000)
	for _, e := range arr {
		if e.MalID > 0 && e.AnidbID > 0 {
			m[strconv.Itoa(e.MalID)] = e.AnidbID
		}
	}
	return m, nil
}

func readFribbCache(path string) (map[string]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]int
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("fribb: parse cache: %w", err)
	}
	return m, nil
}

func writeFribbCache(path string, m map[string]int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
