package mal

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"ani/internal/config"

	"github.com/nstratos/go-myanimelist/mal"
	"golang.org/x/oauth2"
)

// readAuthLine blocks for one line on stdin (used during the one-time OAuth
// flow to wait for the user to press Enter before opening the browser).
func readAuthLine() {
	bufio.NewReader(os.Stdin).ReadString('\n')
}

const malRedirectPort = "8484"

var malOAuth2 = oauth2.Config{
	Endpoint: oauth2.Endpoint{
		AuthURL:   "https://myanimelist.net/v1/oauth2/authorize",
		TokenURL:  "https://myanimelist.net/v1/oauth2/token",
		AuthStyle: oauth2.AuthStyleInParams,
	},
	RedirectURL: "http://localhost:" + malRedirectPort,
}

func malCreds() (id, secret string, ok bool) {
	id = os.Getenv("ANI_MAL_CLIENT_ID")
	secret = os.Getenv("ANI_MAL_CLIENT_SECRET")
	return id, secret, id != "" && secret != ""
}

func malTokenPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ani", "mal-token.json")
}

// fileTokenSource wraps a token source and persists any refreshed token to disk.
type fileTokenSource struct {
	base oauth2.TokenSource
	path string
}

func (f *fileTokenSource) Token() (*oauth2.Token, error) {
	t, err := f.base.Token()
	if err != nil {
		return nil, err
	}
	if data, err := json.Marshal(t); err == nil {
		_ = os.WriteFile(f.path, data, 0o600)
	}
	return t, nil
}

var (
	oauthOnce sync.Once
	oauthCli  *http.Client
	oauthErr  error
	authDebug bool
)

// LoggedIn reports whether a usable MAL session exists WITHOUT triggering the
// browser OAuth flow: environment credentials are present AND a token file with
// a non-empty access token is on disk. Use this to choose between the MAL UI
// and the AnimeTosho fallback so `./ani` never unexpectedly opens a browser.
// (A statically-valid token may still fail to refresh later; that surfaces as a
// normal MAL error at call time.)
func LoggedIn() bool {
	config.LoadDotenv()
	if _, _, ok := malCreds(); !ok {
		return false
	}
	data, err := os.ReadFile(malTokenPath())
	if err != nil {
		return false
	}
	var t oauth2.Token
	if json.Unmarshal(data, &t) != nil {
		return false
	}
	return t.AccessToken != ""
}

// Client returns the go-myanimelist client, authenticating once per process
// (PKCE browser flow on first run, cached token afterwards). debug controls the
// verbose PKCE/token-exchange logging on the initial auth.
func Client(debug bool) (*mal.Client, error) {
	hc, err := OAuthHTTPClient(debug)
	if err != nil {
		return nil, err
	}
	return mal.NewClient(hc), nil
}

// OAuthHTTPClient authenticates (once per process) and returns an OAuth2-backed
// http.Client usable both for go-myanimelist and raw MAL calls.
func OAuthHTTPClient(debug bool) (*http.Client, error) {
	authDebug = debug
	oauthOnce.Do(func() { oauthCli, oauthErr = buildOAuthHTTPClient() })
	return oauthCli, oauthErr
}

func buildOAuthHTTPClient() (*http.Client, error) {
	config.LoadDotenv()
	id, secret, ok := malCreds()
	if !ok {
		return nil, fmt.Errorf("MAL credentials not set — put ANI_MAL_CLIENT_ID and ANI_MAL_CLIENT_SECRET in ./.env or ~/.config/ani/.env")
	}
	conf := malOAuth2
	conf.ClientID = id
	conf.ClientSecret = secret

	tokenPath := malTokenPath()
	var tok *oauth2.Token
	if data, err := os.ReadFile(tokenPath); err == nil {
		t := &oauth2.Token{}
		if json.Unmarshal(data, t) == nil && t.AccessToken != "" {
			tok = t
		}
	}

	if tok == nil {
		t, err := malBrowserAuth(&conf)
		if err != nil {
			return nil, err
		}
		tok = t
		_ = os.MkdirAll(filepath.Dir(tokenPath), 0o700)
		if data, err := json.Marshal(tok); err == nil {
			_ = os.WriteFile(tokenPath, data, 0o600)
		}
	}

	src := &fileTokenSource{base: conf.TokenSource(context.Background(), tok), path: tokenPath}
	return oauth2.NewClient(context.Background(), src), nil
}

// malBrowserAuth runs the one-time OAuth2 PKCE flow: print the auth URL, catch
// the redirect on a local server, exchange the code. PKCE is generated
// manually (not via the oauth2 library helpers) for full control.
func malBrowserAuth(conf *oauth2.Config) (*oauth2.Token, error) {
	// MAL only supports PKCE "plain" (code_challenge = code_verifier directly,
	// no S256 hashing). See https://myanimelist.net/apiconfig/references/authorization
	verifier := RandomString(32) // 32 bytes → base64url → 43 chars
	challenge := verifier        // plain: challenge IS the verifier
	state := RandomString(16)

	// Build auth URL manually.
	q := url.Values{}
	q.Set("client_id", conf.ClientID)
	q.Set("redirect_uri", conf.RedirectURL)
	q.Set("response_type", "code")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "plain")
	q.Set("state", state)
	authURL := conf.Endpoint.AuthURL + "?" + q.Encode()

	fmt.Fprintf(os.Stderr, "\nAuthorize ani with MyAnimeList — open this URL in any browser:\n\n  %s\n\nPress Enter to open in default browser, or open the URL manually…\n", authURL)
	readAuthLine()
	OpenBrowser(authURL)
	fmt.Fprintln(os.Stderr, "Waiting for authorization…")

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "auth error: "+e, http.StatusBadRequest)
			errCh <- fmt.Errorf("auth error: %s", e)
			return
		}
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("oauth state mismatch")
			return
		}
		fmt.Fprintln(w, "ani authorized with MyAnimeList. You can close this tab and return to the terminal.")
		codeCh <- q.Get("code")
	})
	srv := &http.Server{Addr: ":" + malRedirectPort, Handler: mux}
	go srv.ListenAndServe()
	defer srv.Shutdown(context.Background())

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for MAL authorization")
	}
	// Manual token exchange (full control over every param, avoids any
	// oauth2-library quirks).
	v := url.Values{}
	v.Set("client_id", conf.ClientID)
	v.Set("client_secret", conf.ClientSecret)
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("code_verifier", verifier)
	v.Set("redirect_uri", conf.RedirectURL)
	dbg(authDebug, "DEBUG PKCE verifier=%s challenge=%s\n", verifier, challenge)
	dbg(authDebug, "DEBUG MAL token exchange: POST %s body=%s\n", conf.Endpoint.TokenURL, Redact(v))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		conf.Endpoint.TokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange: HTTP %d: %s", resp.StatusCode, string(body))
	}
	tok := &oauth2.Token{}
	if err := json.Unmarshal(body, tok); err != nil {
		return nil, fmt.Errorf("token exchange: decode: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token exchange: empty access token in response: %s", string(body))
	}
	return tok, nil
}

// Redact masks sensitive values in a url.Values for debug printing.
func Redact(v url.Values) string {
	out := url.Values{}
	for k, vs := range v {
		for _, s := range vs {
			if k == "client_secret" || k == "code" || k == "code_verifier" {
				s = "[" + fmt.Sprintf("%d", len(s)) + " chars]"
			}
			out.Add(k, s)
		}
	}
	return out.Encode()
}

// OpenBrowser launches url in the platform default browser (best-effort).
func OpenBrowser(url string) {
	var bin string
	switch runtime.GOOS {
	case "darwin":
		bin = "open"
	case "windows":
		bin = "rundll32"
	default:
		bin = "xdg-open"
	}
	if bin == "rundll32" {
		_ = exec.Command(bin, "url.dll,FileProtocolHandler", url).Start()
		return
	}
	_ = exec.Command(bin, url).Start()
}

// RandomString returns a base64url-encoded random string of n bytes.
func RandomString(n int) string {
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
