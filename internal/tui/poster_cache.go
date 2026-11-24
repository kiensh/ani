package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Trakt artwork is downloaded and cached locally, never hotlinked in a player.
// Cache by public image URL only: no title names, IDs, keys or account tokens.
func posterCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(dir) {
		return ""
	}
	return filepath.Join(dir, "ani", "trakt-posters")
}
func posterCachePath(dir, url string) string {
	hash := sha256.Sum256([]byte(url))
	return filepath.Join(dir, hex.EncodeToString(hash[:])+".img")
}

func readCachedPoster(dir, url string) []byte {
	if dir == "" {
		return nil
	}
	path := posterCachePath(dir, url)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPosterBytes {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPosterBytes+1))
	if err != nil {
		return nil
	}
	if _, _, _, err := posterSize(b); err != nil {
		return nil
	}
	return b
}

func cachePoster(dir, url string, b []byte) {
	if dir == "" || os.MkdirAll(dir, 0700) != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".downloading-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	cerr := f.Close()
	if err != nil || cerr != nil {
		return
	}
	if os.Rename(f.Name(), posterCachePath(dir, url)) != nil {
		return
	}
	// Keep at most 96 downloaded images. Only prune our hashed regular files.
	files, err := os.ReadDir(dir)
	if err != nil || len(files) <= 96 {
		return
	}
	type entry struct {
		name string
		t    time.Time
	}
	var images []entry
	for _, file := range files {
		name := file.Name()
		if !file.Type().IsRegular() || len(name) != 68 || !strings.HasSuffix(name, ".img") {
			continue
		}
		if _, err := hex.DecodeString(name[:64]); err != nil {
			continue
		}
		if info, err := file.Info(); err == nil {
			images = append(images, entry{name, info.ModTime()})
		}
	}
	if len(images) <= 96 {
		return
	}
	sort.Slice(images, func(i, j int) bool { return images[i].t.Before(images[j].t) })
	for _, e := range images[:len(images)-96] {
		_ = os.Remove(filepath.Join(dir, e.name))
	}
}
