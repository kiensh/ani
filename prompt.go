package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

var errCancelled = errors.New("cancelled")

// One shared reader so the menu picker, command loop, and action prompt don't
// each buffer ahead of each other on the same stdin.
var stdin = bufio.NewReader(os.Stdin)

func readLine() (string, error) {
	line, err := stdin.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptAction asks play or download; Enter defaults to play.
func promptAction() (string, error) {
	for {
		fmt.Print("\n  [p] play in mpv   [d] download via aria2c  (default p): ")
		line, err := readLine()
		if err != nil {
			return "", err
		}
		switch strings.ToLower(line) {
		case "", "p", "play":
			return "play", nil
		case "d", "download", "dl":
			return "download", nil
		}
		fmt.Printf("  invalid: %q (p or d)\n", line)
	}
}

// pickIndex presents items as a numbered list (or fzf when available) and
// returns the chosen 0-based index.
func pickIndex(items []string, header, prompt string, useFzf bool) (int, error) {
	if len(items) == 0 {
		return 0, errors.New("nothing to select")
	}
	if useFzf && fzfAvailable() {
		if idx, err := pickFzf(items, header); err == nil {
			return idx, nil
		} else if errors.Is(err, errCancelled) {
			return 0, err
		}
	}
	return pickNumbered(items, header, prompt)
}

func fzfAvailable() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

func pickFzf(items []string, header string) (int, error) {
	var b strings.Builder
	for i, it := range items {
		fmt.Fprintf(&b, "%d\t%s\n", i, it)
	}
	cmd := exec.Command("fzf",
		"--ansi",
		"--delimiter=\t",
		"--with-nth=2..",
		"--header="+header,
		"--height=60%",
		"--reverse",
		"--prompt=❯ ",
	)
	cmd.Stdin = strings.NewReader(b.String())
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 130 {
			return 0, errCancelled
		}
		return 0, err
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		return 0, errCancelled
	}
	idxField := strings.SplitN(line, "\t", 2)[0]
	idx, err := strconv.Atoi(idxField)
	if err != nil {
		return 0, fmt.Errorf("could not parse fzf selection: %q", line)
	}
	if idx < 0 || idx >= len(items) {
		return 0, fmt.Errorf("fzf returned out-of-range index %d", idx)
	}
	return idx, nil
}

func pickNumbered(items []string, header, prompt string) (int, error) {
	fmt.Println()
	if header != "" {
		fmt.Println(header)
	}
	for i, it := range items {
		fmt.Printf("  %2d) %s\n", i+1, it)
	}
	for {
		fmt.Printf("\n%s [1-%d]: ", prompt, len(items))
		line, err := readLine()
		if err != nil {
			return 0, err
		}
		n, perr := strconv.Atoi(line)
		if perr == nil && n >= 1 && n <= len(items) {
			return n - 1, nil
		}
		fmt.Printf("  invalid choice: %q\n", line)
	}
}
