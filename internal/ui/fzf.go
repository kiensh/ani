package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrCancelled is returned when the user dismisses an fzf/numbered menu.
var ErrCancelled = errors.New("cancelled")

// FzfAvailable reports whether the fzf binary is on PATH.
func FzfAvailable() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

// RunFzf launches fzf with args, feeds input, returns the selected (full) line.
// When dryRun is true it prints the fzf command + input and auto-selects the
// first line (no fzf process is started).
func RunFzf(args []string, input string, dryRun bool) (string, error) {
	if dryRun {
		fmt.Fprintf(os.Stderr, "DEBUG fzf %s\n", ShellQuote(append([]string{"fzf"}, args...)))
		fmt.Fprintln(os.Stderr, "DEBUG input (tab-delimited):")
		fmt.Fprintln(os.Stderr, input)
		first := strings.SplitN(input, "\n", 2)[0]
		if first == "" {
			return "", ErrCancelled
		}
		return first, nil
	}
	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(input)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 130 {
			return "", ErrCancelled
		}
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// ShellQuote joins args into a single shell-quoted command line (each arg in
// single quotes, inner ' escaped) so printed commands are copy-paste-runnable.
func ShellQuote(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
}
