package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/player"
)

// Queue navigation in the main view.
//
// The queue lives under "Now Playing"; there is no separate queue panel. The
// list has a cursor: ↑/↓ (or k/j) highlight an entry without touching
// playback, q highlights the playing track (or drops the highlight), space or
// enter jumps to the highlighted track with the whole queue left in place, d
// (also x/delete) removes it, K/J move it, R seeds radio from it, c clears the
// queue, gg/G jump to the ends and esc drops the cursor so the list follows the
// playing track again. shift+↑/↓ jump to the top/bottom of the list and
// shift+d removes the highlighted entry and everything below it. Every other
// key behaves exactly as without a highlight.

// noQueueCursor means no entry is highlighted; the list follows playback.
const noQueueCursor = -1

// queueCursorActive reports whether a queue entry is highlighted.
func (m *Model) queueCursorActive() bool {
	return m.queueCursor >= 0 && m.queueCursor < len(m.queueTracks)
}

// clearQueueCursor drops the highlight; the mini-queue follows playback again.
func (m *Model) clearQueueCursor() {
	m.queueCursor = noQueueCursor
}

// setQueueCursor highlights idx (clamped) and scrolls it into view.
func (m *Model) setQueueCursor(idx int) {
	if len(m.queueTracks) == 0 {
		m.queueCursor = noQueueCursor
		return
	}
	m.queueCursor = max(0, min(idx, len(m.queueTracks)-1))
	m.ensureQueueCursorVisible()
}

// moveQueueCursor moves the highlight by delta rows. The first move starts
// from the playing track (or the restore point, or the first visible row).
func (m *Model) moveQueueCursor(delta int) {
	if len(m.queueTracks) == 0 {
		m.queueCursor = noQueueCursor
		return
	}
	if !m.queueCursorActive() {
		start := m.currentQueueIndex()
		if start < 0 {
			m.setQueueCursor(m.queueMiniOffset)
			return
		}
		m.setQueueCursor(start + delta)
		return
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

// toggleQueueHighlight (the q key) puts the highlight on the playing track,
// or drops it when something is already highlighted.
func (m *Model) toggleQueueHighlight() {
	if m.queueCursorActive() {
		m.clearQueueCursor()
		return
	}
	if cur := m.currentQueueIndex(); cur >= 0 {
		m.setQueueCursor(cur)
		return
	}
	m.setQueueCursor(m.queueMiniOffset)
}

// clearQueue (the c key) empties the queue in the model and the engine.
func (m *Model) clearQueue() tea.Cmd {
	if len(m.queueTracks) == 0 && len(m.queueIDs) == 0 {
		return nil
	}
	m.appendLog("[queue] cleared")
	m.queueTracks = nil
	m.queueIDs = nil
	m.clearQueueCursor()
	m.syncQueue()
	return m.playerCmd(func(p player.Player) error { return p.ClearQueue() })
}

// clampQueueCursor keeps the cursor inside the queue after edits.
func (m *Model) clampQueueCursor() {
	if m.queueCursor >= len(m.queueTracks) {
		m.queueCursor = len(m.queueTracks) - 1 // -1 when the queue is empty
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
	m.queueTracks = append(m.queueTracks[:idx], m.queueTracks[idx+1:]...)
	m.queueIDs = append(m.queueIDs[:idx], m.queueIDs[idx+1:]...)
	m.syncQueue()
	m.appendLog(fmt.Sprintf("[queue] removed #%d: %s — %s", idx+1, t.Artist, t.Title))
	i := idx
	return m.playerCmd(func(p player.Player) error { return p.RemoveFromQueue(i) })
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
	from, dest := idx, to
	return to, m.playerCmd(func(p player.Player) error { return p.MoveInQueue(from, dest) })
}

// handleQueueCursorKey handles the main-view keys that act on the highlighted
// queue entry. It reports whether the key was consumed. Movement keys live in
// handleNormalKey's no-panel switch; this covers the actions.
func (m *Model) handleQueueCursorKey(k string) (tea.Cmd, bool) {
	switch k {
	case "K", "ctrl+up", "J", "ctrl+down":
		if !m.queueCursorActive() {
			// Nothing highlighted yet: highlight the playing track so the
			// next press moves it.
			if cur := m.currentQueueIndex(); cur >= 0 {
				m.setQueueCursor(cur)
			}
			m.lastKey = ""
			return nil, true
		}
		delta := 1
		if k == "K" || k == "ctrl+up" {
			delta = -1
		}
		newIdx, cmd := m.moveQueueTrack(m.queueCursor, delta)
		m.setQueueCursor(newIdx)
		m.lastKey = ""
		return cmd, true
	}

	if !m.queueCursorActive() {
		return nil, false
	}
	switch k {
	case "space", "enter":
		if k == "space" && m.queueCursor == m.currentQueueIndex() && m.playerState.Track != nil {
			return nil, false // plain play/pause on the playing track
		}
		idx := m.queueCursor
		m.clearQueueCursor()
		m.lastKey = ""
		return m.jumpToQueueIndex(idx), true
	case "d", "x", "delete":
		idx := m.queueCursor
		cmd := m.removeQueueAt(idx)
		m.clampQueueCursor()
		if m.queueCursorActive() {
			m.ensureQueueCursorVisible()
		}
		m.lastKey = ""
		return cmd, true
	case "D":
		m.lastKey = ""
		return m.deleteQueueTail(m.queueCursor), true
	}
	return nil, false
}

// deleteQueueTail (shift+d) removes the highlighted entry and everything
// below it. If the playing track is among them, playback stops as well, since
// nothing is left to play after it.
func (m *Model) deleteQueueTail(from int) tea.Cmd {
	if from < 0 || from >= len(m.queueTracks) || from >= len(m.queueIDs) {
		return nil
	}
	last := len(m.queueTracks) - 1
	includesPlaying := false
	if cur := m.currentQueueIndex(); cur >= from && m.playerState.Track != nil {
		includesPlaying = true
	}
	removed := len(m.queueTracks) - from
	m.queueTracks = m.queueTracks[:from]
	m.queueIDs = m.queueIDs[:from]
	m.syncQueue()
	if from > 0 {
		m.setQueueCursor(from - 1)
	} else {
		m.clearQueueCursor()
	}
	m.appendLog(fmt.Sprintf("[queue] removed %d track(s) from position %d to the end", removed, from+1))
	return m.playerCmd(func(p player.Player) error {
		for i := last; i >= from; i-- {
			if err := p.RemoveFromQueue(i); err != nil {
				return err
			}
		}
		if includesPlaying {
			return p.Stop()
		}
		return nil
	})
}
