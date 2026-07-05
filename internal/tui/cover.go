package tui

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// coverMu serializes kitten icat calls so a fast cursor move doesn't let two
// image writes interleave on /dev/tty (which leaves the cover half-cleared).
var coverMu sync.Mutex

// coverRenderedMsg is emitted when a cover render completes (or fails). It
// carries no data — the model re-renders from its own state.
type coverRenderedMsg struct{}

// coverReadyMsg is emitted once by the CoverCache when all requested URLs have
// been downloaded (or failed). The model re-renders on it so a previously
// missing cover (skipped because the file wasn't cached yet) gets drawn once
// its file lands.
type coverReadyMsg struct{}

// coverCacheDirName is the temp subdirectory used for downloaded covers.
const coverCacheDirName = "ani-covers"

// CoverCache pre-downloads cover images to temp files on load and serves them
// by URL. Rendering then reads a local file (instant, no network delay → no
// blank flash on navigation) instead of handing kitten icat a remote URL.
type CoverCache struct {
	dir  string
	maps map[string]string // url → local filepath
	mu   sync.RWMutex
}

// coverDebugf writes cover diagnostics to a log file, not stderr — stderr
// would corrupt the bubbletea TUI during rendering. Logs go to
// /tmp/ani-tui-debug.log; print the path on exit for diagnosis.
var coverDebugFile *os.File

func coverDebugf(format string, args ...any) {
	if coverDebugFile == nil {
		f, err := os.OpenFile("/tmp/ani-tui-debug.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		coverDebugFile = f
	}
	fmt.Fprintf(coverDebugFile, "DEBUG cover: "+format+"\n", args...)
}

// closeDebug flushes/closes the debug log file and tells the user where to find it.
func closeDebug() {
	if coverDebugFile != nil {
		coverDebugFile.Close()
		coverDebugFile = nil
		fmt.Fprintln(os.Stderr, "Debug log: /tmp/ani-tui-debug.log")
	}
}

// NewCoverCache creates a cache backed by a fresh temp directory and returns a
// tea.Cmd that downloads every distinct URL concurrently. Get returns "" until
// a given URL's download finishes, so callers should re-render on coverReadyMsg.
// Empty URLs are skipped (and never stored).
func NewCoverCache(urls []string) (tea.Cmd, *CoverCache) {
	dir, err := os.MkdirTemp("", coverCacheDirName)
	if err != nil {
		coverDebugf("mkdir failed: %v", err)
		return func() tea.Msg { return coverReadyMsg{} }, &CoverCache{maps: map[string]string{}}
	}
	c := &CoverCache{dir: dir, maps: map[string]string{}}
	// De-dup while preserving order so each URL downloads exactly once.
	seen := map[string]bool{}
	distinct := make([]string, 0, len(urls))
	for _, u := range urls {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		distinct = append(distinct, u)
	}
	return c.downloadAll(distinct), c
}

// downloadAll returns a Cmd that fetches each URL in its own goroutine. Each
// completed download updates the map atomically; the Cmd emits a single
// coverReadyMsg once every download has terminated (success or failure).
func (c *CoverCache) downloadAll(urls []string) tea.Cmd {
	return func() tea.Msg {
		var wg sync.WaitGroup
		for _, u := range urls {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				path, err := c.downloadOne(url)
				if err != nil {
					coverDebugf("download %s failed: %v", url, err)
					return
				}
				c.mu.Lock()
				c.maps[url] = path
				c.mu.Unlock()
			}(u)
		}
		wg.Wait()
		return coverReadyMsg{}
	}
}

// downloadOne fetches a single URL into a temp file inside the cache dir. The
// extension is derived from the URL path so kitten icat sniffs the format.
func (c *CoverCache) downloadOne(url string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %s", resp.Status)
	}
	name := "cover" + coverExt(url)
	tmp, err := os.CreateTemp(c.dir, name+"-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	return tmp.Name(), nil
}

// coverExt picks a file extension from a URL path, defaulting to .jpg.
func coverExt(url string) string {
	ext := filepath.Ext(url)
	if ext == "" {
		return ".jpg"
	}
	return ext
}

// Get returns the local filepath for a URL, or "" if not (yet) cached.
func (c *CoverCache) Get(url string) string {
	if c == nil || url == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maps[url]
}

// Cleanup removes the cache directory. Safe to call on a zero/empty cache.
func (c *CoverCache) Cleanup() {
	if c == nil || c.dir == "" {
		return
	}
	os.RemoveAll(c.dir)
}

// RenderCover returns a tea.Cmd that draws the cover image at the LOCAL file
// (looked up from the cache by url) into the terminal cell region described by
// place (a kitten icat --place argument). The draw happens against /dev/tty
// (not stdout) so it survives bubbletea's per-frame full-screen redraws.
//
// Before placing the new image, it emits a targeted kitty graphics delete for
// just the cover cell region (a=d,c=cols,r=rows) — this is pane-local and
// works under tmux, unlike --clear (which can't see tmux panes) or --clear-all
// (which clears images in ALL panes).
//
// If the url is empty or its file isn't cached yet, the Cmd is a no-op.
func RenderCover(cache *CoverCache, url, place string) tea.Cmd {
	return func() tea.Msg {
		if url == "" || place == "" {
			return coverRenderedMsg{}
		}
		path := cache.Get(url)
		if path == "" {
			return coverRenderedMsg{}
		}
		coverMu.Lock()
		defer coverMu.Unlock()
		tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
		if err != nil {
			coverDebugf("open /dev/tty failed: %v", err)
			return coverRenderedMsg{}
		}
		defer tty.Close()
		// No --clear or --clear-all during navigation — neither works reliably
		// in tmux. The --place overwrites the same cell region; any leftover
		// from a different-aspect cover is cosmetic. ClearCover (--clear-all)
		// runs on screen exit (quit/select) when we leave the alt screen.
		cmd := exec.Command("kitten", "icat",
			"--stdin=no", "--scale-up", "--place="+place, path)
		var stderrBuf strings.Builder
		cmd.Stdout = tty
		cmd.Stderr = &stderrBuf
		err = cmd.Run()
		coverDebugf("place=%s url=%s file=%s exit=%v stderr=%q",
			place, url, path, err, strings.TrimSpace(stderrBuf.String()))
		return coverRenderedMsg{}
	}
}

// ClearCover returns a tea.Cmd that clears the cover image in the given region.
// Used on screen transitions (quit/select). Uses a targeted cell-region delete
// (pane-local, tmux-safe) instead of --clear-all (which clears all panes).
func ClearCover(place string) tea.Cmd {
	return func() tea.Msg {
		if place == "" {
			return coverRenderedMsg{}
		}
		coverMu.Lock()
		defer coverMu.Unlock()
		tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
		if err != nil {
			return coverRenderedMsg{}
		}
		defer tty.Close()
		// On full screen transitions (quit/select) we're leaving the alt
		// screen, so --clear-all is fine.
		cmd := exec.Command("kitten", "icat", "--clear-all")
		cmd.Stdout = tty
		cmd.Run()
		return coverRenderedMsg{}
	}
}

// deleteCoverRegion parses a place string ("WxH@XxY"), moves the cursor to
// (X,Y), and emits a kitty graphics delete at that position (a=d,x=1,y=1).
// The pixel offset (1,1) is within the cursor cell, so it deletes images
// overlapping that cell. This is pane-local — works in tmux, doesn't touch
// other panes' images.
//
// We loop over each row of the cover area and delete at each row to cover
// the full region, since a=d,x,y only deletes images overlapping the single
// pixel at cursor+offset.
func deleteCoverRegion(w io.Writer, place string) {
	_, rows, col, row, ok := parsePlace(place)
	if !ok {
		return
	}
	for r := row; r < row+rows; r++ {
		fmt.Fprintf(w, "\x1b[%d;%dH", r, col)
		fmt.Fprintf(w, "\x1b_Ga=d,x=1,y=1\x1b\\")
	}
}

// parsePlace extracts cols, rows, col, row from a "WxH@XxY" string.
func parsePlace(place string) (cols, rows, col, row int, ok bool) {
	var w, h, x, y int
	n, err := fmt.Sscanf(place, "%dx%d@%dx%d", &w, &h, &x, &y)
	if err != nil || n != 4 || w <= 0 || h <= 0 {
		return 0, 0, 0, 0, false
	}
	return w, h, x, y, true
}

// CoverPlace computes the kitten icat --place argument ("WxH@XxY") for the
// cover image. W,H are cell dimensions; X,Y is the top-left cell of the cover
// region. The coordinates MUST match the blank-space cover area the caller
// renders (otherwise the image lands on top of metadata text). cols and rows
// are the exact cell size of that blank area.
func CoverPlace(cols, rows, col, row int) string {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return fmt.Sprintf("%dx%d@%dx%d", cols, rows, col, row)
}
