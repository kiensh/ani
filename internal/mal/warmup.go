package mal

// WarmAidResolvers eagerly builds the Fribb and AniDB-title aid-resolution maps
// so the first aired-episode prefetch storm (right after the MAL load) doesn't
// pay the one-time ~9 MB download/parse inline. Best-effort, fire-and-forget:
// errors fall through to the normal lazy path in AnidbAIDViaFribb/AnidbAIDByTitle.
func WarmAidResolvers(debug bool) {
	_, _ = fribbMap(debug)
	_, _ = anidbTitlesMap(debug)
}
