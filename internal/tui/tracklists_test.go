package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// newListModel builds a model whose queue.json lives in dir, so named lists
// land in dir/tracklists and never near the user's real config.
func newListModel(t *testing.T, dir string) (*Model, *mockPlayer) {
	t.Helper()
	mock := newMockPlayer()
	m := New(testCfg(), &mockProvider{}, mock, Options{QueueStatePath: filepath.Join(dir, "queue.json")})
	return m, mock
}

func fillTracks(m *Model) {
	m.queueTracks = persistTracks()
	m.queueIDs = make([]string, len(m.queueTracks))
	for i, tr := range m.queueTracks {
		m.queueIDs[i] = views.PlaybackID(tr)
	}
	m.syncQueue()
}

func TestSaveTrackList_WritesANamedListNextToQueueJSON(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	_ = m.executeCommand("save road trip")
	if !strings.Contains(m.errMsg, "empty") {
		t.Fatalf("an empty Tracks panel is refused: %q", m.errMsg)
	}
	fillTracks(m)
	_ = m.executeCommand("save road trip")
	if !strings.HasPrefix(m.errMsg, "✓") || !strings.Contains(m.errMsg, "3 tracks") {
		t.Fatalf("save confirms with the count: %q", m.errMsg)
	}
	if _, err := os.Stat(filepath.Join(dir, "tracklists", "road trip.json")); err != nil {
		t.Fatalf("the list is a file in tracklists/: %v", err)
	}
	if got := m.savedTrackLists(); len(got) != 1 || got[0] != "road trip" {
		t.Fatalf("savedTrackLists = %v", got)
	}
	_ = m.executeCommand("save road trip")
	if !strings.Contains(m.errMsg, "replaced") {
		t.Fatalf("saving under the same name replaces: %q", m.errMsg)
	}
	_ = m.executeCommand("save ../escape")
	if !strings.Contains(m.errMsg, "can't") {
		t.Fatalf("a name that leaves the directory is refused: %q", m.errMsg)
	}
	_ = m.executeCommand("save")
	if !strings.Contains(m.errMsg, "requires a name") {
		t.Fatalf("a bare :save asks for a name: %q", m.errMsg)
	}
}

func TestLoadTrackList_IdleEngineBehavesLikeTheLaunchRestore(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	fillTracks(m)
	_ = m.executeCommand("save mix")

	m2, mock := newListModel(t, dir) // fresh model, nothing in the engine
	_ = m2.executeCommand("load mix")
	if len(m2.queueTracks) != 3 || len(m2.queueIDs) != 3 || m2.queueIDs[1] != "i.lib" {
		t.Fatalf("Tracks is replaced by the list: %v", m2.queueIDs)
	}
	if !strings.HasPrefix(m2.errMsg, "✓") {
		t.Fatalf("load confirms: %q", m2.errMsg)
	}
	if mock.setQueueIDs != nil || len(mock.syncCalls) != 0 || mock.playCalled {
		t.Fatalf("loading into an idle engine must not touch the player: %+v", mock)
	}
	if !m2.queueDirty {
		t.Fatal("the loaded list becomes the queue that queue.json remembers")
	}
	if m2.queueCursor != 0 || m2.queueResumeIdx != 0 {
		t.Fatalf("highlight and resume point on the list's first track, got cursor %d resume %d", m2.queueCursor, m2.queueResumeIdx)
	}
	// The first play hands the list to the engine, like a restored queue.
	if cmd := m2.togglePlayPause(); cmd != nil {
		_ = cmd()
	}
	if got := mock.setQueueAtIDs; len(got) != 3 || got[0] != "100" {
		t.Fatalf("SetQueueAt ids = %v", got)
	}
}

func TestLoadTrackList_WhilePlayingSwitchesToTheList(t *testing.T) {
	dir := t.TempDir()
	m, mock := newListModel(t, dir)
	fillTracks(m)
	_ = m.executeCommand("save mix")
	playing := provider.Track{ID: "999", Title: "Other"}
	m.playerState.Track = &playing
	m.playerState.Playing = true
	cmd := m.executeCommand("load mix")
	if cmd == nil {
		t.Fatal("loading over a playing track hands the list to the engine")
	}
	_ = cmd()
	if len(mock.syncCalls) != 1 || len(mock.syncCalls[0].IDs) != 3 || mock.syncCalls[0].Play != "100" {
		t.Fatalf("SyncQueue with the list, starting its first track: %+v", mock.syncCalls)
	}
	if m.queueResumeIdx != noQueueCursor {
		t.Fatalf("the engine holds the queue now, resume idx = %d", m.queueResumeIdx)
	}
}

func TestLoadTrackList_NamesTheListsAndReportsUnknownOnes(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	_ = m.executeCommand("load")
	if !strings.Contains(m.errMsg, "no saved lists") {
		t.Fatalf("a bare :load with nothing saved: %q", m.errMsg)
	}
	fillTracks(m)
	_ = m.executeCommand("save b side")
	_ = m.executeCommand("save a side")
	_ = m.executeCommand("load")
	if m.errMsg != "saved lists: a side, b side" {
		t.Fatalf("a bare :load names the lists, sorted: %q", m.errMsg)
	}
	_ = m.executeCommand("load nope")
	if !strings.Contains(m.errMsg, `no track list "nope"`) || !strings.Contains(m.errMsg, "a side") {
		t.Fatalf("an unknown name lists what exists: %q", m.errMsg)
	}
}

func TestSavePlaylist_IsStillTheAppleMusicPath(t *testing.T) {
	m, _ := newListModel(t, t.TempDir())
	fillTracks(m)
	cmd := m.executeCommand("save-playlist road")
	if cmd == nil {
		t.Fatal(":save-playlist (unlisted) still creates an Apple Music playlist")
	}
	if _, ok := cmd().(playlistCreatedMsg); !ok {
		t.Fatal("expected playlistCreatedMsg")
	}
	if m.savedTrackLists() != nil {
		t.Fatal(":save-playlist must not write a local list")
	}
}
