package ui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// One shared reader so the menu picker, command loop, and action prompt don't
// each buffer ahead of each other on the same stdin.
var stdin = bufio.NewReader(os.Stdin)

// ReadLine reads one trimmed line from the shared stdin.
func ReadLine() (string, error) {
	line, err := stdin.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// PromptAction asks play or download; Enter defaults to play.
func PromptAction() (string, error) {
	for {
		fmt.Print("\n  [p] play in mpv   [d] download via aria2c  (default p): ")
		line, err := ReadLine()
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

// PickIndex presents items as a numbered list (or fzf when useFzf) and returns
// the chosen 0-based index.
func PickIndex(items []string, header, prompt string, useFzf bool) (int, error) {
	if len(items) == 0 {
		return 0, errors.New("nothing to select")
	}
	if useFzf && FzfAvailable() {
		if idx, err := pickFzf(items, header); err == nil {
			return idx, nil
		} else if errors.Is(err, ErrCancelled) {
			return 0, err
		}
	}
	return pickNumbered(items, header, prompt)
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
			return 0, ErrCancelled
		}
		return 0, err
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		return 0, ErrCancelled
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
		line, err := ReadLine()
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
