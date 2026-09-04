package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestQueueCursor_SpaceIsAlwaysPlayPause(t *testing.T) {
	m, mock := navModel(t) // playing "Two"
	key(m, "down")
	key(m, "down") // highlight "Four" (index 3)
	if cmd := key(m, "space"); cmd != nil {
		_ = cmd()
	}
	if !mock.pauseCalled || len(mock.playQueuedCalls) != 0 || mock.setQueueIDs != nil {
		t.Fatalf("space must only toggle play/pause: pause=%v playQueued=%v setQueue=%v", mock.pauseCalled, mock.playQueuedCalls, mock.setQueueIDs)
	}
	if m.queueCursor != 3 {
		t.Fatalf("space must not move the highlight, got %d", m.queueCursor)
	}
}

func TestQueueCursor_EnterOnPlayingTrackRestartsIt(t *testing.T) {
	m, mock := navModel(t) // playing "Two" (index 1), highlight following it
	m.followPlayingTrack()
	if cmd := key(m, "enter"); cmd != nil {
		_ = cmd()
	}
	if len(mock.playQueuedCalls) != 1 || mock.playQueuedCalls[0].Idx != 1 || mock.playQueuedCalls[0].ID != "2" {
		t.Fatalf("enter on the playing track should restart it via PlayQueued, got %+v", mock.playQueuedCalls)
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
	if len(mock.syncCalls) != 1 || len(mock.syncCalls[0].IDs) != 3 || mock.syncCalls[0].Current != "2" || mock.syncCalls[0].Play != "" {
		t.Fatalf("removing a non-playing track should just re-sync the engine, got %+v", mock.syncCalls)
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
	if len(m.queueTracks) != 2 || len(mock.syncCalls) != 2 || mock.syncCalls[1].Play != "4" {
		t.Fatalf("d on the playing track should remove it and move on to the track that took its place: %+v / %+v", m.queueTracks, mock.syncCalls)
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
	if len(mock.syncCalls) != 1 || len(mock.syncCalls[0].IDs) != 4 || mock.syncCalls[0].IDs[1] != "3" || mock.syncCalls[0].Current != "2" {
		t.Fatalf("moving should re-sync the engine with the new order, got %+v", mock.syncCalls)
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
	if len(mock.syncCalls) != 1 || len(mock.syncCalls[0].IDs) != 9 || mock.syncCalls[0].IDs[3] != "r1" || mock.syncCalls[0].Current != "2" || mock.syncCalls[0].Play != "" {
		t.Fatalf("the insert should re-sync the engine with the picks after the seed, got %+v", mock.syncCalls)
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
	if len(mock.syncCalls) != 1 || len(mock.syncCalls[0].IDs) != 2 || mock.syncCalls[0].Current != "2" || mock.syncCalls[0].Play != "" {
		t.Fatalf("the cut should re-sync the engine keeping the playing track, got %+v", mock.syncCalls)
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
	// The playing track is not in the synced list and nothing is asked to
	// play, which is how the engine knows to stop.
	if len(mock.syncCalls) != 1 || len(mock.syncCalls[0].IDs) != 1 || mock.syncCalls[0].Current != "2" || mock.syncCalls[0].Play != "" {
		t.Fatalf("expected a sync without the playing track and no play id, got %+v", mock.syncCalls)
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
	// The playing track ("Two") was above the cut, so playback moves to the
	// highlighted entry, now first.
	if len(mock.syncCalls) != 1 || len(mock.syncCalls[0].IDs) != 2 || mock.syncCalls[0].Play != "3" {
		t.Fatalf("expected a sync that starts the new first entry, got %+v", mock.syncCalls)
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
	if len(m.queueTracks) != 4 || len(mock.syncCalls) != 0 {
		t.Fatal("nothing is above the first entry, so nothing should change")
	}
}

// libraryProvider serves a fixed library for the random-pick tests.
type libraryProvider struct {
	mockProvider
	tracks []provider.Track
	calls  int
}

func (p *libraryProvider) GetLibraryTracks(_ context.Context) ([]provider.Track, error) {
	p.calls++
	return p.tracks, nil
}

func TestShiftT_InsertsFiveRandomLibrarySongsAfterHighlight(t *testing.T) {
	m, mock := navModel(t) // queue One Two Three Four, playing Two
	lib := &libraryProvider{}
	for i := range 12 {
		lib.tracks = append(lib.tracks, provider.Track{ID: fmt.Sprintf("i.lib%d", i), Title: fmt.Sprintf("Lib %d", i), Artist: "L"})
	}
	lib.tracks = append(lib.tracks, provider.Track{ID: "2", Title: "Two", Artist: "B"}) // already queued: must be skipped
	m.provider = lib
	key(m, "down") // highlight "Three" (index 2)

	cmd := key(m, "T")
	if cmd == nil {
		t.Fatal("T should start a library pick")
	}
	msg := cmd()
	res, ok := msg.(randomLibraryResultMsg)
	if !ok {
		t.Fatalf("expected randomLibraryResultMsg, got %T", msg)
	}
	if len(res.tracks) != 5 || res.all == nil {
		t.Fatalf("expected 5 picks and the fetched library, got %d picks all=%v", len(res.tracks), res.all != nil)
	}
	for _, tr := range res.tracks {
		if tr.ID == "2" {
			t.Fatal("a queued track must not be picked")
		}
	}
	_, next := m.Update(res)
	if next != nil {
		_ = next()
	}
	if len(m.queueIDs) != 9 || m.queueIDs[2] != "3" || m.queueIDs[8] != "4" {
		t.Fatalf("picks should sit right after the highlighted track: %v", m.queueIDs)
	}
	for _, id := range m.queueIDs[3:8] {
		if !strings.HasPrefix(id, "i.lib") {
			t.Fatalf("expected library picks at positions 4-8, got %v", m.queueIDs)
		}
	}
	if len(mock.syncCalls) != 1 || mock.syncCalls[0].Current != "2" || mock.syncCalls[0].Play != "" {
		t.Fatalf("the insert should re-sync the engine without changing playback, got %+v", mock.syncCalls)
	}

	// A second press within the cache window does not refetch and skips the
	// five already added.
	cmd = key(m, "T")
	res2, _ := cmd().(randomLibraryResultMsg)
	if lib.calls != 1 || res2.all != nil {
		t.Fatalf("the cached library should be reused, fetch calls=%d", lib.calls)
	}
	if len(res2.tracks) != 5 {
		t.Fatalf("still 7 unqueued library songs left, expected 5 more picks, got %d", len(res2.tracks))
	}
	seen := map[string]bool{}
	for _, id := range m.queueIDs {
		seen[id] = true
	}
	for _, tr := range res2.tracks {
		if seen[tr.ID] {
			t.Fatalf("second pick repeated a queued track: %s", tr.ID)
		}
	}
}

func TestRelated_LibraryOnlySeedIsSkippedSilently(t *testing.T) {
	m, _ := navModel(t)
	m.provider = &mockProvider{}
	own := provider.Track{ID: "i.MyRecording", Title: "Demo take", Artist: "Me"}
	if cmd := m.fetchRelatedCmd(&own); cmd != nil || m.errMsg != "" {
		t.Fatalf("a track without a catalog match must be skipped without any notice (cmd=%v err=%q)", cmd != nil, m.errMsg)
	}
}

func TestHandleRelatedResult_ErrorIsSilent(t *testing.T) {
	m, _ := navModel(t)
	m.errMsg = "⏳ Finding songs related to X…"
	m.relatedGen = 1
	cmd := m.handleRelatedResult(relatedResultMsg{gen: 1, err: errors.New("station unavailable"), seed: provider.Track{Title: "X"}})
	if cmd != nil || m.errMsg != "" {
		t.Fatalf("a failed lookup must clear the notice and show nothing (cmd=%v err=%q)", cmd != nil, m.errMsg)
	}
}
