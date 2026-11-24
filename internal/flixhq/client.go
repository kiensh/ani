// Package flixhq searches the FlixHQ catalog and selects its public Vidmoly
// player. It returns the embed to the player; it does not visit embed or ad URLs.
package flixhq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ani/internal/playable"
)

const (
	primary    = "flixhqz.net"
	mirror     = "flixhq.app"
	ajaxPath   = "/ajax/ajax.php"
	maxPage    = 2 << 20
	maxPlayers = 256 << 10
)

type Title struct{ Name, Kind, URL, Year, PosterURL string }
type Details struct{ Description, Genres, Duration, Rating, PosterURL string }
type Episode struct {
	Season, Number int
	Name, URL      string
}

type Client struct{ http *http.Client }

func New() *Client {
	return &Client{http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: checkRedirect}}
}

var (
	titlePath   = regexp.MustCompile(`^/watch-(movie|series)/([a-zA-Z0-9_-]+)/?$`)
	episodePath = regexp.MustCompile(`^/episode/([a-zA-Z0-9_-]+)/s([0-9]{1,3})-e([0-9]{1,4})/?$`)
	embedURL    = regexp.MustCompile(`^https://kaembed\.net/embed-[a-zA-Z0-9_-]+\.html$`)
	errRedirect = errors.New("flixhq: refused unexpected redirect")
)

// route is the allowlist for both user-selected pages and HTTP redirects. Use
// Host, not Hostname, to reject ports. Reject escaped paths and query-bearing
// media links rather than silently normalizing them to a different resource.
func route(u *url.URL) string {
	if u == nil || u.Scheme != "https" || (u.Host != primary && u.Host != mirror) || u.User != nil {
		return ""
	}
	if len(u.Path)+len(u.RawQuery) > 2048 || u.RawPath != "" || u.Fragment != "" || u.ForceQuery {
		return ""
	}
	if u.Path == "/livesearch" {
		q, err := url.ParseQuery(u.RawQuery)
		if err == nil && len(q) == 1 && len(q["q"]) == 1 && q.Get("q") != "" {
			return "search"
		}
	}
	if u.RawQuery != "" {
		return ""
	}
	if u.Path == "/home" {
		return "home"
	}
	if m := titlePath.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	if episodePath.MatchString(u.Path) {
		return "episode"
	}
	return ""
}

func checkRedirect(next *http.Request, via []*http.Request) error {
	first := via[0]
	// Never replay the selection POST or follow a redirect that rewrites it
	// to a GET. Catalog GETs may canonicalize to another allowed mirror.
	kind := route(first.URL)
	if len(via) >= 5 || first.Method != http.MethodGet || kind == "" || route(next.URL) != kind || (kind == "search" && next.URL.Query().Get("q") != first.URL.Query().Get("q")) {
		return errRedirect
	}
	return nil
}

// catalogLink accepts only absolute or site-root-relative hrefs. In particular,
// don't resolve ../ or a scheme-relative link against a trusted catalog URL.
func catalogLink(raw string, base *url.URL) (*url.URL, string) {
	if len(raw) > 2048 || strings.ContainsAny(raw, `\#`) || strings.HasPrefix(raw, "//") {
		return nil, ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ""
	}
	if base != nil && strings.HasPrefix(raw, "/") {
		u.Scheme, u.Host = base.Scheme, base.Host
	}
	return u, route(u)
}

func (c *Client) read(req *http.Request, limit int64) ([]byte, *url.URL, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		// Don't propagate url.Error: a server can reflect opaque values in a
		// malformed Location header, which net/http includes in its errors.
		switch {
		case req.Context().Err() != nil:
			return nil, nil, req.Context().Err()
		case errors.Is(err, context.DeadlineExceeded):
			return nil, nil, context.DeadlineExceeded
		case errors.Is(err, errRedirect):
			return nil, nil, errRedirect
		default:
			return nil, nil, errors.New("flixhq: request failed")
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("flixhq: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if req.Context().Err() != nil {
			return nil, nil, req.Context().Err()
		}
		return nil, nil, errors.New("flixhq: could not read response")
	}
	if int64(len(body)) > limit {
		return nil, nil, errors.New("flixhq: response too large")
	}
	return body, resp.Request.URL, nil
}

func (c *Client) page(ctx context.Context, u *url.URL) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "text/html")
	b, actual, err := c.read(req, maxPage)
	return string(b), actual, err
}

// Search retries on the mirror only when the primary catalog is unavailable.
// A successful empty result is authoritative.
func (c *Client) Search(ctx context.Context, query string) ([]Title, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 512 {
		return nil, errors.New("flixhq: enter a search term of at most 512 bytes")
	}
	return c.catalog(ctx, "/livesearch", url.Values{"q": {query}}.Encode(), parseTitles)
}

// Use the mirror on either a transport failure or an unrecognized primary page.
func (c *Client) catalog(ctx context.Context, path, query string, parse func(string, *url.URL) ([]Title, error)) ([]Title, error) {
	var failures []error
	for _, host := range []string{primary, mirror} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		u := &url.URL{Scheme: "https", Host: host, Path: path, RawQuery: query}
		body, actual, err := c.page(ctx, u)
		if err == nil {
			var results []Title
			results, err = parse(body, actual)
			if err == nil {
				return results, nil
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		failures = append(failures, fmt.Errorf("%s: %w", host, err))
	}
	return nil, errors.Join(failures...)
}

// Details reads human-facing catalog metadata, never the playback resolver.
func (c *Client) Details(ctx context.Context, t Title) (Details, error) {
	u, kind := catalogLink(t.URL, nil)
	if (kind != "movie" && kind != "series") || kind != t.Kind {
		return Details{}, errors.New("flixhq: expected a matching movie or series page")
	}
	body, _, err := c.page(ctx, u)
	if err != nil {
		return Details{}, err
	}
	d := parseDetails(body)
	// Home cards often use an unsupported image host. Search only for this
	// selected title, and match its exact route across trusted mirrors; names
	// (even with a year) aren't enough to disambiguate a remake.
	if d.PosterURL == "" && t.PosterURL == "" && t.Name != "" && ctx.Err() == nil {
		matches, _ := c.Search(ctx, t.Name) // Optional art mustn't discard text.
		for _, match := range matches {
			v, k := catalogLink(match.URL, nil)
			if k == kind && v.Path == u.Path && match.PosterURL != "" {
				d.PosterURL = match.PosterURL
				break
			}
		}
	}
	return d, nil
}

func (c *Client) Episodes(ctx context.Context, t Title) ([]Episode, error) {
	u, kind := catalogLink(t.URL, nil)
	if t.Kind != "series" || kind != "series" {
		return nil, errors.New("flixhq: expected a series page")
	}
	body, actual, err := c.page(ctx, u)
	if err != nil {
		return nil, err
	}
	return parseEpisodes(body, actual), nil
}

func (c *Client) Resolve(ctx context.Context, pageURL string) (*playable.Release, error) {
	u, kind := catalogLink(pageURL, nil)
	if kind != "movie" && kind != "episode" {
		return nil, errors.New("flixhq: expected a movie or episode page")
	}
	body, actual, err := c.page(ctx, u)
	if err != nil {
		return nil, err
	}
	token, err := selection(body)
	if err != nil {
		return nil, err
	}

	field := "players"
	if kind == "episode" {
		field = "players_show"
	}
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	if err := w.WriteField(field, token); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	origin := "https://" + actual.Host
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+ajaxPath, &form)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", actual.String())
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	b, _, err := c.read(req, maxPlayers)
	if err != nil {
		return nil, err
	}
	var servers []struct{ Name, Link string }
	if err := json.Unmarshal(b, &servers); err != nil {
		return nil, errors.New("flixhq: invalid player list")
	}
	for _, s := range servers {
		if strings.EqualFold(s.Name, "Vidmoly") {
			if embed := canonicalEmbed(s.Link); embed != "" {
				return &playable.Release{Title: "Vidmoly", Group: "Vidmoly", StreamURL: embed, Referer: actual.String()}, nil
			}
		}
	}
	return nil, errors.New("flixhq: no supported Vidmoly player for this page")
}

// Vidmoly sometimes appends a subget subtitle hint. The embed plays without
// it: validate the link and return only its canonical HTTPS host and path.
func canonicalEmbed(link string) string {
	if len(link) > 16<<10 || strings.ContainsAny(link, "#\\\r\n") {
		return ""
	}
	base, query, hasQuery := strings.Cut(link, "?")
	if !embedURL.MatchString(base) {
		return ""
	}
	if hasQuery {
		q, err := url.ParseQuery(query)
		if err != nil || len(q) != 1 || len(q["subget"]) != 1 {
			return ""
		}
	}
	return base
}
