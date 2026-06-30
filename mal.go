package main

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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nstratos/go-myanimelist/mal"
	"golang.org/x/oauth2"
)

// ---- credentials via .env ----

// loadDotenv reads KEY=VALUE lines from ./.env then ~/.config/ani/.env and sets
// any key not already in the environment. No external dependency.
func loadDotenv() {
	for _, p := range dotenvPaths() {
		applyDotenv(p)
	}
}

func dotenvPaths() []string {
	var paths []string
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".env"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "ani", ".env"))
	}
	return paths
}

func applyDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = dotenvUnquote(val)
		if key == "" {
			continue
		}
		if _, present := os.LookupEnv(key); !present {
			_ = os.Setenv(key, val)
		}
	}
}

func dotenvUnquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// ---- OAuth2 ----

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
)

// oauthHTTPClient authenticates (once per process) and returns an OAuth2-backed
// http.Client usable both for go-myanimelist and raw MAL calls.
func oauthHTTPClient() (*http.Client, error) {
	oauthOnce.Do(func() { oauthCli, oauthErr = buildOAuthHTTPClient() })
	return oauthCli, oauthErr
}

func buildOAuthHTTPClient() (*http.Client, error) {
	loadDotenv()
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
	verifier := randomString(32) // 32 bytes → base64url → 43 chars
	challenge := verifier        // plain: challenge IS the verifier
	state := randomString(16)

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
	stdin.ReadString('\n')
	openBrowser(authURL)
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
	if debugMode {
		fmt.Fprintf(os.Stderr, "DEBUG PKCE verifier=%s challenge=%s\n", verifier, challenge)
		fmt.Fprintf(os.Stderr, "DEBUG MAL token exchange: POST %s body=%s\n", conf.Endpoint.TokenURL, redact(v))
	}
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

// redact masks sensitive values in a url.Values for debug printing.
func redact(v url.Values) string {
	out := url.Values{}
	for k, vs := range v {
		for _, s := range vs {
			if k == "client_secret" || k == "code" || k == "code_verifier" {
				s = "[" + strconv.Itoa(len(s)) + " chars]"
			}
			out.Add(k, s)
		}
	}
	return out.Encode()
}

func openBrowser(url string) {
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

// randomString returns a base64url-encoded random string of n bytes.
func randomString(n int) string {
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func malClient() (*mal.Client, error) {
	hc, err := oauthHTTPClient()
	if err != nil {
		return nil, err
	}
	return mal.NewClient(hc), nil
}

// ---- MAL item model ----

type MALItem struct {
	MalID      int
	Title      string
	CoverURL   string
	TotalEps   int
	WatchedEps int
	AirStatus  string
	Score      int
}

func userAnimeToItem(ua mal.UserAnime) MALItem {
	a := ua.Anime
	cover := a.MainPicture.Large
	if cover == "" {
		cover = a.MainPicture.Medium
	}
	return MALItem{
		MalID:      a.ID,
		Title:      a.Title,
		CoverURL:   cover,
		TotalEps:   a.NumEpisodes,
		AirStatus:  a.Status,
		WatchedEps: ua.Status.NumEpisodesWatched,
		Score:      ua.Status.Score,
	}
}

func animeToItem(a mal.Anime) MALItem {
	cover := a.MainPicture.Large
	if cover == "" {
		cover = a.MainPicture.Medium
	}
	return MALItem{MalID: a.ID, Title: a.Title, CoverURL: cover, TotalEps: a.NumEpisodes, AirStatus: a.Status,
		WatchedEps: a.MyListStatus.NumEpisodesWatched, Score: a.MyListStatus.Score}
}

// ---- operations ----

func malMyList(status mal.AnimeStatus) ([]MALItem, error) {
	c, err := malClient()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	const pageSize = 1000
	base := []mal.AnimeListOption{
		mal.Fields{"title", "main_picture", "num_episodes", "status", "my_list_status"},
		mal.NSFW(true),
		mal.Limit(pageSize),
	}
	if status != "" {
		base = append(base, status)
	}
	if debugMode {
		fmt.Fprintf(os.Stderr, "DEBUG MAL GET /users/@me/animelist status=%s\n", status)
	}
	var out []MALItem
	for offset := 0; ; offset += pageSize {
		page, _, err := c.User.AnimeList(ctx, "@me", append(base, mal.Offset(offset))...)
		if err != nil {
			return nil, err
		}
		for _, ua := range page {
			out = append(out, userAnimeToItem(ua))
		}
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

func malSearch(q string) ([]MALItem, error) {
	c, err := malClient()
	if err != nil {
		return nil, err
	}
	if debugMode {
		fmt.Fprintf(os.Stderr, "DEBUG MAL GET /anime?q=%s\n", q)
	}
	anime, _, err := c.Anime.List(context.Background(), q,
		mal.Fields{"title", "main_picture", "num_episodes", "status", "my_list_status"}, mal.Limit(20))
	if err != nil {
		return nil, err
	}
	out := make([]MALItem, 0, len(anime))
	for _, a := range anime {
		out = append(out, animeToItem(a))
	}
	return out, nil
}

// malAnidbAid resolves the AniDB id for a MAL anime via Jikan (which mirrors
// MAL's external links — the MAL v2 API doesn't expose external links).
func malAnidbAid(malID int) (int, error) {
	url := fmt.Sprintf("https://api.jikan.moe/v4/anime/%d/external", malID)
	if debugMode {
		fmt.Fprintf(os.Stderr, "DEBUG Jikan GET %s\n", url)
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("jikan external: %s", resp.Status)
	}
	var d struct {
		Data []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return 0, err
	}
	if debugMode {
		fmt.Fprintf(os.Stderr, "DEBUG Jikan external for mal/%d:\n", malID)
		for _, e := range d.Data {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", e.Name, e.URL)
		}
	}
	for _, e := range d.Data {
		if !strings.EqualFold(e.Name, "AniDB") && !strings.Contains(e.URL, "anidb.net") {
			continue
		}
		if aid := parseAnidbAidFromURL(e.URL); aid > 0 {
			return aid, nil
		}
	}
	return 0, nil
}

// parseAnidbAidFromURL extracts the numeric aid from an AniDB URL, handling
// both the old format (?aid=12345) and the new format (/anime/12345).
func parseAnidbAidFromURL(rawurl string) int {
	for _, prefix := range []string{"aid=", "/anime/"} {
		if i := strings.Index(rawurl, prefix); i >= 0 {
			s := rawurl[i+len(prefix):]
			var n int
			for _, ch := range s {
				if ch < '0' || ch > '9' {
					break
				}
				n = n*10 + int(ch-'0')
			}
			if n > 0 {
				return n
			}
		}
	}
	return 0
}

// malUpdateProgress increments the watched-episode count on MAL and sets status
// to watching. Skipped (printed only) in debug mode.
func malUpdateProgress(malID, watchedEps int) error {
	if dryRunMode {
		fmt.Fprintf(os.Stderr, "DRY-RUN: MAL PATCH /anime/%d num_episodes_watched=%d status=watching (not sent)\n", malID, watchedEps)
		return nil
	}
	c, err := malClient()
	if err != nil {
		return err
	}
	_, _, err = c.Anime.UpdateMyListStatus(context.Background(), malID,
		mal.AnimeStatusWatching, mal.NumEpisodesWatched(watchedEps))
	return err
}
