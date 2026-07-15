package mal

import (
	"fmt"
	"io"
	"os"
)

// debugLog is the always-on debug sink — a file set by main via SetDebugLog on
// every run. Defaults to io.Discard so tests and library use stay silent.
var debugLog io.Writer = io.Discard

// SetDebugLog sets the always-on debug log destination (called from main, which
// opens <configdir>/ani/debug.log).
func SetDebugLog(w io.Writer) { debugLog = w }

// dbg writes a debug line to the always-on log, and also to stderr when echoTerm
// is true (the per-call `debug` flag, driven by --debug). All DEBUG … lines go
// through this so they're always captured to the file and echoed to the terminal
// only under --debug (the TUI's alt screen hides stderr, so the file is the
// reliable record).
func dbg(echoTerm bool, format string, args ...any) {
	fmt.Fprintf(debugLog, format, args...)
	if echoTerm {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}
