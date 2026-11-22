package hianime

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// EpisodeListing is the first page of recently updated shows. MAL often uses
// the romaji name rather than the English display name; index both aliases.
type EpisodeListing map[string]listingShow

type listingShow struct {
	slug   string
	latest float64
}

var (
	listingSubRe   = regexp.MustCompile(`tick-sub">\s*<i[^>]*></i>\s*([0-9]+(?:\.[0-9]+)?)\s*</div>`)
	listingJNameRe = regexp.MustCompile(`\bdata-jname="([^"]+)"`)
)

func FetchEpisodeListing() (EpisodeListing, error) {
	u := baseURL + "/recently-updated"
	dbg("hianime: GET %s\n", u)
	body, status, err := get(u, "")
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("hianime listing: HTTP %d", status)
	}
	return parseListing(string(body)), nil
}

// Latest matches whole normalized titles only. A missing/ambiguous name is
// not a reason to report zero, or to borrow the count from a sibling season.
func (listing EpisodeListing) Latest(title string) (float64, bool) {
	s := listing[normalizeTitle(title)]
	return s.latest, s.latest > 0
}

func parseListing(page string) EpisodeListing {
	grid, _, _ := strings.Cut(page, `id="main-sidebar"`)
	listing := make(EpisodeListing)
	for _, card := range strings.Split(grid, `class="flw-item`)[1:] {
		name := filmNameRe.FindStringSubmatch(card)
		if name == nil {
			continue
		}
		s := listingShow{slug: name[1]}
		if m := listingSubRe.FindStringSubmatch(card); m != nil {
			s.latest, _ = strconv.ParseFloat(m[1], 64)
		}
		aliases := []string{name[2]}
		link, _, _ := strings.Cut(card[strings.Index(card, name[0])+len(name[0]):], ">")
		if m := listingJNameRe.FindStringSubmatch(link); m != nil {
			aliases = append(aliases, m[1])
		}
		for _, a := range aliases {
			key := normalizeTitle(html.UnescapeString(a))
			if key == "" {
				continue
			}
			if prev, exists := listing[key]; exists && prev.slug != s.slug {
				listing[key] = listingShow{} // different shows share this alias
			} else {
				listing[key] = s
			}
		}
	}
	return listing
}
