# Movie and TV tracking with Trakt

The binary is still `ani`; **Watchpane** is the Trakt app registration. Tracking is optional and does not affect anime or MyAnimeList.

## Connect your own account

1. Set `TRAKT_CLIENT_ID` in your environment, in `.env` in the directory where you run `ani`, or in `ani/.env` under the OS user config directory. For this checkout the existing ignored `.env` works. Do not put a client secret, password, or user token in it. Development app registrations only allow the owner and invited testers to sign in.
2. Start `ani` (from this checkout: `go run ./cmd/ani`). Press **Tab** to switch from **Anime** to **Movies & TV**, or select **Movies & TV** in the command palette (`:` → **Source**). On first entry while signed out, choose **Connect Trakt** or continue without an account. You can also use **L** or the **Account** section of the movie palette to connect later. Enter the code shown in the picker **on Trakt's website** and approve Watchpane. Keep the picker open; it detects approval and enables tracking in place. If the browser doesn't open, use the verification URL on screen. `ani trakt login` remains an optional CLI shortcut, not a requirement.
3. `ani trakt status` checks for locally saved authorization. `ani trakt watchlist` reads your movie/show watchlist; `ani trakt history` reads the 20 most recent movie/episode history entries. These two commands are read-only.

This uses device authorization, not a callback server. If you registered a different redirect URI from `http://127.0.0.1:8765/callback`, set `TRAKT_REDIRECT_URI` to the registered value for token refresh.

## Browse movies and shows

Movies & TV opens on **My Watchlist**, even when the list is empty. If you skip sign-in, it stays there with a connection hint instead of fetching provider titles. Press **v** for **Recently Added** (FlixHQ’s Latest Movies and Latest TV Shows), **v** again to return, or **/** to search. Selecting a watchlist item opens matching provider results so you can confirm the right title and year. A title argument (for example `ani --movies Breaking Bad`) still opens search results directly.

Highlight a provider title to see its poster, synopsis and available details; on narrow terminals the preview sits below the list. Loading a preview does not play or track anything.

## Track a selection

In `ani --movies`, highlight a movie or show and press **w** to add it to your Trakt watchlist. Press **m** on a movie or a specific episode to mark it watched. The picker shows candidate Trakt titles (including year and ID); choose the right one and press **Enter** to confirm, or **Esc** to cancel. A series watchlist entry refers to the series; a watched entry refers only to the selected season/episode, never an entire series. Playback, closing `mpv`, and `--dry-run` never mark anything watched. This records watch history, not a playback timestamp.

## Privacy and disconnecting

Only an explicit tracking action sends the title query to Trakt. Confirming sends the selected Trakt identity and, for an episode, the episode identification needed to record the action. Stream URLs, page tokens and player referrers are not sent to Trakt. The global watchlist/history live in your Trakt account. Authorization tokens are stored in a private `trakt-token.json` in the OS user config directory under `ani` (on macOS: `~/Library/Application Support/ani/`), separate from MAL. Do not share this file.

Use **Account → Disconnect Trakt** in the Movies & TV palette, or `ani trakt logout`, to remove authorization from this device. Your online watchlist and history stay intact. To revoke access or remove online entries, use your Trakt account settings or lists.

Watchpane uses Trakt but is not endorsed or certified by Trakt. Developers and distributors must follow the [Trakt API use policy](https://developer.trakt.tv/docs/api-use-policy), including its restrictions on apps promoting copyright infringement. Registration is not approval of a playback source.
