package ui

import (
	"testing"

	"ani/internal/mal"
)

func TestRenderMALLine(t *testing.T) {
	cases := []struct {
		item mal.Item
		want string
	}{
		{mal.Item{Title: "Frieren", TotalEps: 28, WatchedEps: 3, AirStatus: "currently_airing", Score: 9}, "Frieren  ep 3/28  [airing]  ★9"},
		{mal.Item{Title: "X", TotalEps: 12, WatchedEps: 12, AirStatus: "finished_airing"}, "X  ep 12/12  [done]"},
		{mal.Item{Title: "Y", AirStatus: "not_yet_aired"}, "Y  [unaired]"},
	}
	for _, c := range cases {
		if got := RenderMALLine(c.item); got != c.want {
			t.Errorf("RenderMALLine(%+v) = %q, want %q", c.item, got, c.want)
		}
	}
}
