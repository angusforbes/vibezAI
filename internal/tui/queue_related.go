package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// One-shot related songs (the R key): fetch the seed's Apple Music station
// once, keep up to relatedCount picks that are not already queued, and insert
// them right after the seed. Nothing keeps refilling afterwards; the
// continuous station mode is still available as the :radio command.

const relatedCount = 5

// relatedResultMsg carries the picks for one R press.
type relatedResultMsg struct {
	gen    int
	seed   provider.Track
	tracks []provider.Track
	err    error
}

// fetchRelatedCmd starts a related-songs lookup for seed.
func (m *Model) fetchRelatedCmd(seed *provider.Track) tea.Cmd {
	if seed == nil || m.provider == nil {
		return nil
	}
	m.relatedGen++
	gen := m.relatedGen
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
	m.errMsg = fmt.Sprintf("⏳ Finding songs related to %s…", s.Title)
	m.errExpiry = time.Now().Add(15 * time.Second)
	m.appendLog(fmt.Sprintf("[related] fetching station picks for %q", s.Title))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := prov.GetStationTracks(ctx, catalogID)
		if err != nil {
			return relatedResultMsg{gen: gen, seed: s, err: err}
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
			if len(picks) == relatedCount {
				break
			}
		}
		return relatedResultMsg{gen: gen, seed: s, tracks: picks}
	}
}

// handleRelatedResult inserts the picks right after the seed (or at the end
// when the seed has left the queue) and mirrors the change into the engine.
func (m *Model) handleRelatedResult(msg relatedResultMsg) tea.Cmd {
	if msg.gen != m.relatedGen {
		return nil
	}
	if msg.err != nil {
		m.errMsg = "Related songs: " + msg.err.Error()
		m.errExpiry = time.Now().Add(5 * time.Second)
		m.appendLog(fmt.Sprintf("[related] error: %v", msg.err))
		return nil
	}
	if len(msg.tracks) == 0 {
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
	m.errMsg = fmt.Sprintf("✓ Added %d related song(s) after %s", len(ids), msg.seed.Title)
	m.errExpiry = time.Now().Add(4 * time.Second)
	m.appendLog(fmt.Sprintf("[related] inserted %d track(s) after %q (position %d)", len(ids), msg.seed.Title, insertIdx))
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
	appended := append([]string(nil), ids...)
	return m.playerCmd(func(p player.Player) error {
		if err := p.AppendQueue(appended); err != nil {
			return err
		}
		for i := range appended {
			from := origLen + i
			to := insertIdx + i
			if from == to {
				continue
			}
			if err := p.MoveInQueue(from, to); err != nil {
				return err
			}
		}
		return nil
	})
}
