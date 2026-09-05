package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

// writeList puts a list on disk directly, leaving the model's Tracks alone.
func writeList(t *testing.T, dir, name string, tracks []provider.Track) {
	t.Helper()
	if err := queuestate.Save(filepath.Join(dir, "tracklists", name+".json"), queuestate.FromTracks(tracks, -1)); err != nil {
		t.Fatal(err)
	}
}

func ctrlSlash(m *Model) tea.Cmd {
	return m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
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
	if _, err := os.Stat(filepath.Join(dir, "tracklists", "last session.json")); err == nil {
		t.Fatal("no previous session, so no last-session list")
	}
	// The order the SV source shows: newest save first.
	time.Sleep(20 * time.Millisecond)
	_ = m.executeCommand("save newer")
	if got := m.savedTrackLists(); len(got) != 2 || got[0] != "newer" || got[1] != "road trip" {
		t.Fatalf("newest first: %v", got)
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
	// It is the first list the SV source shows, even after newer saves.
	_ = m.executeCommand("save newer")
	m.setSearchSource(searchSaved)
	if l := m.search.SelectedSavedList(); l == nil || l.Name != lastSessionList || len(l.Tracks) != 3 {
		t.Fatalf("last session comes first in SV, got %+v", l)
	}
	// A launch with nothing to restore drops the stale list.
	m2, _ := newListModel(t, t.TempDir())
	if got := m2.savedTrackLists(); len(got) != 0 {
		t.Fatalf("no previous session, no list: %v", got)
	}
}

func TestSavedSource_CtrlSlashCyclesThroughIt(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	writeList(t, dir, "mix", persistTracks())
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	ctrlSlash(m)
	if m.searchSrc != searchClaude {
		t.Fatalf("AM → CC first, got %v", m.searchSrc)
	}
	if cmd := ctrlSlash(m); cmd != nil || m.searchSrc != searchSaved || m.search != m.searchSV {
		t.Fatalf("CC → saved lists, nothing looked up (cmd=%v src=%v)", cmd != nil, m.searchSrc)
	}
	lines := m.searchFindLines(60, 12)
	joined := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "Saved lists") || !strings.Contains(lines[2], "SV") {
		t.Fatalf("the SV source names itself in the header and the prompt:\n%s", joined)
	}
	if !strings.Contains(joined, "mix  ·  3 tracks") || strings.Contains(joined, "One") {
		t.Fatalf("the lists show as folded headers with their size:\n%s", joined)
	}
	footer := ansi.Strip(strings.Join(m.statusLines(200), " "))
	if !strings.Contains(footer, "SV ") || !strings.Contains(footer, "^/ feed") {
		t.Fatalf("the footer shows the SV prompt and where ^/ goes next: %q", footer)
	}
	ctrlSlash(m)
	if m.searchSrc != searchFeed || m.search != m.searchFE {
		t.Fatalf("saved lists → feed, got %v", m.searchSrc)
	}
	ctrlSlash(m)
	if m.searchSrc != searchApple || m.search != m.searchAM {
		t.Fatalf("feed → AM, got %v", m.searchSrc)
	}
	// With nothing saved the source says so.
	m3, _ := newListModel(t, t.TempDir())
	m3.mode = modeSearch
	m3.search.SetSize(60, 12)
	m3.setSearchSource(searchSaved)
	if v := ansi.Strip(strings.Join(m3.searchFindLines(60, 12), "\n")); !strings.Contains(v, "no saved lists yet") {
		t.Fatalf("an empty SV source explains itself:\n%s", v)
	}
}

func TestSavedSource_AddsASongOrTheWholeList(t *testing.T) {
	dir := t.TempDir()
	m, mock := newListModel(t, dir)
	writeList(t, dir, "mix", persistTracks())
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.setSearchSource(searchSaved)
	if l := m.search.SelectedSavedList(); l == nil || l.Name != "mix" {
		t.Fatalf("the highlight starts on the first list's header, got %+v", l)
	}
	// Enter opens the list whole; the first song is one row down.
	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	if v := m.search.View(); !strings.Contains(v, "One") || !strings.Contains(v, "Three") || strings.Contains(v, "more") {
		t.Fatalf("enter opens all songs, no +5 more rows: %q", v)
	}
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if tr := m.search.SelectedTrack(); tr == nil || tr.Title != "One" {
		t.Fatalf("the first song is highlighted, got %+v", tr)
	}
	if cmd := m.addSelection(false); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 1 || m.queueTracks[0].Title != "One" {
		t.Fatalf("one song added to Tracks: %+v", m.queueTracks)
	}
	// Back on the header, ^, adds the whole list; the song already there is not doubled.
	m.handleSearchKey("ctrl+up", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if cmd := m.addSelection(false); cmd != nil {
		_ = cmd()
	}
	if len(m.queueTracks) != 3 {
		t.Fatalf("the whole list is in Tracks, once: %+v", m.queueTracks)
	}
	// Nothing was playing, so nothing started: the engine has not been touched
	// and the first play hands the whole of Tracks over.
	if len(mock.appendQueueIDs) != 0 || mock.playCalled || mock.setQueueIDs != nil {
		t.Fatalf("adding with an idle engine must not start playback: appends=%v play=%v set=%v", mock.appendQueueIDs, mock.playCalled, mock.setQueueIDs)
	}
	if cmd := m.togglePlayPause(); cmd != nil {
		_ = cmd()
	}
	if len(mock.setQueueAtIDs) != 3 {
		t.Fatalf("space starts the three songs that were added: %v", mock.setQueueAtIDs)
	}
	// ctrl+→ marks the header: the whole list goes with the selection.
	m.search.ToggleSelected()
	items := m.search.SelectedItems()
	if len(items) != 1 || items[0].List == nil || items[0].List.Name != "mix" {
		t.Fatalf("a marked header stands for the whole list: %+v", items)
	}
	if v := m.search.View(); !strings.Contains(v, "▶ ") {
		t.Fatalf("the header stays highlighted: %q", v)
	}
	// Enter again folds it back to the header.
	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	if v := m.search.View(); strings.Contains(v, "One") {
		t.Fatalf("enter folds the list: %q", v)
	}
}

func TestSavedSource_CtrlDeleteRemovesTheList(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	writeList(t, dir, "older", persistTracks())
	time.Sleep(20 * time.Millisecond)
	writeList(t, dir, "newer", persistTracks())
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.setSearchSource(searchSaved)
	del := func() { m.handleSearchKey("ctrl+delete", tea.KeyPressMsg{Code: tea.KeyDelete, Mod: tea.ModCtrl}) }
	// On a song row nothing is deleted; the status line says where to be.
	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	del()
	if got := m.savedTrackLists(); len(got) != 2 || !strings.Contains(m.errMsg, "header") {
		t.Fatalf("a song row deletes nothing: lists=%v msg=%q", got, m.errMsg)
	}
	// On the header the list goes, from disk and from the panel, and the
	// highlight lands on the list that took its place.
	m.handleSearchKey("ctrl+up", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if l := m.search.SelectedSavedList(); l == nil || l.Name != "newer" {
		t.Fatalf("setup: the header of newer, got %+v", l)
	}
	del()
	if _, err := os.Stat(filepath.Join(dir, "tracklists", "newer.json")); !os.IsNotExist(err) {
		t.Fatalf("the file is gone: %v", err)
	}
	if got := m.savedTrackLists(); len(got) != 1 || got[0] != "older" {
		t.Fatalf("one list left: %v", got)
	}
	if l := m.search.SelectedSavedList(); l == nil || l.Name != "older" {
		t.Fatalf("the highlight moves to the next list, got %+v", l)
	}
	if !strings.Contains(m.errMsg, `"newer" deleted`) {
		t.Fatalf("the status line says so: %q", m.errMsg)
	}
	if footer := ansi.Strip(strings.Join(m.statusLines(300), " ")); !strings.Contains(footer, "^Del delete list") {
		t.Fatalf("the SEARCH row lists the key in SV: %q", footer)
	}
	m.setSearchSource(searchApple)
	if footer := ansi.Strip(strings.Join(m.statusLines(300), " ")); strings.Contains(footer, "delete list") {
		t.Fatalf("the key is only listed in SV: %q", footer)
	}
	if cmd := m.handleSearchKey("ctrl+delete", tea.KeyPressMsg{Code: tea.KeyDelete, Mod: tea.ModCtrl}); cmd != nil || len(m.savedTrackLists()) != 1 {
		t.Fatal("outside SV the key deletes nothing")
	}
}

func TestSavedSource_RefreshKeepsOpenListsAndTheHighlight(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	writeList(t, dir, "older", persistTracks())
	time.Sleep(20 * time.Millisecond)
	writeList(t, dir, "newer", persistTracks())
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.setSearchSource(searchSaved)
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // header of older
	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	m.refreshSavedLists()
	if l := m.search.SelectedSavedList(); l == nil || l.Name != "older" {
		t.Fatalf("a refresh keeps the highlighted list, got %+v", l)
	}
	if v := m.search.View(); !strings.Contains(v, "One") {
		t.Fatalf("a refresh keeps the list open: %q", v)
	}
}

func TestAddToRestoredQueue_KeepsItWholeForTheFirstPlay(t *testing.T) {
	dir := t.TempDir()
	if err := queuestate.Save(filepath.Join(dir, "queue.json"), queuestate.FromTracks(persistTracks(), 1)); err != nil {
		t.Fatal(err)
	}
	m, mock := newListModel(t, dir) // three restored tracks, none in the engine yet
	extra := provider.Track{ID: "400", Title: "Four", Artist: "D"}
	if cmd := m.addToQueue("Four", []provider.Track{extra}, []string{views.PlaybackID(extra)}); cmd != nil {
		t.Fatal("adding to a restored, unstarted queue must not touch the engine")
	}
	if len(m.queueIDs) != 4 || len(mock.appendQueueIDs) != 0 {
		t.Fatalf("Tracks grows to four, the engine waits: queue=%v appends=%v", m.queueIDs, mock.appendQueueIDs)
	}
	if cmd := m.togglePlayPause(); cmd != nil {
		_ = cmd()
	}
	if len(mock.setQueueAtIDs) != 4 || mock.setQueueAtStart != "i.lib" {
		t.Fatalf("the first play hands all four over, starting where the session left off: %v from %q", mock.setQueueAtIDs, mock.setQueueAtStart)
	}
}

func TestSavedSource_ShowsAFreshSaveAtOnce(t *testing.T) {
	dir := t.TempDir()
	m, _ := newListModel(t, dir)
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.setSearchSource(searchSaved)
	fillTracks(m)
	_ = m.executeCommand("save fresh")
	if l := m.search.SelectedSavedList(); l == nil || l.Name != "fresh" {
		t.Fatalf("a save while SV is showing appears at once, got %+v", l)
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
	// The name Claude would send arrives as a message and is used as given.
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

func TestLoadCommand_IsGone(t *testing.T) {
	m, _ := newListModel(t, t.TempDir())
	_ = m.executeCommand("load mix")
	if !strings.Contains(m.errMsg, "unknown command") {
		t.Fatalf("the lists are reached through Search, not :load: %q", m.errMsg)
	}
	m.mode = modeCommand
	m.cmdBuf = "save my"
	m.handleCommandKey("space")
	if m.cmdBuf != "save my " {
		t.Fatalf("space types a space again: %q", m.cmdBuf)
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
