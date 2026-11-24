package trakt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxDeviceWait = 30 * time.Minute
	maxTokenBytes = 64 << 10
)

var errCodeExpired = errors.New("Trakt device code expired; start login again")

type deviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

type token struct {
	ClientID     string `json:"client_id,omitempty"` // local binding, set by saveToken
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	CreatedAt    int64  `json:"created_at"`
}

// Login presents the user-facing activation URL/code only; the device code
// stays private. Contract: github.com/trakt/trakt-api, projects/api/src/contracts/oauth.
func (c *Client) Login(ctx context.Context, prompt func(verificationURL, userCode string)) error {
	if err := c.ready(ctx); err != nil {
		return err
	}
	if !filepath.IsAbs(c.tokenPath) {
		return errors.New("Trakt: cannot locate the user config directory")
	}
	if prompt == nil {
		return errors.New("Trakt: a device authorization prompt is required")
	}
	var grant deviceCode
	if _, err := c.request(ctx, c.authBase+"/oauth/device/code", http.MethodPost, map[string]string{"client_id": c.clientID}, &grant, ""); err != nil {
		return fmt.Errorf("request device code: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if grant.DeviceCode == "" || grant.Interval <= 0 || grant.ExpiresIn <= 0 || !ValidDevicePrompt(grant.VerificationURL, grant.UserCode) {
		return errors.New("Trakt: invalid device authorization response")
	}
	// Clamp before converting seconds to a duration; never poll indefinitely
	// even if the server (or a proxy) sends absurd lifetimes or Retry-After.
	boundedSeconds := func(s int64) time.Duration {
		return time.Duration(min(s, int64(maxDeviceWait/time.Second))) * time.Second
	}
	interval := boundedSeconds(grant.Interval)
	expires := time.Now().Add(boundedSeconds(grant.ExpiresIn))
	pollCtx, cancel := context.WithDeadlineCause(ctx, expires, errCodeExpired)
	defer cancel()
	prompt(grant.VerificationURL, grant.UserCode)
	body := map[string]string{"client_id": c.clientID, "code": grant.DeviceCode}
	for {
		select {
		case <-pollCtx.Done():
			return context.Cause(pollCtx)
		case <-time.After(interval):
		}
		if err := pollCtx.Err(); err != nil {
			return context.Cause(pollCtx)
		}
		if !time.Now().Before(expires) {
			return errCodeExpired
		}
		var t token
		_, err := c.request(pollCtx, c.authBase+"/oauth/device/token", http.MethodPost, body, &t, "")
		if pollCtx.Err() != nil {
			return context.Cause(pollCtx)
		}
		if err == nil {
			return withTokenWrite(pollCtx, func() error { return c.saveToken(t) })
		}
		var status *statusError
		if !errors.As(err, &status) {
			return fmt.Errorf("poll device authorization: %w", err)
		}
		switch status.StatusCode {
		case http.StatusBadRequest: // Pending; retain the current interval.
		case http.StatusTooManyRequests: // RFC 8628: back off all subsequent polls.
			interval = min(maxDeviceWait, max(interval+5*time.Second, status.retryAfter))
		case http.StatusNotFound:
			return fmt.Errorf("invalid Trakt device code; start login again: %w", err)
		case http.StatusConflict:
			return fmt.Errorf("Trakt device code already used; start login again: %w", err)
		case http.StatusGone:
			return errCodeExpired
		case http.StatusTeapot:
			return fmt.Errorf("Trakt device authorization denied: %w", err)
		default:
			return fmt.Errorf("poll device authorization: %w", err)
		}
	}
}

// ValidDevicePrompt checks both fields before displaying a code or launching
// the browser. Only HTTPS Trakt URLs and terminal-safe user codes are accepted.
func ValidDevicePrompt(link, code string) bool {
	if len(link) > 1024 || strings.IndexFunc(link, func(r rune) bool { return r < '!' || r > '~' || r == '\\' }) >= 0 {
		return false
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Host != "trakt.tv" && u.Host != "auth.trakt.tv" && u.Host != "app.trakt.tv") {
		return false
	}
	if len(code) == 0 || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '-') {
			return false
		}
	}
	return true
}

func (t token) valid() bool {
	return t.AccessToken != "" && strings.EqualFold(t.TokenType, "bearer") &&
		t.CreatedAt > 0 && t.ExpiresIn > 0 && t.CreatedAt <= math.MaxInt64-t.ExpiresIn
}

func (t token) usable(rejected string) bool {
	return t.AccessToken != rejected && time.Now().Unix() < t.CreatedAt+t.ExpiresIn
}

// Connected checks only local state. An expired access token can be used again
// after refresh, and doesn't mean the user needs to log in again.
func (c *Client) Connected() bool {
	t, err := c.loadToken()
	return err == nil && (t.usable("") || t.RefreshToken != "")
}

// Serialize rotation within the process, including across Client instances: a
// refresh token is single-use. Readers see old or new data via atomic rename.
var tokenWrite = make(chan struct{}, 1)

func withTokenWrite(ctx context.Context, f func() error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case tokenWrite <- struct{}{}:
		defer func() { <-tokenWrite }()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f()
}

func (c *Client) accessToken(ctx context.Context, rejected string) (string, error) {
	t, err := c.loadToken()
	if err != nil {
		return "", err
	}
	if t.usable(rejected) {
		return t.AccessToken, nil
	}
	err = withTokenWrite(ctx, func() error {
		// A concurrent request may have refreshed or logged out while we waited.
		current, err := c.loadToken()
		if err != nil {
			return err
		}
		if current.usable(rejected) {
			t = current
			return nil
		}
		if current.RefreshToken == "" {
			return errors.New("Trakt: no refresh token; sign in again")
		}
		body := map[string]string{"client_id": c.clientID, "grant_type": "refresh_token",
			"refresh_token": current.RefreshToken, "redirect_uri": c.redirectURI}
		var next token
		if _, err := c.request(ctx, c.authBase+"/oauth/token", http.MethodPost, body, &next, ""); err != nil {
			return fmt.Errorf("refresh Trakt token: %w", err)
		}
		// Persist both rotated tokens before allowing another request through.
		t = next
		return c.saveToken(next)
	})
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

func (c *Client) loadToken() (token, error) {
	var t token
	if !filepath.IsAbs(c.tokenPath) {
		return t, errors.New("Trakt: cannot locate the user config directory")
	}
	info, err := os.Lstat(c.tokenPath)
	if errors.Is(err, os.ErrNotExist) {
		return t, errors.New("Trakt: not connected; sign in first")
	}
	if err != nil {
		return t, fmt.Errorf("check Trakt token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return t, errors.New("Trakt: token file must be a private regular file (0600)")
	}
	f, err := os.Open(c.tokenPath)
	if err != nil {
		return t, fmt.Errorf("open Trakt token file: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 {
		return t, errors.New("Trakt: could not open a private token file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTokenBytes+1))
	if err != nil || len(data) > maxTokenBytes || json.Unmarshal(data, &t) != nil || !t.valid() {
		return token{}, errors.New("Trakt: invalid local token; sign in again")
	}
	if t.ClientID != "" && c.clientID != "" && t.ClientID != c.clientID {
		return token{}, errors.New("Trakt: stored token belongs to a different Trakt app; sign in again")
	}
	return t, nil
}

func (c *Client) saveToken(t token) error {
	if !t.valid() || t.RefreshToken == "" || !t.usable("") {
		return errors.New("Trakt: invalid token response; session not saved")
	}
	t.ClientID = c.clientID // Never trust a server-supplied binding.
	data, err := json.Marshal(t)
	if err != nil || len(data) > maxTokenBytes {
		return errors.New("Trakt: invalid token response; session not saved")
	}
	dir := filepath.Dir(c.tokenPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Trakt config directory: %w", err)
	}
	// CreateTemp creates the file with 0600 before a single token byte is
	// written. Keep the temp file alongside the target so rename is atomic.
	f, err := os.CreateTemp(dir, ".trakt-token-*")
	if err != nil {
		return fmt.Errorf("create private Trakt token file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), c.tokenPath)
	}
	if err != nil {
		return fmt.Errorf("save Trakt token file: %w", err)
	}
	return nil
}

// Logout forgets local credentials, even if expired or corrupt. No Trakt
// revocation or other network call is made, and an absent file is a success.
func (c *Client) Logout() error {
	return withTokenWrite(context.Background(), func() error {
		if !filepath.IsAbs(c.tokenPath) {
			return errors.New("Trakt: cannot locate the user config directory")
		}
		err := os.Remove(c.tokenPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("forget Trakt token: %w", err)
		}
		return nil
	})
}
