package tui

// MediaSource selects what to browse, not which service supplies playback.
// The empty value returned by a screen means quit rather than switch.
type MediaSource string

const (
	MediaAnime  MediaSource = "anime"
	MediaMovies MediaSource = "movies"
)

// Use identical entries on both sides of Tab, including the signed-out landing.
func mediaSourceCommands(active MediaSource) []Command {
	label := func(source MediaSource, name string) string {
		if source == active {
			return "● " + name
		}
		return name
	}
	return []Command{
		{Category: "Source", Title: label(MediaAnime, "Anime"), Intent: "source:anime", Keywords: "anime MAL MyAnimeList"},
		{Category: "Source", Title: label(MediaMovies, "Movies & TV"), Intent: "source:movies", Keywords: "films series FlixHQ Trakt"},
	}
}
