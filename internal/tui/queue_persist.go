package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/queuestate"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// Queue persistence. The queue is saved to Options.QueueStatePath whenever it
// changes (debounced to the one-second tick) and on every exit path, and read
// back when the model is created. Restoring never touches the player: the
// engine's own queue is filled on the first play instead, so a relaunch shows
// the previous queue without starting to play by itself.

// restoreQueue loads the queue saved by the previous run into the model.
func (m *Model) restoreQueue() {
	m.queueResumeIdx = -1
	if m.queueStatePath == "" {
		return
	}
	st, err := queuestate.Load(m.queueStatePath)
	if err != nil {
		m.appendLog("[queue] could not restore the saved queue: " + err.Error())
		return
	}
	if len(st.Tracks) == 0 {
		return
	}
	tracks := st.ProviderTracks()
	ids := make([]string, len(tracks))
	for i, t := range tracks {
		ids[i] = views.PlaybackID(t)
	}
	m.queueTracks = tracks
	m.queueIDs = ids
	m.queue.SetTracks(m.queueTracks)
	m.queueResumeIdx = st.CurrentIndex
	m.appendLog(fmt.Sprintf("[queue] restored %d track(s) from the previous session", len(tracks)))
}

// syncQueue pushes the model's queue to the queue panel and marks it for
// saving. Every change to queueTracks/queueIDs goes through here.
func (m *Model) syncQueue() {
	m.queue.SetTracks(m.queueTracks)
	m.queueDirty = true
	m.clampQueueCursor()
}

// flushQueueState saves the queue if it changed since the last save.
func (m *Model) flushQueueState() {
	if !m.queueDirty || m.queueStatePath == "" {
		return
	}
	m.queueDirty = false
	st := queuestate.FromTracks(m.queueTracks, m.currentQueueIndex())
	if err := queuestate.Save(m.queueStatePath, st); err != nil {
		m.appendLog("[queue] could not save the queue: " + err.Error())
	}
}

// currentQueueIndex is the position of the playing track in the queue. Before
// anything has played in this session it is the restored position, so saving
// an unchanged-but-appended queue keeps the resume point; otherwise -1.
func (m *Model) currentQueueIndex() int {
	if m.playerState.Track == nil {
		return m.queueResumeIdx
	}
	id := views.PlaybackID(*m.playerState.Track)
	for i, qid := range m.queueIDs {
		if qid == id {
			return i
		}
	}
	return -1
}

// startRestoredQueue hands a restored, not-yet-loaded queue to the engine and
// starts it from the track that was playing when vibez was last closed. Like
// "enter" in the queue panel, the tracks before that one are dropped.
func (m *Model) startRestoredQueue() tea.Cmd {
	if len(m.queueIDs) == 0 {
		return nil
	}
	idx := m.queueResumeIdx
	if idx < 0 || idx >= len(m.queueIDs) {
		idx = 0
	}
	m.appendLog(fmt.Sprintf("[queue] resuming the restored queue at position %d", idx+1))
	return m.playQueueFrom(idx)
}

// trackChanged reports whether the now-playing track differs between two states.
func trackChanged(a, b *provider.Track) bool {
	if (a == nil) != (b == nil) {
		return true
	}
	return a != nil && (a.ID != b.ID || a.CatalogID != b.CatalogID)
}
