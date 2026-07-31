package mal

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	// Tokens written before the Expiry fix have a zero expiry. The oauth2 library
	// treats a zero Expiry as "never expires", so the refresh path never fired and
	// the (now-stale) access token was reused forever. Force a past expiry so
	// reuseTokenSource refreshes using the stored refresh_token on the next call —
	// self-healing if the refresh_token is still valid. (ExpiresIn can't be used
	// here: it's the token lifetime, not the time remaining.)
	if tok != nil && tok.Expiry.IsZero() && tok.RefreshToken != "" {
		tok.Expiry = time.Now().Add(-time.Hour)
	}

	if tok == nil {
		t, err := malBrowserAuth(&conf)
		if err != nil {
			return nil, err
		}
		tok = t
		_ = saveToken(tokenPath, tok)
	}

	src := &fileTokenSource{base: conf.TokenSource(context.Background(), tok), path: tokenPath}
	return oauth2.NewClient(context.Background(), src), nil
}

// saveToken writes tok (0600) to path, creating the parent dir (0700).
func saveToken(path string, tok *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// resetOAuthClient clears the memoized OAuth client so the next Client() rebuilds
// from the token file. Called by Login/Logout, which run from app.Run's loop after
// the TUI has exited (no concurrent Client() call); without this the cached client
// would keep holding the old (dead) token.
func resetOAuthClient() {
	oauthOnce = sync.Once{}
	oauthCli = nil
	oauthErr = nil
}

// Login forces a fresh browser OAuth flow: any existing token is removed first so
// the user re-authorizes, then the new token is saved and the OAuth client cache is
// reset so the next request uses it. debug controls the PKCE/exchange logging.
func Login(debug bool) error {
	authDebug = debug
	config.LoadDotenv()
	id, secret, ok := malCreds()
	if !ok {
		return fmt.Errorf("MAL credentials not set — put ANI_MAL_CLIENT_ID and ANI_MAL_CLIENT_SECRET in ./.env or ~/.config/ani/.env")
	}
	conf := malOAuth2
	conf.ClientID = id
	conf.ClientSecret = secret
	_ = os.Remove(malTokenPath()) // ignore "not exist"; we want a clean re-auth
	tok, err := malBrowserAuth(&conf)
	if err != nil {
		resetOAuthClient()
		return err
	}
	if err := saveToken(malTokenPath(), tok); err != nil {
		resetOAuthClient()
		return fmt.Errorf("save token: %w", err)
	}
	resetOAuthClient()
	return nil
}

// Logout removes the saved token (no error if there isn't one) and drops any cached
// OAuth client so a later login/rebuild starts clean.
func Logout() error {
	if err := os.Remove(malTokenPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	resetOAuthClient()
	return nil
}

// AuthStatusResult is the offline view of the saved MAL session.
type AuthStatusResult struct {
	LoggedIn    bool      // creds present AND an access token is on disk
	Subject     string    // MAL user id from the access-token JWT "sub" claim
	Expiry      time.Time // access-token expiry from the JWT "exp" claim (zero if unknown)
	ExpiryKnown bool
}

// AuthStatus reads the saved token and decodes the access-token JWT for an offline
// snapshot of the session (no network, no browser). The JWT exp is the source of
// truth for expiry: the on-disk "expiry" field is zero for tokens written before the
// fix. A missing/unreadable token file is not an error — it just means LoggedIn is
// false.
func AuthStatus() (AuthStatusResult, error) {
	config.LoadDotenv()
	_, _, credsOK := malCreds()
	var res AuthStatusResult
	data, err := os.ReadFile(malTokenPath())
	if err != nil {
		return res, nil
	}
	var tok oauth2.Token
	if json.Unmarshal(data, &tok) != nil {
		return res, nil
	}
	res.LoggedIn = credsOK && tok.AccessToken != ""
	res.Subject, res.Expiry, res.ExpiryKnown = decodeJWTClaims(tok.AccessToken)
	return res, nil
}

// decodeJWTClaims extracts the "sub" and "exp" claims from a JWT access token
// without verifying the signature (we only display these). Returns known=false if
// the token isn't a 3-part JWT or the payload won't decode.
func decodeJWTClaims(token string) (sub string, exp time.Time, known bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", time.Time{}, false
	}
	var claims struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", time.Time{}, false
	}
	return claims.Sub, time.Unix(claims.Exp, 0).UTC(), true
}

// WhoAmI calls the MAL API for the authenticated user's profile (best-effort name
// for the status overlay). Triggers refresh/network; returns an error on 401.
func WhoAmI(debug bool) (*mal.User, error) {
	c, err := Client(debug)
	if err != nil {
		return nil, err
	}
	u, _, err := c.User.MyInfo(context.Background())
	if err != nil {
		return nil, err
	}
	return u, nil
}

// IsAuthError reports whether err looks like a MAL authentication failure: an
// expired/rejected token (HTTP 401/403 from the API) or a failed token refresh.
// Used by the TUI to hint "press L to log in" instead of showing a bare error.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) { // oauth2 refresh / token-endpoint failure
		return true
	}
	var mer *mal.ErrorResponse
	if errors.As(err, &mer) && mer.Response != nil &&
		(mer.Response.StatusCode == 401 || mer.Response.StatusCode == 403) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "invalid_grant") ||
		strings.Contains(s, "invalid refresh token")
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
	// The oauth2 library never populates Expiry from the wire "expires_in" on a
	// plain Unmarshal — only inside its own token retrieval. Set it ourselves so
	// reuseTokenSource refreshes (via the refresh_token) before the access token
	// expires, instead of treating it as valid forever.
	if tok.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
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
