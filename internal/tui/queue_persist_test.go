package tui

import (
	"path/filepath"
	"testing"

	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/queuestate"
)

func persistTracks() []provider.Track {
	return []provider.Track{
		{ID: "100", CatalogID: "100", Title: "One", Artist: "A"},
		{ID: "i.lib", CatalogID: "200", Title: "Two", Artist: "B"},
		{ID: "300", Title: "Three", Artist: "C"},
	}
}

func TestRestoreQueue_DoesNotAutoPlayAndResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := queuestate.Save(path, queuestate.FromTracks(persistTracks(), 1)); err != nil {
		t.Fatal(err)
	}
	mock := newMockPlayer()
	m := New(testCfg(), &mockProvider{}, mock, Options{QueueStatePath: path})

	if len(m.queueTracks) != 3 || len(m.queueIDs) != 3 {
		t.Fatalf("queue not restored: %d tracks, %d ids", len(m.queueTracks), len(m.queueIDs))
	}
	if m.queueIDs[1] != "i.lib" || m.queueIDs[2] != "300" || m.queueIDs[0] != "100" {
		t.Fatalf("playback ids wrong: %v", m.queueIDs)
	}
	if len(m.queue.m.Tracks()) != 3 {
		t.Fatalf("queue panel not populated: %d", len(m.queue.m.Tracks()))
	}
	if mock.setQueueIDs != nil || len(mock.appendQueueIDs) != 0 || mock.playCalled {
		t.Fatalf("restore must not touch the player: setQueue=%v append=%v play=%v", mock.setQueueIDs, mock.appendQueueIDs, mock.playCalled)
	}

	// First play hands the queue to the engine from the saved position.
	if cmd := m.togglePlayPause(); cmd != nil {
		_ = cmd()
	}
	if got := mock.setQueueIDs; len(got) != 2 || got[0] != "i.lib" || got[1] != "300" {
		t.Fatalf("SetQueue ids = %v, want [i.lib 300]", got)
	}
	if mock.playCalled {
		t.Fatal("Play() must not be called for a restored queue; SetQueue starts it")
	}
	if len(m.queueTracks) != 2 || m.queueTracks[0].Title != "Two" {
		t.Fatalf("model queue not trimmed to the resume point: %+v", m.queueTracks)
	}
}

func TestRestoreQueue_MissingFileIsQuiet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	mock := newMockPlayer()
	m := New(testCfg(), &mockProvider{}, mock, Options{QueueStatePath: path})
	if len(m.queueTracks) != 0 || m.queueResumeIdx != -1 {
		t.Fatalf("expected an empty queue, got %d tracks idx %d", len(m.queueTracks), m.queueResumeIdx)
	}
	// With nothing queued, space behaves as before: a plain Play().
	if cmd := m.togglePlayPause(); cmd != nil {
		_ = cmd()
	}
	if !mock.playCalled || mock.setQueueIDs != nil {
		t.Fatalf("expected Play(), got play=%v setQueue=%v", mock.playCalled, mock.setQueueIDs)
	}
}

func TestSyncQueue_SavesOnFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	m := New(testCfg(), &mockProvider{}, newMockPlayer(), Options{QueueStatePath: path})

	m.flushQueueState() // nothing dirty: no file
	if _, err := queuestate.Load(path); err != nil {
		t.Fatalf("Load before any change: %v", err)
	}

	m.queueTracks = persistTracks()[:2]
	m.queueIDs = []string{"100", "i.lib"}
	m.syncQueue()
	if !m.queueDirty {
		t.Fatal("syncQueue should mark the queue dirty")
	}
	m.flushQueueState()
	if m.queueDirty {
		t.Fatal("flush should clear the dirty flag")
	}
	st, err := queuestate.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Tracks) != 2 || st.Tracks[1].ID != "i.lib" || st.CurrentIndex != -1 {
		t.Fatalf("saved state wrong: %+v", st)
	}

	// The playing track's position is saved as the resume point.
	m.playerState.Track = &provider.Track{ID: "i.lib", CatalogID: "200"}
	m.syncQueue()
	m.flushQueueState()
	st, _ = queuestate.Load(path)
	if st.CurrentIndex != 1 {
		t.Fatalf("current index = %d, want 1", st.CurrentIndex)
	}
}

func TestSyncQueue_NoPathIsNoop(t *testing.T) {
	m := newModel(newMockPlayer())
	m.queueTracks = persistTracks()
	m.syncQueue()
	m.flushQueueState() // must not panic or write anywhere
	if !m.queueDirty {
		t.Fatal("without a path the dirty flag stays set (nothing to flush to)")
	}
}
