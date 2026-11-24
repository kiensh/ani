package flixhq

import (
	"errors"
	"html"
	"iter"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Like the hianime parser, match the small, known HTML fragments, not a
// free-floating URL or token in the entire page. Attributes can appear in
// any order; quoted attribute values may themselves contain spaces or '>'.
const tagAttrs = `(?:[^"'<>]|"[^"]*"|'[^']*')*`

var (
	inertRE  = regexp.MustCompile(`(?is)<!--.*?(?:-->|$)|<script\b` + tagAttrs + `>.*?(?:</script\s*>|$)|<style\b` + tagAttrs + `>.*?(?:</style\s*>|$)`)
	anchorRE = regexp.MustCompile(`(?is)<a\s+(` + tagAttrs + `)>(.*?)</a\s*>`)
	openRE   = regexp.MustCompile(`(?is)<([a-z][a-z0-9:-]*)(?:\s+(` + tagAttrs + `))?/?>`)
	divRE    = regexp.MustCompile(`(?is)<div\s+(` + tagAttrs + `)>`)
	attrRE   = regexp.MustCompile(`(?:^|\s+)([^\s='"<>/]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s'"<>=]+))`)
	tagsRE   = regexp.MustCompile(`(?s)</?[a-zA-Z]` + tagAttrs + `>`)
	yearRE   = regexp.MustCompile(`\b(?:18|19|20)[0-9]{2}\b`)
	epPrefix = regexp.MustCompile(`(?i)^eps?\.?\s*\d+\s*:\s*`)

	divTag    = elementTag("div")
	spanTag   = elementTag("span")
	strongTag = elementTag("strong")
	iconTag   = elementTag("i")
	imgRE     = regexp.MustCompile(`(?is)<img\s+(` + tagAttrs + `)>`)
	inlineRE  = regexp.MustCompile(`(?is)</?(?:em|strong|b|i|a|span)(?:\s+` + tagAttrs + `)?>`)
	posterRE  = regexp.MustCompile(`^https://image\.tmdb\.org/t/p/(?:w92|w154|w185|w342|w500|w780|original)/[A-Za-z0-9_-]{1,128}\.(?:jpg|jpeg|png|webp)$`)
	ratingRE  = regexp.MustCompile(`^(?:[0-9](?:\.[0-9]{1,2})?|10(?:\.0{1,2})?)$`)
	runtimeRE = regexp.MustCompile(`^[1-9][0-9]{0,3} min$`)
)

func attributes(raw string) map[string]string {
	a := make(map[string]string)
	for _, m := range attrRE.FindAllStringSubmatch(raw, -1) {
		key := strings.ToLower(m[1])
		if _, present := a[key]; !present {
			a[key] = html.UnescapeString(m[2] + m[3] + m[4])
		}
	}
	return a
}

// Names end up in a terminal picker; keep them single-line and inert.
func label(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func text(raw string) string {
	return label(html.UnescapeString(tagsRE.ReplaceAllString(raw, " ")))
}

func elementTag(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(?is)<(/?)` + tag + `(?:\s+(` + tagAttrs + `))?>`)
}

// elements yields non-overlapping fragments of a known tag/class, counting
// nested tags of the same name so a card or section can't spill into a sibling.
func elements(body string, tag *regexp.Regexp, class string) iter.Seq[string] {
	return func(yield func(string) bool) {
		start, depth := 0, 0
		for offset := 0; offset < len(body); {
			pos := tag.FindStringSubmatchIndex(body[offset:])
			if pos == nil {
				return
			}
			if pos[3] > pos[2] { // closing tag
				if depth > 0 {
					depth--
					if depth == 0 && !yield(body[start:offset+pos[0]]) {
						return
					}
				}
			} else if depth > 0 {
				depth++
			} else if class == "" || (pos[4] >= 0 && hasClass(body[offset+pos[4]:offset+pos[5]], class)) {
				start, depth = offset+pos[1], 1
			}
			offset += pos[1]
		}
	}
}

func hasClass(attrs, class string) bool {
	return strings.Contains(attrs, class) && slices.Contains(strings.Fields(attributes(attrs)["class"]), class)
}

func firstElement(body string, tag *regexp.Regexp, class string) string {
	for inner := range elements(body, tag, class) {
		return inner
	}
	return ""
}

// ValidPosterURL is the allowlist for both an initial poster request and each
// redirect. Match the entire bounded URL literally: no port, escapes, query,
// fragment, userinfo, or alternate host/path syntax is accepted.
func ValidPosterURL(raw string) bool { return len(raw) <= 256 && posterRE.MatchString(raw) }

func cardPoster(card string) string {
	poster := firstElement(card, divTag, "film-poster")
	if poster != "" {
		card = poster
	}
	for _, img := range imgRE.FindAllStringSubmatch(card, -1) {
		a := attributes(img[1])
		if poster == "" && !slices.Contains(strings.Fields(a["class"]), "film-poster-img") {
			continue
		}
		for _, src := range []string{a["src"], a["data-src"]} {
			if ValidPosterURL(src) {
				// Keep enough source pixels for native graphics on HiDPI displays.
				return "https://image.tmdb.org/t/p/w780/" + src[strings.LastIndexByte(src, '/')+1:]
			}
		}
	}
	return ""
}

// cardLabels reads .film-name and the year in .film-infor. Searching for a
// year across the whole card would turn titles like "2001" into dates.
func cardLabels(body string) (name, year string) {
	var posterName string
	for _, pos := range openRE.FindAllStringSubmatchIndex(body, -1) {
		if pos[4] < 0 {
			continue
		}
		tag := body[pos[2]:pos[3]]
		a := attributes(body[pos[4]:pos[5]])
		if strings.EqualFold(tag, "img") && posterName == "" {
			posterName = label(a["title"])
		}
		classes := strings.Fields(a["class"])
		isName, isInfo := slices.Contains(classes, "film-name"), slices.Contains(classes, "film-infor")
		if !isName && !isInfo {
			continue
		}
		inner, _, ok := strings.Cut(body[pos[1]:], "</"+tag+">")
		if !ok {
			continue
		}
		if isName {
			name = text(inner)
		}
		if isInfo {
			year = yearRE.FindString(text(inner))
		}
	}
	if name == "" {
		name = posterName
	}
	return name, year
}

func parseTitles(body string, base *url.URL) ([]Title, error) {
	body = inertRE.ReplaceAllString(body, "")
	var out []Title
	seen := make(map[string]bool)
	var catalog bool
	for _, row := range anchorRE.FindAllStringSubmatch(body, -1) {
		a := attributes(row[1])
		if !slices.Contains(strings.Fields(a["class"]), "nav-item") {
			continue
		}
		catalog = true
		u, kind := catalogLink(a["href"], base)
		if kind != "movie" && kind != "series" {
			continue
		}
		link := u.String()
		// A normal result card is a few hundred bytes. Don't treat an
		// entire malformed or unterminated HTML fragment as a display row.
		if seen[link] || len(row[2]) > 16<<10 {
			continue
		}
		name, year := cardLabels(row[2])
		if name != "" {
			seen[link] = true
			out = append(out, Title{Name: name, Kind: kind, URL: link, Year: year, PosterURL: cardPoster(row[2])})
		}
	}
	if !catalog && !strings.Contains(strings.ToLower(text(body)), "no results found") {
		return nil, errors.New("flixhq: unexpected catalog response")
	}
	return out, nil
}

func parseDetails(body string) Details {
	body = inertRE.ReplaceAllString(body, "")
	d := Details{PosterURL: cardPoster(firstElement(body, divTag, "m_i-d-poster"))}
	info := firstElement(body, divTag, "m_i-d-content")
	if desc := firstElement(info, divTag, "description"); len(desc) <= 16<<10 {
		// Inline emphasis doesn't add spaces around words or punctuation.
		d.Description = text(inlineRE.ReplaceAllString(desc, ""))
		// Strip the site's bold SEO intro, not an arbitrary first sentence.
		intro := text(inlineRE.ReplaceAllString(firstElement(desc, strongTag, ""), ""))
		if strings.HasSuffix(strings.ToLower(intro), " flixhq watch online streaming") && strings.HasPrefix(d.Description, intro) {
			d.Description = strings.TrimLeft(d.Description[len(intro):], " -–—")
		}
	}
	for item := range elements(firstElement(info, divTag, "stats"), spanTag, "item") {
		value := text(item)
		if runtimeRE.MatchString(value) {
			d.Duration = value
		} else if ratingRE.MatchString(value) {
			for range elements(item, iconTag, "fa-star") {
				d.Rating = value
				break
			}
		}
	}
	for row := range elements(firstElement(info, divTag, "elements"), divTag, "row-line") {
		if !strings.EqualFold(text(firstElement(row, spanTag, "type")), "Genre:") {
			continue
		}
		var genres []string
		for _, a := range anchorRE.FindAllStringSubmatch(row, 16) {
			if s := text(a[2]); s != "" && len(s) <= 100 {
				genres = append(genres, s)
			}
		}
		d.Genres = strings.Join(genres, ", ")
		break
	}
	return d
}

func parseEpisodes(body string, base *url.URL) []Episode {
	slug := titlePath.FindStringSubmatch(base.Path)[2]
	var out []Episode
	seen := make(map[[2]int]bool)
	for _, row := range anchorRE.FindAllStringSubmatch(inertRE.ReplaceAllString(body, ""), -1) {
		a := attributes(row[1])
		u, kind := catalogLink(a["href"], base)
		if kind != "episode" {
			continue
		}
		m := episodePath.FindStringSubmatch(u.Path)
		if m[1] != slug {
			continue
		}
		season, _ := strconv.Atoi(m[2])
		number, _ := strconv.Atoi(m[3])
		key := [2]int{season, number}
		if seen[key] {
			continue
		}
		seen[key] = true
		name := epPrefix.ReplaceAllString(label(a["title"]), "")
		if name == "" {
			name = text(row[2])
		}
		out = append(out, Episode{Season: season, Number: number, Name: name, URL: u.String()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Number < out[j].Number
	})
	return out
}

func selection(body string) (string, error) {
	var token string
	for _, m := range divRE.FindAllStringSubmatch(inertRE.ReplaceAllString(body, ""), -1) {
		v, ok := attributes(m[1])["data-token"]
		if !ok {
			continue
		}
		if token != "" || len(v) > 8192 || strings.TrimSpace(v) == "" {
			return "", errors.New("flixhq: invalid player selection")
		}
		token = v
	}
	if token == "" {
		return "", errors.New("flixhq: missing player selection")
	}
	return token, nil
}
