# Movie and TV tracking with Trakt

The binary is still `ani`; **Watchpane** is the Trakt app registration. Tracking is optional and does not affect anime or MyAnimeList.

## Connect your own account

1. Set `TRAKT_CLIENT_ID` in your environment, in `.env` in the directory where you run `ani`, or in `ani/.env` under the OS user config directory. For this checkout the existing ignored `.env` works. Do not put a client secret, password, or user token in it. Development app registrations only allow the owner and invited testers to sign in.
2. Run `ani trakt login` (from source: `go run ./cmd/ani trakt login`). Open the verification URL it prints, enter the short code **on Trakt**, and approve Watchpane. `ani` waits for approval. The account password never passes through `ani`. You can run `login` again to switch accounts or reauthorize a revoked session.
3. `ani trakt status` checks for locally saved authorization. `ani trakt watchlist` reads your movie/show watchlist; `ani trakt history` reads the 20 most recent movie/episode history entries. These two commands are read-only.

This uses device authorization, not a callback server. If you registered a different redirect URI from `http://127.0.0.1:8765/callback`, set `TRAKT_REDIRECT_URI` to the registered value for token refresh.

## Track a selection

In `ani --movies`, highlight a movie or show and press **w** to add it to your Trakt watchlist. Press **m** on a movie or a specific episode to mark it watched. The picker shows candidate Trakt titles (including year and ID); choose the right one and press **Enter** to confirm, or **Esc** to cancel. A series watchlist entry refers to the series; a watched entry refers only to the selected season/episode, never an entire series. Playback, closing `mpv`, and `--dry-run` never mark anything watched. This records watch history, not a playback timestamp.

## Privacy and disconnecting

Only an explicit tracking action sends the title query to Trakt. Confirming sends the selected Trakt identity and, for an episode, the episode identification needed to record the action. Stream URLs, page tokens and player referrers are not sent to Trakt. The global watchlist/history live in your Trakt account. Authorization tokens are stored in a private `trakt-token.json` in the OS user config directory under `ani` (on macOS: `~/Library/Application Support/ani/`), separate from MAL. Do not share this file.

`ani trakt logout` removes authorization from this device; it does not delete your online watchlist or history. To revoke access or remove online entries, use your Trakt account settings or lists. Reopen Movies & TV after changing the account connection.

Watchpane uses Trakt but is not endorsed or certified by Trakt. Developers and distributors must follow the [Trakt API use policy](https://developer.trakt.tv/docs/api-use-policy), including its restrictions on apps promoting copyright infringement. Registration is not approval of a playback source.
