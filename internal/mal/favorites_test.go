package mal

import "testing"

// TestParseFavoriteStudios: only the companies section (/anime/producer links)
// makes the set; anime favorites and other sections don't, and an unrecognized
// page yields an empty set (the marker is cosmetic — no error).
func TestParseFavoriteStudios(t *testing.T) {
	// Shape lifted from the real /profile/<user>/favorites page.
	body := []byte(`
      <div class="boxlist col-4">
  <div class="di-tc">
    <a href="https://myanimelist.net/anime/producer/287/David_Production">
      <img class="lazyload image profile-w48" src="https://cdn.myanimelist.net/images/spacer.gif" data-src="https://cdn.myanimelist.net/s/common/company_logos/x.jpeg" alt="David Production" />
    </a>
  </div>
  <div class="di-tc va-t pl8 data">
    <div class="title"><a href="https://myanimelist.net/anime/producer/287/David_Production">David Production</a></div>
          </div>
</div>
                  <div class="boxlist col-4">
  <div class="di-tc">
    <a href="https://myanimelist.net/anime/producer/569/MAPPA">
      <img class="lazyload image profile-w48" src="https://cdn.myanimelist.net/images/spacer.gif" data-src="https://cdn.myanimelist.net/s/common/company_logos/y.jpeg" alt="MAPPA" />
    </a>
  </div>
</div>
        <a href="https://myanimelist.net/anime/52991/Sousou_no_Frieren">
      <img alt="Sousou no Frieren" />
    </a>`)
	got := parseFavoriteStudios(body)
	want := map[string]bool{"David Production": true, "MAPPA": true}
	if len(got) != len(want) {
		t.Fatalf("parseFavoriteStudios = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("parseFavoriteStudios missing %q: %v", k, got)
		}
	}

	if got := parseFavoriteStudios([]byte("not the favorites page")); len(got) != 0 {
		t.Errorf("parseFavoriteStudios(unrecognized) = %v, want empty", got)
	}
	if got := parseFavoriteStudios(nil); len(got) != 0 {
		t.Errorf("parseFavoriteStudios(nil) = %v, want empty", got)
	}
}
