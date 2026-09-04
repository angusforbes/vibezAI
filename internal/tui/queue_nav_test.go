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
	m.followPlayingTrack()
	if !m.queueCursorActive() || m.queueCursor != 1 || !m.queueFollow {
		t.Fatalf("highlight should start on the playing track and follow it: cursor=%d follow=%v", m.queueCursor, m.queueFollow)
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
	if m.queueCursor != 2 || m.queueFollow {
		t.Fatal("esc must do nothing in the main view")
	}
	key(m, "q")
	if m.queueCursor != 1 || !m.queueFollow {
		t.Fatalf("q should put the highlight back on the playing track and follow it, got cursor=%d follow=%v", m.queueCursor, m.queueFollow)
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
	if m.queueCursor != 3 || !m.queueFollow {
		t.Fatalf("after starting a track the highlight should sit on it and follow playback, got cursor=%d follow=%v", m.queueCursor, m.queueFollow)
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

func TestQueueCursor_DRemovesHighlighted_EvenThePlayingOne(t *testing.T) {
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
	if m.vibe.PickerActive() || m.discovery.enabled {
		t.Fatal("d must not touch discovery any more")
	}

	key(m, "q") // back on the playing track ("Two", index 1)
	if cmd := key(m, "d"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 2 || mock.removeFromQueueIdx[1] != 1 {
		t.Fatalf("d on the playing track should remove it too: %+v / %v", m.queueTracks, mock.removeFromQueueIdx)
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

	key(m, "q") // highlight back on the playing track ("Two", index 1), following
	if cmd := key(m, "K"); cmd != nil {
		_ = cmd()
	}
	if m.queueIDs[0] != "2" || m.queueCursor != 0 || !m.queueFollow {
		t.Fatalf("K should move the playing track up and keep following it: %v cursor=%d follow=%v", m.queueIDs, m.queueCursor, m.queueFollow)
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

func TestQ_PutsHighlightBackOnPlayingTrack(t *testing.T) {
	m, mock := navModel(t)
	key(m, "down")
	key(m, "down") // cursor on "Four"
	key(m, "q")
	if m.queueCursor != 1 || !m.queueFollow {
		t.Fatalf("q should snap to the playing track (1) and follow, got %d follow=%v", m.queueCursor, m.queueFollow)
	}
	key(m, "q")
	if m.queueCursor != 1 || !m.queueFollow {
		t.Fatal("q again should change nothing")
	}
	if m.activePanel >= 0 || mock.setQueueIDs != nil {
		t.Fatal("q must not open a panel or touch playback")
	}
}

func TestHighlightFollowsPlaybackUntilMoved(t *testing.T) {
	m, _ := navModel(t)
	m.followPlayingTrack()
	next := m.queueTracks[2]
	m.Update(playerStateMsg{Track: &next, Playing: true})
	if m.queueCursor != 2 {
		t.Fatalf("following highlight should move to the new track (2), got %d", m.queueCursor)
	}
	key(m, "up") // user moves it to 1
	later := m.queueTracks[3]
	m.Update(playerStateMsg{Track: &later, Playing: true})
	if m.queueCursor != 1 {
		t.Fatalf("a moved highlight must stay put across track changes, got %d", m.queueCursor)
	}
}

func TestC_ClearsQueueFromMainView(t *testing.T) {
	m, _ := navModel(t)
	key(m, "down")
	if cmd := key(m, "c"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 0 || len(m.queueIDs) != 0 || m.queueCursorActive() {
		t.Fatalf("c should clear the queue and the highlight: %d tracks cursor=%d", len(m.queueTracks), m.queueCursor)
	}
}

func TestR_InsertsFiveRelatedAfterHighlightedTrack(t *testing.T) {
	m, mock := navModel(t) // playing "Two" (1); queue One Two Three Four
	key(m, "down")         // highlight "Three" (2)
	if cmd := key(m, "R"); cmd == nil {
		t.Fatal("R should start a related-songs lookup")
	}
	if m.radio.enabled {
		t.Fatal("R must not turn on continuous radio")
	}
	picks := []provider.Track{
		{ID: "r1", Title: "R1"}, {ID: "r2", Title: "R2"}, {ID: "r3", Title: "R3"}, {ID: "r4", Title: "R4"}, {ID: "r5", Title: "R5"},
	}
	_, cmd := m.Update(relatedResultMsg{gen: m.relatedGen, seed: m.queueTracks[2], tracks: picks})
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, c := range batch {
					if c != nil {
						c()
					}
				}
			}
		}
	}
	want := []string{"1", "2", "3", "r1", "r2", "r3", "r4", "r5", "4"}
	if len(m.queueIDs) != len(want) {
		t.Fatalf("queue ids = %v, want %v", m.queueIDs, want)
	}
	for i := range want {
		if m.queueIDs[i] != want[i] {
			t.Fatalf("queue ids = %v, want %v", m.queueIDs, want)
		}
	}
	if len(mock.appendQueueIDs) != 1 || len(mock.appendQueueIDs[0]) != 5 {
		t.Fatalf("engine should get one AppendQueue of 5 ids, got %v", mock.appendQueueIDs)
	}
	if len(mock.moveInQueueCalls) != 5 || mock.moveInQueueCalls[0].From != 4 || mock.moveInQueueCalls[0].To != 3 {
		t.Fatalf("picks should be moved into place after the seed, got %+v", mock.moveInQueueCalls)
	}
	if m.queueCursor != 2 {
		t.Fatalf("highlight should stay on the seed (2), got %d", m.queueCursor)
	}

	// A stale result (older generation) is ignored.
	m.Update(relatedResultMsg{gen: m.relatedGen - 1, seed: m.queueTracks[2], tracks: picks})
	if len(m.queueIDs) != len(want) {
		t.Fatal("stale related result must be ignored")
	}
}

func TestR_WithoutHighlightUsesPlayingTrack(t *testing.T) {
	m, _ := navModel(t)
	if cmd := key(m, "R"); cmd == nil {
		t.Fatal("R should start a lookup seeded by the playing track")
	}
	_, _ = m.Update(relatedResultMsg{gen: m.relatedGen, seed: *m.playerState.Track, tracks: []provider.Track{{ID: "r1", Title: "R1"}}})
	if len(m.queueIDs) != 5 || m.queueIDs[2] != "r1" {
		t.Fatalf("pick should land right after the playing track: %v", m.queueIDs)
	}
}

func TestPlayerKeysStillWorkWhileHighlighting(t *testing.T) {
	m, mock := navModel(t)
	key(m, "down")
	if cmd := key(m, "r"); cmd != nil {
		_ = cmd()
	}
	if m.playerState.RepeatMode == 0 {
		t.Fatal("r should cycle repeat with a highlight active")
	}
	if cmd := key(m, "n"); cmd != nil {
		_ = cmd()
	}
	if !mock.nextCalled {
		t.Fatal("n should still skip to the next track with a highlight active")
	}
	if !m.queueCursorActive() {
		t.Fatal("player keys must not drop the highlight")
	}
}

func TestShiftUpDown_JumpToTopAndBottom(t *testing.T) {
	m, mock := navModel(t)
	key(m, "shift+down")
	if m.queueCursor != 3 {
		t.Fatalf("shift+down should jump to the last entry, got %d", m.queueCursor)
	}
	key(m, "shift+up")
	if m.queueCursor != 0 {
		t.Fatalf("shift+up should jump to the first entry, got %d", m.queueCursor)
	}
	if len(mock.moveInQueueCalls) != 0 || len(m.queueTracks) != 4 {
		t.Fatal("shift+up/down must only move the highlight, not the tracks")
	}
}

func TestShiftD_CutsFromHighlightToEnd(t *testing.T) {
	m, mock := navModel(t) // playing "Two" (index 1)
	key(m, "down")         // highlight "Three" (index 2)
	if cmd := key(m, "D"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 2 || m.queueTracks[1].Title != "Two" {
		t.Fatalf("queue after D = %+v, want [One Two]", m.queueTracks)
	}
	if len(mock.removeFromQueueIdx) != 2 || mock.removeFromQueueIdx[0] != 3 || mock.removeFromQueueIdx[1] != 2 {
		t.Fatalf("RemoveFromQueue calls = %v, want [3 2] (highest first)", mock.removeFromQueueIdx)
	}
	if mock.stopCalled {
		t.Fatal("playback must continue when the playing track was above the cut")
	}
	if m.queueCursor != 1 {
		t.Fatalf("highlight should move to the new last entry (1), got %d", m.queueCursor)
	}
}

func TestShiftD_IncludingPlayingTrackStopsPlayback(t *testing.T) {
	m, mock := navModel(t) // playing "Two" (index 1)
	key(m, "q")            // highlight the playing track
	if cmd := key(m, "D"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 1 || m.queueTracks[0].Title != "One" {
		t.Fatalf("queue after D = %+v, want [One]", m.queueTracks)
	}
	if !mock.stopCalled {
		t.Fatal("removing the playing track and everything below it should stop playback")
	}
	if m.queueCursor != 0 {
		t.Fatalf("highlight should rest on the remaining entry, got %d", m.queueCursor)
	}
}

func TestCtrlShiftD_CutsEverythingAboveHighlight(t *testing.T) {
	m, mock := navModel(t) // playing "Two" (index 1)
	key(m, "down")         // highlight "Three" (index 2)
	if cmd := key(m, "ctrl+shift+d"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 2 || m.queueTracks[0].Title != "Three" || m.queueTracks[1].Title != "Four" {
		t.Fatalf("queue after ctrl+shift+d = %+v, want [Three Four]", m.queueTracks)
	}
	if len(mock.removeFromQueueIdx) != 2 || mock.removeFromQueueIdx[0] != 1 || mock.removeFromQueueIdx[1] != 0 {
		t.Fatalf("RemoveFromQueue calls = %v, want [1 0] (highest first)", mock.removeFromQueueIdx)
	}
	if mock.stopCalled {
		t.Fatal("cutting above must not stop playback")
	}
	if m.queueCursor != 0 {
		t.Fatalf("highlight should stay on the kept entry, now first (0), got %d", m.queueCursor)
	}
}

func TestCtrlShiftD_OnFirstEntryIsNoop(t *testing.T) {
	m, mock := navModel(t)
	key(m, "shift+up") // highlight the first entry
	if cmd := key(m, "ctrl+shift+d"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 4 || len(mock.removeFromQueueIdx) != 0 {
		t.Fatal("nothing is above the first entry, so nothing should change")
	}
}
