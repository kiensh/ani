package mal

import (
	"context"

	"github.com/nstratos/go-myanimelist/mal"
)

// RelatedEntry is one related anime of a base anime: how it relates to it and
// enough to list and peek at it. Kind is the raw relation type ("sequel",
// "prequel", "side_story", …); Relation its display form ("Sequel").
type RelatedEntry struct {
	Relation string
	Kind     string
	MalID    int
	Title    string
	CoverURL string
}

// Related returns baseID's related anime — prequels, sequels, side stories
// (OVAs/specials/movies), spin-offs, summaries, … — in MAL's own order,
// deduplicated by id (MAL repeats an anime across relation labels; the first
// label wins) and with self-references dropped.
func Related(baseID int, debug bool) ([]RelatedEntry, error) {
	c, err := Client(debug)
	if err != nil {
		return nil, err
	}
	dbg(debug, "DEBUG MAL GET /anime/%d fields=related_anime\n", baseID)
	a, _, err := c.Anime.Details(context.Background(), baseID, mal.Fields{"related_anime"})
	if err != nil {
		return nil, err
	}
	return relatedEntries(a.RelatedAnime, baseID), nil
}

// relatedEntries converts the API's related list into deduplicated entries.
func relatedEntries(rs []mal.RelatedAnime, baseID int) []RelatedEntry {
	seen := map[int]bool{baseID: true}
	out := make([]RelatedEntry, 0, len(rs))
	for _, r := range rs {
		if r.Node.ID == 0 || seen[r.Node.ID] {
			continue
		}
		seen[r.Node.ID] = true
		cover := r.Node.MainPicture.Large
		if cover == "" {
			cover = r.Node.MainPicture.Medium
		}
		relation := r.RelationTypeFormatted
		if relation == "" {
			relation = r.RelationType
		}
		out = append(out, RelatedEntry{
			Relation: relation,
			Kind:     r.RelationType,
			MalID:    r.Node.ID,
			Title:    r.Node.Title,
			CoverURL: cover,
		})
	}
	return out
}

// AnimeItem fetches the full picker item for one anime id — the same field
// set the list rows carry, including my_list_status. Used when peeking at a
// related anime: its preview and write actions need the complete fields the
// thin ring entry doesn't have.
func AnimeItem(id int, debug bool) (Item, error) {
	c, err := Client(debug)
	if err != nil {
		return Item{}, err
	}
	dbg(debug, "DEBUG MAL GET /anime/%d details\n", id)
	a, _, err := c.Anime.Details(context.Background(), id, ExtraFields)
	if err != nil {
		return Item{}, err
	}
	return animeToItem(*a), nil
}
