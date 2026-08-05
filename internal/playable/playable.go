// Package playable is the provider-agnostic "thing you can watch" — a release
// from a torrent index (AnimeTosho) or a streaming variant (anidb.app). The
// release picker, filters, sort, and MAL write-back all work over *Release, so
// any provider that produces these items feeds the same UI.
package playable

// Release is one watchable item.
//
// Torrent providers populate DateAdded/SizeBytes/Seeders/Leechers/Magnet;
// streaming providers populate StreamURL. Group is the torrent release group, or
// the audio track ("sub"/"dub") for streams. Resolution is e.g. "1080"/"720" or
// "1920x1080". Title is the release/variant title (fuzzy filter + announce).
type Release struct {
	Title      string
	Group      string
	Resolution string
	Episode    int
	IsBatch    bool

	// Torrent-only.
	DateAdded string
	SizeBytes int64
	Seeders   int
	Leechers  int
	Magnet    string

	// Stream-only (anidb). When set, the item plays via mpv+URL, not webtorrent.
	StreamURL string
}

// IsStream reports whether this item is a direct stream (mpv) rather than a
// torrent (webtorrent). Render and play dispatch branch on this.
func (r *Release) IsStream() bool { return r.StreamURL != "" }
