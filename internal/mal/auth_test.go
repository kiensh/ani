package mal

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// authHarness points malBrowserAuth at a free local port, a fake token
// endpoint, and a controllable Enter source, and captures the printed auth
// URL from stderr. Returns the oauth2 config, the Enter trigger, a func that
// waits for the printed URL, and how many times the browser opener ran.
func authHarness(t *testing.T) (conf *oauth2.Config, pressEnter func(), printedURL func() string, opened *int32) {
	t.Helper()

	// A free port for the redirect listener.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "the-code" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"tok","refresh_token":"ref","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(tokenSrv.Close)

	enterCh := make(chan struct{}, 1)
	opened = new(int32)
	oldAddr, oldWatch, oldOpen := authListenAddr, authWatchEnter, authOpenURL
	authListenAddr = addr
	authWatchEnter = func() (<-chan struct{}, func()) { return enterCh, func() {} }
	authOpenURL = func(string) { atomic.AddInt32(opened, 1) }

	// Capture stderr to read the printed auth URL.
	oldStderr := os.Stderr
	pr, pw, _ := os.Pipe()
	os.Stderr = pw
	urlCh := make(chan string, 1)
	go func() {
		b := make([]byte, 0, 4096)
		buf := make([]byte, 1024)
		re := regexp.MustCompile(`https?://\S+state=\S+`)
		for {
			n, err := pr.Read(buf)
			b = append(b, buf[:n]...)
			if m := re.Find(b); m != nil {
				urlCh <- string(m)
				io.Copy(io.Discard, pr)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		os.Stderr = oldStderr
		pw.Close()
		authListenAddr, authWatchEnter, authOpenURL = oldAddr, oldWatch, oldOpen
	})

	conf = &oauth2.Config{
		ClientID:     "id",
		ClientSecret: "secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://myanimelist.example/authorize",
			TokenURL: tokenSrv.URL,
		},
		RedirectURL: "http://" + addr,
	}
	return conf, func() { enterCh <- struct{}{} }, func() string {
		select {
		case u := <-urlCh:
			return u
		case <-time.After(5 * time.Second):
			t.Fatal("auth URL never printed")
			return ""
		}
	}, opened
}

// redirect simulates MAL's redirect back to the local server with the auth
// URL's state.
func redirect(t *testing.T, conf *oauth2.Config, authURL, code string) *http.Response {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	resp, err := http.Get(fmt.Sprintf("%s/?code=%s&state=%s", conf.RedirectURL, code, url.QueryEscape(state)))
	if err != nil {
		t.Fatalf("redirect hit a closed port (the bug): %v", err)
	}
	return resp
}

// TestBrowserAuthListensBeforeEnter: the redirect server must already be
// listening when the auth URL is printed — a user who opens the URL themselves
// and logs in without ever pressing Enter used to land on a closed port,
// because the server only started after Enter.
func TestBrowserAuthListensBeforeEnter(t *testing.T) {
	conf, _, printedURL, opened := authHarness(t)

	type result struct {
		tok *oauth2.Token
		err error
	}
	done := make(chan result, 1)
	go func() {
		tok, err := malBrowserAuth(conf)
		done <- result{tok, err}
	}()

	authURL := printedURL()
	resp := redirect(t, conf, authURL, "the-code") // no Enter pressed
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "authorized") {
		t.Fatalf("redirect = %d %q, want 200 + authorized page", resp.StatusCode, body)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("malBrowserAuth: %v", r.err)
		}
		if r.tok.AccessToken != "tok" {
			t.Fatalf("token = %+v, want access token from the exchange", r.tok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("malBrowserAuth never returned after the redirect")
	}
	if n := atomic.LoadInt32(opened); n != 0 {
		t.Errorf("browser opened %d times without Enter, want 0", n)
	}
}

// TestBrowserAuthEnterOpensBrowserAndKeepsWaiting: Enter opens the browser and
// the flow keeps waiting for the redirect; a stray request with the wrong
// state is rejected without ending the flow.
func TestBrowserAuthEnterOpensBrowserAndKeepsWaiting(t *testing.T) {
	conf, pressEnter, printedURL, opened := authHarness(t)

	done := make(chan error, 1)
	go func() {
		_, err := malBrowserAuth(conf)
		done <- err
	}()

	authURL := printedURL()
	pressEnter()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(opened) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(opened) != 1 {
		t.Fatalf("Enter did not open the browser")
	}

	// A stray request (wrong state) is rejected but doesn't end the flow.
	stray, err := http.Get(conf.RedirectURL + "/?code=x&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	stray.Body.Close()
	if stray.StatusCode != http.StatusBadRequest {
		t.Errorf("stray request = %d, want 400", stray.StatusCode)
	}
	select {
	case err := <-done:
		t.Fatalf("flow ended on a stray request: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	resp := redirect(t, conf, authURL, "the-code")
	resp.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("malBrowserAuth: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("malBrowserAuth never returned after the redirect")
	}
}

// TestBrowserAuthPortBusy: a busy redirect port fails up front with a clear
// error instead of printing a URL whose redirect can never land.
func TestBrowserAuthPortBusy(t *testing.T) {
	conf, _, _, _ := authHarness(t)
	busy, err := net.Listen("tcp", authListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	if _, err := malBrowserAuth(conf); err == nil || !strings.Contains(err.Error(), "listen for the MAL redirect") {
		t.Fatalf("malBrowserAuth on a busy port = %v, want a listen error", err)
	}
}
