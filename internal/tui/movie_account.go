package tui

import (
	"context"
	"strings"

	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

// MovieAccount connects and disconnects the account used for explicit tracking
// actions. Connected reads local state only; Login must respect cancellation.
type MovieAccount interface {
	MovieTracker
	Connected() bool
	Login(context.Context, func(verificationURL, userCode string)) error
	Logout() error
}

type movieAccountMode int

const (
	movieAccountClosed movieAccountMode = iota
	movieAccountOffer
	movieAccountWaiting
	movieAccountConfirmLogout
	movieAccountDisconnecting
)

type movieAccountDialog struct {
	mode          movieAccountMode
	skip          bool
	url, code     string
	browserFailed bool
}

type movieDevicePromptMsg struct {
	id        int
	ctx       context.Context
	url, code string
}

type movieBrowserMsg struct {
	id  int
	err error
}

type movieAccountDoneMsg struct {
	id  int
	err error
}

func (m *moviePicker) refreshAccount() {
	m.connected = m.account != nil && m.account.Connected()
}

func (m *moviePicker) offerLogin() {
	m.dialog = movieAccountDialog{mode: movieAccountOffer}
	m.problem, m.notice = "", ""
}

func (m *moviePicker) closeAccount() tea.Cmd {
	m.stopLoad()
	m.dialog = movieAccountDialog{}
	m.problem = ""
	return m.Init() // resume any initial title query held for the welcome prompt
}

func (m *moviePicker) accountKey(key string) tea.Cmd {
	switch m.dialog.mode {
	case movieAccountOffer:
		switch key {
		case "up", "k":
			m.dialog.skip = false
		case "down", "j":
			m.dialog.skip = true
		case "L":
			return m.startLogin()
		case "enter":
			if !m.dialog.skip {
				return m.startLogin()
			}
			fallthrough
		case "s", "n", "esc":
			return m.closeAccount()
		case "q":
			return m.leave("")
		}
	case movieAccountWaiting:
		if key == "esc" {
			m.notice = "Trakt authorization canceled."
			return m.closeAccount()
		}
	case movieAccountConfirmLogout:
		switch key {
		case "y", "Y": // a second Enter from the palette must not disconnect
			m.dialog.mode = movieAccountDisconnecting
			_, id := m.beginRequest()
			account := m.account
			return func() tea.Msg { return movieAccountDoneMsg{id, account.Logout()} }
		case "n", "N", "esc", "enter":
			return m.closeAccount()
		}
	}
	return nil
}

func (m *moviePicker) startLogin() tea.Cmd {
	if m.dialog.mode == movieAccountWaiting || m.dialog.mode == movieAccountDisconnecting || (m.track != nil && m.track.saving) {
		return nil
	}
	if m.account == nil {
		m.offerLogin()
		m.problem = "Trakt is unavailable. You can still browse and play without an account."
		return nil
	}
	m.track = nil
	ctx, id := m.beginRequest()
	m.dialog = movieAccountDialog{mode: movieAccountWaiting}
	// Login both issues the prompt and polls until approval. Hand the prompt
	// back to Update independently so neither polling nor opening a browser
	// blocks input. Canceling the request releases both commands, including
	// when Login fails before it obtains a device code.
	prompts := make(chan movieDevicePromptMsg, 1)
	account := m.account
	return tea.Batch(func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		err := account.Login(ctx, func(link, code string) {
			select {
			case prompts <- movieDevicePromptMsg{id, ctx, link, code}:
			case <-ctx.Done():
			}
		})
		return movieAccountDoneMsg{id, err}
	}, func() tea.Msg {
		select {
		case prompt := <-prompts:
			return prompt
		case <-ctx.Done():
			return nil
		}
	})
}

func (m *moviePicker) awaitingLogin(id int) bool {
	return m.dialog.mode == movieAccountWaiting && m.cancel != nil && id == m.request
}

func (m *moviePicker) showDevicePrompt(msg movieDevicePromptMsg) tea.Cmd {
	if !m.awaitingLogin(msg.id) || m.dialog.url != "" {
		return nil
	}
	if !trakt.ValidDevicePrompt(msg.url, msg.code) {
		m.stopLoad()
		m.offerLogin()
		m.problem = "Trakt returned an invalid verification URL or code. Nothing was opened."
		return nil
	}
	m.dialog.url, m.dialog.code = msg.url, msg.code
	open := m.openURL
	if open == nil {
		return nil // the URL and user code are always usable manually
	}
	return func() tea.Msg {
		if msg.ctx.Err() != nil {
			return nil
		}
		return movieBrowserMsg{msg.id, open(msg.url)}
	}
}

func (m *moviePicker) accountDone(msg movieAccountDoneMsg) tea.Cmd {
	mode := m.dialog.mode
	if m.cancel == nil || msg.id != m.request || (mode != movieAccountWaiting && mode != movieAccountDisconnecting) {
		return nil
	}
	m.stopLoad()
	m.refreshAccount() // failed reauthorization need not invalidate the old account
	if msg.err != nil {
		if mode == movieAccountWaiting {
			m.offerLogin()
		} else {
			m.dialog = movieAccountDialog{}
		}
		m.problem = msg.err.Error()
		return nil
	}
	if mode == movieAccountWaiting && !m.connected {
		m.offerLogin()
		m.problem = "Trakt authorization finished without a connected account. Retry or continue browsing."
		return nil
	}
	if m.connected {
		m.notice = "Trakt connected. Use w for your watchlist or m to mark watched."
	} else {
		m.notice = "Trakt disconnected. You can still browse and play."
	}
	return m.closeAccount()
}

func (m *moviePicker) accountView() string {
	var lines []string
	switch m.dialog.mode {
	case movieAccountOffer:
		connect, skip := "Connect Trakt", "Continue without an account"
		if m.connected {
			connect, skip = "Reauthorize Trakt", "Keep current account"
		}
		lines = []string{TitleStyle.Render("Trakt account (optional)"), "Connect to save a watchlist and mark movies or episodes watched.", "Browsing and playback never require an account.", ""}
		if m.dialog.skip {
			lines = append(lines, "  "+connect, SelectedStyle.Render(CursorGlyph+skip))
		} else {
			lines = append(lines, SelectedStyle.Render(CursorGlyph+connect), "  "+skip)
		}
		lines = append(lines, HelpStyle.Render("↑/↓ choose · Enter select · Esc skip · q quit · : commands · Tab Anime"))
	case movieAccountWaiting:
		lines = []string{TitleStyle.Render("Connect your Trakt account")}
		if m.dialog.url == "" {
			lines = append(lines, "Requesting a device code…")
		} else {
			if m.dialog.browserFailed {
				lines = append(lines, "Couldn't open the default browser. Open this URL manually:")
			} else {
				lines = append(lines, "Verify in any browser:")
			}
			lines = append(lines, m.dialog.url, "Enter code: "+TitleStyle.Render(m.dialog.code), "Waiting for approval…")
		}
		lines = append(lines, HelpStyle.Render("Esc cancel · Tab Anime"))
	case movieAccountConfirmLogout:
		lines = []string{TitleStyle.Render("Disconnect Trakt from this device?"), "Your Trakt watchlist and history will not be removed.", HelpStyle.Render("y disconnect · n/Esc keep account")}
	case movieAccountDisconnecting:
		lines = []string{TitleStyle.Render("Disconnecting Trakt…")}
	}
	if m.problem != "" {
		lines = append(lines, ErrorStyle.Render(m.problem))
	}
	return strings.Join(lines, "\n")
}
