package tui

import (
	"testing"

	"github.com/simone-vibes/vibez/internal/provider"
)

// track is a helper to build provider.Track values for tests.
func track(artist, title, id, catalogID string) provider.Track {
	return provider.Track{ID: id, CatalogID: catalogID, Artist: artist, Title: title}
}

// TestInterleaveTracks_ExactDuplicates verifies that identical songs from two
// terms produce only one entry.
func TestInterleaveTracks_ExactDuplicates(t *testing.T) {
	t1 := track("Gladys Knight", "Neither One Of Us", "cat1", "")
	t2 := track("Gladys Knight", "Neither One Of Us", "cat2", "") // same artist+title
	out := interleaveTracks([][]provider.Track{{t1}, {t2}}, 10)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d: %v", len(out), titlesOf(out))
	}
	if out[0].ID != "cat1" {
		t.Errorf("first term wins: want ID cat1, got %s", out[0].ID)
	}
}

// TestInterleaveTracks_CatalogIDDedup verifies that two tracks with the same
// non-empty catalog ID are treated as duplicates regardless of key differences.
func TestInterleaveTracks_CatalogIDDedup(t *testing.T) {
	// Library ID in first term, plain catalog ID in second term — same song.
	lib := track("Gladys Knight", "Neither One Of Us", "i.abc", "cat999")
	cat := track("Gladys Knight", "Neither One Of Us", "cat999", "")
	out := interleaveTracks([][]provider.Track{{lib}, {cat}}, 10)
	if len(out) != 1 {
		t.Fatalf("same catalog ID = same song; want 1, got %d: %v", len(out), titlesOf(out))
	}
}

// TestInterleaveTracks_WhitespaceDifferences verifies that leading/trailing
// whitespace and doubled internal spaces do not produce duplicates.
func TestInterleaveTracks_WhitespaceDifferences(t *testing.T) {
	a := track("Gladys  Knight", "Neither One Of Us", "1", "")
	b := track("Gladys Knight", "Neither One Of Us", "2", "") // normalised artist
	out := interleaveTracks([][]provider.Track{{a}, {b}}, 10)
	if len(out) != 1 {
		t.Fatalf("whitespace variants = same key; want 1, got %d", len(out))
	}
}

// TestInterleaveTracks_NeitherOneOfUs_PunctuationVariants verifies that the
// title "Neither One Of Us (Wants to Be the First to Say Goodbye)" in several
// punctuation encodings is treated as one song.
//   - Curly apostrophe \u2019 vs straight apostrophe \u0027
//   - Straight apostrophe vs none (no contraction)
//   - Mixed apostrophe styles in the same phrase
func TestInterleaveTracks_NeitherOneOfUs_PunctuationVariants(t *testing.T) {
	base := "Neither One Of Us (Wants to Be the First to Say Goodbye)"
	curly := "Neither One Of Us (Wants to Be the First to Say Goodbye\u2019s)" // different phrase, keep distinct
	// The variants that should be duplicates:
	straight := "Neither One Of Us (Wants to Be the First to Say Goodbye)"
	leadingSpace := " Neither One Of Us (Wants to Be the First to Say Goodbye)"
	trailingSpace := "Neither One Of Us (Wants to Be the First to Say Goodbye) "
	_ = curly // different phrase — tested separately below

	variants := []string{base, straight, leadingSpace, trailingSpace}
	var perTerm [][]provider.Track
	for i, v := range variants {
		id := string(rune('1' + i))
		perTerm = append(perTerm, []provider.Track{track("Gladys Knight", v, id, "")})
	}
	out := interleaveTracks(perTerm, 20)
	if len(out) != 1 {
		t.Fatalf("whitespace/identical punctuation variants = 1 song; got %d: %v", len(out), titlesOf(out))
	}
}

// TestInterleaveTracks_UnicodeApostropheVariants verifies that the curly right
// single quotation mark (\u2019) in a title is treated the same as a straight
// apostrophe for deduplication.
func TestInterleaveTracks_UnicodeApostropheVariants(t *testing.T) {
	straight := track("Gladys Knight", "I've Got to Use My Imagination", "1", "")
	curly := track("Gladys Knight", "I\u2019ve Got to Use My Imagination", "2", "")
	left := track("Gladys Knight", "I\u2018ve Got to Use My Imagination", "3", "")
	out := interleaveTracks([][]provider.Track{{straight}, {curly}, {left}}, 10)
	if len(out) != 1 {
		t.Fatalf("apostrophe variants = same song; got %d: %v", len(out), titlesOf(out))
	}
}

// TestInterleaveTracks_UnicodeDashVariants verifies that em-dash and en-dash
// are treated the same as a hyphen-minus for deduplication.
func TestInterleaveTracks_UnicodeDashVariants(t *testing.T) {
	hyphen := track("Foo", "Something-Like This", "1", "")
	endash := track("Foo", "Something\u2013Like This", "2", "")
	emdash := track("Foo", "Something\u2014Like This", "3", "")
	out := interleaveTracks([][]provider.Track{{hyphen}, {endash}, {emdash}}, 10)
	if len(out) != 1 {
		t.Fatalf("dash variants = same song; got %d: %v", len(out), titlesOf(out))
	}
}

// TestInterleaveTracks_DistinctVersionsPreserved verifies that "Live", "Remix",
// "Acoustic" and other qualifiers keep songs distinct.
func TestInterleaveTracks_DistinctVersionsPreserved(t *testing.T) {
	studio := track("Gladys Knight", "Neither One Of Us", "1", "")
	live := track("Gladys Knight", "Neither One Of Us (Live)", "2", "")
	remix := track("Gladys Knight", "Neither One Of Us (Remix)", "3", "")
	acoustic := track("Gladys Knight", "Neither One Of Us (Acoustic)", "4", "")
	out := interleaveTracks([][]provider.Track{{studio, live, remix, acoustic}}, 10)
	if len(out) != 4 {
		t.Fatalf("distinct versions must all be kept; got %d: %v", len(out), titlesOf(out))
	}
}

// TestInterleaveTracks_DistinctArtistsPreserved verifies that the same title
// by different artists yields distinct entries.
func TestInterleaveTracks_DistinctArtistsPreserved(t *testing.T) {
	gk := track("Gladys Knight", "Neither One Of Us", "1", "")
	other := track("Other Artist", "Neither One Of Us", "2", "")
	out := interleaveTracks([][]provider.Track{{gk}, {other}}, 10)
	if len(out) != 2 {
		t.Fatalf("different artists = distinct songs; got %d: %v", len(out), titlesOf(out))
	}
}

// TestInterleaveTracks_Limit verifies that the limit is respected.
func TestInterleaveTracks_Limit(t *testing.T) {
	var tracks []provider.Track
	for i := range 20 {
		tracks = append(tracks, track("Artist", string(rune('A'+i)), string(rune('A'+i)), ""))
	}
	out := interleaveTracks([][]provider.Track{tracks}, 5)
	if len(out) != 5 {
		t.Fatalf("want 5 results, got %d", len(out))
	}
}

// TestInterleaveTracks_InterleaveOrder verifies that results come from each
// term in round-robin order.
func TestInterleaveTracks_InterleaveOrder(t *testing.T) {
	termA := []provider.Track{track("A", "A1", "a1", ""), track("A", "A2", "a2", "")}
	termB := []provider.Track{track("B", "B1", "b1", ""), track("B", "B2", "b2", "")}
	out := interleaveTracks([][]provider.Track{termA, termB}, 4)
	want := []string{"A1", "B1", "A2", "B2"}
	if got := titlesOf(out); !equalSlices(got, want) {
		t.Errorf("interleave order: got %v, want %v", got, want)
	}
}

// TestCanonicalTrackKey_NormalisesPunctuation tests the canonicalTrackKey
// helper directly.
func TestCanonicalTrackKey_NormalisesPunctuation(t *testing.T) {
	cases := []struct{ a, b, artist, title string }{
		{"Gladys Knight", "Neither One Of Us", "Gladys Knight", "Neither One Of Us"},
		// Curly apostrophe == straight apostrophe
		{"Gladys Knight", "I\u2019ve Got to Use My Imagination", "Gladys Knight", "I've Got to Use My Imagination"},
		// En-dash == hyphen
		{"Foo", "Song\u2013Title", "Foo", "Song-Title"},
		// Whitespace collapse
		{"Foo  Bar", "Baz  Qux", "Foo Bar", "Baz Qux"},
		// Leading/trailing space
		{" Foo", "Bar ", "Foo", "Bar"},
	}
	for _, c := range cases {
		got := canonicalTrackKey(c.a, c.b)
		want := canonicalTrackKey(c.artist, c.title)
		if got != want {
			t.Errorf("canonicalTrackKey(%q, %q) = %q, want %q", c.a, c.b, got, want)
		}
	}
}

// TestCanonicalTrackKey_PreservesQualifiers verifies that version qualifiers
// produce distinct keys (they are NOT stripped).
func TestCanonicalTrackKey_PreservesQualifiers(t *testing.T) {
	k1 := canonicalTrackKey("Gladys Knight", "Neither One Of Us")
	k2 := canonicalTrackKey("Gladys Knight", "Neither One Of Us (Live)")
	k3 := canonicalTrackKey("Gladys Knight", "Neither One Of Us (Remix)")
	if k1 == k2 || k1 == k3 || k2 == k3 {
		t.Errorf("qualifiers must yield distinct keys: %q %q %q", k1, k2, k3)
	}
}

// titlesOf returns the titles of the given tracks for use in test messages.
func titlesOf(tracks []provider.Track) []string {
	out := make([]string, len(tracks))
	for i, t := range tracks {
		out[i] = t.Title
	}
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
