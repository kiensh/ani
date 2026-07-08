package app

// Options holds the runtime knobs parsed from the (hidden) flags + config and
// threaded through every call that needs them.
type Options struct {
	Debug  bool
	DryRun bool

	Player  string // streaming player (mpv default)
	Dir     string // download directory (default cwd)
	Group   string // release-group pre-filter (in-TUI; persisted to config)
	Quality string // quality filter (from config)
	Sort    string // newest | oldest | smallest | largest
	Query   string // positional search argument (name or anidb id)
}
