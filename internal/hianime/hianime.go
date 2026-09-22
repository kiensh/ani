// Package hianime is a client for the hianime.at streaming site (search →
// episode list → per-server embeds → direct HLS stream URL). It produces
// playable.Release items so the release picker pipeline works unchanged: each
// episode's variants (audio × resolution) become rows where Group = "sub"/"dub"
// and Resolution = "1080"/"720"/"360".
//
// The site lists each season as its own entry numbered from 1, so episode
// numbers map 1:1 to MAL per-season numbers (unlike the old anidb.app provider,
// which used cumulative numbering and needed an offset). Streams come from the
// ZokoAnime server embeds — the only server whose player config we can decode
// (base64 + XOR with a fixed key, see decodeEmbedBlob).
package hianime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ani/internal/hostgate"
	"ani/internal/playable"
)

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	timeout   = 20 * time.Second
)

// baseURL is the site root (a var so tests can point it at httptest).
var baseURL = "https://hianime.at"

// Pooled client: Go's DefaultTransport reuses only ~2 idle conns/host. A pooled
// transport lets the aired-count prefetch (one search+episodes per airing anime)
// actually run in parallel.
var client = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
	},
	Timeout: timeout,
}

// debugLog is the always-on debug sink (set by main via SetDebugLog).
var debugLog io.Writer = io.Discard

// SetDebugLog sets the debug log destination (called from main).
func SetDebugLog(w io.Writer) { debugLog = w }

func dbg(format string, args ...any) {
	if debugLog != nil && debugLog != io.Discard {
		fmt.Fprintf(debugLog, format, args...)
	}
}

// Show is one anime from hianime. ID is the slug (e.g. "frieren-…-481").
type Show struct {
	ID   string
	Name string
}

// Episode is one episode with hianime's numeric id (data-id) and its number
// (data-number). Number is float64 in case of fractional specials (0.5, 5.5);
// AiredCount and the picker's integer episode input only deal in whole numbers.
type Episode struct {
	ID     int
	Number float64
}

// ---- HTTP helper ----

// pingTimeout bounds a reachability probe: short, since the probe runs between
// pickers on a provider switch and shouldn't hang it.
const pingTimeout = 5 * time.Second

// errCloudflare marks a Cloudflare challenge page: the site is up but refusing
// the client (ani-cli documents this too and ships a curl-impersonate fallback).
var errCloudflare = errors.New("blocked by cloudflare")

// cloudflareBlocked reports whether body is a Cloudflare challenge page.
func cloudflareBlocked(body []byte) bool {
	return strings.Contains(string(body), "<title>Just a moment")
}

// ---- site-host rate limiting ----

// hianime.at answers bursts with HTTP 429 (the aired prefetch — one search +
// episode-list per airing anime, ~180 requests for a full season — tripped it
// at 4-wide concurrency). Sequential traffic is fine (verified: 12 rapid
// back-to-back /search requests all 200), so requests to the SITE host are
// serialized and spaced, and a 429 trips a fail-fast cooldown. Only the site
// host is gated — the embed/master stream hosts are separate services with
// their own headroom, and gating them would needlessly slow the all-episodes
// fetch. Pace/Cooldown are fields so tests can shorten them.
var siteGate = &hostgate.Gates{Pace: 200 * time.Millisecond, Cooldown: 20 * time.Second}

// hostOf extracts the URL's host for the gate's site filter.
func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return ""
}

// isSiteURL reports whether u points at the gated site host (baseURL's).
func isSiteURL(u string) bool {
	host := hostOf(u)
	return host != "" && host == hostOf(baseURL)
}

// Ping checks the site is reachable with one request to the homepage: any HTTP
// response below 400 counts as up (maintenance/outage answers 5xx, a Cloudflare
// block answers 403). app.applySourceSwitch uses it to warn immediately when
// switching to a dead provider.
func Ping() error {
	u := baseURL + "/"
	if err := siteGate.Wait(u); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(req.Context(), pingTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", userAgent)
	dbg("hianime: GET %s\n", u)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		siteGate.Trip(u, hostgate.ParseRetryAfter(resp.Header.Get("Retry-After")))
		return hostgate.ErrLimited
	}
	// The challenge marker lives in the page head; the first 64KB are enough.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if cloudflareBlocked(b) {
		return errCloudflare
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reused
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// get fetches u (with referer when non-empty — the stream host enforces it) and
// returns the body + status. A Cloudflare challenge page is surfaced as
// errCloudflare, and a site 429 trips the cooldown (see siteGate), so callers
// can name either in the down warning.
func get(u, referer string) ([]byte, int, error) {
	if isSiteURL(u) {
		if err := siteGate.Wait(u); err != nil {
			return nil, 0, err
		}
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests && isSiteURL(u) {
		siteGate.Trip(u, hostgate.ParseRetryAfter(resp.Header.Get("Retry-After")))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, resp.StatusCode, fmt.Errorf("%s: %w", u, hostgate.ErrLimited)
	}
	b, err := io.ReadAll(resp.Body)
	if err == nil && cloudflareBlocked(b) {
		return nil, resp.StatusCode, fmt.Errorf("%s: %w", u, errCloudflare)
	}
	return b, resp.StatusCode, err
}

// ---- Search ----

// filmNameRe matches one search-result row: the slug is captured after the
// link's last "/" so absolute and relative hrefs both work.
var filmNameRe = regexp.MustCompile(`<h3 class="film-name">\s*<a href="[^"]*/([^"/]+)"\s+title="([^"]+)"`)

// Search queries hianime's search page and returns the matching shows. The
// top-10 sidebar repeats the result markup; everything from it on is cut first
// so entries aren't doubled.
func Search(query string) ([]Show, error) {
	u := baseURL + "/search?keyword=" + url.QueryEscape(query)
	dbg("hianime: GET %s\n", u)
	body, status, err := get(u, "")
	if err != nil {
		dbg("hianime: search %q: %v\n", query, err)
		return nil, err
	}
	if status != 200 {
		dbg("hianime: search %q: HTTP %d\n", query, status)
		return nil, fmt.Errorf("hianime search: HTTP %d", status)
	}
	page := body
	if i := strings.Index(string(page), `id="main-sidebar"`); i >= 0 {
		page = page[:i]
	}
	matches := filmNameRe.FindAllSubmatch(page, -1)
	dbg("hianime: search %q: %d matches\n", query, len(matches))
	out := make([]Show, 0, len(matches))
	for _, m := range matches {
		out = append(out, Show{
			ID:   string(m[1]),
			Name: htmlUnesc(string(m[2])),
		})
	}
	return out, nil
}

// ResolveShow searches by title and returns the best match (see pickShow). No
// results is an error here — the caller asked for a specific show to stream.
func ResolveShow(title string) (Show, error) {
	shows, err := Search(cleanQuery(title))
	if err != nil {
		return Show{}, err
	}
	if len(shows) == 0 {
		return Show{}, fmt.Errorf("hianime: no results for %q", title)
	}
	return pickShow(shows, title), nil
}

// pickShow chooses the best result for title: the site's relevance ranking
// occasionally puts a similarly-named spinoff above the asked-for entry when
// the MAL title is punctuation-heavy ("Re:ZERO … Season 3" → "Re:Zero … Break
// Time 2nd Season"), so a result whose name matches the query after
// normalization (case- and punctuation-folded) is preferred; the top result is
// the fallback.
func pickShow(shows []Show, title string) Show {
	want := normalizeTitle(title)
	for _, s := range shows {
		if normalizeTitle(s.Name) == want {
			return s
		}
	}
	return shows[0]
}

// normalizeTitle folds a title for comparison: lowercased, everything that
// isn't a letter or digit dropped.
func normalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- Episodes ----

// The episode-list and servers endpoints both answer {"status":…,"html":"…"}
// where html is an HTML fragment (server-rendered) with the attributes we
// parse spread over multiple lines.
type htmlFragment struct {
	Status bool   `json:"status"`
	HTML   string `json:"html"`
}

// epNumRe / epIDRe extract one episode row's number and id from an ep-item
// fragment (data-number comes first).
var (
	epNumRe = regexp.MustCompile(`data-number="([^"]*)"`)
	epIDRe  = regexp.MustCompile(`data-id="([0-9]+)"`)
)

// slugNum extracts the trailing numeric id from a slug (e.g. "frieren-…-481" → "481").
func slugNum(slug string) string {
	if i := strings.LastIndex(slug, "-"); i >= 0 {
		return slug[i+1:]
	}
	return slug
}

// fragment fetches u and decodes its {"status","html"} envelope.
func fragment(u string) (string, error) {
	body, status, err := get(u, "")
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("hianime: HTTP %d", status)
	}
	var f htmlFragment
	if err := json.Unmarshal(body, &f); err != nil {
		return "", fmt.Errorf("hianime: decode: %w", err)
	}
	return f.HTML, nil
}

// Episodes returns the episode list for a show (by slug ID).
func Episodes(showID string) ([]Episode, error) {
	u := baseURL + "/api/theme/episode/list/" + slugNum(showID)
	dbg("hianime: GET %s\n", u)
	html, err := fragment(u)
	if err != nil {
		return nil, err
	}
	// Each episode is one "ep-item" div; split on the marker so a row's
	// data-number and data-id pair up (a whole-page regex could pair across
	// rows).
	var out []Episode
	for _, item := range strings.Split(html, "ep-item")[1:] {
		nm := epNumRe.FindStringSubmatch(item)
		idm := epIDRe.FindStringSubmatch(item)
		if nm == nil || idm == nil {
			continue
		}
		n, err := strconv.ParseFloat(nm[1], 64)
		if err != nil {
			continue
		}
		id, err := strconv.Atoi(idm[1])
		if err != nil {
			continue
		}
		out = append(out, Episode{ID: id, Number: n})
	}
	return out, nil
}

// AiredCount resolves the show by title and returns the latest episode number
// it lists — per-season numbering, so it's already in MAL terms. Returns
// float64 so fractional specials (e.g. 3.5) are preserved in the display.
//
// A non-nil error means the FETCH itself failed (site down, blocked, HTTP
// error) — the caller must not cache that as an answer. A (0, nil) return is a
// real answer: the show isn't on hianime or lists no episodes, safe to cache.
func AiredCount(title string) (float64, error) {
	shows, err := Search(cleanQuery(title))
	if err != nil {
		dbg("hianime: aired %q: %v\n", title, err)
		return 0, err
	}
	if len(shows) == 0 {
		dbg("hianime: aired %q: no show\n", title)
		return 0, nil
	}
	show := pickShow(shows, title)
	eps, err := Episodes(show.ID)
	if err != nil {
		dbg("hianime: aired %q show=%s: %v\n", title, show.ID, err)
		return 0, err
	}
	n := 0.0
	for _, e := range eps {
		if e.Number > n {
			n = e.Number
		}
	}
	if n == 0 {
		dbg("hianime: aired %q show=%s: no episodes\n", title, show.ID)
	}
	dbg("hianime: aired %q show=%s -> %g\n", title, show.ID, n)
	return n, nil
}

// ---- Stream resolution ----

// serverRe matches one server row of the servers fragment after whitespace is
// compacted (the attributes span lines in the raw html). Only ZokoAnime embeds
// are used — the other servers (HD-1, Vidstream → megacloud) use players whose
// config we can't decode.
var serverRe = regexp.MustCompile(`data-type="(sub|dub)" data-server-name="ZokoAnime" data-hash="([^"]+)"`)

// blobRe extracts the obfuscated player config from a ZokoAnime embed page.
var blobRe = regexp.MustCompile(`window\.__P="([^"]+)"`)

// embedKey is the repeating XOR key for the embed page's config blob.
var embedKey = []byte("otaku-embed-v1")

// embedConfig is the decoded player config: the master playlist and subtitle
// tracks (the site marks the English track default).
type embedConfig struct {
	Src       string `json:"src"`
	Subtitles []struct {
		Label   string `json:"label"`
		Default bool   `json:"default"`
		Src     string `json:"src"`
	} `json:"subtitles"`
}

// decodeEmbedConfig base64-decodes the blob and XORs it with embedKey.
func decodeEmbedBlob(blob string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(blob)
		if err != nil {
			return nil, fmt.Errorf("embed blob: decode: %w", err)
		}
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = b ^ embedKey[i%len(embedKey)]
	}
	return out, nil
}

// resAttrRe pulls the resolution out of an #EXT-X-STREAM-INF line.
var resAttrRe = regexp.MustCompile(`RESOLUTION=(\d+)x(\d+)`)

type variant struct {
	height string // "1080", "720", "360"
	url    string
}

// masterVariants fetches the master playlist (referer required — the stream
// host answers 403 without it) and parses its variant ladder. Relative variant
// URIs are joined against the master's directory.
func masterVariants(masterURL, referer string) ([]variant, error) {
	body, status, err := get(masterURL, referer)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("hianime: master playlist: HTTP %d", status)
	}
	dir := masterURL
	if i := strings.LastIndex(masterURL, "/"); i >= 0 {
		dir = masterURL[:i+1]
	}
	var out []variant
	height := ""
	for _, ln := range strings.Split(string(body), "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(ln, "#EXT-X-STREAM-INF"):
			height = ""
			if m := resAttrRe.FindStringSubmatch(ln); m != nil {
				height = m[2]
			}
		case ln != "" && !strings.HasPrefix(ln, "#"):
			if height == "" {
				continue // media segment, not a variant
			}
			u := ln
			if !strings.HasPrefix(u, "http") {
				u = dir + u
			}
			out = append(out, variant{height: height, url: u})
			height = ""
		}
	}
	return out, nil
}

// originOf returns "scheme://host/" for u — the referer the stream host
// expects (the embed page's origin).
func originOf(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Scheme == "" || p.Host == "" {
		return ""
	}
	return p.Scheme + "://" + p.Host + "/"
}

// streamSet is one audio track's resolved streams: the variant ladder, the
// referer its stream host requires (the embed page's origin), and the default
// subtitle track that embed ships.
type streamSet struct {
	variants []variant
	referer  string
	subURL   string
}

// episodeStreams resolves one episode's sub/dub variants: servers → ZokoAnime
// hash → embed URL → decoded player config → master playlist ladder.
func episodeStreams(epID int) (map[string]streamSet, error) {
	u := baseURL + "/api/theme/episode/servers?episodeId=" + strconv.Itoa(epID)
	dbg("hianime: GET %s\n", u)
	html, err := fragment(u)
	if err != nil {
		return nil, err
	}
	// The attributes span lines; compact whitespace so serverRe sees one row.
	compact := strings.Join(strings.Fields(html), " ")
	hashes := map[string]string{}
	for _, m := range serverRe.FindAllStringSubmatch(compact, -1) {
		if _, dup := hashes[m[1]]; !dup {
			hashes[m[1]] = m[2]
		}
	}
	streams := map[string]streamSet{}
	for _, group := range []string{"sub", "dub"} {
		hash, ok := hashes[group]
		if !ok {
			continue // e.g. no dub for this episode
		}
		raw, err := base64.StdEncoding.DecodeString(hash)
		if err != nil {
			dbg("hianime: %s hash decode: %v\n", group, err)
			continue
		}
		embed := string(raw)
		refr := originOf(embed)
		body, status, err := get(embed, "")
		if err != nil {
			dbg("hianime: %s embed: %v\n", group, err)
			continue
		}
		if status != 200 {
			dbg("hianime: %s embed: HTTP %d\n", group, status)
			continue
		}
		m := blobRe.FindSubmatch(body)
		if m == nil {
			dbg("hianime: %s embed: no __P blob\n", group)
			continue
		}
		blob, err := decodeEmbedBlob(string(m[1]))
		if err != nil {
			dbg("hianime: %s embed blob: %v\n", group, err)
			continue
		}
		var cfg embedConfig
		if err := json.Unmarshal(blob, &cfg); err != nil {
			dbg("hianime: %s embed config: %v\n", group, err)
			continue
		}
		if cfg.Src == "" {
			continue
		}
		vs, err := masterVariants(cfg.Src, refr)
		if err != nil {
			dbg("hianime: %s master: %v\n", group, err)
			continue
		}
		if len(vs) == 0 {
			vs = []variant{{height: "auto", url: cfg.Src}} // let mpv pick
		}
		// The default track (marked by the site) is the English subtitle.
		subURL := ""
		for _, s := range cfg.Subtitles {
			if s.Default && s.Src != "" {
				subURL = s.Src
				break
			}
		}
		streams[group] = streamSet{variants: vs, referer: refr, subURL: subURL}
	}
	return streams, nil
}

// allEpisodesCap bounds the "all episodes" (episode == 0) fetch: each listed
// episode costs ~3 GETs (servers + embed page + master m3u8), and long series
// list 1000+ (One Piece). The newest episodes are kept — what a viewer of an
// airing or long show needs. Single-episode fetches are already small and stay
// uncapped.
const allEpisodesCap = 100

// allFetchConcurrency bounds the per-episode fan-out of the all-episodes
// fetch. 16 mirrors the tui prefetch cap; the pooled client (50 idle
// conns/host) absorbs it.
const allFetchConcurrency = 16

// FetchReleases resolves the playable variants for a show. With episode > 0 it
// returns just that episode's variants; with episode == 0 (the picker's "all"
// filter) it returns every listed episode's variants, newest-episode first
// (capped at allEpisodesCap). Each playable.Release is one (audio ×
// resolution) combination: Group is "sub" or "dub"; Resolution is the height
// (e.g. "1080p"); StreamURL is the HLS variant URL (playable by mpv with the
// carried Referer). The release picker's group filter selects sub/dub; the
// quality filter selects resolution.
func FetchReleases(showID string, episode int) ([]*playable.Release, error) {
	eps, err := Episodes(showID)
	if err != nil {
		return nil, err
	}
	if len(eps) == 0 {
		return nil, fmt.Errorf("hianime: no episodes for this show")
	}

	if episode == 0 {
		return allReleases(showID, eps)
	}

	var epID int
	for _, e := range eps {
		if e.Number == float64(episode) {
			epID = e.ID
			break
		}
	}
	if epID == 0 {
		dbg("hianime: episode %d not available\n", episode)
		return nil, nil
	}
	streams, err := episodeStreams(epID)
	if err != nil {
		return nil, err
	}
	return releaseRows(showID, episode, streams), nil
}

// allReleases fetches every listed whole episode's variants (episode == 0).
// Rows carry their episode number, so the picker's episode filter still
// narrows client-side over the merged list. Assembled newest-episode first,
// mirroring the torrent path's all-view order.
func allReleases(showID string, eps []Episode) ([]*playable.Release, error) {
	type epEntry struct{ id, num int }
	var entries []epEntry
	for _, e := range eps {
		// Whole-numbered episodes only — fractional specials (5.5) can't be
		// reached through the picker's integer episode input anyway.
		if e.Number >= 1 && e.Number == float64(int(e.Number)) {
			entries = append(entries, epEntry{id: e.ID, num: int(e.Number)})
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].num < entries[j].num })
	if len(entries) > allEpisodesCap {
		dbg("hianime: all-episodes fetch capped to newest %d of %d episodes\n",
			allEpisodesCap, len(entries))
		entries = entries[len(entries)-allEpisodesCap:]
	}

	// Bounded fan-out; goroutine i writes only its own slot (no mutex), then
	// the slots are assembled newest-first. A failed episode is skipped (dbg)
	// — same tolerance as a failed embed resolve.
	type epResult struct {
		rels []*playable.Release
	}
	results := make([]epResult, len(entries))
	sem := make(chan struct{}, allFetchConcurrency)
	var wg sync.WaitGroup
	for i, ent := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			streams, err := episodeStreams(ent.id)
			if err != nil {
				dbg("hianime: all-episodes fetch ep %d failed: %v\n", ent.num, err)
				return
			}
			results[i] = epResult{rels: releaseRows(showID, ent.num, streams)}
		}()
	}
	wg.Wait()

	var out []*playable.Release
	for i := len(results) - 1; i >= 0; i-- {
		out = append(out, results[i].rels...)
	}
	return out, nil
}

// releaseRows projects one episode's resolved variants into playable.Release
// rows, sorted sub before dub, then highest resolution first. Referer is the
// variant's embed origin (the stream host 403s without it); SubtitleURL is
// that track's default (English) subtitle when present.
func releaseRows(showID string, malEp int, streams map[string]streamSet) []*playable.Release {
	var out []*playable.Release
	for group, ss := range streams {
		for _, v := range ss.variants {
			// Append "p" to numeric heights (e.g. "1080" → "1080p") so the
			// release picker's resolutionHeight/matchResolution recognise them
			// (they only match \d+p or \d+x\d+ patterns).
			res := v.height
			if _, e := strconv.Atoi(v.height); e == nil {
				res = v.height + "p"
			}
			out = append(out, &playable.Release{
				Title:       fmt.Sprintf("%s Episode %d", showID, malEp),
				Group:       group,
				Resolution:  res,
				Episode:     malEp,
				StreamURL:   v.url,
				Referer:     ss.referer,
				SubtitleURL: ss.subURL,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group == "sub"
		}
		return resolutionValue(out[i].Resolution) > resolutionValue(out[j].Resolution)
	})
	return out
}

// resolutionValue ranks a resolution label numerically for sorting; unknown
// labels ("auto") sort last. ("720p" would outrank "1080p" lexicographically.)
func resolutionValue(res string) int {
	n, err := strconv.Atoi(strings.TrimSuffix(res, "p"))
	if err != nil {
		return -1
	}
	return n
}

// ---- Helpers ----

// cleanQuery flattens a MAL title into a search keyword: punctuation runs
// become single spaces. hianime's search treats a "-word" fragment as an
// exclusion operator, so MAL's "Title -Subtitle- Season" shapes (e.g. "Re:ZERO
// -Starting Life in Another World- Season 3") return only spinoffs when passed
// as-is — with punctuation flattened the real entry is the top result. The
// season/subtitle qualifiers themselves are KEPT (each season is its own
// hianime entry, so they're what picks the right one); the caller's
// normalized-name match (pickShow) guards against a wrong top hit.
func cleanQuery(title string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 0x7f {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func htmlUnesc(s string) string {
	return strings.NewReplacer("&#039;", "'", "&quot;", "\"", "&amp;", "&").Replace(s)
}
