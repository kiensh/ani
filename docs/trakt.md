# Movie and TV tracking with Trakt

The binary is still `ani`; **Watchpane** is the Trakt app registration. Tracking is optional and does not affect anime or MyAnimeList.

## Connect your own account

1. Set `TRAKT_CLIENT_ID` in your environment, in `.env` in the directory where you run `ani`, or in `ani/.env` under the OS user config directory. For this checkout the existing ignored `.env` works. Do not put a client secret, password, or user token in it. Development app registrations only allow the owner and invited testers to sign in.
2. Start `ani` (from this checkout: `go run ./cmd/ani`). Press **Tab** to switch from **Anime** to **Movies & TV**, or select **Movies & TV** in the command palette (`:` → **Source**). On first entry while signed out, choose **Connect Trakt** or continue without an account. You can also use **L** or the **Account** section of the movie palette to connect later. Enter the code shown in the picker **on Trakt's website** and approve Watchpane. Keep the picker open; it detects approval and enables tracking in place. If the browser doesn't open, use the verification URL on screen. `ani trakt login` remains an optional CLI shortcut, not a requirement.
3. `ani trakt status` checks for locally saved authorization. `ani trakt watchlist` reads your movie/show watchlist; `ani trakt history` reads the 20 most recent movie/episode history entries. These two commands are read-only.

This uses device authorization, not a callback server. If you registered a different redirect URI from `http://127.0.0.1:8765/callback`, set `TRAKT_REDIRECT_URI` to the registered value for token refresh.

## Browse movies and shows

Movies & TV opens on **My Watchlist**, even if empty. **/** searches Trakt for movies and TV series; `ani --movies Breaking Bad` opens those search results directly. Use **v** to cycle **My Watchlist → Recent Releases → Watched Movies**, or pick any shelf from **: → View**. **Watched Movies** lists the distinct films you have marked watched on Trakt, not just the last 20 plays. For dated movie and episode plays, `ani trakt history` still shows recent activity. **Recent Releases** covers Trakt’s public release calendars for the last seven UTC dates: movie releases and new-series premieres, not new uploads at a playback provider. Search, releases, and episode lists require the app client ID but not a linked personal account; the two personal shelves do require sign-in.

The same Trakt record supplies the title, year, poster, overview, genres, runtime and rating on all shelves and in search. If a list row lacks artwork or details, the focused preview can retrieve its Trakt summary **by ID**, not by title. Available posters are cached under the OS user cache directory in `ani/trakt-posters`; missing artwork does not prevent playback or tracking. WebP artwork uses Kitty graphics where available; the text-thumbnail fallback can decode WebP with `ffmpeg` on PATH.

Only choosing **Play** (or playing with a subtitle file) asks FlixHQ for a stream. If several provider pages could match, the picker may ask you to choose a **playback source**; that choice does not change the Trakt item.

## Subtitles in Movies & TV

**Enter** plays the selected movie or episode and attaches full English captions when the provider has them, even if the video has no embedded track. It prefers a regular English track, then English SDH/CC; a forced-only or commentary track is not a substitute for the dialogue. If the provider has none, the video still plays. **s** (or `:` → **Play with subtitle file…**) lets you paste a local `.srt`, `.vtt`, `.ass`, or `.ssa` path and press **Enter** to play with that file instead. From any shelf, select the Trakt movie or a particular episode first. The local file is used only for this playback; it is not uploaded, copied into your account, or deleted. Provider captions are downloaded into a private temporary file and removed when the player exits. In mpv, **j** cycles subtitle tracks and **v** toggles their visibility (default bindings). Use a subtitle file that matches the cut of the movie to keep dialogue in sync.

## Track a selection

In **Movies & TV**, search with **/**, enter a title and press **Enter**. Highlight a result and press **Space** for its **Actions**, or press **:** and search for `add` or `watched`. Choose **Add to My Watchlist** to save the movie for later, or **Mark movie as watched** to record it in your history. The direct shortcuts are **w** for watchlist and **m** for watched. For a series, add the series to your watchlist or select a specific episode before marking it watched. The picker already knows the chosen Trakt ID. Press **Enter** to confirm that exact item, or **Esc** to cancel. There is no second title search or match selection. A series watchlist entry refers to the series; a watched entry refers only to the selected season/episode, never an entire series. Playback, closing `mpv`, and `--dry-run` never mark anything watched. This records watch history, not a playback timestamp.

## Privacy and disconnecting

Your movie searches and catalog requests go to Trakt. Account reads and writes use your authorization; writes use the selected movie, show, or episode ID. FlixHQ is not contacted for browsing or tracking. Playback sends the selected title and, for TV, the requested episode to the playback provider. Stream URLs, page tokens and player referrers are not sent to Trakt. The global watchlist/history live in your Trakt account. The local artwork cache contains public images under hashed filenames; you can clear `ani/trakt-posters` in your OS cache directory without changing your account. Authorization tokens are stored in a private `trakt-token.json` in the OS user config directory under `ani` (on macOS: `~/Library/Application Support/ani/`), separate from MAL. Do not share this file.

Use **Account → Disconnect Trakt** in the Movies & TV palette, or `ani trakt logout`, to remove authorization from this device. Your online watchlist and history stay intact. To revoke access or remove online entries, use your Trakt account settings or lists.

Watchpane uses Trakt but is not endorsed or certified by Trakt. Developers and distributors must follow the [Trakt API use policy](https://developer.trakt.tv/docs/api-use-policy), including its restrictions on apps promoting copyright infringement. Registration is not approval of a playback source.
