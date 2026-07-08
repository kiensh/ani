package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ani/internal/animetosho"
	"ani/internal/mal"

	"github.com/charmbracelet/lipgloss"
)

// RenderMALLine renders the one-line label used by the anime picker list. With a
// preview pane showing the details, the list line is just the title.
func RenderMALLine(m mal.Item) string {
	return Truncate(m.Title, 40)
}

// FormatProgress builds the "ep …" progress string for an anime.
//   - airing: always three numbers — "ep watched/aired/total", with "?" for an
//     unknown aired count (no Jikan data) or unknown total. e.g. "0/4/12",
//     "0/?/12", "0/4/?", "0/?" "?".
//   - not airing: "ep watched/total" (or "watched/?"); "" when nothing is known.
func FormatProgress(watched, total, aired int, airing bool) string {
	if airing {
		a := "?"
		if aired > 0 {
			a = strconv.Itoa(aired)
		}
		t := "?"
		if total > 0 {
			t = strconv.Itoa(total)
		}
		return fmt.Sprintf("ep %d/%s/%s", watched, a, t)
	}
	switch {
	case total > 0:
		return fmt.Sprintf("ep %d/%d", watched, total)
	case watched > 0:
		return fmt.Sprintf("ep %d/?", watched)
	}
	return ""
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

// statusBadgeStyle returns a background+foreground lipgloss style per MAL list
// status (watching=green, completed=blue, on_hold=orange, dropped=red,
// plan_to_watch=gray). Zero style for unknown/empty.
func statusBadgeStyle(s string) lipgloss.Style {
	switch s {
	case "watching":
		return lipgloss.NewStyle().Background(lipgloss.Color("46")).Foreground(lipgloss.Color("0")).Bold(true)
	case "completed":
		return lipgloss.NewStyle().Background(lipgloss.Color("27")).Foreground(lipgloss.Color("15")).Bold(true)
	case "on_hold":
		return lipgloss.NewStyle().Background(lipgloss.Color("208")).Foreground(lipgloss.Color("0")).Bold(true)
	case "dropped":
		return lipgloss.NewStyle().Background(lipgloss.Color("124")).Foreground(lipgloss.Color("15")).Bold(true)
	case "plan_to_watch":
		return lipgloss.NewStyle().Background(lipgloss.Color("240")).Foreground(lipgloss.Color("15")).Bold(true)
	}
	return lipgloss.NewStyle()
}

// hasStatusColor reports whether status is one of the five with a badge color.
func hasStatusColor(s string) bool {
	switch s {
	case "watching", "completed", "on_hold", "dropped", "plan_to_watch":
		return true
	}
	return false
}

// ColoredStatus renders the status label as a colored badge (background +
// foreground per status). Returns "" for an empty status; for an unknown status
// it returns the plain label.
func ColoredStatus(status string) string {
	label := MALListStatusShort(status)
	if status == "" || label == "" {
		return ""
	}
	if !hasStatusColor(status) {
		return label
	}
	return statusBadgeStyle(status).Render(" " + label + " ")
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
	// Every field is a fixed rune-width so the columns line up across rows.
	// The group is bracketed at its natural width, then padded as a whole to
	// 16 runes so the padding sits OUTSIDE the brackets: "[erai-raw]      ".
	// Truncate caps the group at 14 runes so "[grp]" never exceeds 16.
	return fmt.Sprintf("%s %-16s %-5s %-5s %9s %5d↑ %3d↓",
		date, "["+Truncate(grp, 14)+"]", res, eps, HumanSize(r.Entry.SizeBytes),
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
// aired is the latest aired episode (0 if unknown) for the "watched/aired/total"
// form on airing anime.
func MALItemHeader(item *mal.Item, aired int) string {
	if item == nil {
		return ""
	}
	s := FormatProgress(item.WatchedEps, item.TotalEps, aired, item.AirStatus == "currently_airing")
	if badge := ColoredStatus(item.ListStatus); badge != "" {
		if s != "" {
			s += "  —  "
		}
		s += badge
	} else if item.WatchedEps > 0 {
		if s != "" {
			s += "  —  "
		}
		s += "Watching"
	}
	return s
}
