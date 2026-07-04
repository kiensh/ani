package ui

import (
	"testing"

	"ani/internal/animetosho"
)

func TestDedupSeries(t *testing.T) {
	in := []animetosho.SeriesSummary{
		{AnidbAID: 18886, Title: "Sousou no Frieren", TVDBSeason: 2, TorrentCount: 516},
		{AnidbAID: 18886, Title: "Frieren: Beyond Journey's End", TVDBSeason: 2, TorrentCount: 137},
		{AnidbAID: 17617, Title: "Frieren", TVDBSeason: 1, TorrentCount: 67},
		{AnidbAID: 18886, Title: "中配 variant", TVDBSeason: 2, TorrentCount: 1},
	}
	got := DedupSeries(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct anime, got %d: %+v", len(got), got)
	}
	// most torrents first
	if got[0].AnidbAID != 18886 || got[0].Title != "Sousou no Frieren" {
		t.Errorf("top pick wrong: %+v", got[0])
	}
	if got[1].AnidbAID != 17617 {
		t.Errorf("second pick wrong: %+v", got[1])
	}
}

func mkReleases() []*animetosho.Release {
	mk := func(title, date string, size int64, group string, ep int, batch bool) *animetosho.Release {
		return &animetosho.Release{
			Entry: &animetosho.Entry{Title: title, DateAdded: date, SizeBytes: size, ReleaseGroup: group, Series: animetosho.Series{EpisodeNumber: ep}, IsBatch: batch},
			Group: group, Episode: ep, IsBatch: batch,
		}
	}
	return []*animetosho.Release{
		mk("a", "2026-01-01T00:00:00Z", 100, "Erai-raws", 1, false),
		mk("b", "2026-03-01T00:00:00Z", 300, "SubsPlease", 2, false),
		mk("c", "2026-02-01T00:00:00Z", 200, "Erai-raws", 0, true),
	}
}

func TestSortedReleases(t *testing.T) {
	rs := mkReleases()

	newest := SortedReleases(rs, "newest")
	if newest[0].Entry.Title != "b" || newest[2].Entry.Title != "a" {
		t.Errorf("newest order wrong: %v", titles(newest))
	}
	oldest := SortedReleases(rs, "oldest")
	if oldest[0].Entry.Title != "a" || oldest[2].Entry.Title != "b" {
		t.Errorf("oldest order wrong: %v", titles(oldest))
	}
	smallest := SortedReleases(rs, "smallest")
	if smallest[0].Entry.SizeBytes != 100 || smallest[2].Entry.SizeBytes != 300 {
		t.Errorf("smallest order wrong: %v", sizes(smallest))
	}
	largest := SortedReleases(rs, "largest")
	if largest[0].Entry.SizeBytes != 300 || largest[2].Entry.SizeBytes != 100 {
		t.Errorf("largest order wrong: %v", sizes(largest))
	}
	// original must be untouched
	if rs[0].Entry.Title != "a" {
		t.Errorf("SortedReleases mutated its input")
	}
}

func TestFilterByGroup(t *testing.T) {
	rs := mkReleases()
	erai := FilterByGroup(rs, "Erai-raws")
	if len(erai) != 2 {
		t.Fatalf("expected 2 Erai-raws, got %d", len(erai))
	}
	if len(FilterByGroup(rs, "All")) != 3 {
		t.Errorf("All should return everything")
	}
	if len(FilterByGroup(rs, "")) != 3 {
		t.Errorf("empty group should return everything")
	}
}

func titles(rs []*animetosho.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Entry.Title
	}
	return out
}

func sizes(rs []*animetosho.Release) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.Entry.SizeBytes
	}
	return out
}

func TestDetectSeason(t *testing.T) {
	cases := map[string]int{
		"Ascendance of a Bookworm Season 3 | …3rd Season": 3,
		"Honzuki no Gekokujou Part 2":                     2,
		"Honzuki no Gekokujou 2nd Season":                 2,
		"Re:ZERO Season 4":                                4,
		"Sousou no Frieren":                               0,
		"(2022)":                                          0,
	}
	for title, want := range cases {
		if got := DetectSeason(title); got != want {
			t.Errorf("DetectSeason(%q) = %d, want %d", title, got, want)
		}
	}
}

func TestDedupSeriesComputesSeason(t *testing.T) {
	in := []animetosho.SeriesSummary{
		{AnidbAID: 15634, Title: "Ascendance of a Bookworm Season 3 | 3rd Season", TorrentCount: 1},
		{AnidbAID: 15634, Title: "Honzuki no Gekokujou (2022)", TorrentCount: 1},
	}
	got := DedupSeries(in)
	if len(got) != 1 || got[0].Season != 3 {
		t.Fatalf("expected 1 entry with season 3, got %+v", got)
	}
}
