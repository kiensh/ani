package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config lives at $XDG_CONFIG_HOME/ani/config.json (falls back to
// ~/.config/ani/config.json on macOS via os.UserConfigDir). Missing file = defaults.
type Config struct {
	Group  string `json:"group"`  // default group pre-filter, "" = All
	Sort   string `json:"sort"`   // default sort: newest|oldest|smallest|largest
	Player string `json:"player"` // streaming player, default mpv
	Dir    string `json:"dir"`    // default download dir, "" = cwd
}

func defaultConfig() Config {
	return Config{Sort: "newest", Player: "mpv"}
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ani", "config.json"), nil
}

func loadConfig() Config {
	cfg := defaultConfig()
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
