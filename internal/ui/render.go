package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ani/internal/animetosho"
	"ani/internal/mal"
)

// RenderMALLine renders the one-line summary used by the anime picker.
func RenderMALLine(m mal.Item) string {
	var b strings.Builder
	b.WriteString(Truncate(m.Title, 40))
	switch {
	case m.TotalEps > 0:
		fmt.Fprintf(&b, "  ep %d/%d", m.WatchedEps, m.TotalEps)
	case m.WatchedEps > 0:
		fmt.Fprintf(&b, "  ep %d", m.WatchedEps)
	}
	if a := MALAirShort(m.AirStatus); a != "" {
		fmt.Fprintf(&b, "  [%s]", a)
	}
	if m.Score > 0 {
		fmt.Fprintf(&b, "  ★%d", m.Score)
	}
	return b.String()
}

// MALListStatusShort turns a MAL status code into a display label.
func MALListStatusShort(s string) string {
	switch s {
	case "watching":
		return "Watching"
	case "completed":
		return "Completed"
	case "on_hold":
		return "On Hold"
	case "dropped":
		return "Dropped"
	case "plan_to_watch":
		return "Plan to Watch"
	}
	return mal.TitleCase(s)
}

// MALAirShort shortens a MAL airing status.
func MALAirShort(s string) string {
	switch s {
	case "currently_airing":
		return "airing"
	case "finished_airing":
		return "done"
	case "not_yet_aired":
		return "unaired"
	}
	return s
}

// RenderReleaseLine renders the tab-delimited concise line for a release.
func RenderReleaseLine(r *animetosho.Release) string {
	date := "??-??"
	if t, err := time.Parse(time.RFC3339, r.Entry.DateAdded); err == nil {
		date = t.Format("06-01-02") // YY-MM-DD, leads the line (sorted by date)
	}
	grp := r.Group
	if grp == "" {
		grp = "?"
	}
	res := r.Resolution
	if res == "" {
		res = "-"
	}
	eps := "-"
	if r.IsBatch {
		eps = "BATCH"
	} else if r.Episode > 0 {
		eps = fmt.Sprintf("ep%d", r.Episode)
	}
	return fmt.Sprintf("%s [%s] %-5s %-5s %9s %5d↑ %3d↓",
		date, Truncate(grp, 14), res, eps, HumanSize(r.Entry.SizeBytes),
		r.Entry.Seeders, r.Entry.Leechers)
}

// HumanSize formats a byte count with a binary unit suffix.
func HumanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// HumanCount formats a large integer compactly (e.g. 12.3K, 1.2M).
func HumanCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

// Truncate clips s to n runes, appending an ellipsis when truncated.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// WrapLine breaks s to no more than width runes per line, preferring to break
// after a space; long unbreakable tokens break at width. (Rune-based so CJK
// titles wrap too.)
func WrapLine(s string, width int) string {
	runes := []rune(s)
	if width <= 0 || len(runes) <= width {
		return s
	}
	var b strings.Builder
	for len(runes) > 0 {
		end := width
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			// prefer breaking after the last space in (0, end), if not too early
			for i := end - 1; i > width/2; i-- {
				if runes[i] == ' ' {
					end = i + 1
					break
				}
			}
		}
		b.WriteString(string(runes[:end]))
		runes = runes[end:]
		if len(runes) > 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// cprint writes text in the given ANSI color, wrapping to width, resetting
// after each line. Used by the fzf preview panes.
func cprint(color, text string, width int) {
	for _, line := range strings.Split(WrapLine(text, width), "\n") {
		fmt.Printf("%s%s\033[0m\n", color, line)
	}
}

// MALItemHeader renders the progress/status header shown above the release list.
func MALItemHeader(item *mal.Item) string {
	if item == nil {
		return ""
	}
	s := ""
	if item.TotalEps > 0 {
		s = fmt.Sprintf("ep %d/%d", item.WatchedEps, item.TotalEps)
	} else if item.WatchedEps > 0 {
		s = fmt.Sprintf("ep %d", item.WatchedEps)
	}
	if ls := MALListStatusShort(item.ListStatus); ls != "" {
		if s != "" {
			s += "  —  "
		}
		s += ls
	} else if item.WatchedEps > 0 {
		if s != "" {
			s += "  —  "
		}
		s += "Watching"
	}
	return s
}
