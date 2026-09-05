package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/queuestate"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// newListModel builds a model whose queue.json lives in dir, so named lists
// land in dir/tracklists and never near the user's real config. testCfg picks
// the keyword planner, so no Claude CLI is ever spawned from here.
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
	time.Sleep(20 * time.Millisecond) // distinct mtimes: the newest save comes first
	_ = m.executeCommand("save a side")
	_ = m.executeCommand("load")
	if !strings.Contains(m.errMsg, "saved lists: a side, b side") {
		t.Fatalf("a bare :load names the lists, newest first: %q", m.errMsg)
	}
	_ = m.executeCommand("load nope")
	if !strings.Contains(m.errMsg, `no track list "nope"`) || !strings.Contains(m.errMsg, "a side") {
		t.Fatalf("an unknown name lists what exists: %q", m.errMsg)
	}
}

func TestLastSession_IsAListAtLaunchAndReserved(t *testing.T) {
	dir := t.TempDir()
	if err := queuestate.Save(filepath.Join(dir, "queue.json"), queuestate.FromTracks(persistTracks(), 1)); err != nil {
		t.Fatal(err)
	}
	m, _ := newListModel(t, dir)
	if got := m.savedTrackLists(); len(got) != 1 || got[0] != lastSessionList {
		t.Fatalf("the restored queue is kept as %q, got %v", lastSessionList, got)
	}
	_ = m.executeCommand("save last session")
	if !strings.Contains(m.errMsg, "reserved") {
		t.Fatalf("the name is reserved: %q", m.errMsg)
	}
	// After something else replaced Tracks, it comes back like any list, at
	// its saved position.
	m.queueTracks, m.queueIDs = nil, nil
	m.syncQueue()
	_ = m.executeCommand("load last session")
	if len(m.queueTracks) != 3 || m.queueResumeIdx != 1 || m.queueCursor != 1 {
		t.Fatalf("last session loads with its saved position: %d tracks, resume %d, cursor %d", len(m.queueTracks), m.queueResumeIdx, m.queueCursor)
	}
	// A launch with nothing to restore drops the stale list.
	m2, _ := newListModel(t, t.TempDir())
	if got := m2.savedTrackLists(); len(got) != 0 {
		t.Fatalf("no previous session, no list: %v", got)
	}
}

func TestCycleLoadName_SpaceStepsThroughTheLists(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	fillTracks(m)
	_ = m.executeCommand("save older")
	time.Sleep(20 * time.Millisecond)
	_ = m.executeCommand("save newer")
	m.mode = modeCommand
	m.cmdBuf = "load"
	for _, want := range []string{"load newer", "load older", "load newer"} {
		m.handleCommandKey("space")
		if m.cmdBuf != want {
			t.Fatalf("space steps through the lists, newest first, wrapping: got %q, want %q", m.cmdBuf, want)
		}
	}
	m.cmdBuf = "load ro"
	m.handleCommandKey("space")
	if m.cmdBuf != "load ro " {
		t.Fatalf("while a name is being typed, space is a space: %q", m.cmdBuf)
	}
	m.cmdBuf = "save my"
	m.handleCommandKey("space")
	if m.cmdBuf != "save my " {
		t.Fatalf("outside :load, space is a space: %q", m.cmdBuf)
	}
	m.cmdBuf = "load"
	plain := ansi.Strip(strings.Join(m.statusNavLines(300), " "))
	if !strings.Contains(plain, "spc next saved list") {
		t.Fatalf("the CMD row names the space key inside :load: %q", plain)
	}
	m.cmdBuf = "save"
	if plain := ansi.Strip(strings.Join(m.statusNavLines(300), " ")); strings.Contains(plain, "spc next") {
		t.Fatalf("the hint is only there inside :load: %q", plain)
	}
}

func TestAutoSave_NamesTheListFromItsSongs(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir) // keyword planner: no CLI, the songs name the list
	fillTracks(m)                // artists A, B, C: no majority, no genres
	if cmd := m.executeCommand("save"); cmd != nil {
		t.Fatal("without Claude the save is immediate")
	}
	names := m.savedTrackLists()
	if len(names) != 1 || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{2}-\d{2}_a and others$`).MatchString(names[0]) {
		t.Fatalf("automatic name = %v", names)
	}
	if !strings.HasPrefix(m.errMsg, "✓") || !strings.Contains(m.errMsg, names[0]) {
		t.Fatalf("the status line shows the name: %q", m.errMsg)
	}
	// The name Claude would send arrives as a message and is cleaned the same way.
	st := queuestate.FromTracks(persistTracks(), -1)
	m.finishAutoSave(trackListNamedMsg{stamp: "2026-09-05_13-10", short: "late night jazz", state: st})
	if _, err := os.Stat(filepath.Join(dir, "tracklists", "2026-09-05_13-10_late night jazz.json")); err != nil {
		t.Fatalf("Claude's name is used as given: %v", err)
	}
	m.finishAutoSave(trackListNamedMsg{stamp: "2026-09-05_13-11", state: st, err: os.ErrDeadlineExceeded})
	if _, err := os.Stat(filepath.Join(dir, "tracklists", "2026-09-05_13-11_a and others.json")); err != nil {
		t.Fatalf("a failed naming falls back to the songs: %v", err)
	}
}

func TestFallbackListName(t *testing.T) {
	miles := []provider.Track{{Artist: "Miles Davis", Title: "So What"}, {Artist: "Miles Davis", Title: "Blue in Green"}, {Artist: "Bill Evans", Title: "Peace Piece"}}
	if got := fallbackListName(miles); got != "miles davis" {
		t.Fatalf("majority artist: %q", got)
	}
	mixed := []provider.Track{{Artist: "A", Genres: []string{"Jazz", "Music"}}, {Artist: "B", Genres: []string{"Jazz", "Music"}}, {Artist: "C", Genres: []string{"Rock"}}}
	if got := fallbackListName(mixed); got != "jazz" {
		t.Fatalf("top genre, ignoring Apple's catch-all Music: %q", got)
	}
	if got := fallbackListName(nil); got != "tracks" {
		t.Fatalf("nothing to go on: %q", got)
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
