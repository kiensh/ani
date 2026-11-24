package flixhq

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

var (
	sectionTag = elementTag("section")
	h2Tag      = elementTag("h2")
	h3Tag      = elementTag("h3")
)

// RecentlyAdded interleaves the homepage's Latest Movies and Latest TV Shows
// in their published order. The site doesn't supply a shared added timestamp.
func (c *Client) RecentlyAdded(ctx context.Context) ([]Title, error) {
	return c.catalog(ctx, "/home", "", parseRecent)
}

func parseRecent(body string, base *url.URL) ([]Title, error) {
	const perKind = 24
	var movies, series []Title
	seen := make(map[string]bool)
	for section := range elements(inertRE.ReplaceAllString(body, ""), sectionTag, "block_area_home") {
		var rows *[]Title
		var kind string
		switch text(firstElement(section, h2Tag, "cat-heading")) {
		case "Latest Movies":
			kind, rows = "movie", &movies
		case "Latest TV Shows":
			kind, rows = "series", &series
		default:
			continue
		}
		for card := range elements(section, divTag, "flw-item") {
			if len(*rows) == perKind {
				break
			}
			if len(card) > 16<<10 {
				continue
			}
			heading := firstElement(card, h3Tag, "film-name")
			anchor := anchorRE.FindStringSubmatch(heading)
			if anchor == nil {
				continue
			}
			u, linkKind := catalogLink(attributes(anchor[1])["href"], base)
			if linkKind != kind {
				continue
			}
			key, name := strings.TrimSuffix(u.Path, "/"), text(anchor[2])
			if seen[key] || name == "" {
				continue
			}
			seen[key] = true
			t := Title{Name: name, Kind: kind, URL: u.String(), PosterURL: cardPoster(card)}
			// A TV card often contains only "SS 1" / "EPS 7". Never infer a
			// year from a slug, title, or four-digit episode count.
			for item := range elements(firstElement(card, divTag, "fd-infor"), spanTag, "fdi-item") {
				if s := text(item); len(s) == 4 && yearRE.MatchString(s) {
					t.Year = s
					break
				}
			}
			*rows = append(*rows, t)
		}
	}
	if len(movies) == 0 || len(series) == 0 {
		return nil, errors.New("flixhq: missing latest movie or TV listings")
	}
	out := make([]Title, 0, len(movies)+len(series))
	for i := range max(len(movies), len(series)) {
		if i < len(movies) {
			out = append(out, movies[i])
		}
		if i < len(series) {
			out = append(out, series[i])
		}
	}
	return out, nil
}
