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
	Group  string `json:"group"`  // default group pre-filter, "" = All
	Sort   string `json:"sort"`   // default sort: newest|oldest|smallest|largest
	Player string `json:"player"` // streaming player, default mpv
	Dir    string `json:"dir"`    // default download dir, "" = cwd
}

// Default returns the built-in default config.
func Default() Config {
	return Config{Sort: "newest", Player: "mpv"}
}

func configPath() (string, error) {
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
