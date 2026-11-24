package trakt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// In-memory HTTP handlers let synctest advance poll/expiry and HTTP timeout
// timers deterministically. No production hosts, dotenv files or tokens used.
type authTransport http.HandlerFunc

func (h authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h(w, r)
	return w.Result(), nil
}

func authClient(t *testing.T, h authTransport) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ani", "trakt-token.json")
	c := newClient("fixture-id", "https://api.example.test", path, &http.Client{Transport: h})
	c.authBase = "https://auth.example.test"
	return c
}

func checkHeaders(t *testing.T, r *http.Request, method, host, bearer string) {
	t.Helper()
	if r.Method != method || r.URL.Host != host || r.Header.Get("Content-Type") != "application/json" ||
		r.Header.Get("User-Agent") == "" || r.Header.Get("trakt-api-version") != "2" ||
		r.Header.Get("trakt-api-key") != "fixture-id" || r.Header.Get("Authorization") != bearer {
		t.Errorf("incorrect method, origin, or headers on %s", r.URL.Path)
	}
}

func checkBody(t *testing.T, r *http.Request, want map[string]string) {
	t.Helper()
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !maps.Equal(got, want) || r.URL.RawQuery != "" {
		t.Errorf("incorrect JSON or unexpected query on %s: %+v", r.URL.Path, got)
	}
}

func writeGrant(w http.ResponseWriter, ttl int64) {
	_ = json.NewEncoder(w).Encode(deviceCode{DeviceCode: "private-device", UserCode: "ABC12345",
		VerificationURL: "https://auth.trakt.tv/activate", Interval: 2, ExpiresIn: ttl})
}

func fixtureToken() token {
	return token{AccessToken: "fixture-access", TokenType: "bearer", RefreshToken: "fixture-refresh",
		CreatedAt: time.Now().Unix(), ExpiresIn: 3600}
}

func writeLocal(t *testing.T, c *Client, tok token) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(c.tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.tokenPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceGrantWaitsAndPersists(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var polls []time.Duration
		c := authClient(t, func(w http.ResponseWriter, r *http.Request) {
			checkHeaders(t, r, "POST", "auth.example.test", "")
			switch r.URL.Path {
			case "/oauth/device/code":
				checkBody(t, r, map[string]string{"client_id": "fixture-id"})
				writeGrant(w, 90)
			case "/oauth/device/token":
				checkBody(t, r, map[string]string{"client_id": "fixture-id", "code": "private-device"})
				polls = append(polls, time.Since(start))
				switch len(polls) {
				case 1, 4:
					w.WriteHeader(400) // pending, even with empty/non-JSON bodies
				case 2:
					w.WriteHeader(429) // subsequent interval: 2s + 5s
				case 3:
					w.Header().Set("Retry-After", "14")
					w.WriteHeader(429) // respect the larger server delay
				default:
					_ = json.NewEncoder(w).Encode(fixtureToken())
				}
			default:
				t.Fatalf("unexpected request %s", r.URL.Path)
			}
		})
		prompts := 0
		err := c.Login(t.Context(), func(url, code string) {
			prompts++
			if url != "https://auth.trakt.tv/activate" || code != "ABC12345" {
				t.Error("prompt must receive the public URL and user code, not device code")
			}
		})
		if err != nil || prompts != 1 {
			t.Fatalf("login: %v, prompts: %d", err, prompts)
		}
		if !slices.Equal(polls, []time.Duration{2 * time.Second, 4 * time.Second, 11 * time.Second, 25 * time.Second, 39 * time.Second}) {
			t.Fatalf("poll times: %v", polls)
		}
		info, err := os.Stat(c.tokenPath)
		if err != nil || info.Mode().Perm() != 0o600 || !c.Connected() {
			t.Fatalf("expected private persisted token: %v", err)
		}
		tok, err := c.loadToken()
		want := fixtureToken()
		want.ClientID = c.clientID
		if err != nil || tok != want {
			t.Fatalf("wrong saved token: %v", err)
		}
	})
}

func TestDevicePromptRejectsUnsafeValues(t *testing.T) {
	for _, tc := range []struct{ url, code string }{
		{"http://trakt.tv/activate", "ABC12345"}, {"https://other.test/", "ABC12345"},
		{"https://evil@trakt.tv/activate", "ABC12345"}, {"https://auth.trakt.tv/\u009b31m", "ABC12345"},
		{"https://trakt.tv/activate", "\x1b[31m"}, {"https://trakt.tv/activate", strings.Repeat("A", 65)},
	} {
		c := authClient(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(deviceCode{DeviceCode: "private-device", UserCode: tc.code, VerificationURL: tc.url, Interval: 1, ExpiresIn: 1})
		})
		if err := c.Login(t.Context(), func(string, string) { t.Fatal("unsafe prompt value displayed") }); err == nil {
			t.Fatal("unsafe prompt accepted")
		}
	}
}

func TestLocalTokenBoundToRegistration(t *testing.T) {
	c := authClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("registration mismatch should fail locally") })
	if err := c.saveToken(fixtureToken()); err != nil {
		t.Fatal(err)
	}
	c.clientID = "other-client"
	if c.Connected() {
		t.Fatal("old registration still connected")
	}
	if err := c.do(t.Context(), "GET", "/users/me", nil, nil, true); err == nil || !strings.Contains(err.Error(), "different Trakt app") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeviceGrantStops(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status, polls int
		message       string
	}{
		{"denied", 418, 1, "denied"}, {"invalid", 404, 1, "invalid"}, {"used", 409, 1, "already used"},
		{"expired remotely", 410, 1, "expired"}, {"expired locally", 400, 2, "expired"},
		{"slow past expiry", 429, 1, "expired"}, {"server failure", 503, 1, "503"},
		{"canceled at prompt", 0, 0, "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				polls := 0
				c := authClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/oauth/device/code" {
						writeGrant(w, 6) // t=2,4 allowed; no poll exactly at expiry
						return
					}
					polls++
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, "private-device") // must not surface in errors
				})
				err := c.Login(ctx, func(string, string) {
					if tc.status == 0 {
						cancel()
					}
				})
				if err == nil || !strings.Contains(err.Error(), tc.message) || strings.Contains(err.Error(), "private-device") ||
					polls != tc.polls || c.Connected() {
					t.Fatalf("unexpected termination: err=%v polls=%d", err, polls)
				}
				if tc.status == 0 && !errors.Is(err, context.Canceled) {
					t.Fatalf("context error lost: %v", err)
				}
			})
		})
	}
}

func TestAuthRefreshAndLocalForget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		expired     bool
		firstStatus int
	}{
		{"expired locally", true, 0}, {"rejected early", false, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refreshes, writes := 0, 0
			c := authClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/token":
					checkHeaders(t, r, "POST", "auth.example.test", "")
					checkBody(t, r, map[string]string{"grant_type": "refresh_token", "client_id": "fixture-id",
						"refresh_token": "old-refresh", "redirect_uri": "http://127.0.0.1:8765/callback"})
					refreshes++
					_ = json.NewEncoder(w).Encode(fixtureToken())
				case "/sync/history":
					writes++
					if tc.firstStatus != 0 && writes == 1 {
						checkHeaders(t, r, "POST", "api.example.test", "Bearer old-access")
						w.WriteHeader(tc.firstStatus)
						return
					}
					checkHeaders(t, r, "POST", "api.example.test", "Bearer fixture-access")
					w.Header().Set("X-Pagination-Page-Count", "5")
					w.WriteHeader(201)
				default:
					t.Fatalf("unexpected request: %s", r.URL.Path)
				}
			})
			old := fixtureToken()
			old.AccessToken, old.RefreshToken = "old-access", "old-refresh"
			if tc.expired {
				old.CreatedAt -= old.ExpiresIn
			}
			writeLocal(t, c, old)
			oldFile, err := os.Stat(c.tokenPath)
			if err != nil {
				t.Fatal(err)
			}
			if !c.Connected() {
				t.Fatal("expired but refreshable must count as connected")
			}
			header, err := c.doWithHeaders(t.Context(), "POST", "/sync/history", struct{}{}, nil, true)
			if err != nil || refreshes != 1 || header.Get("X-Pagination-Page-Count") != "5" {
				t.Fatalf("refresh: %v; refresh calls: %d; headers: %v", err, refreshes, header)
			}
			wantWrites := 1
			if tc.firstStatus != 0 {
				wantWrites++
			}
			if writes != wantWrites {
				t.Fatalf("API calls: %d, want %d", writes, wantWrites)
			}
			newFile, err := os.Stat(c.tokenPath)
			if err != nil || os.SameFile(oldFile, newFile) || newFile.Mode().Perm() != 0o600 {
				t.Fatalf("token must be atomically replaced by a private file: %v", err)
			}
			reopened := newClient(c.clientID, c.apiBase, c.tokenPath, c.httpClient)
			tok, err := reopened.loadToken()
			if err != nil || tok.AccessToken != "fixture-access" || tok.RefreshToken != "fixture-refresh" {
				t.Fatalf("rotated pair not persisted: %v", err)
			}
			if err := reopened.Logout(); err != nil {
				t.Fatal(err)
			}
			if err := c.Logout(); err != nil || c.Connected() {
				t.Fatalf("local logout not idempotent: %v", err)
			}
			if err := c.do(t.Context(), "GET", "/users/me", nil, nil, true); err == nil || writes != wantWrites {
				t.Fatal("logged-out request must fail before network")
			}
		})
	}
}

func TestAuthFailuresAreSafe(t *testing.T) {
	var code int
	var calls int
	c := authClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"fixture-refresh"}`)
	})
	old := fixtureToken()
	old.CreatedAt -= old.ExpiresIn
	writeLocal(t, c, old)
	code = 400
	err := c.do(t.Context(), "GET", "/users/me", nil, nil, true)
	var status *statusError
	if !errors.As(err, &status) || status.StatusCode != 400 || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "fixture-refresh") || calls != 1 {
		t.Fatalf("unhelpful or unsafe refresh error: %v, calls=%d", err, calls)
	}
	old.CreatedAt = time.Now().Unix()
	writeLocal(t, c, old)
	code = 503
	err = c.do(t.Context(), "POST", "/sync/history", struct{}{}, nil, true)
	if !errors.As(err, &status) || status.StatusCode != 503 || calls != 2 {
		t.Fatal("do not retry ambiguous writes")
	}
	for _, path := range []string{"https://other.test/", "//other.test/", `/\other.test/`, "relative"} {
		if err := c.do(t.Context(), "GET", path, nil, nil, true); err == nil || calls != 2 {
			t.Fatalf("off-origin path accepted: %s", path)
		}
	}
	if err := os.Chmod(c.tokenPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if c.Connected() {
		t.Fatal("refuse insecure token file")
	}
	if err := c.Logout(); err != nil {
		t.Fatal(err)
	}
	c.clientID = ""
	if err := c.do(t.Context(), "GET", "/search/movie", nil, nil, false); err == nil || !strings.Contains(err.Error(), "TRAKT_CLIENT_ID") || calls != 2 {
		t.Fatal("missing client ID should fail locally")
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var leaked atomic.Bool
	outside := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
	defer outside.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, outside.URL+"/?fixture-query", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	c := newClient("fixture-id", origin.URL, filepath.Join(t.TempDir(), "token.json"), origin.Client())
	writeLocal(t, c, fixtureToken())
	for _, err := range []error{c.do(t.Context(), "POST", "/sync/history", struct{}{}, nil, true), c.Login(t.Context(), func(string, string) { t.Fatal("unexpected prompt") })} {
		var status *statusError
		if !errors.As(err, &status) || status.StatusCode != 307 || strings.Contains(err.Error(), "fixture-query") || leaked.Load() {
			t.Fatalf("redirect not blocked safely: %v", err)
		}
	}
}

func TestDeviceGrantCancelsInFlightPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := authClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oauth/device/code" {
				writeGrant(w, 4)
				return
			}
			<-r.Context().Done()
			w.WriteHeader(503)
		})
		start := time.Now()
		if err := c.Login(t.Context(), func(string, string) {}); !errors.Is(err, errCodeExpired) || time.Since(start) != 4*time.Second {
			t.Fatalf("poll must be cut off by expiry: %v at %v", err, time.Since(start))
		}
	})
}

func TestAuthPublicRequestsNeverAttachBearer(t *testing.T) {
	c := authClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r, "GET", "api.example.test", "")
		if r.URL.Query().Get("query") != "a&b" {
			t.Errorf("query encoding not preserved: %s", r.URL)
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	writeLocal(t, c, fixtureToken())
	var out struct{ OK bool }
	if err := c.do(t.Context(), "GET", "/search/movie?query=a%26b", nil, &out, false); err != nil || !out.OK {
		t.Fatalf("public request: %v", err)
	}
}
