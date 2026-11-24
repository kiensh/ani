package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ani/internal/flixhq"
	"ani/internal/trakt"
	tea "github.com/charmbracelet/bubbletea"
)

type movieAccountStub struct {
	*trackerStub
	connected  bool
	login      func(context.Context, func(string, string)) error
	logoutErr  error
	logouts    int
	shelf      []trakt.Title
	shelfErr   error
	shelfCalls int
}

func (a *movieAccountStub) Watchlist(context.Context) ([]trakt.Title, error) {
	a.shelfCalls++
	return a.shelf, a.shelfErr
}
func (a *movieAccountStub) Connected() bool { return a.connected }
func (a *movieAccountStub) Login(ctx context.Context, prompt func(string, string)) error {
	return a.login(ctx, prompt)
}
func (a *movieAccountStub) Logout() error {
	a.logouts++
	if a.logoutErr == nil {
		a.connected = false
	}
	return a.logoutErr
}

// Run the independent tea commands the way Program does, so a fake Login can
// wait for the user's approval while the picker receives keys and renders.
func runMovieLogin(t *testing.T, cmd tea.Cmd) <-chan tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no login command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected device login and prompt listener")
	}
	out := make(chan tea.Msg, len(batch))
	for _, c := range batch {
		go func() { out <- c() }()
	}
	return out
}

func nextMovieEvent(t *testing.T, events <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-events:
		return msg
	case <-time.After(time.Second):
		t.Fatal("device flow blocked")
		return nil
	}
}

func chooseMovieCommand(t *testing.T, m *moviePicker, filter string) tea.Cmd {
	t.Helper()
	m.Update(movieKey(":"))
	if !m.palette.Active() {
		t.Fatal("palette not open")
	}
	for _, r := range filter {
		m.Update(movieKey(string(r)))
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.palette.Active() {
		t.Fatalf("no command matched %q", filter)
	}
	return cmd
}

func TestMovieWelcomeAndSourceRoundTrip(t *testing.T) {
	catalog := &movieFixture{titles: []flixhq.Title{{Name: "Original"}, {Name: "Remake"}}}
	a := &movieAccountStub{}
	state := &MovieState{}
	m := newMoviePicker("seed", catalog, a, nil, state)
	if m.Init() != nil || m.cancel != nil || catalog.lastQuery != "" {
		t.Fatal("signed-out entry should offer login before doing any work")
	}
	for _, part := range []string{"Source: Movies & TV", "Provider: FlixHQ", "Trakt: signed out", "Connect Trakt", "Continue without an account"} {
		if !strings.Contains(m.View(), part) {
			t.Errorf("missing %q on first entry: %s", part, m.View())
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, search := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if search == nil || m.dialog.mode != movieAccountClosed {
		t.Fatal("skip did not resume the initial query")
	}
	m.Update(search())
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, exit := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.source != MediaAnime || exit == nil || m.play {
		t.Fatal("Tab did not switch sources from --movies query results")
	}
	a.connected = true // simulate a change while viewing the other source
	back := newMoviePicker("another query", catalog, a, nil, state)
	if back.source != "" || back.Init() != nil || !back.connected || back.stage != movieTitles || back.cursor[movieTitles] != 1 || back.titles[1].Name != "Remake" || string(back.query) != "seed" {
		t.Fatalf("source round trip lost navigation or account refresh: %+v", back)
	}
	if cmd := chooseMovieCommand(t, back, "movies"); cmd != nil || back.source != "" {
		t.Fatal("selecting current source exited picker")
	}
	if cmd := chooseMovieCommand(t, back, "anime"); cmd == nil || back.source != MediaAnime {
		t.Fatal("Source > Anime did not leave the picker")
	}
	a.connected = false
	back = newMoviePicker("", catalog, a, nil, state)
	if back.dialog.mode != movieAccountClosed || back.connected {
		t.Fatal("returning to Movies repeated the optional welcome")
	}
	// The welcome itself must not trap someone who wanted the anime source.
	welcome := newMoviePicker("", nil, a, nil, nil)
	if cmd := chooseMovieCommand(t, welcome, "anime"); cmd == nil || welcome.source != MediaAnime || welcome.cancel != nil {
		t.Fatal("could not switch directly from the optional welcome")
	}
}

func TestMovieDeviceLoginAndTrackingWithoutRestart(t *testing.T) {
	a := &movieAccountStub{trackerStub: &trackerStub{results: []trakt.Title{{ID: 12, Kind: "movie", Name: "Example"}}}}
	approved := make(chan struct{})
	a.login = func(ctx context.Context, prompt func(string, string)) error {
		prompt("https://trakt.tv/activate", "TEST-CODE")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-approved:
			a.connected = true
			return nil
		}
	}
	var opened []string
	f := &movieFixture{recent: []flixhq.Title{{Kind: "movie", Name: "Example"}}}
	m := newMoviePicker("", f, a, func(u string) error {
		opened = append(opened, u)
		return errors.New("headless")
	}, nil)
	t.Cleanup(m.stopLoad)
	_, browse := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(browse())
	m.Update(movieKey("w"))
	_, login := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	events := runMovieLogin(t, login)
	_, open := m.Update(nextMovieEvent(t, events))
	view := m.View()
	for _, want := range []string{"https://trakt.tv/activate", "TEST-CODE", "Waiting for approval", "Esc cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in approval UI: %s", want, view)
		}
	}
	if open == nil || len(opened) != 0 {
		t.Fatal("opening the browser must be a separate nonblocking command")
	}
	m.Update(open())
	if len(opened) != 1 || opened[0] != "https://trakt.tv/activate" || !strings.Contains(m.View(), "Open this URL manually") {
		t.Fatal("failed browser launch hid manual verification")
	}
	close(approved)
	m.Update(nextMovieEvent(t, events))
	if !m.connected || m.cancel != nil || !strings.Contains(m.View(), "Trakt: connected") || a.watchlist != 0 || a.watched != 0 {
		t.Fatalf("login did not update status in place: %s", m.View())
	}
	_, find := m.Update(movieKey("w"))
	m.Update(find())
	_, write := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(write())
	if a.watchlist != 1 || a.id != 12 || m.stage != movieTitles {
		t.Fatal("watchlisting did not use the newly connected account")
	}
	if cmd := chooseMovieCommand(t, m, "disconnect"); cmd != nil || a.logouts != 0 || !strings.Contains(m.View(), "Disconnect Trakt from this device?") {
		t.Fatal("palette selection alone must not disconnect")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.logouts != 0 || !m.connected || m.dialog.mode != movieAccountClosed {
		t.Fatal("a second Enter should keep the account connected")
	}
	chooseMovieCommand(t, m, "disconnect")
	_, logout := m.Update(movieKey("y"))
	m.Update(logout())
	if a.logouts != 1 || m.connected || m.cancel != nil || !strings.Contains(m.View(), "Trakt: signed out") {
		t.Fatal("confirmed logout did not refresh picker immediately")
	}
	m.Update(movieKey("m"))
	if m.dialog.mode != movieAccountOffer || m.track != nil || a.watched != 0 {
		t.Fatal("tracking still enabled after disconnect")
	}
}

func TestMovieDeviceFlowCancelAndIgnoreSupersededEvents(t *testing.T) {
	a := &movieAccountStub{connected: true}
	a.login = func(ctx context.Context, prompt func(string, string)) error {
		prompt("https://auth.trakt.tv/activate", "FIRST")
		<-ctx.Done()
		return ctx.Err()
	}
	m := newMoviePicker("", &movieFixture{}, a, func(string) error { t.Fatal("opened a canceled browser request"); return nil }, nil)
	t.Cleanup(m.stopLoad)
	m.Update(m.Init()())
	_, reauth := m.Update(movieKey("L"))
	events := runMovieLogin(t, reauth)
	first := nextMovieEvent(t, events).(movieDevicePromptMsg)
	_, open := m.Update(first)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.dialog.mode != movieAccountClosed || open() != nil || m.cancel != nil {
		t.Fatal("cancel should stop polling and pending browser open")
	}
	staleDone := nextMovieEvent(t, events)
	retry := chooseMovieCommand(t, m, "reauthorize")
	if retry == nil || m.dialog.mode != movieAccountWaiting {
		t.Fatal("connected user could not reauthorize from the palette")
	}
	if _, cmd := m.Update(first); cmd != nil {
		t.Fatal("stale prompt tried to open a browser")
	}
	m.Update(staleDone)
	m.Update(movieBrowserMsg{first.id, errors.New("old browser error")})
	if m.dialog.mode != movieAccountWaiting || m.dialog.url != "" || m.dialog.browserFailed || m.problem != "" || !m.connected {
		t.Fatal("stale OAuth event changed a newer attempt")
	}
	events = runMovieLogin(t, retry)
	prompt := nextMovieEvent(t, events).(movieDevicePromptMsg)
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.source != MediaAnime || m.cancel != nil || prompt.ctx.Err() == nil {
		t.Fatal("switching source did not cancel device authorization")
	}
	m.Update(nextMovieEvent(t, events))
	if _, cmd := m.Update(prompt); cmd != nil {
		t.Fatal("stale callback after switching source opened a browser")
	}
}

func TestMovieDeviceFlowFailureAndUntrustedPrompt(t *testing.T) {
	a := &movieAccountStub{login: func(context.Context, func(string, string)) error { return errors.New("fake sign-in failure") }}
	f := &movieFixture{}
	m := newMoviePicker("", f, a, nil, nil)
	t.Cleanup(m.stopLoad)
	events := runMovieLogin(t, chooseMovieCommand(t, m, "connect"))
	m.Update(nextMovieEvent(t, events))
	if !strings.Contains(m.View(), "fake sign-in failure") || m.dialog.mode != movieAccountOffer || nextMovieEvent(t, events) != nil {
		t.Fatal("early login failure must release listener and offer retry or skip")
	}
	for _, p := range []struct{ url, code string }{
		{"https://trakt.tv.example.org/activate", "TEST"},
		{"https://other@trakt.tv/activate", "TEST"},
		{"http://trakt.tv/activate", "TEST"},
		{"https://trakt.tv/activate", "TEST\x1b[2J"},
	} {
		a.login = func(ctx context.Context, prompt func(string, string)) error {
			prompt(p.url, p.code)
			<-ctx.Done()
			return ctx.Err()
		}
		_, login := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		events = runMovieLogin(t, login)
		_, open := m.Update(nextMovieEvent(t, events))
		if open != nil || m.connected || strings.Contains(m.View(), p.url) || !strings.Contains(m.View(), "invalid verification") {
			t.Fatalf("untrusted prompt was accepted: %s", m.View())
		}
		m.Update(nextMovieEvent(t, events))
	}
	_, browse := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	finishMovieLoad(t, m, browse)
	if m.dialog.mode != movieAccountClosed || m.problem != "" || string(m.query) != "" || f.recentCalls != 1 {
		t.Fatal("unable to continue browsing after login failed")
	}
}

func TestMovieAccountErrorsKeepExistingConnection(t *testing.T) {
	a := &movieAccountStub{connected: true, logoutErr: errors.New("could not disconnect"), login: func(context.Context, func(string, string)) error { return errors.New("approval denied") }}
	m := newMoviePicker("", &movieFixture{}, a, nil, nil)
	t.Cleanup(m.stopLoad)
	finishMovieLoad(t, m, m.Init())
	events := runMovieLogin(t, chooseMovieCommand(t, m, "reauthorize"))
	m.Update(nextMovieEvent(t, events))
	if !m.connected || !strings.Contains(m.View(), "approval denied") || !strings.Contains(m.View(), "Keep current account") || nextMovieEvent(t, events) != nil {
		t.Fatal("failed reauthorization should preserve the previous account")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	chooseMovieCommand(t, m, "disconnect")
	_, logout := m.Update(movieKey("y"))
	m.Update(logout())
	if !m.connected || m.dialog.mode != movieAccountClosed || !strings.Contains(m.View(), "could not disconnect") {
		t.Fatal("failed local logout presented as success")
	}
}

func TestMovieInputAndPaletteHaveSeparateFocus(t *testing.T) {
	f := &movieFixture{}
	m := newMoviePicker("", f, &movieAccountStub{connected: true}, nil, nil)
	m.Update(movieKey("/")) // in the editor, even a leading L or ':' is literal
	for _, r := range "Léon: w/m q/L" {
		m.Update(movieKey(string(r)))
	}
	if string(m.query) != "Léon: w/m q/L" || !m.editing || m.dialog.mode != movieAccountClosed || m.palette.Active() {
		t.Fatalf("title input triggered shortcuts: %s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.palette.Active() {
		t.Fatal("commands inaccessible while typing")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	_, search := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(search())
	if f.lastQuery != "Léon: w/m q/L" {
		t.Fatal("opening palette lost the search text")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // leave text editing, not the source
	if cmd := chooseMovieCommand(t, m, "anime"); cmd == nil || m.source != MediaAnime {
		t.Fatal("':' did not work outside text editing")
	}
}
