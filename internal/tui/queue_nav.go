package tui

import (
	"fmt"
	"math/rand"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// Queue navigation in the main view.
//
// The queue lives under the track block; there is no separate queue panel and
// no highlight mode. One entry is always highlighted: it follows the playing
// track until ↑/↓ (or k/j), gg/G or shift+↑/↓ move it, and q puts it back on
// the playing track. space is always play/pause; enter plays the highlighted
// entry (queue kept), restarting it when it is the playing one. d (also
// x/delete) removes it, K/J
// move it, shift+d removes it and everything below, ctrl+shift+d removes
// everything above, R inserts five related songs right after it, c clears the
// queue and s jumps to a random queued song. Nothing else changes with the
// highlight's position, and esc does nothing in this view.

// noQueueCursor is the highlight value while the queue is empty.
const noQueueCursor = -1

// queueCursorActive reports whether a queue entry is highlighted, which is
// the case whenever the queue is not empty.
func (m *Model) queueCursorActive() bool {
	return m.queueCursor >= 0 && m.queueCursor < len(m.queueTracks)
}

// followPlayingTrack (the q key, and the state after a jump) puts the
// highlight on the playing track and keeps it there as tracks change.
func (m *Model) followPlayingTrack() {
	m.queueFollow = true
	if len(m.queueTracks) == 0 {
		m.queueCursor = noQueueCursor
		return
	}
	cur := m.currentQueueIndex()
	if cur < 0 {
		cur = max(0, min(m.queueCursor, len(m.queueTracks)-1))
	}
	m.queueCursor = cur
	m.ensureQueueCursorVisible()
}

// setQueueCursor moves the highlight to idx (clamped), scrolls it into view
// and stops it from following playback.
func (m *Model) setQueueCursor(idx int) {
	if len(m.queueTracks) == 0 {
		m.queueCursor = noQueueCursor
		return
	}
	m.queueFollow = false
	m.queueCursor = max(0, min(idx, len(m.queueTracks)-1))
	m.ensureQueueCursorVisible()
}

// moveQueueCursor moves the highlight by delta rows.
func (m *Model) moveQueueCursor(delta int) {
	if len(m.queueTracks) == 0 {
		m.queueCursor = noQueueCursor
		return
	}
	if !m.queueCursorActive() {
		m.followPlayingTrack()
	}
	m.setQueueCursor(m.queueCursor + delta)
}

// ensureQueueCursorVisible scrolls the mini-queue so the cursor row is shown.
func (m *Model) ensureQueueCursorVisible() {
	if !m.queueCursorActive() {
		return
	}
	visibleRows := max(1, m.panelHeight()-2)
	if m.queueCursor < m.queueMiniOffset {
		m.queueMiniOffset = m.queueCursor
	} else if m.queueCursor >= m.queueMiniOffset+visibleRows {
		m.queueMiniOffset = m.queueCursor - visibleRows + 1
	}
	m.queueMiniOffset = max(0, m.queueMiniOffset)
}

// clearQueue (the c key) empties the queue in the model and the engine.
func (m *Model) clearQueue() tea.Cmd {
	if len(m.queueTracks) == 0 && len(m.queueIDs) == 0 {
		return nil
	}
	m.appendLog("[queue] cleared")
	m.queueTracks = nil
	m.queueIDs = nil
	m.queueFollow = true
	m.syncQueue()
	return m.playerCmd(func(p player.Player) error { return p.ClearQueue() })
}

// jumpToRandomQueued (the s key) plays a random queued track other than the
// one playing, leaving the queue as it is.
func (m *Model) jumpToRandomQueued() tea.Cmd {
	n := len(m.queueTracks)
	if n == 0 || len(m.queueIDs) != n {
		return nil
	}
	cur := m.currentQueueIndex()
	if cur < 0 || cur >= n {
		return m.jumpToQueueIndex(rand.Intn(n)) //nolint:gosec // not security sensitive
	}
	if n == 1 {
		return nil
	}
	idx := rand.Intn(n - 1) //nolint:gosec // not security sensitive
	if idx >= cur {
		idx++
	}
	m.appendLog(fmt.Sprintf("[queue] random jump to position %d", idx+1))
	return m.jumpToQueueIndex(idx)
}

// syncEngineQueue makes the engine's queue match the model's after an edit.
// The playing track is kept by id; playID, when set, starts that track.
func (m *Model) syncEngineQueue(playID string) tea.Cmd {
	ids := append([]string(nil), m.queueIDs...)
	currentID := ""
	if m.playerState.Track != nil {
		currentID = views.PlaybackID(*m.playerState.Track)
	}
	return m.playerCmd(func(p player.Player) error { return p.SyncQueue(ids, currentID, playID) })
}

// clampQueueCursor keeps the highlight valid after queue edits: inside the
// queue, and on the playing track while it is following playback.
func (m *Model) clampQueueCursor() {
	if len(m.queueTracks) == 0 {
		m.queueCursor = noQueueCursor
		return
	}
	if m.queueFollow {
		if cur := m.currentQueueIndex(); cur >= 0 {
			m.queueCursor = cur
		}
	}
	if m.queueCursor >= len(m.queueTracks) {
		m.queueCursor = len(m.queueTracks) - 1
	}
	if m.queueCursor < 0 {
		m.queueCursor = 0
	}
}

// jumpToQueueIndex plays queue entry idx and leaves the queue untouched. When
// the engine has nothing loaded yet (a restored queue on a fresh start) the
// whole queue is handed over and started at idx.
func (m *Model) jumpToQueueIndex(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.queueIDs) || idx >= len(m.queueTracks) {
		return nil
	}
	t := m.queueTracks[idx]
	id := m.queueIDs[idx]
	m.appendLog(fmt.Sprintf("[queue] jumping to position %d: %s — %s", idx+1, t.Artist, t.Title))
	m.playerState.Loading = true
	m.playerState.Playing = false
	m.playerState.Position = 0
	m.queueFollow = true
	m.queueCursor = idx
	m.ensureQueueCursorVisible()
	if m.playerState.Track == nil {
		ids := append([]string(nil), m.queueIDs...)
		m.queueResumeIdx = noQueueCursor
		return m.playerCmd(func(p player.Player) error { return p.SetQueueAt(ids, id) })
	}
	return m.playerCmd(func(p player.Player) error { return p.PlayQueued(idx, id) })
}

// playQueueFrom starts playback at queue position idx. Like the queue panel's
// enter, the entries before idx are dropped and the rest is handed to the
// engine as the new queue.
func (m *Model) playQueueFrom(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.queueIDs) || idx >= len(m.queueTracks) {
		return nil
	}
	t := m.queueTracks[idx]
	ids := m.queueIDs[idx:]
	m.queueTracks = m.queueTracks[idx:]
	m.queueIDs = ids
	m.queueResumeIdx = noQueueCursor
	m.syncQueue()
	m.appendLog(fmt.Sprintf("[queue] playing from position %d: %s — %s", idx+1, t.Artist, t.Title))
	m.playerState.Loading = true
	m.playerState.Playing = false
	m.playerState.Position = 0
	return m.playerCmd(func(p player.Player) error { return p.SetQueue(ids) })
}

// removeQueueAt drops queue entry idx from the model and the engine.
func (m *Model) removeQueueAt(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.queueTracks) || idx >= len(m.queueIDs) {
		return nil
	}
	t := m.queueTracks[idx]
	wasPlaying := idx == m.currentQueueIndex() && m.playerState.Track != nil
	m.queueTracks = append(m.queueTracks[:idx], m.queueTracks[idx+1:]...)
	m.queueIDs = append(m.queueIDs[:idx], m.queueIDs[idx+1:]...)
	m.syncQueue()
	m.appendLog(fmt.Sprintf("[queue] removed #%d: %s — %s", idx+1, t.Artist, t.Title))
	// Removing the playing track moves on to the one that took its place.
	playID := ""
	if wasPlaying && idx < len(m.queueIDs) {
		playID = m.queueIDs[idx]
	}
	return m.syncEngineQueue(playID)
}

// moveQueueTrack swaps entry idx with its neighbour (delta -1 up, +1 down) in
// the model and the engine. It returns the entry's new index.
func (m *Model) moveQueueTrack(idx, delta int) (int, tea.Cmd) {
	to := idx + delta
	if idx < 0 || to < 0 || idx >= len(m.queueTracks) || to >= len(m.queueTracks) || len(m.queueIDs) != len(m.queueTracks) {
		return idx, nil
	}
	m.queueTracks[idx], m.queueTracks[to] = m.queueTracks[to], m.queueTracks[idx]
	m.queueIDs[idx], m.queueIDs[to] = m.queueIDs[to], m.queueIDs[idx]
	m.syncQueue()
	if delta < 0 {
		m.appendLog(fmt.Sprintf("[queue] moved #%d up", idx+1))
	} else {
		m.appendLog(fmt.Sprintf("[queue] moved #%d down", idx+1))
	}
	return to, m.syncEngineQueue("")
}

// handleQueueCursorKey handles the main-view keys that act on the highlighted
// queue entry. It reports whether the key was consumed. Movement keys live in
// handleNormalKey's no-panel switch; this covers the actions.
func (m *Model) handleQueueCursorKey(k string) (tea.Cmd, bool) {
	if !m.queueCursorActive() {
		return nil, false
	}
	switch k {
	case "K", "J":
		delta := 1
		if k == "K" {
			delta = -1
		}
		follow := m.queueFollow
		newIdx, cmd := m.moveQueueTrack(m.queueCursor, delta)
		m.queueCursor = newIdx
		m.queueFollow = follow // moving the playing track keeps following it
		m.ensureQueueCursorVisible()
		m.lastKey = ""
		return cmd, true
	case "enter":
		// Start the highlighted entry; on the playing track this restarts it.
		// space is never handled here: it is always plain play/pause.
		m.lastKey = ""
		return m.jumpToQueueIndex(m.queueCursor), true
	case "d", "x", "delete":
		idx := m.queueCursor
		cmd := m.removeQueueAt(idx)
		if m.queueCursorActive() {
			m.ensureQueueCursorVisible()
		}
		m.lastKey = ""
		return cmd, true
	case "D":
		m.lastKey = ""
		return m.deleteQueueTail(m.queueCursor), true
	case "ctrl+shift+d", "ctrl+shift+D":
		m.lastKey = ""
		return m.deleteQueueHead(m.queueCursor), true
	}
	return nil, false
}

// deleteQueueHead (ctrl+shift+d) removes everything above the highlighted
// entry, which becomes the first in the queue. If the playing track is among
// them, playback moves to the highlighted entry.
func (m *Model) deleteQueueHead(before int) tea.Cmd {
	if before <= 0 || before >= len(m.queueTracks) || before >= len(m.queueIDs) {
		return nil
	}
	cur := m.currentQueueIndex()
	playingRemoved := m.playerState.Track != nil && cur >= 0 && cur < before
	m.queueTracks = m.queueTracks[before:]
	m.queueIDs = m.queueIDs[before:]
	m.syncQueue()
	if !m.queueFollow {
		m.setQueueCursor(0)
	}
	m.appendLog(fmt.Sprintf("[queue] removed the %d track(s) above position %d", before, before+1))
	// If the playing track was among them, playback moves to the highlighted
	// entry, now first in the queue.
	playID := ""
	if playingRemoved && len(m.queueIDs) > 0 {
		playID = m.queueIDs[0]
	}
	return m.syncEngineQueue(playID)
}

// deleteQueueTail (shift+d) removes the highlighted entry and everything
// below it. If the playing track is among them, playback stops as well, since
// nothing is left to play after it.
func (m *Model) deleteQueueTail(from int) tea.Cmd {
	if from < 0 || from >= len(m.queueTracks) || from >= len(m.queueIDs) {
		return nil
	}
	includesPlaying := false
	if cur := m.currentQueueIndex(); cur >= from && m.playerState.Track != nil {
		includesPlaying = true
	}
	removed := len(m.queueTracks) - from
	m.queueTracks = m.queueTracks[:from]
	m.queueIDs = m.queueIDs[:from]
	m.syncQueue()
	if from > 0 && !includesPlaying {
		m.setQueueCursor(from - 1)
	}
	m.appendLog(fmt.Sprintf("[queue] removed %d track(s) from position %d to the end", removed, from+1))
	// The engine stops by itself when the playing track is among the removed.
	return m.syncEngineQueue("")
}
