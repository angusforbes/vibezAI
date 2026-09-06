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

// Related songs from the seed's Apple Music station, fetched once and inserted
// right after the seed. R takes relatedCount picks for the highlighted track;
// discover (F) takes one pick for each track that starts playing. Nothing keeps
// refilling afterwards; the continuous station mode is the :radio command.
const relatedCount = 5

// relatedResultMsg carries the picks for one R press.
type relatedResultMsg struct {
	gen    int
	seed   provider.Track
	tracks []provider.Track
	err    error
	// discover marks a pick fetched by discover (F): quiet (log only) and never
	// superseded by relatedGen, unlike an R press.
	discover bool
}

// fetchRelatedCmd starts a station lookup for seed and keeps up to count picks
// that are not already queued. discover marks the quiet, once-per-track form.
func (m *Model) fetchRelatedCmd(seed *provider.Track, count int, discover bool) tea.Cmd {
	if count <= 0 {
		count = 1
	}
	if seed == nil || m.provider == nil {
		return nil
	}
	// A library-only track (the user's own recording or upload) has no catalog
	// counterpart, so Apple cannot build a station from it. Nothing to do.
	if seed.CatalogID == "" && strings.HasPrefix(seed.ID, "i.") {
		m.appendLog(fmt.Sprintf("[related] %q has no catalog match; skipping", seed.Title))
		return nil
	}
	gen := 0
	if !discover {
		// Only the latest R press counts; a discover pick is never superseded.
		m.relatedGen++
		gen = m.relatedGen
	}
	s := *seed
	seedID := views.PlaybackID(s)
	catalogID := s.CatalogID
	if catalogID == "" {
		catalogID = s.ID
	}
	prov := m.provider
	exclude := make(map[string]bool, 2*len(m.queueTracks)+1)
	exclude[seedID] = true
	for _, id := range m.queueIDs {
		exclude[id] = true
	}
	for _, t := range m.queueTracks {
		exclude[strings.ToLower(t.Artist+"||"+t.Title)] = true
	}
	tag := "related"
	if discover {
		tag = "discover"
	} else {
		m.errMsg = fmt.Sprintf("⏳ Finding songs related to %s…", s.Title)
		m.errExpiry = time.Now().Add(15 * time.Second)
	}
	m.appendLog(fmt.Sprintf("[%s] fetching station picks for %q", tag, s.Title))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := prov.GetStationTracks(ctx, catalogID)
		if err != nil {
			return relatedResultMsg{gen: gen, seed: s, err: err, discover: discover}
		}
		var picks []provider.Track
		seen := map[string]bool{}
		for _, t := range res {
			id := views.PlaybackID(t)
			key := strings.ToLower(t.Artist + "||" + t.Title)
			if exclude[id] || exclude[key] || seen[key] {
				continue
			}
			seen[key] = true
			picks = append(picks, t)
			if len(picks) == count {
				break
			}
		}
		return relatedResultMsg{gen: gen, seed: s, tracks: picks, discover: discover}
	}
}

// handleRelatedResult inserts the picks right after the seed (or at the end
// when the seed has left the queue) and mirrors the change into the engine.
// R results report on the status line; discover picks only log.
func (m *Model) handleRelatedResult(msg relatedResultMsg) tea.Cmd {
	tag := "related"
	switch {
	case msg.discover && !m.discover.on:
		m.appendLog(fmt.Sprintf("[discover] off before the pick for %q arrived; dropped", msg.seed.Title))
		return nil
	case msg.discover:
		tag = "discover"
	case msg.gen != m.relatedGen:
		return nil
	}
	if msg.err != nil {
		// Fail quietly: take down the "Finding songs…" notice and log it.
		if !msg.discover {
			m.errMsg = ""
		}
		m.appendLog(fmt.Sprintf("[%s] error: %v", tag, msg.err))
		return nil
	}
	if len(msg.tracks) == 0 {
		if msg.discover {
			m.appendLog(fmt.Sprintf("[discover] no new station pick for %q", msg.seed.Title))
			return nil
		}
		m.errMsg = "ℹ No new related songs found for " + msg.seed.Title
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
	if !msg.discover {
		m.errMsg = fmt.Sprintf("✓ Added %d related song(s) after %s", len(ids), msg.seed.Title)
		m.errExpiry = time.Now().Add(4 * time.Second)
	}
	m.appendLog(fmt.Sprintf("[%s] inserted %d track(s) after %q (position %d)", tag, len(ids), msg.seed.Title, insertIdx))
	return m.insertQueueAt(insertIdx, msg.tracks, ids)
}

// insertQueueAt inserts tracks at insertIdx in the model and the engine (the
// engine only appends, so each track is appended and then moved into place).
// A highlight below the insertion point keeps pointing at the same track.
func (m *Model) insertQueueAt(insertIdx int, tracks []provider.Track, ids []string) tea.Cmd {
	origLen := len(m.queueTracks)
	if insertIdx < 0 || insertIdx > origLen {
		insertIdx = origLen
	}
	m.queueTracks = append(m.queueTracks[:insertIdx], append(append([]provider.Track(nil), tracks...), m.queueTracks[insertIdx:]...)...)
	m.queueIDs = append(m.queueIDs[:insertIdx], append(append([]string(nil), ids...), m.queueIDs[insertIdx:]...)...)
	if m.queueCursor >= insertIdx {
		m.queueCursor += len(tracks)
	}
	m.syncQueue()
	return m.syncEngineQueue("")
}
