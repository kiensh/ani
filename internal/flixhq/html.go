package flixhq

import (
	"errors"
	"html"
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
			out = append(out, Title{Name: name, Kind: kind, URL: link, Year: year})
		}
	}
	if !catalog && !strings.Contains(strings.ToLower(text(body)), "no results found") {
		return nil, errors.New("flixhq: unexpected catalog response")
	}
	return out, nil
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
