package tui

import (
	"context"
	"testing"

	"github.com/simone-vibes/vibez/internal/provider"
)

// artistSearchProvider answers Search with a fixed list (and records the query).
type artistSearchProvider struct {
	mockProvider
	query string
	res   []provider.Track
}

func (p *artistSearchProvider) Search(_ context.Context, q string) (*provider.SearchResult, error) {
	p.query = q
	return &provider.SearchResult{Tracks: p.res}, nil
}

func TestSameArtist(t *testing.T) {
	cases := []struct {
		got, want string
		ok        bool
	}{
		{"Radiohead", "radiohead", true},
		{"Radiohead  ", "Radiohead", true},
		{"Radiohead & Thom Yorke", "Radiohead", true},
		{"Radiohead feat. Someone", "Radiohead", true},
		{"Radioheadz", "Radiohead", false},
		{"Thom Yorke", "Radiohead", false},
		{"", "Radiohead", false},
	}
	for _, c := range cases {
		if got := sameArtist(c.got, c.want); got != c.ok {
			t.Errorf("sameArtist(%q, %q) = %v, want %v", c.got, c.want, got, c.ok)
		}
	}
}

func TestB_InsertsFiveMoreBySameArtistNotQueued(t *testing.T) {
	m, _ := navModel(t)
	// The queue already holds "Two" by B; the search also returns another
	// artist, a duplicate title and more than five new songs by B.
	p := &artistSearchProvider{res: []provider.Track{
		{ID: "2", Title: "Two", Artist: "B"},
		{ID: "x1", Title: "Other", Artist: "Someone Else"},
		{ID: "b1", Title: "B1", Artist: "B"},
		{ID: "b1dup", Title: "b1", Artist: "b"},
		{ID: "b2", Title: "B2", Artist: "B & Friend"},
		{ID: "b3", Title: "B3", Artist: "B"},
		{ID: "b4", Title: "B4", Artist: "B"},
		{ID: "b5", Title: "B5", Artist: "B"},
		{ID: "b6", Title: "B6", Artist: "B"},
	}}
	m.provider = p
	key(m, "q") // highlight the playing track, "Two" by B
	cmd := key(m, "B")
	if cmd == nil {
		t.Fatal("B should start a search")
	}
	msg := cmd()
	if p.query != "B" {
		t.Fatalf("searched for %q, want the artist B", p.query)
	}
	res, ok := msg.(artistMoreResultMsg)
	if !ok {
		t.Fatalf("got %T, want artistMoreResultMsg", msg)
	}
	var got []string
	for _, tr := range res.tracks {
		got = append(got, tr.ID)
	}
	want := []string{"b1", "b2", "b3", "b4", "b5"}
	if len(got) != len(want) {
		t.Fatalf("picks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("picks = %v, want %v", got, want)
		}
	}
	m.Update(res)
	wantQ := []string{"1", "2", "b1", "b2", "b3", "b4", "b5", "3", "4"}
	if len(m.queueIDs) != len(wantQ) {
		t.Fatalf("queue = %v, want %v", m.queueIDs, wantQ)
	}
	for i := range wantQ {
		if m.queueIDs[i] != wantQ[i] {
			t.Fatalf("queue = %v, want %v", m.queueIDs, wantQ)
		}
	}

	// A second press skips the ones now queued.
	key(m, "q")
	res2 := key(m, "B")().(artistMoreResultMsg)
	if len(res2.tracks) != 1 || res2.tracks[0].ID != "b6" {
		t.Fatalf("second press should only offer b6, got %+v", res2.tracks)
	}
}
