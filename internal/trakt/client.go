// Package trakt tracks movies and TV separately from anime tracking.
package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ani/internal/config"
)

const (
	apiBaseURL  = "https://api.trakt.tv"
	authBaseURL = "https://auth.trakt.tv"
	// The refresh contract requires the app's registered URI even for a device
	// grant. Nothing listens on this URL during device authentication.
	defaultRedirectURI = "http://127.0.0.1:8765/callback"
	requestTimeout     = 15 * time.Second
	maxResponseBytes   = 8 << 20
)

// Client uses a public client ID; it never needs a client secret.
type Client struct {
	clientID    string
	apiBase     string
	authBase    string
	redirectURI string
	tokenPath   string
	httpClient  *http.Client
}

// New reads configuration at runtime; it performs no network requests.
func New() *Client {
	config.LoadDotenv()
	var path string // Never fall back to storing a token in the working directory.
	if dir, err := os.UserConfigDir(); err == nil && filepath.IsAbs(dir) {
		path = filepath.Join(dir, "ani", "trakt-token.json")
	}
	c := newClient(os.Getenv("TRAKT_CLIENT_ID"), apiBaseURL, path, nil)
	c.authBase = authBaseURL
	if uri := strings.TrimSpace(os.Getenv("TRAKT_REDIRECT_URI")); uri != "" {
		c.redirectURI = uri
	}
	return c
}

// newClient directs both API and OAuth calls to baseURL for offline tests.
func newClient(clientID, baseURL, tokenPath string, httpClient *http.Client) *Client {
	var h http.Client
	if httpClient != nil {
		h = *httpClient
	}
	if h.Timeout <= 0 || h.Timeout > requestTimeout {
		h.Timeout = requestTimeout
	}
	// A redirect could expose the API key, bearer, or a replayable OAuth POST
	// body. Reject even same-origin redirects rather than forwarding secrets.
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{clientID: strings.TrimSpace(clientID), apiBase: baseURL, authBase: baseURL,
		redirectURI: defaultRedirectURI, tokenPath: tokenPath, httpClient: &h}
}

// do makes an API call. Only an explicit 401 on an authenticated request is
// retried; never replay a mutation on a timeout, rate limit, or ambiguous 5xx.
func (c *Client) do(ctx context.Context, method, path string, body, out any, auth bool) error {
	_, err := c.doWithHeaders(ctx, method, path, body, out, auth)
	return err
}

// doWithHeaders also returns the final API response headers (e.g. pagination).
func (c *Client) doWithHeaders(ctx context.Context, method, path string, body, out any, auth bool) (http.Header, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || u.IsAbs() || strings.ContainsAny(path, `\#`) {
		return nil, errors.New("Trakt: expected a rooted API path")
	}
	var bearer string
	if auth {
		bearer, err = c.accessToken(ctx, "")
		if err != nil {
			return nil, err
		}
	}
	headers, err := c.request(ctx, c.apiBase+path, method, body, out, bearer)
	var status *statusError
	if auth && errors.As(err, &status) && status.StatusCode == http.StatusUnauthorized {
		bearer, err = c.accessToken(ctx, bearer)
		if err != nil {
			return nil, err
		}
		return c.request(ctx, c.apiBase+path, method, body, out, bearer)
	}
	return headers, err
}

func (c *Client) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.clientID == "" {
		return errors.New("Trakt: set TRAKT_CLIENT_ID to your app's client ID")
	}
	return nil
}

// request is called only with a URL assembled from a fixed origin and a
// validated path or literal OAuth endpoint. Never echo bodies, headers, URLs,
// or server-controlled status strings into an error destined for the terminal.
func (c *Client) request(ctx context.Context, endpoint, method string, body, out any, bearer string) (http.Header, error) {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("Trakt: could not encode request as JSON")
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, errors.New("Trakt: invalid request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ani")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", c.clientID)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, requestError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := &statusError{StatusCode: resp.StatusCode, retryAfter: retryAfter(resp.Header.Get("Retry-After"))}
		if resp.StatusCode == http.StatusBadRequest && endpoint == c.authBase+"/oauth/token" {
			// Never return arbitrary text (even error_description can echo an input).
			var detail struct{ Error string }
			if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&detail) == nil {
				switch detail.Error {
				case "invalid_grant", "invalid_client", "invalid_request", "unsupported_grant_type":
					status.oauthError = detail.Error
				}
			}
		}
		return resp.Header, status
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return resp.Header, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.Header, requestError(ctx, err)
	}
	if len(data) > maxResponseBytes {
		return resp.Header, errors.New("Trakt: response exceeds size limit")
	}
	if json.Unmarshal(data, out) != nil {
		return resp.Header, errors.New("Trakt: invalid JSON response")
	}
	return resp.Header, nil
}

func requestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("Trakt request: %w", context.DeadlineExceeded)
	}
	return errors.New("Trakt: request failed; check your connection and try again")
}

type statusError struct {
	StatusCode int
	retryAfter time.Duration
	oauthError string
}

func (e *statusError) Error() string {
	msg := fmt.Sprintf("Trakt: HTTP %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if e.oauthError == "invalid_grant" {
		return msg + " (invalid_grant; sign in to Trakt again)"
	}
	if e.oauthError != "" {
		return msg + " (" + e.oauthError + ")"
	}
	return msg
}

func retryAfter(header string) time.Duration {
	if n, err := strconv.ParseUint(header, 10, 64); err == nil {
		return time.Duration(min(n, uint64(maxDeviceWait/time.Second))) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		return max(0, time.Until(t))
	}
	return 0
}
