package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"ani/internal/mal"
)

// PreviewRelease is the internal subcommand backing the fzf --preview pane for
// releases: reads the temp TSV (same lines fed to fzf) and prints the full
// title + concise detail + magnet for the line at index, word-wrapped.
func PreviewRelease(file, indexStr string) {
	idx, err := strconv.Atoi(indexStr)
	if err != nil || idx < 0 {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if idx >= len(lines) {
		return
	}
	f := strings.SplitN(lines[idx], "\t", 4) // [index, concise, magnet, title]
	if len(f) < 3 {
		return
	}
	title := "(no title)"
	if len(f) >= 4 && f[3] != "" {
		title = f[3]
	}
	width := atoiSafe(os.Getenv("FZF_PREVIEW_COLUMNS"))
	if width <= 0 {
		width = 40
	}
	cprint("\033[1;36m", title, width)
	cprint("\033[33m", f[1], width)
	if m := f[2]; m != "" {
		if len([]rune(m)) > 56 {
			m = string([]rune(m)[:56]) + "…"
		}
		cprint("\033[2m", "Magnet: "+m, width)
	}
}

// PreviewAnime is the internal subcommand backing the fzf --preview pane for the
// anime picker: renders the cover image (kitty) then info text below.
func PreviewAnime(tmpfile, malIDStr string) {
	data, err := os.ReadFile(tmpfile)
	if err != nil {
		return
	}
	var items []mal.Item
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}
	malID, _ := strconv.Atoi(malIDStr)
	var m *mal.Item
	for i := range items {
		if items[i].MalID == malID {
			m = &items[i]
			break
		}
	}
	if m == nil {
		return
	}

	// Cover image (top ~40% of the preview pane).
	cols, lines := atoiSafe(os.Getenv("FZF_PREVIEW_COLUMNS")), atoiSafe(os.Getenv("FZF_PREVIEW_LINES"))
	if cols <= 0 {
		cols = 40
	}
	if lines <= 0 {
		lines = 20
	}
	if m.CoverURL != "" {
		imageRows := lines * 40 / 100
		if imageRows < 4 {
			imageRows = 4
		}
		if imageRows > 14 {
			imageRows = 14
		}
		cmd := exec.Command("kitten", "icat", "--clear", "--transfer-mode=memory",
			"--stdin=no", "--scale-up",
			fmt.Sprintf("--place=%dx%d@0x0", cols, imageRows), m.CoverURL)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
		fmt.Println()
	}

	// Text info below the cover (each field colored for readability).
	cprint("\033[1;36m", m.Title, cols) // bold cyan: title

	progress := ""
	switch {
	case m.TotalEps > 0:
		progress = fmt.Sprintf("ep %d/%d", m.WatchedEps, m.TotalEps)
	default:
		progress = fmt.Sprintf("ep %d", m.WatchedEps)
	}
	if a := MALAirShort(m.AirStatus); a != "" {
		progress += "  [" + a + "]"
	}
	if m.ListStatus != "" {
		progress += "  —  " + MALListStatusShort(m.ListStatus)
	} else if m.WatchedEps > 0 {
		progress += "  ·  Watching"
	}
	cprint("\033[33m", progress, cols) // yellow: progress + status

	if m.MeanScore > 0 {
		s := fmt.Sprintf("★ %.2f", m.MeanScore)
		if m.Score > 0 {
			s += fmt.Sprintf("   (your: %d)", m.Score)
		}
		cprint("\033[35m", s, cols) // magenta: score
	} else if m.Score > 0 {
		cprint("\033[35m", fmt.Sprintf("your score: %d", m.Score), cols)
	}

	if m.Genres != "" {
		cprint("\033[32m", "Genres: "+m.Genres, cols) // green
	}
	if m.Studios != "" {
		cprint("\033[34m", "Studios: "+m.Studios, cols) // blue
	}
	seasonType := ""
	if m.StartSeason != "" {
		seasonType = "Season: " + m.StartSeason
		if m.MediaType != "" {
			seasonType += "  (" + strings.ToUpper(m.MediaType) + ")"
		}
	} else if m.MediaType != "" {
		seasonType = "Type: " + strings.ToUpper(m.MediaType)
	}
	if seasonType != "" {
		cprint("\033[2m", seasonType, cols) // dim: season/type
	}

	if m.Rank > 0 || m.Members > 0 {
		parts := []string{}
		if m.Rank > 0 {
			parts = append(parts, fmt.Sprintf("Rank #%d", m.Rank))
		}
		if m.Members > 0 {
			parts = append(parts, fmt.Sprintf("%s members", HumanCount(m.Members)))
		}
		cprint("\033[2m", strings.Join(parts, "  "), cols) // dim: rank/members
	}
}
