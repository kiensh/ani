package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"ani/internal/trakt"
)

const traktHelp = `Usage: ani trakt <command>
  login       Connect a Trakt account using a code in your own browser
  status      Check for saved authorization on this device (offline)
  logout      Forget authorization on this device, keeping your Trakt history
  watchlist   List movies and shows on your Trakt watchlist
  history     Show your 20 most recent movie/episode history entries

In the TUI, Tab switches to Movies & TV. Connect Trakt there on first entry or
press L later; these CLI commands are optional. Use w for your watchlist or m
to mark a movie/episode watched. Playback alone never changes your account.

Watchpane uses Trakt but is not endorsed or certified by Trakt.`

type traktAccount interface {
	Login(context.Context, func(string, string)) error
	Connected() bool
	Logout() error
	Watchlist(context.Context) ([]trakt.Title, error)
	History(context.Context) ([]trakt.HistoryItem, error)
}

func traktCommand(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runTraktCommand(ctx, args, trakt.New(), os.Stdout)
}

func runTraktCommand(ctx context.Context, args []string, c traktAccount, out io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		fmt.Fprintln(out, traktHelp)
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("expected one Trakt command; see ani trakt --help")
	}
	switch args[0] {
	case "login":
		// An expired or revoked refresh token can still look connected offline.
		// An explicit login must always allow reauthorization; the client only
		// replaces the previous session after the user grants the new one.
		if err := c.Login(ctx, func(url, code string) {
			fmt.Fprintf(out, "Open this URL in your browser:\n%s\nEnter code: %s\nWaiting for approval… (Ctrl+C to cancel)\n", url, code)
		}); err != nil {
			return err
		}
		fmt.Fprintln(out, "Trakt connected. Open ani --movies to track a title.")
	case "status":
		if c.Connected() {
			fmt.Fprintln(out, "Trakt: authorization saved on this device (not checked online).")
		} else {
			fmt.Fprintln(out, "Trakt: not connected. Run ani trakt login.")
		}
	case "logout":
		if err := c.Logout(); err != nil {
			return err
		}
		fmt.Fprintln(out, "Trakt authorization removed from this device. Your online history and watchlist are unchanged.")
	case "watchlist", "history":
		if !c.Connected() {
			return fmt.Errorf("Trakt is not connected; run ani trakt login")
		}
		if args[0] == "watchlist" {
			titles, err := c.Watchlist(ctx)
			if err != nil {
				return err
			}
			if len(titles) == 0 {
				fmt.Fprintln(out, "Your Trakt movie/series watchlist is empty.")
			}
			for _, t := range titles {
				fmt.Fprintf(out, "[%s] %s\n", t.Kind, formatTraktTitle(t))
			}
		} else {
			items, err := c.History(ctx)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Fprintln(out, "Your Trakt movie/episode history is empty.")
			}
			for _, i := range items {
				label := formatTraktTitle(i.Title)
				if i.Title.Kind == "series" {
					label += fmt.Sprintf(" S%02dE%02d", i.Season, i.Episode)
				}
				fmt.Fprintf(out, "%s  %s\n", i.WatchedAt.Format("2006-01-02"), label)
			}
		}
	default:
		return fmt.Errorf("unknown Trakt command %q; see ani trakt --help", args[0])
	}
	return nil
}

func formatTraktTitle(t trakt.Title) string {
	if t.Year > 0 {
		return fmt.Sprintf("%s (%d)", t.Name, t.Year)
	}
	return t.Name
}
