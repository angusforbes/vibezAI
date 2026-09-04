package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/provider"
)

func navModel(t *testing.T) (*Model, *mockPlayer) {
	t.Helper()
	mock := newMockPlayer()
	m := newModel(mock)
	m.width, m.height = 100, 40
	m.queueTracks = []provider.Track{
		{ID: "1", Title: "One", Artist: "A"},
		{ID: "2", Title: "Two", Artist: "B"},
		{ID: "3", Title: "Three", Artist: "C"},
		{ID: "4", Title: "Four", Artist: "D"},
	}
	m.queueIDs = []string{"1", "2", "3", "4"}
	m.queue.SetTracks(m.queueTracks)
	tr := m.queueTracks[1]
	m.playerState.Track = &tr // "Two" is playing
	m.playerState.Playing = true
	return m, mock
}

func key(m *Model, k string) tea.Cmd {
	msg := tea.KeyPressMsg{Text: k}
	if len(k) == 1 {
		msg.Code = rune(k[0])
	}
	return m.handleNormalKey(msg, k)
}

func TestQueueCursor_ArrowsMoveWithoutPlaying(t *testing.T) {
	m, mock := navModel(t)
	if m.queueCursorActive() {
		t.Fatal("cursor should start inactive")
	}
	key(m, "down") // from the playing track (1) to 2
	if m.queueCursor != 2 {
		t.Fatalf("cursor after down = %d, want 2", m.queueCursor)
	}
	key(m, "j")
	key(m, "j") // clamps at the end
	if m.queueCursor != 3 {
		t.Fatalf("cursor after two more j = %d, want 3", m.queueCursor)
	}
	key(m, "up")
	if m.queueCursor != 2 {
		t.Fatalf("cursor after up = %d, want 2", m.queueCursor)
	}
	if mock.setQueueIDs != nil || mock.nextCalled || mock.prevCalled || mock.playCalled {
		t.Fatal("moving the cursor must not touch playback")
	}
	key(m, "esc")
	if m.queueCursorActive() {
		t.Fatal("esc should clear the cursor")
	}
	key(m, "G")
	if m.queueCursor != 3 {
		t.Fatalf("G should jump to the last entry, got %d", m.queueCursor)
	}
	key(m, "g")
	key(m, "g")
	if m.queueCursor != 0 {
		t.Fatalf("gg should jump to the first entry, got %d", m.queueCursor)
	}
}

func TestQueueCursor_SpaceStartsHighlightedTrack(t *testing.T) {
	m, mock := navModel(t)
	key(m, "down")
	key(m, "down") // cursor on "Four" (index 3)
	if cmd := key(m, "space"); cmd != nil {
		_ = cmd()
	}
	if len(mock.playQueuedCalls) != 1 || mock.playQueuedCalls[0].Idx != 3 || mock.playQueuedCalls[0].ID != "4" {
		t.Fatalf("PlayQueued calls = %+v, want [{3 4}]", mock.playQueuedCalls)
	}
	if mock.setQueueIDs != nil || mock.playCalled || mock.pauseCalled {
		t.Fatal("space on a highlighted track must jump in place, not replace the queue or toggle play/pause")
	}
	if len(m.queueTracks) != 4 || len(m.queueIDs) != 4 {
		t.Fatalf("queue must stay intact, got %d tracks", len(m.queueTracks))
	}
	if m.queueCursorActive() {
		t.Fatal("cursor should clear after starting a track")
	}
}

func TestQueueCursor_SpaceOnPlayingTrackTogglesPause(t *testing.T) {
	m, mock := navModel(t)
	key(m, "down")
	key(m, "up") // back on the playing track
	if m.queueCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.queueCursor)
	}
	if cmd := key(m, "space"); cmd != nil {
		_ = cmd()
	}
	if !mock.pauseCalled || mock.setQueueIDs != nil {
		t.Fatalf("expected a plain pause, got pause=%v setQueue=%v", mock.pauseCalled, mock.setQueueIDs)
	}
}

func TestQueueCursor_EnterStartsHighlightedTrack(t *testing.T) {
	m, mock := navModel(t)
	key(m, "down")
	if cmd := key(m, "enter"); cmd != nil {
		_ = cmd()
	}
	if len(mock.playQueuedCalls) != 1 || mock.playQueuedCalls[0].Idx != 2 || mock.playQueuedCalls[0].ID != "3" {
		t.Fatalf("PlayQueued calls = %+v, want [{2 3}]", mock.playQueuedCalls)
	}
	if len(m.queueTracks) != 4 {
		t.Fatalf("enter must not trim the queue, got %d tracks", len(m.queueTracks))
	}
}

func TestQueueCursor_DRemovesHighlighted_DWithoutCursorIsDiscovery(t *testing.T) {
	m, mock := navModel(t)
	key(m, "down") // cursor on "Three"
	if cmd := key(m, "d"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 3 || m.queueTracks[2].Title != "Four" {
		t.Fatalf("queue after d = %+v", m.queueTracks)
	}
	if len(mock.removeFromQueueIdx) != 1 || mock.removeFromQueueIdx[0] != 2 {
		t.Fatalf("RemoveFromQueue calls = %v, want [2]", mock.removeFromQueueIdx)
	}
	if m.queueCursor != 2 {
		t.Fatalf("cursor should stay on the next entry (2), got %d", m.queueCursor)
	}
	if m.vibe.PickerActive() {
		t.Fatal("d with a cursor must not open the discovery picker")
	}

	key(m, "esc")
	key(m, "d") // no cursor: the old discovery behaviour
	if !m.vibe.PickerActive() {
		t.Fatal("d without a cursor should still open the discovery metric picker")
	}
	if len(m.queueTracks) != 3 {
		t.Fatal("d without a cursor must not remove anything")
	}
}

func TestQueueCursor_KJMoveHighlightedTrack(t *testing.T) {
	m, mock := navModel(t)
	key(m, "down") // cursor on "Three" (2)
	if cmd := key(m, "K"); cmd != nil {
		_ = cmd()
	}
	if m.queueTracks[1].Title != "Three" || m.queueIDs[1] != "3" || m.queueCursor != 1 {
		t.Fatalf("K should move the entry up and follow it: %v cursor=%d", m.queueIDs, m.queueCursor)
	}
	if len(mock.moveInQueueCalls) != 1 || mock.moveInQueueCalls[0].From != 2 || mock.moveInQueueCalls[0].To != 1 {
		t.Fatalf("MoveInQueue calls = %+v", mock.moveInQueueCalls)
	}
	if cmd := key(m, "J"); cmd != nil {
		_ = cmd()
	}
	if m.queueIDs[2] != "3" || m.queueCursor != 2 {
		t.Fatalf("J should move it back down: %v cursor=%d", m.queueIDs, m.queueCursor)
	}

	key(m, "esc")
	key(m, "K") // no cursor: highlights the playing track instead of moving it
	if m.queueCursor != 1 || m.queueIDs[1] != "2" {
		t.Fatalf("K without a cursor should only highlight the playing track: %v cursor=%d", m.queueIDs, m.queueCursor)
	}
}

func TestQueueCursor_KeepsVisibleAndClampsAfterClear(t *testing.T) {
	m, _ := navModel(t)
	m.height = 12 // small panel: forces scrolling
	rows := max(1, m.panelHeight()-2)
	for i := 0; i < 10; i++ {
		key(m, "down")
	}
	if m.queueCursor != 3 {
		t.Fatalf("cursor = %d, want 3", m.queueCursor)
	}
	if m.queueCursor < m.queueMiniOffset || m.queueCursor >= m.queueMiniOffset+rows {
		t.Fatalf("cursor %d not within visible window [%d,%d)", m.queueCursor, m.queueMiniOffset, m.queueMiniOffset+rows)
	}
	m.queueTracks, m.queueIDs = nil, nil
	m.syncQueue()
	if m.queueCursorActive() || m.queueCursor != noQueueCursor {
		t.Fatalf("cursor should clear with the queue, got %d", m.queueCursor)
	}
}

func TestQueueCursor_AutoScrollOnlyWhenNoCursor(t *testing.T) {
	m, _ := navModel(t)
	m.height = 12
	key(m, "down") // cursor active at 2
	offset := m.queueMiniOffset
	next := m.queueTracks[3]
	m.Update(playerStateMsg{Track: &next, Playing: true})
	if m.queueMiniOffset != offset {
		t.Fatalf("auto-scroll must not move the list while a cursor is active (offset %d -> %d)", offset, m.queueMiniOffset)
	}
}
