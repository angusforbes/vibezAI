package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// More by the same artist (B): search for the highlighted (or playing)
// track's artist and insert up to artistMoreCount of their songs that are not
// in Tracks yet, right after it, the way R and T insert. Apple's search order
// (roughly popularity) is kept, so repeat presses walk further down the list;
// further catalog pages are fetched while the pick is short.

const (
	artistMoreCount    = 5
	artistMoreMaxPages = 4 // extra catalog pages one B press may fetch
)

// artistMoreResultMsg carries the picks for one B press.
type artistMoreResultMsg struct {
	gen    int
	seed   provider.Track
	tracks []provider.Track
	err    error
}

// fetchArtistMoreCmd starts the search for seed's artist.
func (m *Model) fetchArtistMoreCmd(seed *provider.Track) tea.Cmd {
	if seed == nil || m.provider == nil {
		return nil
	}
	artist := strings.TrimSpace(seed.Artist)
	if artist == "" {
		m.errMsg = "ℹ " + seed.Title + " has no artist to search for"
		m.errExpiry = time.Now().Add(4 * time.Second)
		return nil
	}
	m.artistGen++
	gen := m.artistGen
	s := *seed
	exclude := m.queuedKeys()
	exclude[views.PlaybackID(s)] = true
	prov := m.provider
	m.errMsg = fmt.Sprintf("⏳ Finding more by %s…", artist)
	m.errExpiry = time.Now().Add(20 * time.Second)
	m.appendLog(fmt.Sprintf("[artist] searching for more by %q", artist))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := prov.Search(ctx, artist)
		if err != nil {
			return artistMoreResultMsg{gen: gen, seed: s, err: err}
		}
		seen := map[string]bool{}
		picks := pickArtistTracks(res.Tracks, artist, exclude, seen, artistMoreCount)
		if pager, ok := prov.(provider.SongPager); ok {
			next, more := res.CatalogNext, res.CatalogMore
			for page := 0; len(picks) < artistMoreCount && more && page < artistMoreMaxPages; page++ {
				p, err := pager.SearchSongsPage(ctx, artist, next)
				if err != nil {
					break // keep what we have
				}
				picks = append(picks, pickArtistTracks(p.Tracks, artist, exclude, seen, artistMoreCount-len(picks))...)
				next, more = p.Next, p.More
			}
		}
		return artistMoreResultMsg{gen: gen, seed: s, tracks: picks}
	}
}

// pickArtistTracks returns up to n tracks from res, in order, whose artist is
// artist (see sameArtist), skipping anything in exclude or already in seen.
func pickArtistTracks(res []provider.Track, artist string, exclude, seen map[string]bool, n int) []provider.Track {
	var picks []provider.Track
	for _, t := range res {
		if len(picks) >= n {
			break
		}
		id := views.PlaybackID(t)
		key := strings.ToLower(t.Artist + "||" + t.Title)
		if id == "" || !sameArtist(t.Artist, artist) || exclude[id] || exclude[key] || seen[key] || seen[id] {
			continue
		}
		seen[key], seen[id] = true, true
		picks = append(picks, t)
	}
	return picks
}

// sameArtist reports whether a track credited to got is by want: the same
// name (case and spacing aside), or want leading a credit like
// "want & Other", "want, Other" or "want feat. Other".
func sameArtist(got, want string) bool {
	g := strings.ToLower(strings.Join(strings.Fields(got), " "))
	w := strings.ToLower(strings.Join(strings.Fields(want), " "))
	if g == "" || w == "" {
		return false
	}
	if g == w {
		return true
	}
	rest, ok := strings.CutPrefix(g, w)
	if !ok {
		return false
	}
	for _, sep := range []string{" & ", ", ", " and ", " feat. ", " feat ", " ft. ", " x ", " with "} {
		if strings.HasPrefix(rest, sep) {
			return true
		}
	}
	return false
}

// handleArtistMoreResult inserts the picks right after the seed (or at the
// end when the seed has left Tracks).
func (m *Model) handleArtistMoreResult(msg artistMoreResultMsg) tea.Cmd {
	if msg.gen != m.artistGen {
		return nil
	}
	artist := strings.TrimSpace(msg.seed.Artist)
	if msg.err != nil {
		m.errMsg = ""
		m.appendLog(fmt.Sprintf("[artist] error: %v", msg.err))
		return nil
	}
	if len(msg.tracks) == 0 {
		m.errMsg = "ℹ No more songs by " + artist + " that aren't in Tracks"
		m.errExpiry = time.Now().Add(4 * time.Second)
		return nil
	}
	seedID := views.PlaybackID(msg.seed)
	insertIdx := len(m.queueTracks)
	for i, t := range m.queueTracks {
		if views.PlaybackID(t) == seedID {
			insertIdx = i + 1
			break
		}
	}
	ids := make([]string, len(msg.tracks))
	for i, t := range msg.tracks {
		ids[i] = views.PlaybackID(t)
	}
	m.errMsg = fmt.Sprintf("✓ Added %d more by %s after %s", len(ids), artist, msg.seed.Title)
	m.errExpiry = time.Now().Add(4 * time.Second)
	m.appendLog(fmt.Sprintf("[artist] inserted %d track(s) by %q at position %d", len(ids), artist, insertIdx+1))
	return m.insertQueueAt(insertIdx, msg.tracks, ids)
}
