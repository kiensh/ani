package tui

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// coverMu serializes kitten icat calls so a fast cursor move doesn't let two
// image writes interleave on /dev/tty (which leaves the cover half-cleared).
var coverMu sync.Mutex

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

// splitPassthrough divides kitten's output into the upload segments (image
// data, to write to /dev/tty) and the remaining placeholder text (to render in
// the View). The upload is the kitty APC: under tmux it's wrapped in a DCS
// passthrough (\x1bPtmux;...\x1b\\, ESCs doubled inside); outside tmux it's a
// raw APC (\x1b_G...\x1b\\). Both are terminated by a lone \x1b\\ (ST).
func splitPassthrough(data []byte) (segments [][]byte, rest []byte) {
	var r bytes.Buffer
	for i := 0; i < len(data); {
		if data[i] == 0x1b && i+1 < len(data) && (data[i+1] == 'P' || data[i+1] == '_') {
			j := i + 2
			for j < len(data)-1 {
				if data[j] == 0x1b && data[j+1] == 0x1b { // doubled ESC (passthrough data)
					j += 2
					continue
				}
				if data[j] == 0x1b && data[j+1] == '\\' { // ST terminator
					break
				}
				j++
			}
			end := j + 2
			if end > len(data) {
				end = len(data)
			}
			segments = append(segments, data[i:end])
			i = end
			continue
		}
		r.WriteByte(data[i])
		i++
	}
	return segments, r.Bytes()
}

// stripCursorMoves keeps SGR color codes (\x1b[..m), the U+10EEEE placeholder
// chars + their combining diacritics, and newlines; drops cursor moves, CR, and
// DECSC/DECRC so the text renders cleanly inline in bubbletea's View.
func stripCursorMoves(data []byte) string {
	var b bytes.Buffer
	for i := 0; i < len(data); {
		c := data[i]
		if c == 0x1b && i+1 < len(data) {
			if data[i+1] == '[' { // CSI: read to final byte 0x40-0x7e
				j := i + 2
				for j < len(data) && (data[j] < 0x40 || data[j] > 0x7e) {
					j++
				}
				if j < len(data) && data[j] == 'm' { // keep SGR (color)
					b.Write(data[i : j+1])
				}
				i = j + 1
				continue
			}
			// non-CSI ESC (DECSC \x1b7, DECRC \x1b8, ...): drop
			i += 2
			continue
		}
		if c == '\r' { // drop CR
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// RenderCoverPlaceholder runs kitten in unicode-placeholder mode for the cached
// cover file and returns the passthrough upload segments (write via WriteUpload)
// and the placeholder text (render in the View). With unicode placeholders the
// image anchors to wherever the text is rendered, so there are no absolute
// coordinates — clearing is automatic when the text changes. Requires tmux
// `allow-passthrough on` (raw APC reaches kitty unwrapped).
func RenderCoverPlaceholder(path string, cols, rows int) (upload [][]byte, text string, err error) {
	var out bytes.Buffer
	place := fmt.Sprintf("%dx%d@1x1", cols, rows)
	// --unicode-placeholder anchors the image to text (tmux-safe, auto-clears).
	// --passthrough defaults to "detect": wraps in tmux passthrough inside tmux,
	// raw APC outside — splitPassthrough handles both.
	c := exec.Command("kitten", "icat", "--unicode-placeholder",
		"--silent", "--stdin=no", "--place="+place, path)
	c.Stdout = &out
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if rerr := c.Run(); rerr != nil {
		return nil, "", fmt.Errorf("%v: %s", rerr, stderr.String())
	}
	segs, rest := splitPassthrough(out.Bytes())
	coverDebugf("placeholder place=%s file=%s segs=%d text=%dB",
		place, path, len(segs), len(rest))
	return segs, stripCursorMoves(rest), nil
}

// WriteUpload writes passthrough segments (the image upload) to /dev/tty under
// the cover mutex so concurrent draws don't interleave.
func WriteUpload(segments [][]byte) {
	coverMu.Lock()
	defer coverMu.Unlock()
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		coverDebugf("open /dev/tty failed: %v", err)
		return
	}
	defer tty.Close()
	for _, s := range segments {
		tty.Write(s)
	}
}
