// Package config loads ani's JSON config and .env credentials.
package config

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Config lives at $XDG_CONFIG_HOME/ani/config.json (falls back to
// ~/.config/ani/config.json on macOS via os.UserConfigDir). Missing file = defaults.
type Config struct {
	Group   string `json:"group"`   // default group pre-filter, "" = All
	Quality string `json:"quality"` // default quality filter, "" = All
	Sort    string `json:"sort"`    // default sort: newest|oldest|smallest|largest
	Player  string `json:"player"`  // streaming player, default mpv
	Dir     string `json:"dir"`     // default download dir, "" = cwd

	// AnidbOverrides maps a MAL anime id to a user-chosen AniDB id (set by the
	// manual animetosho-series fallback), so an anime resolved once by hand
	// resolves instantly ever after. malID → aid.
	AnidbOverrides map[int]int `json:"anidb_overrides"`
}

// Default returns the built-in default config.
func Default() Config {
	return Config{Sort: "newest", Player: "mpv"}
}

// configFile overrides the on-disk config path (used by tests); empty means
// derive it from os.UserConfigDir() like the MAL token.
var configFile = ""

func configPath() (string, error) {
	if configFile != "" {
		return configFile, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ani", "config.json"), nil
}

// Load reads the config file, applying defaults for missing fields.
func Load() Config {
	cfg := Default()
	p, err := configPath()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	if cfg.Player == "" {
		cfg.Player = "mpv"
	}
	if cfg.Sort == "" {
		cfg.Sort = "newest"
	}
	return cfg
}

// SaveFilters persists the user's release filter preferences to config.json.
func SaveFilters(group, quality, sort string) {
	cfg := Load()
	cfg.Group = group
	cfg.Quality = quality
	cfg.Sort = sort
	save(cfg)
}

// AnidbOverride returns a user-saved AniDB id for malID, if one was set.
func AnidbOverride(malID int) (int, bool) {
	cfg := Load()
	aid, ok := cfg.AnidbOverrides[malID]
	return aid, ok && aid > 0
}

// SaveAnidbOverride records a user-chosen AniDB id for a MAL anime id (from the
// manual animetosho-series fallback) so it resolves instantly next time.
func SaveAnidbOverride(malID, aid int) {
	cfg := Load()
	if cfg.AnidbOverrides == nil {
		cfg.AnidbOverrides = map[int]int{}
	}
	cfg.AnidbOverrides[malID] = aid
	save(cfg)
}

func save(cfg Config) {
	p, err := configPath()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o600)
}

// ---- .env loader (no external dependency) ----

// LoadDotenv reads KEY=VALUE lines from ./.env then ~/.config/ani/.env and sets
// any key not already in the environment.
func LoadDotenv() {
	for _, p := range DotenvPaths() {
		applyDotenv(p)
	}
}

// DotenvPaths returns the .env locations consulted by LoadDotenv, in order.
func DotenvPaths() []string {
	var paths []string
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".env"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "ani", ".env"))
	}
	return paths
}

func applyDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = DotenvUnquote(val)
		if key == "" {
			continue
		}
		if _, present := os.LookupEnv(key); !present {
			_ = os.Setenv(key, val)
		}
	}
}

// DotenvUnquote strips one layer of matching surrounding quotes.
func DotenvUnquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
