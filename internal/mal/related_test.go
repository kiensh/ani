package mal

import (
	"testing"

	"github.com/nstratos/go-myanimelist/mal"
)

// TestRelatedEntries: dedup by id, self-references dropped, cover falls back
// to medium, and the formatted relation label is preferred over the raw kind.
func TestRelatedEntries(t *testing.T) {
	in := []mal.RelatedAnime{
		{Node: mal.Anime{ID: 101, Title: "S2"}, RelationType: "sequel", RelationTypeFormatted: "Sequel"},
		{Node: mal.Anime{ID: 101, Title: "S2 duplicate"}, RelationType: "sequel", RelationTypeFormatted: "Sequel"}, // dup id
		{Node: mal.Anime{ID: 100, Title: "base itself"}, RelationType: "other"},                                    // self
		{Node: mal.Anime{ID: 102, Title: "OVA", MainPicture: mal.Picture{Medium: "m.jpg"}},
			RelationType: "side_story", RelationTypeFormatted: "Side story"},
		{Node: mal.Anime{ID: 103, Title: "Pre"}, RelationType: "prequel"}, // no formatted label
	}
	got := relatedEntries(in, 100)
	if len(got) != 3 {
		t.Fatalf("relatedEntries = %d entries, want 3 (dup + self dropped): %+v", len(got), got)
	}
	want := []RelatedEntry{
		{Relation: "Sequel", Kind: "sequel", MalID: 101, Title: "S2"},
		{Relation: "Side story", Kind: "side_story", MalID: 102, Title: "OVA", CoverURL: "m.jpg"},
		{Relation: "prequel", Kind: "prequel", MalID: 103, Title: "Pre"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("entry[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}
