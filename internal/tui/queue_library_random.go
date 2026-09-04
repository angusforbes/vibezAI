package tui

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// Random songs from the user's library (ctrl+shift+r): pick randomLibraryCount
// library songs that are not queued yet and insert them right after the
// highlighted track, the way R inserts related songs. The whole library list
// is fetched once and cached for libraryCacheTTL so repeat presses are instant.

const (
	randomLibraryCount = 5
	libraryCacheTTL    = 10 * time.Minute
)

// randomLibraryResultMsg carries the picks (and, after a fetch, the library
// list to cache) for one ctrl+shift+r press.
type randomLibraryResultMsg struct {
	gen    int
	seed   *provider.Track
	all    []provider.Track // non-nil after a fresh fetch, to refresh the cache
	tracks []provider.Track
	err    error
}

// fetchRandomLibraryCmd starts (or, with a fresh cache, completes) a random
// library pick seeded after seed (nil appends at the end).
func (m *Model) fetchRandomLibraryCmd(seed *provider.Track) tea.Cmd {
	if m.provider == nil {
		return nil
	}
	m.randomGen++
	gen := m.randomGen
	exclude := m.queuedKeys()
	if time.Since(m.libraryCacheAt) < libraryCacheTTL && len(m.libraryCache) > 0 {
		picks := pickRandomTracks(m.libraryCache, exclude, randomLibraryCount)
		return func() tea.Msg { return randomLibraryResultMsg{gen: gen, seed: seed, tracks: picks} }
	}
	prov := m.provider
	m.errMsg = "⏳ Picking songs from your library…"
	m.errExpiry = time.Now().Add(60 * time.Second)
	m.appendLog("[library] fetching the library for a random pick")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		all, err := prov.GetLibraryTracks(ctx)
		if err != nil {
			return randomLibraryResultMsg{gen: gen, seed: seed, err: err}
		}
		return randomLibraryResultMsg{gen: gen, seed: seed, all: all, tracks: pickRandomTracks(all, exclude, randomLibraryCount)}
	}
}

// queuedKeys returns the playback ids and artist||title keys of the queue,
// the two ways a duplicate is recognised.
func (m *Model) queuedKeys() map[string]bool {
	keys := make(map[string]bool, 2*len(m.queueTracks))
	for _, id := range m.queueIDs {
		keys[id] = true
	}
	for _, t := range m.queueTracks {
		keys[strings.ToLower(t.Artist+"||"+t.Title)] = true
	}
	return keys
}

// pickRandomTracks returns up to n tracks from all, in random order, skipping
// anything in exclude and repeats within the pick.
func pickRandomTracks(all []provider.Track, exclude map[string]bool, n int) []provider.Track {
	order := rand.Perm(len(all)) //nolint:gosec // not security sensitive
	seen := make(map[string]bool, n)
	var picks []provider.Track
	for _, i := range order {
		t := all[i]
		id := views.PlaybackID(t)
		key := strings.ToLower(t.Artist + "||" + t.Title)
		if id == "" || exclude[id] || exclude[key] || seen[key] {
			continue
		}
		seen[key] = true
		picks = append(picks, t)
		if len(picks) == n {
			break
		}
	}
	return picks
}

// handleRandomLibraryResult caches a fresh library list and inserts the picks
// after the seed (or at the end when the seed is gone or nil).
func (m *Model) handleRandomLibraryResult(msg randomLibraryResultMsg) tea.Cmd {
	if msg.all != nil {
		m.libraryCache = msg.all
		m.libraryCacheAt = time.Now()
	}
	if msg.gen != m.randomGen {
		return nil
	}
	if msg.err != nil {
		m.errMsg = "Library: " + msg.err.Error()
		m.errExpiry = time.Now().Add(5 * time.Second)
		m.appendLog(fmt.Sprintf("[library] random pick error: %v", msg.err))
		return nil
	}
	if len(msg.tracks) == 0 {
		m.errMsg = "ℹ No unqueued songs left in your library"
		m.errExpiry = time.Now().Add(4 * time.Second)
		return nil
	}
	insertIdx := len(m.queueTracks)
	after := "the end"
	if msg.seed != nil {
		seedID := views.PlaybackID(*msg.seed)
		for i, t := range m.queueTracks {
			if views.PlaybackID(t) == seedID {
				insertIdx = i + 1
				after = msg.seed.Title
				break
			}
		}
	}
	ids := make([]string, len(msg.tracks))
	for i, t := range msg.tracks {
		ids[i] = views.PlaybackID(t)
	}
	m.errMsg = fmt.Sprintf("✓ Added %d song(s) from your library after %s", len(ids), after)
	m.errExpiry = time.Now().Add(4 * time.Second)
	m.appendLog(fmt.Sprintf("[library] inserted %d random track(s) at position %d", len(ids), insertIdx+1))
	return m.insertQueueAt(insertIdx, msg.tracks, ids)
}
