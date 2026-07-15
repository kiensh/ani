package mal

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// TestDbgFileAlways asserts every dbg line goes to the log sink regardless of
// the terminal-echo flag.
func TestDbgFileAlways(t *testing.T) {
	var buf bytes.Buffer
	SetDebugLog(&buf)
	defer SetDebugLog(io.Discard) // restore for other tests

	dbg(false, "DEBUG a=%d\n", 1)
	dbg(true, "DEBUG b=%d\n", 2)

	if !strings.Contains(buf.String(), "DEBUG a=1") || !strings.Contains(buf.String(), "DEBUG b=2") {
		t.Errorf("dbg file log = %q; want both DEBUG lines (file is always written)", buf.String())
	}
}

// TestDbgTerminalOnlyWhenFlag asserts dbg writes to stderr only when echoTerm.
func TestDbgTerminalOnlyWhenFlag(t *testing.T) {
	var buf bytes.Buffer
	SetDebugLog(&buf)
	defer SetDebugLog(io.Discard)

	// Capture stderr via a pipe.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	dbg(true, "DEBUG yes=%d\n", 1)
	dbg(false, "DEBUG no=%d\n", 2)
	w.Close()
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "DEBUG yes=1") {
		t.Errorf("echoTerm=true must write to stderr; stderr=%q", string(out))
	}
	if strings.Contains(string(out), "DEBUG no=2") {
		t.Errorf("echoTerm=false must NOT write to stderr; stderr=%q", string(out))
	}
}
