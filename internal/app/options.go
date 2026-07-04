package app

// Options holds the runtime knobs parsed from flags + config and threaded
// through every call that needs them (replaces the old debugMode/dryRunMode
// globals).
type Options struct {
	Debug  bool
	DryRun bool
	UseFzf bool

	Player string
	Dir    string
	Group  string
	Sort   string
	Status string
	Query  string
}
