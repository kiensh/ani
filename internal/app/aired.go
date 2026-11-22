package app

import (
	"sync"
	"time"

	"ani/internal/animetosho"
	"ani/internal/hianime"
	"ani/internal/mal"
	"ani/internal/tui"
)

// One public listing per provider, reused across filters, picker re-entry and
// provider switches. Even a full season doesn't trigger per-show requests in
// the background. Only the focused-item path needs to fill a listing miss.
type airedSnapshots struct {
	feed    func() (animetosho.EpisodeFeed, error)
	listing func() (hianime.EpisodeListing, error)
}

func newAiredSnapshots(health *tui.ProviderHealth) airedSnapshots {
	// Only real HTTP results change health. Reusing a cached success must not
	// clear a 429 reported by a later, unrelated release request.
	report := func(provider string, err error) {
		if err != nil {
			health.MarkDown(provider, downReason(err))
			mal.LogDebug("aired snapshot (%s): %v\n", provider, err)
		} else {
			health.MarkUp(provider)
		}
	}
	return airedSnapshots{
		feed: cachedSnapshot(func() (animetosho.EpisodeFeed, error) {
			feed, err := animetosho.FetchEpisodeFeed()
			report("torrent", err)
			return feed, err
		}, time.Now),
		listing: cachedSnapshot(func() (hianime.EpisodeListing, error) {
			listing, err := hianime.FetchEpisodeListing()
			report("hianime", err)
			return listing, err
		}, time.Now),
	}
}

// cachedSnapshot serializes refreshes (including concurrent cold starts).
// On error return the last good snapshot AND the error: the preview can still
// use known counts without clearing the provider warning. Back off on both
// cold failures and failed refreshes; the HTTP gate also honors Retry-After.
func cachedSnapshot[T any](load func() (T, error), now func() time.Time) func() (T, error) {
	var mu sync.Mutex
	var value T
	var lastErr error
	var next time.Time
	return func() (T, error) {
		mu.Lock()
		defer mu.Unlock()
		if !now().Before(next) {
			v, err := load()
			lastErr = err
			if err == nil {
				value, next = v, now().Add(10*time.Minute)
			} else {
				next = now().Add(time.Minute)
			}
		}
		return value, lastErr
	}
}

func (s airedSnapshots) prefetch(opt Options) tui.AiredPrefetch {
	return func(items []mal.Item) map[int]float64 {
		counts := make(map[int]float64)
		if len(items) == 0 {
			return counts
		}
		if opt.Source == "hianime" {
			listing, _ := s.listing()
			for _, it := range items {
				if n, ok := listing.Latest(it.Title); ok {
					counts[it.MalID] = n
				}
			}
		} else {
			feed, _ := s.feed()
			if len(feed) != 0 {
				for i := range items {
					it := &items[i]
					if n, ok := feed.Latest(resolveAidFast(it, &opt), it.TotalEps); ok {
						counts[it.MalID] = float64(n)
					}
				}
			}
		}
		return counts
	}
}

// Only the app loop selects a cache. Commands already handed to a retiring
// picker keep its provider's pointer, including while the new picker starts.
type providerAiredCaches map[string]*tui.AiredCache

func (c providerAiredCaches) forSource(source string) *tui.AiredCache {
	if source != "hianime" {
		source = "torrent"
	}
	if c[source] == nil {
		c[source] = tui.NewAiredCache()
	}
	return c[source]
}
