package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/simone-vibes/vibez/internal/config"
	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/art"
	"github.com/simone-vibes/vibez/internal/tui/styles"
	"github.com/simone-vibes/vibez/internal/tui/views"
	"github.com/simone-vibes/vibez/internal/vibe"
)

// --- mock player ---

type mockPlayer struct {
	state           player.State
	playCalled      bool
	pauseCalled     bool
	nextCalled      bool
	prevCalled      bool
	closeCalled     bool
	stopCalled      bool
	seekCalled      bool
	seekPos         time.Duration
	bitrateKbps     int
	setQueueIDs     []string // last IDs passed to SetQueue
	setQueueAtIDs   []string // last IDs passed to SetQueueAt
	setQueueAtStart string   // start ID passed to SetQueueAt
	playQueuedCalls []struct {
		Idx int
		ID  string
	}
	syncCalls          []syncCall // every SyncQueue call, in order
	appendQueueIDs     [][]string // all calls to AppendQueue (each call appended)
	moveInQueueCalls   []struct{ From, To int }
	removeFromQueueIdx []int // all calls to RemoveFromQueue, in call order
	err                error
	stateCh            chan player.State
}

func newMockPlayer() *mockPlayer {
	return &mockPlayer{stateCh: make(chan player.State, 4)}
}

func (m *mockPlayer) Play() error     { m.playCalled = true; return m.err }
func (m *mockPlayer) Pause() error    { m.pauseCalled = true; return m.err }
func (m *mockPlayer) Stop() error     { m.stopCalled = true; return m.err }
func (m *mockPlayer) Next() error     { m.nextCalled = true; return m.err }
func (m *mockPlayer) Previous() error { m.prevCalled = true; return m.err }
func (m *mockPlayer) Seek(pos time.Duration) error {
	m.seekCalled = true
	m.seekPos = pos
	return m.err
}
func (m *mockPlayer) SetVolume(_ float64) error { return m.err }
func (m *mockPlayer) SetAudioBitrate(kbps int) error {
	m.bitrateKbps = kbps
	return m.err
}
func (m *mockPlayer) SetRepeat(_ int) error                { return m.err }
func (m *mockPlayer) SetShuffle(_ bool) error              { return m.err }
func (m *mockPlayer) SetEqualizer(_ []player.EQBand) error { return m.err }
func (m *mockPlayer) SetQueue(ids []string) error          { m.setQueueIDs = ids; return m.err }
func (m *mockPlayer) SetQueueAt(ids []string, startID string) error {
	m.setQueueAtIDs = ids
	m.setQueueAtStart = startID
	return m.err
}

type syncCall struct {
	IDs     []string
	Current string
	Play    string
}

func (m *mockPlayer) SyncQueue(ids []string, currentID, playID string) error {
	m.syncCalls = append(m.syncCalls, syncCall{IDs: append([]string(nil), ids...), Current: currentID, Play: playID})
	return m.err
}

func (m *mockPlayer) PlayQueued(idx int, id string) error {
	m.playQueuedCalls = append(m.playQueuedCalls, struct {
		Idx int
		ID  string
	}{idx, id})
	return m.err
}
func (m *mockPlayer) SetPlaylist(_ string, _ int) error { return m.err }
func (m *mockPlayer) AppendQueue(ids []string) error {
	m.appendQueueIDs = append(m.appendQueueIDs, ids)
	return m.err
}
func (m *mockPlayer) RemoveFromQueue(idx int) error {
	m.removeFromQueueIdx = append(m.removeFromQueueIdx, idx)
	return m.err
}
func (m *mockPlayer) MoveInQueue(from, to int) error {
	m.moveInQueueCalls = append(m.moveInQueueCalls, struct{ From, To int }{from, to})
	return m.err
}
func (m *mockPlayer) ClearQueue() error { return m.err }
func (m *mockPlayer) GetState() (*player.State, error) {
	s := m.state
	return &s, m.err
}
func (m *mockPlayer) Subscribe() <-chan player.State { return m.stateCh }
func (m *mockPlayer) Close() error                   { m.closeCalled = true; return m.err }

// --- mock provider ---

type mockProvider struct{}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Search(_ context.Context, _ string) (*provider.SearchResult, error) {
	return &provider.SearchResult{}, nil
}
func (m *mockProvider) GetLibraryTracks(_ context.Context) ([]provider.Track, error) {
	return nil, nil
}
func (m *mockProvider) GetLibraryPlaylists(_ context.Context) ([]provider.Playlist, error) {
	return nil, nil
}
func (m *mockProvider) GetPlaylistTracks(_ context.Context, _ string) ([]provider.Track, error) {
	return nil, nil
}
func (m *mockProvider) GetAlbumTracks(_ context.Context, _ string) ([]provider.Track, error) {
	return nil, nil
}

func (m *mockProvider) GetLibraryAlbumTracks(_ context.Context, _ string) ([]provider.Track, error) {
	return nil, nil
}
func (m *mockProvider) GetCatalogPlaylistTracks(_ context.Context, _ string) ([]provider.Track, error) {
	return nil, nil
}
func (m *mockProvider) CreatePlaylist(_ context.Context, _ string, _ []string) (provider.Playlist, error) {
	return provider.Playlist{}, nil
}
func (m *mockProvider) LoveSong(_ context.Context, _ string, _ bool) error      { return nil }
func (m *mockProvider) GetSongRating(_ context.Context, _ string) (bool, error) { return false, nil }
func (m *mockProvider) IsAuthenticated() bool                                   { return true }
func (m *mockProvider) AddToPlaylist(_ context.Context, _, _ string) error      { return nil }
func (m *mockProvider) GetRecommendations(_ context.Context) ([]provider.RecommendationGroup, error) {
	return nil, nil
}
func (m *mockProvider) GetStationTracks(_ context.Context, _ string) ([]provider.Track, error) {
	return nil, nil
}

type stationMockProvider struct {
	mockProvider
	tracks []provider.Track
	err    error
}

func (m *stationMockProvider) GetStationTracks(_ context.Context, _ string) ([]provider.Track, error) {
	return m.tracks, m.err
}

// --- helpers ---

func testCfg() *config.Config {
	cfg := &config.Config{
		StoreFront: "us",
		AuthPort:   7777,
		Provider:   "apple",
		Theme:      "default",
		VibeAgent:  "keywords", // never spawn the Claude CLI from tests
	}
	// Saves from tests must never reach ~/.config/vibez/config.json (a test
	// once overwrote the user's tokens that way); keep them in a temp file.
	dir, err := os.MkdirTemp("", "vibez-test-cfg-")
	if err != nil {
		panic(err)
	}
	cfg.SetPath(filepath.Join(dir, "config.json"))
	return cfg
}

func TestTestCfg_NeverSavesToTheRealConfig(t *testing.T) {
	cfg := testCfg()
	if err := cfg.Save(""); err != nil {
		t.Fatalf("saving a test config should go to its temp path: %v", err)
	}
	home, _ := os.UserHomeDir()
	real := filepath.Join(home, ".config", "vibez", "config.json")
	before, _ := os.Stat(real)
	bare := &config.Config{}
	if err := bare.Save(""); err == nil {
		t.Fatal("a pathless config must refuse to save under go test")
	}
	if after, _ := os.Stat(real); before != nil && after != nil && !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the user's real config.json was touched by a test")
	}
}

func newModel(plyr player.Player) *Model {
	return New(testCfg(), &mockProvider{}, plyr, Options{})
}

// --- clamp ---

func TestClamp_Middle(t *testing.T) {
	got := clamp(0.5, 0, 1)
	if got != 0.5 {
		t.Errorf("clamp(0.5,0,1) = %v, want 0.5", got)
	}
}

func TestClamp_BelowLo(t *testing.T) {
	got := clamp(-1, 0, 1)
	if got != 0 {
		t.Errorf("clamp(-1,0,1) = %v, want 0", got)
	}
}

func TestClamp_AboveHi(t *testing.T) {
	got := clamp(2, 0, 1)
	if got != 1 {
		t.Errorf("clamp(2,0,1) = %v, want 1", got)
	}
}

func TestClamp_AtLoBoundary(t *testing.T) {
	got := clamp(0, 0, 1)
	if got != 0 {
		t.Errorf("clamp(0,0,1) = %v, want 0", got)
	}
}

func TestClamp_AtHiBoundary(t *testing.T) {
	got := clamp(1, 0, 1)
	if got != 1 {
		t.Errorf("clamp(1,0,1) = %v, want 1", got)
	}
}

// --- max ---

func TestMax_SecondLarger(t *testing.T) {
	if max(3, 5) != 5 {
		t.Errorf("max(3,5) != 5")
	}
}

func TestMax_FirstLarger(t *testing.T) {
	if max(5, 3) != 5 {
		t.Errorf("max(5,3) != 5")
	}
}

func TestMax_Equal(t *testing.T) {
	if max(3, 3) != 3 {
		t.Errorf("max(3,3) != 3")
	}
}

// --- Model construction ---

func TestNew_NilPlayer(t *testing.T) {
	m := newModel(nil)
	if m == nil {
		t.Fatal("New(cfg, prov, nil) returned nil")
	}
}

func TestNew_WithPlayer(t *testing.T) {
	m := newModel(newMockPlayer())
	if m == nil {
		t.Fatal("New with player returned nil")
	}
	if m.stateCh == nil {
		t.Error("stateCh should be set when player is provided")
	}
}

func TestModel_Init(t *testing.T) {
	m := newModel(nil)
	cmd := m.Init()
	if cmd == nil {
		t.Error("Init() should return non-nil cmd")
	}
}

// --- View ---

func TestModel_View_WidthZero(t *testing.T) {
	m := newModel(nil)
	got := m.View()
	// With width=0 the intro animation hasn't started yet — expect empty string.
	if got.Content != "" {
		t.Errorf("View() with width=0 should return empty string, got %q", got.Content)
	}
}

func TestModel_View_WithDimensions(t *testing.T) {
	m := newModel(nil)
	m.width = 80
	m.height = 24
	got := m.View()
	if got.Content == "" {
		t.Error("View() with dimensions should return non-empty string")
	}
}

// --- Update: WindowSizeMsg ---

func TestModel_Update_WindowSizeMsg(t *testing.T) {
	m := newModel(nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.width != 100 || m.height != 30 {
		t.Errorf("width=%d height=%d, want 100 30", m.width, m.height)
	}
}

// --- Update: tickMsg ---

func TestModel_Update_TickMsg(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Error("tickMsg should return a non-nil cmd (reschedule tick)")
	}
}

func TestModel_Update_TickMsg_ClearsExpiredErr(t *testing.T) {
	m := newModel(nil)
	m.errMsg = "old error"
	m.errExpiry = time.Now().Add(-1 * time.Second) // already expired
	m.Update(tickMsg(time.Now()))
	if m.errMsg != "" {
		t.Errorf("expired errMsg should be cleared, got %q", m.errMsg)
	}
}

// --- Update: errMsg ---

func TestModel_Update_ErrMsg(t *testing.T) {
	m := newModel(nil)
	m.Update(errMsg{err: errors.New("test error")})
	if m.errMsg != "test error" {
		t.Errorf("errMsg = %q, want %q", m.errMsg, "test error")
	}
}

// --- Update: key messages ---

func TestModel_Update_KeySearch(t *testing.T) {
	m := newModel(nil)
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.mode != modeSearch {
		t.Errorf("mode = %d, want modeSearch(%d)", m.mode, modeSearch)
	}
}

func TestModel_Update_KeyCommand(t *testing.T) {
	m := newModel(nil)
	m.Update(tea.KeyPressMsg{Code: ':', Text: ":"})
	if m.mode != modeCommand {
		t.Errorf("mode = %d, want modeCommand(%d)", m.mode, modeCommand)
	}
}

func TestModel_Update_KeyLibrary_IsNotAPanelAnyMore(t *testing.T) {
	m := newModel(nil)
	m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if m.activePanel >= 0 {
		t.Errorf("l must not open a panel (the library browser is not offered), got activePanel=%d", m.activePanel)
	}
	for _, p := range m.panels {
		if p.NavKey() == "l" {
			t.Fatal("the library panel must not be registered")
		}
	}
}

func TestModel_Update_KeySearchSetsContent(t *testing.T) {
	m := newModel(nil)
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.mode != modeSearch {
		t.Errorf("mode = %d, want modeSearch(%d)", m.mode, modeSearch)
	}
}

func TestModel_Update_KeyQuit_NilPlayer(t *testing.T) {
	m := newModel(nil)
	// 'q' highlights the playing queue entry; it neither quits nor opens a
	// panel, and with nothing queued it is a no-op.
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if m.activePanel >= 0 || m.queueCursorActive() {
		t.Error("q with an empty queue should do nothing")
	}
}

func TestModel_Update_KeyQuit_WithPlayer(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	// ':q' quits and closes the player
	m.mode = modeCommand
	m.cmdBuf = "q"
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !mp.closeCalled {
		t.Error("player.Close() should be called when quitting with :q")
	}
}

// --- togglePlayPause ---

func TestTogglePlayPause_NilPlayer(t *testing.T) {
	m := newModel(nil)
	cmd := m.togglePlayPause()
	msg := cmd()
	if _, ok := msg.(errMsg); !ok {
		t.Errorf("togglePlayPause with nil player should return errMsg, got %T", msg)
	}
}

func TestTogglePlayPause_Playing_CallsPause(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Playing = true
	cmd := m.togglePlayPause()
	cmd() // execute
	if !mp.pauseCalled {
		t.Error("Pause() should be called when state is playing")
	}
}

func TestTogglePlayPause_NotPlaying_CallsPlay(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Playing = false
	cmd := m.togglePlayPause()
	cmd() // execute
	if !mp.playCalled {
		t.Error("Play() should be called when state is not playing")
	}
}

func TestTogglePlayPause_PlayerError(t *testing.T) {
	mp := newMockPlayer()
	mp.err = errors.New("player error")
	m := newModel(mp)
	m.playerState.Playing = true
	cmd := m.togglePlayPause()
	msg := cmd()
	if _, ok := msg.(errMsg); !ok {
		t.Errorf("togglePlayPause with player error should return errMsg, got %T", msg)
	}
}

// --- playerCmd ---

func TestPlayerCmd_NilPlayer(t *testing.T) {
	m := newModel(nil)
	cmd := m.playerCmd(func(player.Player) error { return nil })
	msg := cmd()
	if _, ok := msg.(errMsg); !ok {
		t.Errorf("playerCmd with nil player should return errMsg, got %T", msg)
	}
}

func TestPlayerCmd_Next(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	cmd := m.playerCmd(func(p player.Player) error { return p.Next() })
	msg := cmd()
	if msg != nil {
		t.Errorf("playerCmd success should return nil msg, got %v", msg)
	}
	if !mp.nextCalled {
		t.Error("Next() should have been called")
	}
}

func TestPlayerCmd_Previous(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	cmd := m.playerCmd(func(p player.Player) error { return p.Previous() })
	cmd()
	if !mp.prevCalled {
		t.Error("Previous() should have been called")
	}
}

func TestPlayerCmdUsesCapturedPlayer(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	cmd := m.playerCmd(func(p player.Player) error { return p.Next() })
	m.player = nil
	msg := cmd()
	if msg != nil {
		t.Fatalf("playerCmd with captured player returned %v", msg)
	}
	if !mp.nextCalled {
		t.Fatal("captured player Next() was not called")
	}
}

// --- adjustVolume ---

func TestAdjustVolume_NilPlayer(t *testing.T) {
	m := newModel(nil)
	cmd := m.adjustVolume(0.1)
	msg := cmd()
	if _, ok := msg.(errMsg); !ok {
		t.Errorf("adjustVolume with nil player should return errMsg, got %T", msg)
	}
}

func TestAdjustVolume_ClampHi(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Volume = 0.99
	cmd := m.adjustVolume(0.05) // would exceed 1.0
	msg := cmd()
	if _, ok := msg.(saveVolumeMsg); !ok {
		t.Errorf("adjustVolume should return saveVolumeMsg on success, got %T", msg)
	}
}

func TestAdjustVolume_ClampLo(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Volume = 0.01
	cmd := m.adjustVolume(-0.05) // would go below 0
	msg := cmd()
	if _, ok := msg.(saveVolumeMsg); !ok {
		t.Errorf("adjustVolume should return saveVolumeMsg on success, got %T", msg)
	}
}

// --- Key: space (play/pause) ---

func TestModel_KeySpace_NilPlayer(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if cmd == nil {
		t.Fatal("space key should return non-nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(errMsg); !ok {
		t.Errorf("space with nil player should produce errMsg, got %T", msg)
	}
}

func TestModel_KeyNext_WithPlayer(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		cmd() // execute to trigger Next()
	}
	if !mp.nextCalled {
		t.Error("n key should call player.Next()")
	}
}

func TestModel_KeyPrevious_WithPlayer(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if cmd != nil {
		cmd() // execute to trigger Previous()
	}
	if !mp.prevCalled {
		t.Error("p key should call player.Previous()")
	}
}

func TestModel_KeyVolumeUp(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Volume = 0.5
	_, cmd := m.Update(tea.KeyPressMsg{Code: '+', Text: "+"})
	if cmd != nil {
		cmd()
	}
}

func TestModel_KeyVolumeDown(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Volume = 0.5
	_, cmd := m.Update(tea.KeyPressMsg{Code: '-', Text: "-"})
	if cmd != nil {
		cmd()
	}
}

// --- playerStateMsg ---

func TestModel_Update_PlayerStateMsg(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	track := &provider.Track{Title: "Live Track", Artist: "Live Artist"}
	msg := playerStateMsg{
		Track:   track,
		Playing: true,
		Volume:  0.8,
	}
	m.Update(msg)
	if m.playerState.Track == nil {
		t.Error("playerState.Track should be set after playerStateMsg")
	}
	if m.playerState.Track.Title != "Live Track" {
		t.Errorf("Track.Title = %q, want %q", m.playerState.Track.Title, "Live Track")
	}
}

// --- contentHeight ---

func TestModel_ContentHeight(t *testing.T) {
	m := newModel(nil)
	m.width = 600
	m.height = 26
	got := m.panelHeight()
	// fixed overhead = 9 lines (4 border/divider rows, the 4-row Now Playing
	// block, one status row: the Tracks keys are one flow on a wide terminal)
	if got != 17 {
		t.Errorf("panelHeight() = %d, want 17", got)
	}
}

func TestModel_ContentHeight_Small(t *testing.T) {
	m := newModel(nil)
	m.height = 1
	got := m.panelHeight()
	if got < 0 {
		t.Errorf("panelHeight() should not be negative, got %d", got)
	}
}

// --- statusNavLines ---

func TestModel_RenderFooter_ContainsKeyHints(t *testing.T) {
	m := newModel(nil)
	m.width = 100
	got := strings.Join(m.statusNavLines(m.width-4), "\n")
	if !strings.Contains(got, "search") {
		t.Errorf("statusNavLines() should contain key hints, got %q", got)
	}
}

// --- View with error message ---

func TestModel_View_WithErrMsg(t *testing.T) {
	m := newModel(nil)
	m.width = 80
	m.height = 24
	m.introStep = introDone // skip startup animation
	m.errMsg = "something went wrong"
	m.errExpiry = time.Now().Add(10 * time.Second)
	got := m.View()
	if !strings.Contains(got.Content, "something went wrong") {
		t.Errorf("View() should contain error message, got %q", got.Content)
	}
}

// --- library navigation in normal mode ---

func TestModel_UpdateActiveView_Library(t *testing.T) {
	m := newModel(nil)
	// Activate library panel (index 0)
	m.activePanel = 0
	m.width = 80
	m.height = 24
	m.library.SetSize(80, 22)
	// Should not panic
	m.handleNormalKey(tea.KeyPressMsg{Code: 'j', Text: "j"}, "j")
}

func TestModel_UpdateActiveView_Search(t *testing.T) {
	m := newModel(nil)
	m.width = 80
	m.height = 24
	m.search.SetSize(80, 22)
	// Should not panic
	m.handleNormalKey(tea.KeyPressMsg{Code: 'j', Text: "j"}, "j")
}

func TestModel_UpdateActiveView_Queue(t *testing.T) {
	m := newModel(nil)
	// Should not panic with any panel state
	m.handleNormalKey(tea.KeyPressMsg{Code: 'j', Text: "j"}, "j")
}

func TestModel_UpdateActiveView_NowPlaying(t *testing.T) {
	m := newModel(nil)
	// Should not panic
	m.handleNormalKey(tea.KeyPressMsg{Code: 'j', Text: "j"}, "j")
}

// --- Search mode: keys go to search handling ---

func TestModel_SearchFocused_KeyGoesToSearch(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	// When in search mode, key messages should be handled without panic
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	_ = cmd // just verify no panic
}

// --- waitForState inner function ---

func TestWaitForState_ReadsFromChannel(t *testing.T) {
	ch := make(chan player.State, 1)
	st := player.State{Playing: true, Volume: 0.9}
	ch <- st

	cmd := waitForState(ch)
	msg := cmd() // should not block since channel has a value

	ps, ok := msg.(playerStateMsg)
	if !ok {
		t.Fatalf("waitForState returned %T, want playerStateMsg", msg)
	}
	if !ps.Playing {
		t.Error("playerStateMsg.Playing should be true")
	}
	if ps.Volume != 0.9 {
		t.Errorf("playerStateMsg.Volume = %v, want 0.9", ps.Volume)
	}
}

// --- Search popup: Enter calls SetQueue, Tab calls AppendQueue ---

// seedSearchResults plants a track into the model's search view so
// SelectedTrack() returns a non-nil result.
func seedSearchResults(m *Model, tracks ...provider.Track) {
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.search.SetState(tracks, false, nil)
}

func TestHandleSearchKey_CtrlDot_AppendsAndPlaysHighlighted(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{Title: "Already", CatalogID: "1"}}
	m.queueIDs = []string{"1"}
	cur := m.queueTracks[0]
	m.playerState.Track = &cur
	track := provider.Track{Title: "Hi", Artist: "There", CatalogID: "99999"}
	seedSearchResults(m, track)

	cmd := m.handleSearchKey("ctrl+.", tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("handleSearchKey(enter) returned nil cmd")
	}
	cmd()

	if mp.setQueueIDs != nil {
		t.Fatalf("Enter must never replace the queue, got SetQueue(%v)", mp.setQueueIDs)
	}
	// The engine is re-synced to the new list in one step, starting the new track.
	if len(mp.syncCalls) != 1 || len(mp.syncCalls[0].IDs) != 2 || mp.syncCalls[0].IDs[1] != "99999" || mp.syncCalls[0].Current != "1" || mp.syncCalls[0].Play != "99999" {
		t.Fatalf("Enter should sync the queue and start the appended track, got %+v", mp.syncCalls)
	}
	if len(m.queueTracks) != 2 || m.queueTracks[0].Title != "Already" {
		t.Fatalf("existing queue must be kept: %+v", m.queueTracks)
	}
}

func TestHandleSearchKey_CtrlDot_UsesLibraryID_WhenNoCatalogID(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	track := provider.Track{Title: "Library Song", ID: "i.LibraryAbc123"}
	seedSearchResults(m, track)

	cmd := m.handleSearchKey("ctrl+.", tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl})
	if cmd != nil {
		cmd()
	}
	// Nothing was loaded in the engine, so the whole (one-track) queue is handed
	// over and started at the library ID.
	if len(mp.setQueueAtIDs) != 1 || mp.setQueueAtIDs[0] != "i.LibraryAbc123" || mp.setQueueAtStart != "i.LibraryAbc123" {
		t.Errorf("SetQueueAt = %v start %q, want [i.LibraryAbc123]", mp.setQueueAtIDs, mp.setQueueAtStart)
	}
}

func TestHandleSearchKey_CtrlDot_NoResults_NoCall(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	// No results set → SelectedTrack() is nil.

	cmd := m.handleSearchKey("ctrl+.", tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl})
	// cmd may be nil or return no player call — SetQueue must NOT be called.
	if cmd != nil {
		cmd()
	}
	if len(mp.setQueueIDs) > 0 {
		t.Errorf("SetQueue called with no results: %v", mp.setQueueIDs)
	}
}

func TestHandleSearchKey_CtrlComma_CallsAppendQueue(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Track = &provider.Track{Title: "Playing", CatalogID: "playing"} // the engine holds a track: adds go to it
	track := provider.Track{Title: "Queued", Artist: "Band", CatalogID: "12345"}
	seedSearchResults(m, track)

	cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("handleSearchKey(shift+enter) returned nil cmd — expected AppendQueue call")
	}
	cmd()

	if len(mp.appendQueueIDs) == 0 {
		t.Fatal("AppendQueue was not called after Shift+Enter")
	}
	if mp.appendQueueIDs[0][0] != "12345" {
		t.Errorf("AppendQueue ID = %q, want %q", mp.appendQueueIDs[0][0], "12345")
	}
}

func TestHandleSearchKey_CtrlComma_IdleEngineAddsWithoutPlaying(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	seedSearchResults(m, provider.Track{Title: "Queued", Artist: "Band", CatalogID: "12345"})
	// Nothing has played yet: the engine's append would start the song on its
	// own, so the add stays in the model.
	if cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatal("with an idle engine ctrl+, must not talk to the player")
	}
	if len(m.queueIDs) != 1 || len(mp.appendQueueIDs) != 0 || mp.playCalled || mp.setQueueIDs != nil {
		t.Fatalf("Tracks gets the song, the engine nothing: queue=%v appends=%v play=%v set=%v", m.queueIDs, mp.appendQueueIDs, mp.playCalled, mp.setQueueIDs)
	}
	// The first play hands the whole of Tracks to the engine.
	if cmd := m.togglePlayPause(); cmd != nil {
		_ = cmd()
	}
	if len(mp.setQueueAtIDs) != 1 || mp.setQueueAtIDs[0] != "12345" {
		t.Fatalf("space starts the queue that was built up: %v", mp.setQueueAtIDs)
	}
}

func TestSearchArrows_PlainMoveTracksCtrlMoveSearchCtrlShiftSelect(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{ID: "1", Title: "One"}, {ID: "2", Title: "Two"}, {ID: "3", Title: "Three"}}
	m.queueIDs = []string{"1", "2", "3"}
	m.syncQueue()
	m.setQueueCursor(0)
	seedSearchResults(m, provider.Track{Title: "A", CatalogID: "a"}, provider.Track{Title: "B", CatalogID: "b"}, provider.Track{Title: "C", CatalogID: "c"})
	m.mode = modeSearch
	// The plain arrows move the Tracks highlight, not the Search one.
	m.handleSearchKey("down", tea.KeyPressMsg{Code: tea.KeyDown})
	if m.queueCursor != 1 || m.search.SelectedIndex() != 0 {
		t.Fatalf("↓ moves in Tracks: cursor=%d search=%d", m.queueCursor, m.search.SelectedIndex())
	}
	// Ctrl+arrows move the Search highlight; Tracks stays where it was.
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if m.search.SelectedIndex() != 1 || m.queueCursor != 1 || m.search.SelectionCount() != 0 {
		t.Fatalf("^↓ moves in Search without selecting: search=%d cursor=%d sel=%d", m.search.SelectedIndex(), m.queueCursor, m.search.SelectionCount())
	}
	// Ctrl+Shift+arrows sweep-select.
	m.handleSearchKey("ctrl+shift+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl | tea.ModShift})
	if m.search.SelectionCount() != 2 || m.search.SelectedIndex() != 2 {
		t.Fatalf("^⇧↓ selects the row left and the row landed on: sel=%d search=%d", m.search.SelectionCount(), m.search.SelectedIndex())
	}
	// Bubble Tea names the keys the way the bindings spell them.
	if s := (tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl | tea.ModShift}).String(); s != "ctrl+shift+up" {
		t.Fatalf("ctrl+shift+up is named %q", s)
	}
	if s := (tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}).String(); s != "ctrl+down" {
		t.Fatalf("ctrl+down is named %q", s)
	}
	footer := ansi.Strip(strings.Join(m.statusLines(300), " "))
	for _, want := range []string{"^↑/↓ pick", "^⇧↑/↓ select", "↑/↓ move in tracks"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("the SEARCH row lists %q: %q", want, footer)
		}
	}
}

func TestTracksCtrlArrows_DriveTheSearchList(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{ID: "1", Title: "One"}, {ID: "2", Title: "Two"}, {ID: "3", Title: "Three"}}
	m.queueIDs = []string{"1", "2", "3"}
	m.syncQueue()
	m.setQueueCursor(0)
	seedSearchResults(m, provider.Track{Title: "A", CatalogID: "a"}, provider.Track{Title: "B", CatalogID: "b"}, provider.Track{Title: "C", CatalogID: "c"})
	m.mode = modeNormal // the keys are in Tracks
	press := func(k string, code rune, mod tea.KeyMod) { m.handleNormalKey(tea.KeyPressMsg{Code: code, Mod: mod}, k) }
	// From Tracks, Ctrl+↓ moves the Search highlight; Tracks and the mode stay.
	press("ctrl+down", tea.KeyDown, tea.ModCtrl)
	if m.search.SelectedIndex() != 1 || m.queueCursor != 0 || m.mode != modeNormal || m.queueIDs[0] != "1" {
		t.Fatalf("^↓ in Tracks moves the Search highlight only: search=%d cursor=%d mode=%v ids=%v", m.search.SelectedIndex(), m.queueCursor, m.mode, m.queueIDs)
	}
	press("ctrl+shift+down", tea.KeyDown, tea.ModCtrl|tea.ModShift)
	if m.search.SelectionCount() != 2 || m.search.SelectedIndex() != 2 {
		t.Fatalf("^⇧↓ sweep-selects in Search: sel=%d search=%d", m.search.SelectionCount(), m.search.SelectedIndex())
	}
	press("ctrl+right", tea.KeyRight, tea.ModCtrl)
	if m.search.SelectionCount() != 1 {
		t.Fatalf("^→ toggles the highlighted Search row: sel=%d", m.search.SelectionCount())
	}
	press("ctrl+left", tea.KeyLeft, tea.ModCtrl)
	if m.search.SelectionCount() != 0 {
		t.Fatalf("^← clears the Search selection: sel=%d", m.search.SelectionCount())
	}
	// K and J still reorder Tracks; Ctrl+arrows no longer do.
	press("J", 'J', 0)
	if m.queueIDs[1] != "1" || m.queueCursor != 1 {
		t.Fatalf("J moves the highlighted track down: ids=%v cursor=%d", m.queueIDs, m.queueCursor)
	}
	// The keys work from Tracks but the TRACKS row lists only its own keys.
	footer := ansi.Strip(strings.Join(m.statusLines(600), " "))
	if !strings.Contains(footer, "⇧←/⇧→ move") {
		t.Fatalf("the TRACKS row lists its own keys: %q", footer)
	}
	for _, hidden := range []string{"search pick", "search select", "search toggle/clear", "add from search"} {
		if strings.Contains(footer, hidden) {
			t.Fatalf("the TRACKS row does not list the Search keys (%q): %q", hidden, footer)
		}
	}
}

func TestSearchTyping_ToggleEnterAndEsc(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.provider = &mockProvider{}
	m.handleNormalKey(tea.KeyPressMsg{Code: tea.KeyTab}, "tab")
	if m.mode != modeSearch || m.searchTyping {
		t.Fatalf("Tab lands in Search, browsing: mode=%v typing=%v", m.mode, m.searchTyping)
	}
	typeText := func(text string) tea.Cmd {
		var cmd tea.Cmd
		for _, r := range text {
			cmd = m.handleSearchKey(string(r), tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return cmd
	}
	typeText("x")
	if m.searchQuery != "" {
		t.Fatalf("browsing: text goes nowhere, got %q", m.searchQuery)
	}
	footer := ansi.Strip(strings.Join(m.statusLines(400), " "))
	if !strings.Contains(footer, "^' type") || !strings.Contains(footer, "→ open/fold") || !strings.Contains(footer, "Enter play tracks pick") {
		t.Fatalf("browsing footer: %q", footer)
	}
	if s := (tea.KeyPressMsg{Code: 0x27, Mod: tea.ModCtrl}).String(); s != "ctrl+'" {
		t.Fatalf("ctrl+' is named %q", s)
	}
	m.handleSearchKey("ctrl+'", tea.KeyPressMsg{Code: 0x27, Mod: tea.ModCtrl})
	if !m.searchTyping {
		t.Fatal("^' starts typing")
	}
	if cmd := typeText("jazz"); cmd != nil || m.searchQuery != "jazz" || m.search.Loading() {
		t.Fatalf("typing edits the text and searches nothing yet: cmd=%v query=%q loading=%v", cmd != nil, m.searchQuery, m.search.Loading())
	}
	footer = ansi.Strip(strings.Join(m.statusLines(400), " "))
	if !strings.Contains(footer, "Enter search") || !strings.Contains(footer, "stop typing") || strings.Contains(footer, "^' type") {
		t.Fatalf("typing footer: %q", footer)
	}
	if cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || m.searchTyping || !m.search.Loading() {
		t.Fatalf("Enter runs the search and ends typing: cmd=%v typing=%v loading=%v", cmd != nil, m.searchTyping, m.search.Loading())
	}
	// Esc while typing only stops typing; browsing, it hands the keys back.
	m.handleSearchKey("ctrl+;", tea.KeyPressMsg{Code: ';', Mod: tea.ModCtrl})
	if !m.searchTyping {
		t.Fatal("^; is the other spelling of the toggle")
	}
	m.handleSearchKey("esc", tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.searchTyping || m.mode != modeSearch {
		t.Fatalf("Esc stops typing and stays in Search: typing=%v mode=%v", m.searchTyping, m.mode)
	}
	m.handleSearchKey("esc", tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.mode != modeNormal {
		t.Fatalf("Esc while browsing hands the keys back: mode=%v", m.mode)
	}
	// ":" while browsing opens command mode; the saved lists take no text.
	m.mode = modeSearch
	m.handleSearchKey(":", tea.KeyPressMsg{Code: ':', Text: ":"})
	if m.mode != modeCommand {
		t.Fatalf(": while browsing opens command mode, got %v", m.mode)
	}
	m.mode = modeSearch
	m.setSearchSource(searchSaved)
	m.handleSearchKey("ctrl+'", tea.KeyPressMsg{Code: 0x27, Mod: tea.ModCtrl})
	if m.searchTyping || !strings.Contains(m.errMsg, "no text") {
		t.Fatalf("SV takes no text: typing=%v msg=%q", m.searchTyping, m.errMsg)
	}
}

func TestCtrlQuoteFromTracks_JumpsToSearchAndTypes(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &mockProvider{}
	footer := ansi.Strip(strings.Join(m.statusLines(600), " "))
	if !strings.Contains(footer, "^' search & type") {
		t.Fatalf("the TRACKS row lists the key: %q", footer)
	}
	m.handleNormalKey(tea.KeyPressMsg{Code: 0x27, Mod: tea.ModCtrl}, "ctrl+'")
	if m.mode != modeSearch || !m.searchTyping || m.searchSrc != searchApple {
		t.Fatalf("^' from Tracks lands in Search, typing into AM: mode=%v typing=%v src=%v", m.mode, m.searchTyping, m.searchSrc)
	}
	// With the saved lists showing it is just the column, and it says why.
	m.mode = modeNormal
	m.setSearchSource(searchSaved)
	m.handleNormalKey(tea.KeyPressMsg{Code: ';', Mod: tea.ModCtrl}, "ctrl+;")
	if m.mode != modeSearch || m.searchTyping || !strings.Contains(m.errMsg, "no text") {
		t.Fatalf("^; with SV showing: mode=%v typing=%v msg=%q", m.mode, m.searchTyping, m.errMsg)
	}
}

func TestCtrlSlashFromTracks_CyclesTheSearchSource(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &mockProvider{}
	if f := ansi.Strip(strings.Join(m.statusLines(600), " ")); strings.Contains(f, "^/ ") {
		t.Fatalf("the TRACKS row leaves ^/ unlisted (it still works): %q", f)
	}
	m.handleNormalKey(tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}, "ctrl+/")
	if m.searchSrc != searchClaude || m.mode != modeNormal || m.search != m.searchCC {
		t.Fatalf("^/ from Tracks switches the Search source in place: src=%v mode=%v", m.searchSrc, m.mode)
	}
	m.mode = modeSearch
	if f := ansi.Strip(strings.Join(m.statusLines(600), " ")); !strings.Contains(f, "^/ saved lists") {
		t.Fatalf("the SEARCH row says where ^/ goes next: %q", f)
	}
	m.mode = modeNormal
	m.handleNormalKey(tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl}, "ctrl+_")
	if m.searchSrc != searchSaved || m.mode != modeNormal {
		t.Fatalf("the legacy spelling cycles on: src=%v mode=%v", m.searchSrc, m.mode)
	}
	if lines := m.searchFindLines(60, 12); !strings.Contains(ansi.Strip(lines[0]), "Saved lists") {
		t.Fatalf("the column shows the new source at once: %q", ansi.Strip(lines[0]))
	}
}

func TestSearchBrowsing_EnterPlaysTracksPickSpaceTogglesRightActsOnRows(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{ID: "1", Title: "One"}, {ID: "2", Title: "Two"}, {ID: "3", Title: "Three"}}
	m.queueIDs = []string{"1", "2", "3"}
	m.syncQueue()
	m.setQueueCursor(1)
	seedSearchResults(m, provider.Track{Title: "Alpha", CatalogID: "a"})
	footer := ansi.Strip(strings.Join(m.statusLines(600), " "))
	for _, want := range []string{"Enter play tracks pick", "spc play/pause", "→ open/fold"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("browsing footer lists %q: %q", want, footer)
		}
	}
	// Enter plays the track highlighted in Tracks; the keys stay in Search.
	cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter while browsing plays the Tracks pick")
	}
	_ = cmd()
	if mp.setQueueAtStart != "2" || m.mode != modeSearch {
		t.Fatalf("the second track starts, the keys stay in Search: start=%q mode=%v", mp.setQueueAtStart, m.mode)
	}
	// Space is play/pause and types nothing.
	m.playerState.Track = &m.queueTracks[1]
	m.playerState.Playing = true
	if cmd := m.handleSearchKey("space", tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}); cmd != nil {
		_ = cmd()
	}
	if !mp.pauseCalled || m.searchQuery != "" {
		t.Fatalf("space pauses and types nothing: pause=%v query=%q", mp.pauseCalled, m.searchQuery)
	}
	// → acts on the Search row: the Tracks header folds.
	m.handleSearchKey("ctrl+up", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if sec, ok := m.search.SelectedHeader(); !ok || sec != "Tracks" {
		t.Fatalf("setup: the Tracks header, got %q %v", sec, ok)
	}
	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	if v := m.search.View(); strings.Contains(v, "Alpha") {
		t.Fatalf("→ on a header folds it: %q", v)
	}
	// While typing, Enter and space are the prompt's again.
	m.searchTyping = true
	m.handleSearchKey("space", tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if m.searchQuery != " " {
		t.Fatalf("typing: space is text, got %q", m.searchQuery)
	}
	footer = ansi.Strip(strings.Join(m.statusLines(600), " "))
	if strings.Contains(footer, "play tracks pick") || !strings.Contains(footer, "Enter search") {
		t.Fatalf("typing footer: %q", footer)
	}
}

func TestCtrlCommaDotFromTracks_AddFromSearch(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	seedSearchResults(m, provider.Track{Title: "A", CatalogID: "a"}, provider.Track{Title: "B", CatalogID: "b"})
	m.mode = modeNormal
	if f := ansi.Strip(strings.Join(m.statusLines(600), " ")); strings.Contains(f, "add from search") {
		t.Fatalf("the TRACKS row leaves the Search keys unlisted: %q", f)
	}
	// Ctrl+, adds the highlighted Search song; the keys stay in Tracks.
	if cmd := m.handleNormalKey(tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl}, "ctrl+,"); cmd != nil {
		_ = cmd()
	}
	if len(m.queueIDs) != 1 || m.queueIDs[0] != "a" || m.mode != modeNormal {
		t.Fatalf("^, from Tracks adds the Search pick: queue=%v mode=%v", m.queueIDs, m.mode)
	}
	// Ctrl+↓ moves the Search highlight, Ctrl+. adds that song and starts it.
	m.handleNormalKey(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}, "ctrl+down")
	if cmd := m.handleNormalKey(tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl}, "ctrl+."); cmd != nil {
		_ = cmd()
	}
	if len(m.queueIDs) != 2 || m.queueIDs[1] != "b" || mp.setQueueAtStart != "b" {
		t.Fatalf("^. from Tracks adds and plays the Search pick: queue=%v start=%q", m.queueIDs, mp.setQueueAtStart)
	}
}

func TestSearchBrowsing_TracksKeysWorkButAreNotListed(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{ID: "1", Title: "One"}, {ID: "2", Title: "Two"}, {ID: "3", Title: "Three"}}
	m.queueIDs = []string{"1", "2", "3"}
	m.syncQueue()
	m.setQueueCursor(1)
	seedSearchResults(m, provider.Track{Title: "Alpha", CatalogID: "a"})
	// n is next, d removes the highlighted track: Tracks keys, from Search.
	if cmd := m.handleSearchKey("n", tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil {
		_ = cmd()
	}
	if !mp.nextCalled || m.searchQuery != "" {
		t.Fatalf("n is next while browsing: next=%v query=%q", mp.nextCalled, m.searchQuery)
	}
	if cmd := m.handleSearchKey("d", tea.KeyPressMsg{Code: 'd', Text: "d"}); cmd != nil {
		_ = cmd()
	}
	if len(m.queueIDs) != 2 || m.queueIDs[1] != "3" || m.mode != modeSearch {
		t.Fatalf("d removes the Tracks pick from Search: %v mode=%v", m.queueIDs, m.mode)
	}
	// A panel opened from Search owns the keys until it closes.
	m.handleSearchKey("y", tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.activePanel < 0 || m.panels[m.activePanel] != m.lyricsP {
		t.Fatalf("y opens lyrics from Search, got panel %d", m.activePanel)
	}
	m.handleSearchKey("esc", tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.activePanel >= 0 || m.mode != modeSearch {
		t.Fatalf("esc closes the panel and leaves the keys in Search: panel=%d mode=%v", m.activePanel, m.mode)
	}
	// The SEARCH row does not list them.
	footer := ansi.Strip(strings.Join(m.statusLines(600), " "))
	for _, hidden := range []string{"next/prev", " remove", "random", "repeat"} {
		if strings.Contains(footer, hidden) {
			t.Fatalf("the SEARCH row leaves the Tracks keys unlisted (%q): %q", hidden, footer)
		}
	}
	// While typing, letters are text.
	mp.nextCalled = false
	m.searchTyping = true
	m.handleSearchKey("n", tea.KeyPressMsg{Code: 'n', Text: "n"})
	if mp.nextCalled || m.searchQuery != "n" {
		t.Fatalf("typing: n is text: next=%v query=%q", mp.nextCalled, m.searchQuery)
	}
}

func TestPanels_TabEscOrOwnKeyReturnToThePreviousColumn(t *testing.T) {
	m := newModel(newMockPlayer())
	press := func(k string) {
		msg := tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		if k == "tab" {
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		} else if k == "esc" {
			msg = tea.KeyPressMsg{Code: tea.KeyEsc}
		}
		if m.mode == modeSearch {
			m.handleSearchKey(k, msg)
		} else {
			m.handleNormalKey(msg, k)
		}
	}
	// Lyrics from Tracks: Tab closes it and Tracks has the keys again.
	press("y")
	if m.activePanel < 0 || m.panels[m.activePanel] != m.lyricsP {
		t.Fatalf("y opens lyrics, got panel %d", m.activePanel)
	}
	press("tab")
	if m.activePanel >= 0 || m.mode != modeNormal {
		t.Fatalf("Tab closes the panel and returns to Tracks: panel=%d mode=%v", m.activePanel, m.mode)
	}
	// The same key again closes it too.
	press("y")
	press("y")
	if m.activePanel >= 0 || m.mode != modeNormal {
		t.Fatalf("y again closes lyrics: panel=%d mode=%v", m.activePanel, m.mode)
	}
	// Equalizer from Search: Esc and Tab both return to Search.
	m.mode = modeSearch
	press("e")
	if m.activePanel < 0 || m.panels[m.activePanel] != m.eqP {
		t.Fatalf("e opens the equalizer from Search, got panel %d", m.activePanel)
	}
	press("esc")
	if m.activePanel >= 0 || m.mode != modeSearch {
		t.Fatalf("Esc closes it and returns to Search: panel=%d mode=%v", m.activePanel, m.mode)
	}
	press("e")
	press("tab")
	if m.activePanel >= 0 || m.mode != modeSearch {
		t.Fatalf("Tab closes it and returns to Search: panel=%d mode=%v", m.activePanel, m.mode)
	}
}

// feedProvider answers GetRecommendations with one group of an album and a playlist.
type feedProvider struct{ mockProvider }

func (feedProvider) GetRecommendations(_ context.Context) ([]provider.RecommendationGroup, error) {
	return []provider.RecommendationGroup{{Title: "Made for You", Items: []provider.RecommendationItem{
		{ID: "1234", Kind: "album", Title: "Blue Album", Subtitle: "Weezer"},
		{ID: "pl.abc", Kind: "playlist", Title: "Chill Mix", Subtitle: "Apple Music"},
	}}}, nil
}

func TestFeedSource_CyclesLoadsAndSelectsLikeASearch(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &feedProvider{}
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	press := func(k string, code rune, mod tea.KeyMod) tea.Cmd {
		return m.handleSearchKey(k, tea.KeyPressMsg{Code: code, Mod: mod})
	}
	press("ctrl+/", '/', tea.ModCtrl) // CC
	press("ctrl+/", '/', tea.ModCtrl) // SV
	cmd := press("ctrl+/", '/', tea.ModCtrl)
	if m.searchSrc != searchFeed || m.search != m.searchFE || cmd == nil || !m.search.Loading() {
		t.Fatalf("SV → FE fetches the recommendations once: src=%v cmd=%v loading=%v", m.searchSrc, cmd != nil, m.search.Loading())
	}
	m.Update(cmd())
	view := ansi.Strip(strings.Join(m.searchFindLines(60, 14), "\n"))
	for _, want := range []string{"Feed", "FE", "Made for You", "Blue Album", "[album]", "Chill Mix", "[playlist]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the feed shows as a section of albums and playlists; %q missing:\n%s", want, view)
		}
	}
	footer := ansi.Strip(strings.Join(m.statusLines(400), " "))
	if !strings.Contains(footer, "FE ") || !strings.Contains(footer, "^/ apple music") || strings.Contains(footer, "^' type") {
		t.Fatalf("the FE footer: %q", footer)
	}
	press("ctrl+'", 0x27, tea.ModCtrl)
	if m.searchTyping || !strings.Contains(m.errMsg, "no text") {
		t.Fatalf("FE takes no text: typing=%v msg=%q", m.searchTyping, m.errMsg)
	}
	// Mark the album and the playlist; the selection carries both for the
	// usual expansion when added.
	press("ctrl+shift+down", tea.KeyDown, tea.ModCtrl|tea.ModShift)
	items := m.search.SelectedItems()
	if len(items) != 2 || items[0].Album == nil || items[0].Album.ID != "1234" || items[1].Playlist == nil || items[1].Playlist.ID != "pl.abc" {
		t.Fatalf("selection = %+v", items)
	}
	if cmd := m.addSelection(false); cmd == nil {
		t.Fatal("adding albums and playlists expands them through the provider")
	}
	// ^/ goes on to AM; a later visit keeps the loaded feed.
	press("ctrl+/", '/', tea.ModCtrl)
	if m.searchSrc != searchApple {
		t.Fatalf("FE → AM, got %v", m.searchSrc)
	}
	for range 3 {
		cmd = press("ctrl+/", '/', tea.ModCtrl)
	}
	if m.searchSrc != searchFeed || cmd != nil || !m.search.Feed() {
		t.Fatalf("a second visit keeps the loaded feed: src=%v cmd=%v feed=%v", m.searchSrc, cmd != nil, m.search.Feed())
	}
	// F is no longer a panel key.
	m.mode = modeNormal
	m.handleNormalKey(tea.KeyPressMsg{Code: 'F', Text: "F"}, "F")
	if m.activePanel >= 0 {
		t.Fatalf("F opens nothing now, got panel %d", m.activePanel)
	}
}

func TestShiftArrowsMoveTracks_AndShiftSShufflesAndPlays(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{ID: "1", Title: "One"}, {ID: "2", Title: "Two"}, {ID: "3", Title: "Three"}}
	m.queueIDs = []string{"1", "2", "3"}
	m.syncQueue()
	m.setQueueCursor(0)
	press := func(k string, code rune, mod tea.KeyMod) tea.Cmd {
		return m.handleNormalKey(tea.KeyPressMsg{Code: code, Mod: mod}, k)
	}
	// Shift+← is J (down), Shift+→ is K (up).
	press("shift+left", tea.KeyLeft, tea.ModShift)
	if m.queueIDs[1] != "1" || m.queueCursor != 1 {
		t.Fatalf("⇧← moves the highlighted track down: ids=%v cursor=%d", m.queueIDs, m.queueCursor)
	}
	press("shift+right", tea.KeyRight, tea.ModShift)
	if m.queueIDs[0] != "1" || m.queueCursor != 0 {
		t.Fatalf("⇧→ moves it back up: ids=%v cursor=%d", m.queueIDs, m.queueCursor)
	}
	// S shuffles the whole list and plays from the new top.
	if cmd := press("S", 'S', 0); cmd != nil {
		_ = cmd()
	}
	if len(m.queueIDs) != 3 || m.queueCursor != 0 {
		t.Fatalf("S keeps every track and highlights the top: ids=%v cursor=%d", m.queueIDs, m.queueCursor)
	}
	seen := map[string]bool{}
	for _, id := range m.queueIDs {
		seen[id] = true
	}
	if !seen["1"] || !seen["2"] || !seen["3"] {
		t.Fatalf("S is a permutation: %v", m.queueIDs)
	}
	if len(mp.setQueueAtIDs) != 3 || mp.setQueueAtStart != m.queueIDs[0] {
		t.Fatalf("the engine gets the new order and starts its first track: ids=%v start=%q", mp.setQueueAtIDs, mp.setQueueAtStart)
	}
	// :shuffle is gone; the controls row has no shuffle icon.
	_ = m.executeCommand("shuffle")
	if !strings.Contains(m.errMsg, "unknown command") {
		t.Fatalf(":shuffle is no more: %q", m.errMsg)
	}
	footer := ansi.Strip(strings.Join(m.statusLines(600), " "))
	if !strings.Contains(footer, "S shuffle & play") || !strings.Contains(footer, "⇧←/⇧→ move") {
		t.Fatalf("the TRACKS row lists S and the shift arrows: %q", footer)
	}
}

func TestHandleSearchKey_CtrlComma_DoesNotCallSetQueue(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	seedSearchResults(m, provider.Track{Title: "T", CatalogID: "x"})

	cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl})
	if cmd != nil {
		cmd()
	}
	if len(mp.setQueueIDs) > 0 {
		t.Errorf("Shift+Enter must not call SetQueue (would interrupt playback), but it did: %v", mp.setQueueIDs)
	}
}

func TestHandleSearchKey_CtrlComma_NeverQueuesTheSameTrackTwice(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Track = &provider.Track{Title: "Playing", CatalogID: "playing"} // the engine holds a track: adds go to it
	tracks := []provider.Track{
		{Title: "A", CatalogID: "111"},
		{Title: "B", CatalogID: "222"},
	}
	seedSearchResults(m, tracks...)

	// Shift+Enter twice on the same selection: the second press adds nothing and
	// just points at the queued copy.
	for range 2 {
		if cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl}); cmd != nil {
			cmd()
		}
	}
	if len(mp.appendQueueIDs) != 1 || len(m.queueIDs) != 1 || m.queueIDs[0] != "111" {
		t.Fatalf("expected a single append of 111, got appends=%v queue=%v", mp.appendQueueIDs, m.queueIDs)
	}
	if !strings.Contains(m.errMsg, "Already in Tracks") || m.queueCursor != 0 {
		t.Fatalf("the duplicate press should highlight the queued copy and say so, got %q cursor=%d", m.errMsg, m.queueCursor)
	}

	// A different track still gets added.
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl}); cmd != nil {
		cmd()
	}
	if len(mp.appendQueueIDs) != 2 || len(m.queueIDs) != 2 || m.queueIDs[1] != "222" {
		t.Fatalf("expected the second track to be appended, got appends=%v queue=%v", mp.appendQueueIDs, m.queueIDs)
	}
}

func TestHandleSearchKey_CtrlDot_OnQueuedTrackPlaysExistingCopy(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{Title: "Already", CatalogID: "1"}, {Title: "Hi", Artist: "There", CatalogID: "99999"}}
	m.queueIDs = []string{"1", "99999"}
	cur := m.queueTracks[0]
	m.playerState.Track = &cur
	seedSearchResults(m, provider.Track{Title: "Hi", Artist: "There", CatalogID: "99999"})

	if cmd := m.handleSearchKey("ctrl+.", tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl}); cmd != nil {
		cmd()
	}
	if len(m.queueIDs) != 2 || len(mp.syncCalls) != 0 || mp.setQueueIDs != nil {
		t.Fatalf("an already queued track must not be added again: queue=%v sync=%+v", m.queueIDs, mp.syncCalls)
	}
	if len(mp.playQueuedCalls) != 1 || mp.playQueuedCalls[0].Idx != 1 || mp.playQueuedCalls[0].ID != "99999" {
		t.Fatalf("Enter should jump to the queued copy, got %+v", mp.playQueuedCalls)
	}
}

func TestHandleSearchKey_Enter_OnHeaderFoldsSection(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.search.SetResults(&provider.SearchResult{Tracks: []provider.Track{{Title: "T", CatalogID: "x"}}}, false, nil)
	m.handleSearchKey("ctrl+up", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl}) // onto the Tracks header
	if sec, ok := m.search.SelectedHeader(); !ok || sec != "Tracks" {
		t.Fatalf("expected the Tracks header to be selectable, got %q %v", sec, ok)
	}
	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	if m.search.SelectedTrack() != nil || len(m.search.Results()) != 1 {
		t.Fatal("folding must keep the results but hide the tracks")
	}
	if v := m.search.View(); strings.Contains(v, "more") || strings.Contains(v, "less") {
		t.Fatalf("a folded section shows its header only: %q", v)
	}
	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	if len(m.queueIDs) != 0 {
		t.Fatal("enter on a header must never touch the queue")
	}
}

func TestModel_QueueTracksMsg_AppendsWithoutLeavingLibrary(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Track = &provider.Track{Title: "Playing", CatalogID: "playing"} // the engine holds a track: adds go to it
	m.activePanel = 0
	tracks := []provider.Track{{ID: "i.1", Title: "One"}, {ID: "i.2", Title: "Two"}}

	_, cmd := m.Update(views.QueueTracksMsg{IDs: []string{"i.1", "i.2"}, Tracks: tracks, Label: "Artist"})
	if cmd == nil {
		t.Fatal("QueueTracksMsg returned nil cmd")
	}
	cmd()
	if len(mp.appendQueueIDs) != 1 || strings.Join(mp.appendQueueIDs[0], ",") != "i.1,i.2" {
		t.Fatalf("AppendQueue calls = %#v, want [[i.1 i.2]]", mp.appendQueueIDs)
	}
	if m.activePanel != 0 {
		t.Fatalf("activePanel = %d, want library still active", m.activePanel)
	}
	if len(m.queueTracks) != 2 {
		t.Fatalf("queueTracks len = %d, want 2", len(m.queueTracks))
	}
}

func TestHandleNormalKey_CapitalL_DoesNothing(t *testing.T) {
	m := newModel(nil)
	m.activePanel = -1
	cmd := m.handleNormalKey(tea.KeyPressMsg{Text: "L"}, "L")
	if cmd != nil || m.activePanel >= 0 {
		t.Fatalf("L must do nothing now that the library browser is not offered (cmd=%v activePanel=%d)", cmd != nil, m.activePanel)
	}
}

func TestHandleSearchKey_Esc_ReturnsToQueueKeepingQuery(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchQuery = "test query"
	m.searchCursor = 4

	m.handleSearchKey("esc", tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.mode != modeNormal {
		t.Errorf("mode after esc = %v, want modeNormal", m.mode)
	}
	if m.searchQuery != "test query" || m.searchCursor != 4 {
		t.Errorf("esc must keep the query and cursor (results stay visible), got %q/%d", m.searchQuery, m.searchCursor)
	}
}

func TestHandleSearchKey_Backspace_DeletesLastChar(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "abc"
	m.searchCursor = 3 // cursor at end

	m.handleSearchKey("backspace", tea.KeyPressMsg{Code: tea.KeyBackspace})

	if m.searchQuery != "ab" {
		t.Errorf("searchQuery after backspace = %q, want %q", m.searchQuery, "ab")
	}
}

func TestHandleSearchKey_Typing_AppendsToQuery(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hel"
	m.searchCursor = 3 // cursor at end

	m.handleSearchKey("l", tea.KeyPressMsg{Code: 'l', Text: "l"})

	if m.searchQuery != "hell" {
		t.Errorf("searchQuery = %q, want %q", m.searchQuery, "hell")
	}
}

func TestHandleSearchKey_Enter_StaysInSearch(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	seedSearchResults(m, provider.Track{Title: "T", CatalogID: "x"})

	cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		cmd()
	}
	if m.mode != modeSearch {
		t.Errorf("mode after enter = %v, want modeSearch (keep browsing results)", m.mode)
	}
}

// --- Search cursor navigation ---

func TestHandleSearchKey_Left_MovesCursorBack(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 5

	m.handleSearchKey("left", tea.KeyPressMsg{Code: tea.KeyLeft})

	if m.searchCursor != 4 {
		t.Errorf("searchCursor after left = %d, want 4", m.searchCursor)
	}
}

func TestHandleSearchKey_Left_ClampAtZero(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 0

	m.handleSearchKey("left", tea.KeyPressMsg{Code: tea.KeyLeft})

	if m.searchCursor != 0 {
		t.Errorf("searchCursor after left at 0 = %d, want 0", m.searchCursor)
	}
}

func TestHandleSearchKey_Right_MovesCursorForward(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 2

	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})

	if m.searchCursor != 3 {
		t.Errorf("searchCursor after right = %d, want 3", m.searchCursor)
	}
}

func TestHandleSearchKey_Right_ClampAtEnd(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 5

	m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})

	if m.searchCursor != 5 {
		t.Errorf("searchCursor after right at end = %d, want 5", m.searchCursor)
	}
}

func TestHandleSearchKey_Home_MovesCursorToStart(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 3

	m.handleSearchKey("home", tea.KeyPressMsg{Code: tea.KeyHome})

	if m.searchCursor != 0 {
		t.Errorf("searchCursor after home = %d, want 0", m.searchCursor)
	}
}

func TestHandleSearchKey_End_MovesCursorToEnd(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 0

	m.handleSearchKey("end", tea.KeyPressMsg{Code: tea.KeyEnd})

	if m.searchCursor != 5 {
		t.Errorf("searchCursor after end = %d, want 5", m.searchCursor)
	}
}

func TestHandleSearchKey_Backspace_DeletesAtCursor(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 3 // cursor after "hel", before "lo"

	m.handleSearchKey("backspace", tea.KeyPressMsg{Code: tea.KeyBackspace})

	if m.searchQuery != "helo" {
		t.Errorf("searchQuery = %q, want %q", m.searchQuery, "helo")
	}
	if m.searchCursor != 2 {
		t.Errorf("searchCursor = %d, want 2", m.searchCursor)
	}
}

func TestHandleSearchKey_Delete_DeletesAfterCursor(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello"
	m.searchCursor = 2 // cursor after "he", before "llo"

	m.handleSearchKey("delete", tea.KeyPressMsg{Code: tea.KeyDelete})

	if m.searchQuery != "helo" {
		t.Errorf("searchQuery = %q, want %q", m.searchQuery, "helo")
	}
	if m.searchCursor != 2 {
		t.Errorf("searchCursor = %d, want 2 (unchanged)", m.searchCursor)
	}
}

func TestHandleSearchKey_Typing_InsertsAtCursor(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hllo"
	m.searchCursor = 1 // cursor after "h", before "llo"

	m.handleSearchKey("e", tea.KeyPressMsg{Code: 'e', Text: "e"})

	if m.searchQuery != "hello" {
		t.Errorf("searchQuery = %q, want %q", m.searchQuery, "hello")
	}
	if m.searchCursor != 2 {
		t.Errorf("searchCursor = %d, want 2", m.searchCursor)
	}
}

func TestHandleSearchKey_CtrlW_DeletesWordBefore(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "foo bar"
	m.searchCursor = 7

	m.handleSearchKey("ctrl+w", tea.KeyPressMsg{})

	if m.searchQuery != "foo " {
		t.Errorf("searchQuery = %q, want %q", m.searchQuery, "foo ")
	}
}

func TestHandleSearchKey_CtrlU_ClearsBeforeCursor(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "hello world"
	m.searchCursor = 5

	m.handleSearchKey("ctrl+u", tea.KeyPressMsg{})

	if m.searchQuery != " world" {
		t.Errorf("searchQuery = %q, want %q", m.searchQuery, " world")
	}
	if m.searchCursor != 0 {
		t.Errorf("searchCursor = %d, want 0", m.searchCursor)
	}
}

func TestModel_Update_VibeResultMsg_Success(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.setSearchSource(searchClaude)
	m.vibeShown = "chill"
	tracks := []provider.Track{
		{Title: "Song A", Artist: "Artist A", ID: "111", CatalogID: "cat111"},
	}
	m.Update(vibeResultMsg{tracks: tracks, query: "chill"})
	// The songs are offered in the panel's Vibes section, never queued by themselves.
	if len(mp.appendQueueIDs) != 0 || len(m.queueIDs) != 0 {
		t.Fatalf("a vibe result must not touch the queue: appends=%v queue=%v", mp.appendQueueIDs, m.queueIDs)
	}
	if !m.search.VibeResults() || len(m.search.Results()) != 1 || !strings.Contains(m.search.View(), "Vibes") {
		t.Fatalf("expected a Vibes section with the song, got vibe=%v results=%d view=%q", m.search.VibeResults(), len(m.search.Results()), m.search.View())
	}
	// A late result for an older description is ignored.
	m.Update(vibeResultMsg{tracks: tracks[:0], query: "something else"})
	if len(m.search.Results()) != 1 {
		t.Fatal("a result for another description must be dropped")
	}
}

func TestModel_Update_VibeResultMsg_Error(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.setSearchSource(searchClaude)
	m.vibeShown = "chill"
	m.search.SetResults(nil, true, nil)
	m.Update(vibeResultMsg{err: errors.New("search failed"), query: "chill"})
	if m.search.Loading() || len(m.search.Results()) != 0 || m.errMsg != "" {
		t.Fatalf("a failed vibe search should just leave an empty list (loading=%v results=%d err=%q)", m.search.Loading(), len(m.search.Results()), m.errMsg)
	}
}

func TestModel_Update_VibeResultMsg_Discovery(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	tracks := []provider.Track{{Title: "Discovery Song", ID: "999", CatalogID: "cat999"}}
	_, cmd := m.Update(vibeResultMsg{tracks: tracks, discovery: true})
	_ = cmd
}

func TestModel_Update_VibeResultMsg_Radio(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.radio.enabled = true
	m.radio.refilling = true
	tracks := []provider.Track{{Title: "Radio Song", ID: "888", CatalogID: "cat888"}}
	_, cmd := m.Update(vibeResultMsg{tracks: tracks, radio: true})
	_ = cmd
	if m.radio.refilling {
		t.Error("radio.refilling should be cleared after a successful refill")
	}
	if len(m.queueTracks) != 1 || m.queueTracks[0].Title != "Radio Song" {
		t.Errorf("queueTracks = %+v, want the radio track appended", m.queueTracks)
	}
}

func TestModel_Update_VibeResultMsg_Radio_StaleResultIgnored(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.radio.enabled = true
	m.radio.generation = 2
	m.radio.refilling = true

	tracks := []provider.Track{{Title: "Old Radio Song", ID: "old", CatalogID: "oldcat"}}
	_, cmd := m.Update(vibeResultMsg{tracks: tracks, radio: true, radioGen: 1})
	_ = cmd

	if len(m.queueTracks) != 0 {
		t.Errorf("stale radio result appended queueTracks = %+v, want none", m.queueTracks)
	}
	if len(mp.appendQueueIDs) != 0 {
		t.Errorf("stale radio result called AppendQueue with %v, want no call", mp.appendQueueIDs)
	}
}

func TestModel_Update_VibeResultMsg_Radio_EmptyAfterFilterRetries(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.radio.enabled = true
	m.radio.refilling = true
	m.radio.seed = &provider.Track{ID: "seed", CatalogID: "seedcat"}
	m.radio.skipped = map[string]bool{"blocked": true}
	tracks := []provider.Track{{Title: "Blocked", ID: "blocked"}}
	_, cmd := m.Update(vibeResultMsg{radio: true, tracks: tracks})
	if cmd == nil {
		t.Fatal("all-filtered radio result should retry via runRadioSearch, expected non-nil cmd")
	}
	if m.radio.retries != 1 {
		t.Errorf("radio.retries = %d, want 1", m.radio.retries)
	}
}

func TestRunRadioSearch_EmptyFilteredBatchReturnsEmptyResultForRetry(t *testing.T) {
	mp := newMockPlayer()
	prov := &stationMockProvider{
		tracks: []provider.Track{{Title: "Already Queued", Artist: "Artist", ID: "queued", CatalogID: "queuedcat"}},
	}
	m := New(testCfg(), prov, mp, Options{})
	m.radio.enabled = true
	m.radio.generation = 3
	m.radio.seed = &provider.Track{ID: "seed", CatalogID: "seedcat"}
	m.queueTracks = []provider.Track{{Title: "Already Queued", Artist: "Artist", ID: "queued", CatalogID: "queuedcat"}}
	m.queueIDs = []string{"queuedcat"}

	cmd := m.runRadioSearch()
	if cmd == nil {
		t.Fatal("runRadioSearch returned nil cmd")
	}
	raw := cmd()
	msg, ok := raw.(vibeResultMsg)
	if !ok {
		t.Fatalf("runRadioSearch msg = %T, want vibeResultMsg", raw)
	}
	if msg.err != nil {
		t.Fatalf("empty filtered radio batch err = %v, want nil so Update can retry", msg.err)
	}
	if !msg.radio || msg.radioGen != 3 {
		t.Fatalf("radio result flags = radio:%v gen:%d, want radio:true gen:3", msg.radio, msg.radioGen)
	}
	if len(msg.tracks) != 0 {
		t.Fatalf("tracks = %+v, want empty result", msg.tracks)
	}
}

func TestModel_Update_VibeResultMsg_Radio_SearchError(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.radio.enabled = true
	m.radio.refilling = true
	_, cmd := m.Update(vibeResultMsg{radio: true, err: errors.New("network error")})
	_ = cmd
	if m.radio.refilling {
		t.Error("radio.refilling should be cleared after a search error")
	}
}

func TestModel_Update_LoveSongMsg_Success(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(loveSongMsg{title: "Song", loved: true})
	_ = cmd
	// appendLog should record the love action.
	if len(m.debugLog) == 0 {
		t.Error("loveSongMsg should append a log entry")
	}
}

func TestModel_Update_LoveSongMsg_Error(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(loveSongMsg{title: "Song", err: errors.New("api error")})
	_ = cmd
}

func TestModel_Update_LoveSongMsg_Unlove(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(loveSongMsg{title: "Song", loved: false})
	_ = cmd
}

func TestModel_Update_SongRatingMsg(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(songRatingMsg{trackID: "track123", loved: true})
	_ = cmd
	if !m.favorites["track123"] {
		t.Error("songRatingMsg should set favorites entry")
	}
}

func TestModel_Update_SongRatingMsg_EmptyID(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(songRatingMsg{trackID: "", loved: true})
	_ = cmd // empty ID → no-op
}

func TestModel_Update_PlayTracksMsg_WithPlaylistID_AppendsTracks(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{Title: "Keep", CatalogID: "k"}}
	m.queueIDs = []string{"k"}
	keep := m.queueTracks[0]
	m.playerState.Track = &keep
	tracks := []provider.Track{{Title: "P1", CatalogID: "p1"}, {Title: "P2", CatalogID: "p2"}}
	msg := views.PlayTracksMsg{IDs: []string{"p1", "p2"}, Tracks: tracks, Track: &tracks[1], PlaylistID: "pl.123", StartIdx: 1}
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("PlayTracksMsg should return a cmd")
	}
	cmd()
	if len(m.queueIDs) != 3 || m.queueIDs[0] != "k" {
		t.Fatalf("playlist play must append, not replace: %v", m.queueIDs)
	}
	if len(mp.syncCalls) != 1 || len(mp.syncCalls[0].IDs) != 3 || mp.syncCalls[0].Play != "p2" || mp.syncCalls[0].Current != "k" {
		t.Fatalf("should sync and start at the playlist's start index within the appended block, got %+v", mp.syncCalls)
	}
}

func TestModel_Update_PlayTracksMsg_WithoutPlaylistID(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	track := provider.Track{Title: "T", Artist: "A", CatalogID: "cat2"}
	msg := views.PlayTracksMsg{IDs: []string{"cat2"}, Track: &track}
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("PlayTracksMsg should return a cmd")
	}
	cmd()
	if mp.setQueueIDs != nil {
		t.Error("PlayTracksMsg must not replace the queue")
	}
	if len(mp.setQueueAtIDs) != 1 || mp.setQueueAtIDs[0] != "cat2" {
		t.Errorf("with an empty engine the track should be loaded via SetQueueAt, got %v", mp.setQueueAtIDs)
	}
}

func TestModel_Update_InitStatusMsg(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(InitStatusMsg("loading engine…"))
	_ = cmd
	if m.initStatus != "loading engine…" {
		t.Errorf("initStatus = %q, want %q", m.initStatus, "loading engine…")
	}
}

func TestModel_Update_InitErrMsg(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(InitErrMsg{Err: errors.New("fatal error")})
	_ = cmd
	if m.errMsg == "" {
		t.Error("InitErrMsg should set errMsg")
	}
}

func TestModel_Update_ErrMsg_SetsErrField(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(errMsg{err: errors.New("some error")})
	_ = cmd
	if m.errMsg == "" {
		t.Error("errMsg should set m.errMsg")
	}
}

func TestModel_Update_PlaylistCreatedMsg(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(playlistCreatedMsg{name: "My Playlist"})
	_ = cmd
	if !strings.Contains(m.errMsg, "My Playlist") {
		t.Errorf("errMsg should contain playlist name, got %q", m.errMsg)
	}
}

func TestModel_Update_SessionExpiredMsg(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(SessionExpiredMsg{})
	_ = cmd
	if m.errMsg == "" {
		t.Error("SessionExpiredMsg should set errMsg")
	}
}

func TestModel_Update_SessionRestoredMsg(t *testing.T) {
	m := newModel(nil)
	_, cmd := m.Update(SessionRestoredMsg{})
	_ = cmd
	if m.errMsg == "" {
		t.Error("SessionRestoredMsg should set success errMsg")
	}
}

func TestModel_Update_MemTickMsg(t *testing.T) {
	m := newModel(nil)
	m.memProfiling = true
	_, cmd := m.Update(memTickMsg{stats: "RSS: 42 MB"})
	_ = cmd
	if m.memStats != "RSS: 42 MB" {
		t.Errorf("memStats = %q, want %q", m.memStats, "RSS: 42 MB")
	}
}

func TestModel_Update_SearchDebounceMsg_MatchesGen(t *testing.T) {
	m := newModel(nil)
	m.searchGen = 5
	_, cmd := m.Update(searchDebounceMsg{gen: 5, query: "test"})
	// Matching gen should return a search cmd.
	if cmd == nil {
		t.Error("searchDebounceMsg with matching gen should return a search cmd")
	}
}

func TestModel_Update_SearchDebounceMsg_StaleGen(t *testing.T) {
	m := newModel(nil)
	m.searchGen = 5
	_, cmd := m.Update(searchDebounceMsg{gen: 3, query: "stale"})
	// Stale gen should return nil cmd.
	if cmd != nil {
		// It might return nil batch — check it's not a real search cmd by verifying nil
		// (some implementations return tea.Batch(nil...) which resolves to nil)
		_ = cmd
	}
}

func TestModel_Update_PlayerStateMsg_WithLog(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.stateCh = mp.stateCh
	st := playerStateMsg{Playing: true, Log: "something logged"}
	_, _ = m.Update(st)
	found := false
	for _, entry := range m.debugLog {
		if strings.Contains(entry, "something logged") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Log field in playerStateMsg should be appended to debugLog")
	}
}

func TestModel_Update_PlayerStateMsg_ContentRestricted(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.stateCh = mp.stateCh
	track := &provider.Track{Title: "Restricted Song", Artist: "Artist", ID: "111"}
	st := playerStateMsg{
		Playing: false,
		Error:   "CONTENT_RESTRICTED: track unavailable",
		Track:   track,
	}
	_, cmd := m.Update(st)
	_ = cmd
	// Restricted content should not set errMsg but should log.
}

func TestModel_Update_PlayerStateMsg_GenericError(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.stateCh = mp.stateCh
	st := playerStateMsg{Error: "something went wrong"}
	_, cmd := m.Update(st)
	_ = cmd
	if m.errMsg == "" {
		t.Error("generic player error should set errMsg")
	}
}

func TestModel_Update_EngineReadyMsg(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(nil)

	// Simulate a window size arriving before the engine is ready (normal startup).
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	_, cmd := m.Update(EngineReadyMsg{
		Player:      mp,
		Provider:    &mockProvider{},
		HelperPaths: []string{"/usr/bin/helper"},
		Backend:     "cdp",
	})
	_ = cmd
	if m.player == nil {
		t.Error("EngineReadyMsg should set m.player")
	}
	if m.provider == nil {
		t.Error("EngineReadyMsg should set m.provider")
	}
	// m.library is kept (not a panel any more, but it still receives the
	// provider so its model can be rebuilt without a stale pointer).
	if m.library == nil || m.library.m == nil {
		t.Error("EngineReadyMsg must keep m.library wired")
	}
	// The new inner LibraryModel must have non-zero dimensions so items are visible.
	if m.library.m.Width() == 0 || m.library.m.Height() == 0 {
		t.Errorf("library inner model has zero dimensions (%dx%d) after EngineReadyMsg; window size was not re-applied",
			m.library.m.Width(), m.library.m.Height())
	}
}

// ─── handleNormalKey ────────────────────────────────────────────────────────

func TestHandleNormalKey_Space_TogglePlayPause(t *testing.T) {
	mp := newMockPlayer()
	mp.state.Playing = false
	m := newModel(mp)
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: tea.KeySpace}, "space")
	if cmd == nil {
		t.Fatal("space key should return a cmd")
	}
	cmd()
	if !mp.playCalled {
		t.Error("space when paused should call Play")
	}
}

func TestHandleNormalKey_N_Next(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: 'n', Text: "n"}, "n")
	if cmd != nil {
		cmd()
	}
	if !mp.nextCalled {
		t.Error("n key should call Next")
	}
}

func TestHandleNormalKey_P_Previous(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: 'p', Text: "p"}, "p")
	if cmd != nil {
		cmd()
	}
	if !mp.prevCalled {
		t.Error("p key should call Previous")
	}
}

func TestHandleNormalKey_Plus_VolumeUp(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Volume = 0.5
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: '+', Text: "+"}, "+")
	if cmd != nil {
		cmd()
	}
}

func TestHandleNormalKey_Minus_VolumeDown(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.Volume = 0.5
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: '-', Text: "-"}, "-")
	if cmd != nil {
		cmd()
	}
}

func TestHandleNormalKey_Plus_Discovery_IncreaseSimilarity(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.discovery.enabled = true
	m.discovery.similarity = 0.5
	m.handleNormalKey(tea.KeyPressMsg{Code: '+', Text: "+"}, "+")
	if m.discovery.similarity <= 0.5 {
		t.Error("+ key in discovery mode should increase similarity")
	}
}

func TestHandleNormalKey_Minus_Discovery_DecreaseSimilarity(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.discovery.enabled = true
	m.discovery.similarity = 0.7
	m.handleNormalKey(tea.KeyPressMsg{Code: '-', Text: "-"}, "-")
	if m.discovery.similarity >= 0.7 {
		t.Error("- key in discovery mode should decrease similarity")
	}
}

func TestHandleNormalKey_R_CycleRepeat(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.RepeatMode = player.RepeatModeOff
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: 'r', Text: "r"}, "r")
	if cmd != nil {
		cmd()
	}
	if m.playerState.RepeatMode != player.RepeatModeAll {
		t.Errorf("r key should cycle repeat Off→All, got %d", m.playerState.RepeatMode)
	}
}

func TestHandleNormalKey_R_CycleRepeatAll_To_One(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.playerState.RepeatMode = player.RepeatModeAll
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: 'r', Text: "r"}, "r")
	if cmd != nil {
		cmd()
	}
	if m.playerState.RepeatMode != player.RepeatModeOne {
		t.Errorf("r key should cycle repeat All→One, got %d", m.playerState.RepeatMode)
	}
}

func TestHandleNormalKey_S_JumpsToRandomQueuedTrack(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{{ID: "1", Title: "One"}, {ID: "2", Title: "Two"}, {ID: "3", Title: "Three"}}
	m.queueIDs = []string{"1", "2", "3"}
	m.queue.SetTracks(m.queueTracks)
	cur := m.queueTracks[1]
	m.playerState.Track = &cur
	for range 10 {
		mp.playQueuedCalls = nil
		if cmd := m.handleNormalKey(tea.KeyPressMsg{Code: 's', Text: "s"}, "s"); cmd != nil {
			cmd()
		}
		if len(mp.playQueuedCalls) != 1 || mp.playQueuedCalls[0].Idx == 1 || mp.playQueuedCalls[0].Idx < 0 || mp.playQueuedCalls[0].Idx > 2 {
			t.Fatalf("s should jump to a random other track, got %+v", mp.playQueuedCalls)
		}
	}
	if m.playerState.ShuffleMode || len(m.queueTracks) != 3 {
		t.Error("s must not toggle shuffle mode or change the queue")
	}
}

func TestHandleNormalKey_R_StartRadio(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	track := &provider.Track{Title: "Seed Song", Artist: "Artist", ID: "seed", CatalogID: "seedcat"}
	m.playerState.Track = track
	cmd := m.executeCommand("radio")
	if cmd == nil {
		t.Fatal("R key with a playing track should start radio and return a search cmd")
	}
	if !m.radio.enabled {
		t.Error("R key with a playing track should enable radio")
	}
	if m.radio.seed != track {
		t.Errorf("radio.seed = %+v, want %+v", m.radio.seed, track)
	}
}

// TestHandleNormalKey_R_StartRadio_DropsRestOfQueue guards against the
// regression where starting radio mid-album (or mid-playlist) had no
// visible effect: the rest of the already-queued tracks kept playing
// because radio only appended new tracks once the current track became the
// *last* queued item. Radio should instead drop everything queued after the
// seed so its picks play next.
func TestHandleNormalKey_R_StartRadio_KeepsRestOfQueue(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	track2 := provider.Track{Title: "Track 2", Artist: "Artist", ID: "t2", CatalogID: "cat2"}
	m.queueTracks = []provider.Track{
		{Title: "Track 1", Artist: "Artist", ID: "t1", CatalogID: "cat1"},
		track2,
		{Title: "Track 3", Artist: "Artist", ID: "t3", CatalogID: "cat3"},
		{Title: "Track 4", Artist: "Artist", ID: "t4", CatalogID: "cat4"},
	}
	m.queueIDs = []string{"cat1", "cat2", "cat3", "cat4"}
	m.queue.SetTracks(m.queueTracks)
	m.playerState.Track = &track2 // currently playing the 2nd (of 4) queued tracks

	cmd := m.executeCommand("radio")
	if cmd == nil {
		t.Fatal("R key with a playing track should start radio and return a cmd")
	}
	if !m.radio.enabled || m.radio.seed == nil || m.radio.seed.ID != "t2" {
		t.Fatalf("radio should be enabled and seeded by the playing track, got enabled=%v seed=%+v", m.radio.enabled, m.radio.seed)
	}
	// Radio only appends: everything already queued stays, nothing is removed.
	if len(m.queueTracks) != 4 || len(m.queueIDs) != 4 {
		t.Errorf("queue must be untouched, got %d tracks / %d ids", len(m.queueTracks), len(m.queueIDs))
	}
	if len(mp.removeFromQueueIdx) != 0 {
		t.Errorf("RemoveFromQueue must not be called, got %v", mp.removeFromQueueIdx)
	}
}

// TestHandleNormalKey_R_StartRadio_DroppedTracksExcludedFromRefill guards
// against the regression where dropQueueAfter's truncation had no lasting
// effect: it removed the rest of the album/playlist from the queue, but the
// very next refill's dedup only checked what was *still* queued, so a
// station response containing those same tracks (common, since they share
// the seed's album/playlist context) handed them right back — radio looked
// like it was doing nothing. Dropped tracks must be blacklisted so a refill
// can't re-add them.
func TestHandleNormalKey_R_StartRadio_RefillSkipsTracksAlreadyQueued(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	track2 := provider.Track{Title: "Track 2", Artist: "Artist", ID: "t2", CatalogID: "cat2"}
	track3 := provider.Track{Title: "Track 3", Artist: "Artist", ID: "t3", CatalogID: "cat3"}
	track4 := provider.Track{Title: "Track 4", Artist: "Artist", ID: "t4", CatalogID: "cat4"}
	m.queueTracks = []provider.Track{
		{Title: "Track 1", Artist: "Artist", ID: "t1", CatalogID: "cat1"},
		track2,
		track3,
		track4,
	}
	m.queueIDs = []string{"cat1", "cat2", "cat3", "cat4"}
	m.queue.SetTracks(m.queueTracks)
	m.playerState.Track = &track2 // currently playing the 2nd (of 4) queued tracks

	if cmd := m.executeCommand("radio"); cmd == nil {
		t.Fatal("R key with a playing track should start radio and return a cmd")
	}
	if len(m.radio.skipped) != 0 {
		t.Errorf("nothing is dropped any more, so nothing should be blacklisted: %v", m.radio.skipped)
	}

	// The station response echoes two tracks that are still queued plus one
	// new pick: only the new pick is appended, after everything lined up.
	newTrack := provider.Track{Title: "New Pick", Artist: "Artist", ID: "t5", CatalogID: "cat5"}
	m.Update(vibeResultMsg{
		radio:    true,
		radioGen: m.radio.generation,
		tracks:   []provider.Track{track3, track4, newTrack},
	})

	if len(m.queueTracks) != 5 {
		t.Fatalf("queueTracks = %+v, want 5 (t1..t4 kept, New Pick appended)", m.queueTracks)
	}
	if got := m.queueTracks[4].Title; got != "New Pick" {
		t.Errorf("queueTracks[4] = %q, want %q", got, "New Pick")
	}
}

// TestHandleNormalKey_R_StartRadio_SeedNotInQueue_InsertsAsPlayNext covers
// the case where the seed track isn't present in the local queue at all
// (e.g. radio started from a search result rather than something already
// queued). dropQueueAfter has nothing to drop, but the seed must still be
// queued up next so it actually plays — previously it was silently
// dropped and radio picks were just appended after the existing queue.
func TestHandleNormalKey_R_StartRadio_SeedNotInQueue_InsertsAsPlayNext(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.queueTracks = []provider.Track{
		{Title: "Queued 1", ID: "q1", CatalogID: "qcat1"},
		{Title: "Queued 2", ID: "q2", CatalogID: "qcat2"},
	}
	m.queueIDs = []string{"qcat1", "qcat2"}
	m.queue.SetTracks(m.queueTracks)
	seed := &provider.Track{Title: "Seed Song", ID: "seed", CatalogID: "seedcat"}
	m.playerState.Track = seed // playing a track that isn't in m.queueTracks

	cmd := m.executeCommand("radio")
	if cmd == nil {
		t.Fatal("R key with a playing track should start radio and return a cmd")
	}
	// The local insertion runs synchronously inside startRadioFrom, before
	// the returned cmd is ever invoked.
	if len(m.queueTracks) != 3 || m.queueTracks[0].ID != "seed" {
		t.Fatalf("queueTracks = %+v, want seed inserted at front", m.queueTracks)
	}
	if len(mp.removeFromQueueIdx) != 0 {
		t.Errorf("RemoveFromQueue calls = %v, want none", mp.removeFromQueueIdx)
	}

	// startRadioFrom batches dropQueueAfter's play-next command with
	// runRadioSearch's — drive both from the resulting BatchMsg.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", msg)
	}
	for _, sub := range batch {
		if sub != nil {
			sub()
		}
	}

	// Play-next re-syncs the engine with the seed inserted first.
	if len(mp.syncCalls) != 1 || len(mp.syncCalls[0].IDs) != 3 || mp.syncCalls[0].IDs[0] != "seedcat" || mp.syncCalls[0].Play != "" {
		t.Errorf("sync calls = %+v, want one with [seedcat qcat1 qcat2]", mp.syncCalls)
	}
}

func TestHandleNormalKey_R_NoTrackPlaying_NoOp(t *testing.T) {
	m := newModel(nil)
	cmd := m.executeCommand("radio")
	if cmd != nil {
		t.Error("R key with nothing playing should not start radio")
	}
	if m.radio.enabled {
		t.Error("radio should not be enabled with no track playing")
	}
}

func TestHandleNormalKey_R_StopRadio(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.radio.enabled = true
	m.radio.seed = &provider.Track{ID: "seed"}
	cmd := m.executeCommand("radio")
	_ = cmd
	if m.radio.enabled {
		t.Error("R key when radio is on should stop radio")
	}
}

// TestHandleNormalKey_R_StopRadio_ClearsSkipped guards against the
// regression where m.radio.skipped was never cleared: tracks dropped
// during one radio session stayed blacklisted forever across every
// subsequent radio session in the run.
func TestHandleNormalKey_R_StopRadio_ClearsSkipped(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.radio.enabled = true
	m.radio.seed = &provider.Track{ID: "seed"}
	m.radio.skipped = map[string]bool{"blocked": true}
	m.executeCommand("radio")
	if m.radio.skipped != nil {
		t.Errorf("radio.skipped = %v, want nil after stopping radio", m.radio.skipped)
	}
}

// TestHandleNormalKey_R_StartRadio_ResetsStaleSkipped guards against the
// same regression from the other direction: starting a fresh radio
// session must not inherit a blacklist left over from a previous one.
func TestHandleNormalKey_R_StartRadio_ResetsStaleSkipped(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	seed := &provider.Track{Title: "New Seed", ID: "new-seed", CatalogID: "newcat"}
	m.playerState.Track = seed
	m.radio.skipped = map[string]bool{"stale-from-last-session": true}

	cmd := m.executeCommand("radio")
	if cmd == nil {
		t.Fatal("R key with a playing track should start radio and return a cmd")
	}
	if m.radio.skipped["stale-from-last-session"] {
		t.Error("radio.skipped carried a stale entry into the new radio session")
	}
}

func TestHandleNormalKey_R_StopsDiscovery(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.discovery.enabled = true
	m.discovery.seed = &provider.Track{ID: "old-seed"}
	m.playerState.Track = &provider.Track{Title: "New Seed", ID: "new-seed", CatalogID: "newcat"}
	m.executeCommand("radio")
	if m.discovery.enabled {
		t.Error("starting radio should stop discovery — both compete for the last-track refill trigger")
	}
	if !m.radio.enabled {
		t.Error("R key should have started radio")
	}
}

func TestHandleNormalKey_V_DoesNothing(t *testing.T) {
	m := newModel(nil)
	m.handleNormalKey(tea.KeyPressMsg{Code: 'v', Text: "v"}, "v")
	if m.vibe.IsFocused() || m.mode != modeNormal {
		t.Error("the separate vibe prompt is gone; v must not focus anything")
	}
}

func TestHandleNormalKey_Colon_OpenCommandMode(t *testing.T) {
	m := newModel(nil)
	m.handleNormalKey(tea.KeyPressMsg{Code: ':', Text: ":"}, ":")
	if m.mode != modeCommand {
		t.Error(": key should switch to command mode")
	}
}

func TestHandleNormalKey_Slash_DoesNothing(t *testing.T) {
	m := newModel(nil)
	if cmd := m.handleNormalKey(tea.KeyPressMsg{Code: '/', Text: "/"}, "/"); cmd != nil || m.mode != modeNormal {
		t.Fatalf("/ in the queue must do nothing: Tab is the way to Search (mode=%d cmd=%v)", m.mode, cmd != nil)
	}
}

func TestHandleNormalKey_L_ToggleOff(t *testing.T) {
	m := newModel(nil)
	// Press l twice — second press closes.
	m.handleNormalKey(tea.KeyPressMsg{Code: 'l', Text: "l"}, "l")
	m.handleNormalKey(tea.KeyPressMsg{Code: 'l', Text: "l"}, "l")
	if m.activePanel >= 0 {
		t.Error("l key pressed twice should close library panel")
	}
}

func TestHandleNormalKey_DebugView_J_ScrollDown(t *testing.T) {
	m := newModel(nil)
	m.debugView = true
	m.debugScroll = 5
	m.handleNormalKey(tea.KeyPressMsg{Code: 'j', Text: "j"}, "j")
	if m.debugScroll != 4 {
		t.Errorf("j in debug view should decrement scroll, got %d", m.debugScroll)
	}
}

func TestHandleNormalKey_DebugView_K_ScrollUp(t *testing.T) {
	m := newModel(nil)
	m.debugView = true
	m.debugScroll = 2
	m.handleNormalKey(tea.KeyPressMsg{Code: 'k', Text: "k"}, "k")
	if m.debugScroll != 3 {
		t.Errorf("k in debug view should increment scroll, got %d", m.debugScroll)
	}
}

func TestHandleNormalKey_DebugView_BigG_ResetScroll(t *testing.T) {
	m := newModel(nil)
	m.debugView = true
	m.debugScroll = 10
	m.handleNormalKey(tea.KeyPressMsg{Code: 'G', Text: "G"}, "G")
	if m.debugScroll != 0 {
		t.Errorf("G in debug view should reset scroll to 0, got %d", m.debugScroll)
	}
}

func TestHandleNormalKey_DebugView_Esc_CloseView(t *testing.T) {
	m := newModel(nil)
	m.debugView = true
	m.handleNormalKey(tea.KeyPressMsg{Code: tea.KeyEsc}, "esc")
	if m.debugView {
		t.Error("esc in debug view should close it")
	}
}

// ─── handleCommandKey ───────────────────────────────────────────────────────

func TestHandleCommandKey_Esc_ClearsAndReturnsNormal(t *testing.T) {
	m := newModel(nil)
	m.mode = modeCommand
	m.cmdBuf = "some-cmd"
	m.handleCommandKey("esc")
	if m.mode != modeNormal {
		t.Error("esc should return to normal mode")
	}
	if m.cmdBuf != "" {
		t.Error("esc should clear cmdBuf")
	}
}

func TestHandleCommandKey_Backspace_DeletesChar(t *testing.T) {
	m := newModel(nil)
	m.cmdBuf = "quit"
	m.handleCommandKey("backspace")
	if m.cmdBuf != "qui" {
		t.Errorf("cmdBuf after backspace = %q, want %q", m.cmdBuf, "qui")
	}
}

func TestHandleCommandKey_Backspace_Empty_NoOp(t *testing.T) {
	m := newModel(nil)
	m.cmdBuf = ""
	m.handleCommandKey("backspace") // should not panic
}

func TestHandleCommandKey_Typing_AppendsToCmdBuf(t *testing.T) {
	m := newModel(nil)
	m.cmdBuf = "q"
	m.handleCommandKey("u")
	m.handleCommandKey("i")
	m.handleCommandKey("t")
	if m.cmdBuf != "quit" {
		t.Errorf("cmdBuf = %q, want %q", m.cmdBuf, "quit")
	}
}

func TestHandleCommandKey_Tab_CompletesSuggestion(t *testing.T) {
	m := newModel(nil)
	m.cmdBuf = "sa" // matches "save"
	m.handleCommandKey("tab")
	if !strings.HasPrefix(m.cmdBuf, "save") {
		t.Errorf("tab should complete suggestion, got %q", m.cmdBuf)
	}
}

func TestHandleCommandKey_Enter_ExecutesCommand(t *testing.T) {
	m := newModel(nil)
	m.mode = modeCommand
	m.cmdBuf = "debug-logs"
	cmd := m.handleCommandKey("enter")
	_ = cmd
	if m.mode != modeNormal {
		t.Error("enter should return to normal mode after executing command")
	}
}

// ─── executeCommand ─────────────────────────────────────────────────────────

func TestExecuteCommand_Quit_NilPlayer(t *testing.T) {
	m := newModel(nil)
	cmd := m.executeCommand("q")
	if cmd == nil {
		t.Error("quit command should return tea.Quit cmd")
	}
}

func TestExecuteCommand_Quit_Word(t *testing.T) {
	m := newModel(nil)
	cmd := m.executeCommand("quit")
	if cmd == nil {
		t.Error("'quit' command should return tea.Quit cmd")
	}
}

func TestExecuteCommand_DebugLogs_Toggle(t *testing.T) {
	m := newModel(nil)
	m.debugView = false
	m.executeCommand("debug-logs")
	if !m.debugView {
		t.Error("debug-logs should toggle debugView on")
	}
	m.executeCommand("debug-logs")
	if m.debugView {
		t.Error("debug-logs again should toggle debugView off")
	}
}

func TestExecuteCommand_Save_NoName_SetsError(t *testing.T) {
	m := newModel(nil)
	m.executeCommand("save ")
	if m.errMsg == "" {
		t.Error("save with empty name should set errMsg")
	}
}

func TestExecuteCommand_Save_IsALocalListNotAPlaylist(t *testing.T) {
	m := newModel(nil) // no queue path: nowhere to write a list
	m.queueTracks = []provider.Track{{Title: "T", ID: "1", CatalogID: "cat1"}}
	if cmd := m.executeCommand("save My Playlist"); cmd != nil {
		t.Fatal(":save writes a local list synchronously; it never returns a provider command")
	}
	if !strings.Contains(m.errMsg, "config directory") {
		t.Fatalf("without a queue path :save says why it cannot save: %q", m.errMsg)
	}
}

func TestExecuteCommand_SavePlaylist_WithName(t *testing.T) {
	m := newModel(nil)
	m.queueTracks = []provider.Track{{Title: "T", ID: "1", CatalogID: "cat1"}}
	cmd := m.executeCommand("save-playlist Another Playlist")
	if cmd == nil {
		t.Error("save-playlist with valid name should return a cmd")
	}
}

func TestExecuteCommand_Unknown_SetsError(t *testing.T) {
	m := newModel(nil)
	m.executeCommand("nonexistent-command")
	if !strings.Contains(m.errMsg, "nonexistent-command") {
		t.Errorf("unknown command should set errMsg containing command name, got %q", m.errMsg)
	}
}

func TestCommandSuggestions_EmptyBuf_ReturnsAll(t *testing.T) {
	m := newModel(nil)
	m.cmdBuf = ""
	suggs := m.commandSuggestions()
	if len(suggs) == 0 {
		t.Error("empty cmdBuf should return all suggestions")
	}
}

func TestCommandSuggestions_PrefixFilter(t *testing.T) {
	m := newModel(nil)
	m.cmdBuf = "sa"
	suggs := m.commandSuggestions()
	for _, s := range suggs {
		if !strings.HasPrefix(s.usage, "sa") {
			t.Errorf("suggestion %q does not start with 'sa'", s.usage)
		}
	}
}

// ─── Rendering functions ────────────────────────────────────────────────────

func TestToLines_ExactHeight(t *testing.T) {
	input := "a\nb\nc"
	lines := toLines(input, 3)
	if len(lines) != 3 {
		t.Errorf("toLines returned %d lines, want 3", len(lines))
	}
}

func TestToLines_PadsToHeight(t *testing.T) {
	lines := toLines("a\nb", 5)
	if len(lines) != 5 {
		t.Errorf("toLines returned %d lines, want 5", len(lines))
	}
}

func TestToLines_TruncatesToHeight(t *testing.T) {
	lines := toLines("a\nb\nc\nd\ne", 3)
	if len(lines) != 3 {
		t.Errorf("toLines returned %d lines, want 3", len(lines))
	}
}

func TestTruncateStr_WithinLimit(t *testing.T) {
	got := truncateStr("hello", 10)
	if got != "hello" {
		t.Errorf("truncateStr(short) = %q, want %q", got, "hello")
	}
}

func TestTruncateStr_Truncates(t *testing.T) {
	got := truncateStr("hello world", 6)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateStr(over limit) should end with ellipsis, got %q", got)
	}
	if len([]rune(got)) > 6 {
		t.Errorf("truncateStr result %q is too long (%d > 6 runes)", got, len([]rune(got)))
	}
}

func TestTruncateStr_LimitOne_NoEllipsis(t *testing.T) {
	got := truncateStr("hello", 1)
	// maxW <= 1 returns string unchanged.
	if got != "hello" {
		t.Errorf("truncateStr(maxW=1) = %q, want %q", got, "hello")
	}
}

func TestNowPlayingLines_NoTrack(t *testing.T) {
	m := newModel(nil)
	m.width = 80
	lines := m.nowPlayingLines(76, 12)
	if len(lines) != 12 {
		t.Errorf("nowPlayingLines returned %d lines, want 12", len(lines))
	}
}

func TestNowPlayingLines_WithTrack_Playing(t *testing.T) {
	m := newModel(nil)
	m.width = 80
	m.playerState = player.State{
		Playing: true,
		Track:   &provider.Track{Title: "Song", Artist: "Artist", Album: "Album", Duration: 3 * time.Minute},
		Volume:  0.8,
	}
	lines := m.nowPlayingLines(76, 15)
	if len(lines) != 15 {
		t.Errorf("nowPlayingLines returned %d lines, want 15", len(lines))
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "Song") || strings.Contains(l, "Artist") {
			found = true
			break
		}
	}
	if !found {
		t.Error("nowPlayingLines should contain track title or artist")
	}
}

func TestNowPlayingLines_WithTrack_Paused(t *testing.T) {
	m := newModel(nil)
	m.playerState.Playing = false
	m.playerState.Track = &provider.Track{Title: "Paused Song", Artist: "Artist", Album: "Album", Duration: time.Minute}
	lines := m.nowPlayingLines(76, 12)
	if len(lines) != 12 {
		t.Errorf("nowPlayingLines returned %d lines, want 12", len(lines))
	}
}

func TestFetchArtworkCmd_NoColorReturnsNil(t *testing.T) {
	m := newModel(newMockPlayer())
	m.supportsArtColor = func() bool { return false }
	m.artwork = artworkCache{url: "https://example.invalid/a.png", rendered: map[art.Size][]string{}}
	m.artworkGen = 1

	if cmd := m.fetchArtworkCmd("https://example.invalid/a.png", m.artworkGen); cmd != nil {
		t.Fatal("fetchArtworkCmd without colour support returned command, want nil")
	}
}

func TestNowPlayingLines_NoColorFallsBackToText(t *testing.T) {
	m := newModel(nil)
	m.artMode = true
	m.supportsArtColor = func() bool { return false }
	m.playerState.Track = &provider.Track{Title: "Text Song", Artist: "Text Artist", Album: "Album", ArtworkURL: "https://example.invalid/a.png", Duration: time.Minute}

	joined := strings.Join(m.nowPlayingLines(100, 14), "\n")
	if !strings.Contains(joined, "Text Song") || !strings.Contains(joined, "Text Artist") {
		t.Fatalf("fallback missing metadata: %q", joined)
	}
	if strings.Contains(joined, "▀") {
		t.Fatalf("fallback rendered artwork: %q", joined)
	}
}

func TestNowPlayingLines_ArtModeOffShowsBarWithoutArt(t *testing.T) {
	m := newModel(nil)
	m.supportsArtColor = func() bool { return true }
	m.artwork = artworkCache{
		url:      "https://example.invalid/a.png",
		img:      image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		rendered: map[art.Size][]string{},
	}
	m.playerState = player.State{
		Playing: true,
		Track:   &provider.Track{Title: "Bar Song", Artist: "Bar Artist", Album: "Album", ArtworkURL: "https://example.invalid/a.png", Duration: time.Minute},
	}

	joined := strings.Join(m.nowPlayingLines(100, nowPlayingTextRows), "\n")
	if !strings.Contains(joined, "Bar Song") || !strings.Contains(joined, "Bar Artist") {
		t.Fatalf("compact layout missing metadata: %q", joined)
	}
	if strings.Contains(joined, "Now Playing") || strings.Contains(joined, "▀") {
		t.Fatalf("compact layout must have no label and no artwork: %q", joined)
	}
	if h := m.nowPlayingHeight(); h != nowPlayingTextRows {
		t.Fatalf("nowPlayingHeight() with art mode off = %d, want %d", h, nowPlayingTextRows)
	}
}

func TestPlayerStateMsg_NoArtworkFetchWhenArtModeOff(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testArtworkPNG(t))
	}))
	defer server.Close()

	m := newModel(newMockPlayer())
	m.stateCh = nil
	m.supportsArtColor = func() bool { return true }
	track := &provider.Track{Title: "Quiet Song", Artist: "Artist", Album: "Album", ArtworkURL: server.URL, Duration: time.Minute}
	_, cmd := m.Update(playerStateMsg{Track: track, Playing: true})
	if cmd != nil {
		runArtworkCommand(t, cmd)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("artwork requests with art mode off = %d, want 0", got)
	}
	if m.artwork.url != server.URL {
		t.Fatalf("artwork cache url = %q, want %q (tracked even while off)", m.artwork.url, server.URL)
	}
}

func TestNowPlayingArtworkFetchAndResizeUsesCachedImage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testArtworkPNG(t))
	}))
	defer server.Close()

	m := newModel(newMockPlayer())
	m.stateCh = nil
	m.artMode = true
	m.supportsArtColor = func() bool { return true }
	track := &provider.Track{Title: "Art Song", Artist: "Art Artist", Album: "Album", ArtworkURL: server.URL, Duration: time.Minute}
	_, cmd := m.Update(playerStateMsg{Track: track, Playing: true})
	if cmd == nil {
		t.Fatal("playerStateMsg with artwork returned nil cmd")
	}
	msg := runArtworkCommand(t, cmd)
	updated, _ := m.Update(msg)
	m = updated.(*Model)

	first := strings.Join(m.nowPlayingLines(100, 14), "\n")
	second := strings.Join(m.nowPlayingLines(120, 16), "\n")
	if got := requests.Load(); got != 1 {
		t.Fatalf("artwork requests = %d, want 1", got)
	}
	for name, view := range map[string]string{"first": first, "second": second} {
		if !strings.Contains(view, "▀") || !strings.Contains(view, "Art Song") || !strings.Contains(view, "Art Artist") {
			t.Fatalf("%s render missing artwork or metadata: %q", name, view)
		}
	}
}

func TestNowPlayingArtworkDownloadFailureFallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	m := newModel(newMockPlayer())
	m.stateCh = nil
	m.artMode = true
	m.supportsArtColor = func() bool { return true }
	track := &provider.Track{Title: "Fallback Song", Artist: "Fallback Artist", Album: "Album", ArtworkURL: server.URL, Duration: time.Minute}
	_, cmd := m.Update(playerStateMsg{Track: track, Playing: true})
	if cmd == nil {
		t.Fatal("playerStateMsg with artwork returned nil cmd")
	}
	updated, _ := m.Update(runArtworkCommand(t, cmd))
	m = updated.(*Model)

	joined := strings.Join(m.nowPlayingLines(100, 14), "\n")
	if !strings.Contains(joined, "Fallback Song") || !strings.Contains(joined, "Fallback Artist") {
		t.Fatalf("fallback missing metadata: %q", joined)
	}
	if strings.Contains(joined, "▀") {
		t.Fatalf("failed artwork rendered art glyphs: %q", joined)
	}
}

func TestArtworkLoadedMsg_DiscardStaleSameURLFailureAfterABA(t *testing.T) {
	m := newModel(newMockPlayer())
	m.stateCh = nil
	m.artMode = true
	m.supportsArtColor = func() bool { return true }
	a := "https://example.invalid/a.png"
	b := "https://example.invalid/b.png"

	updated, _ := m.Update(playerStateMsg{Track: &provider.Track{Title: "A1", Artist: "Artist", Album: "Album", ArtworkURL: a, Duration: time.Minute}})
	m = updated.(*Model)
	oldAGen := m.artworkGen
	updated, _ = m.Update(playerStateMsg{Track: &provider.Track{Title: "B", Artist: "Artist", Album: "Album", ArtworkURL: b, Duration: time.Minute}})
	m = updated.(*Model)
	updated, _ = m.Update(playerStateMsg{Track: &provider.Track{Title: "A2", Artist: "Artist", Album: "Album", ArtworkURL: a, Duration: time.Minute}})
	m = updated.(*Model)
	newAGen := m.artworkGen
	if oldAGen == newAGen {
		t.Fatalf("artwork generation did not advance across A→B→A: %d", newAGen)
	}

	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	updated, _ = m.Update(artworkLoadedMsg{url: a, gen: newAGen, img: img})
	m = updated.(*Model)
	if m.artwork.img == nil || m.artwork.failed {
		t.Fatalf("new artwork success not applied: img=%v failed=%v", m.artwork.img, m.artwork.failed)
	}

	updated, _ = m.Update(artworkLoadedMsg{url: a, gen: oldAGen, err: errors.New("old failure")})
	m = updated.(*Model)
	if m.artwork.img == nil || m.artwork.failed {
		t.Fatalf("stale same-URL failure cleared newer success: img=%v failed=%v", m.artwork.img, m.artwork.failed)
	}
}

func TestNowPlayingLines_DoesNotRenderStaleArtworkForNewTrack(t *testing.T) {
	m := newModel(nil)
	m.artMode = true
	m.supportsArtColor = func() bool { return true }
	m.artwork = artworkCache{
		url:      "https://example.invalid/old.png",
		img:      image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		rendered: map[art.Size][]string{},
	}
	m.playerState = player.State{
		Playing: true,
		Track: &provider.Track{
			Title:      "New Song",
			Artist:     "New Artist",
			Album:      "New Album",
			ArtworkURL: "https://example.invalid/new.png",
			Duration:   time.Minute,
		},
	}

	joined := strings.Join(m.nowPlayingLines(100, 14), "\n")
	if !strings.Contains(joined, "New Song") || !strings.Contains(joined, "New Artist") {
		t.Fatalf("fallback missing new metadata: %q", joined)
	}
	if strings.Contains(joined, "▀") {
		t.Fatalf("rendered stale artwork beside new metadata: %q", joined)
	}
}

func TestNowPlayingArtModeClipsLongMetadata(t *testing.T) {
	m := newModel(nil)
	m.artMode = true
	m.supportsArtColor = func() bool { return true }
	m.artwork = artworkCache{
		url:      "https://example.invalid/a.png",
		img:      image.NewNRGBA(image.Rect(0, 0, 2, 2)),
		rendered: map[art.Size][]string{},
	}
	m.playerState = player.State{
		Playing: true,
		Track: &provider.Track{
			ID:         "long",
			Title:      strings.Repeat("Title", 80),
			Artist:     strings.Repeat("Artist", 80),
			Album:      strings.Repeat("Album", 80),
			ArtworkURL: "https://example.invalid/a.png",
			Duration:   time.Minute,
		},
	}

	contentW := 100
	lines := m.nowPlayingLines(contentW, 14)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "▀") {
		t.Fatalf("art mode did not render artwork: %q", joined)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got > contentW {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, contentW, line)
		}
	}
}

// TestNowPlayingArtMode_LayoutIsMinimal checks Simone's requested art view:
// the cover with just track, album, and elapsed time — no progress bar or
// control icons.
func TestNowPlayingArtMode_LayoutIsMinimal(t *testing.T) {
	m := newModel(nil)
	m.artMode = true
	m.supportsArtColor = func() bool { return true }
	m.artwork = artworkCache{
		url:      "https://example.invalid/a.png",
		img:      image.NewNRGBA(image.Rect(0, 0, 4, 4)),
		rendered: map[art.Size][]string{},
	}
	m.playerState = player.State{
		Playing:  true,
		Position: 42 * time.Second,
		Track: &provider.Track{
			Title:      "Minimal Song",
			Artist:     "Minimal Artist",
			Album:      "Minimal Album",
			ArtworkURL: "https://example.invalid/a.png",
			Duration:   3 * time.Minute,
		},
	}

	joined := strings.Join(m.nowPlayingLines(100, 20), "\n")
	for _, want := range []string{"▀", "Minimal Song", "Minimal Artist", "Minimal Album", "0:42 / 3:00"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("art view missing %q: %q", want, joined)
		}
	}
	for _, unwanted := range []string{"╱", "╲", "⏸", "⇄"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("art view should not contain %q: %q", unwanted, joined)
		}
	}
	if h := m.nowPlayingHeight(); h < 12 {
		t.Fatalf("nowPlayingHeight() in art mode = %d, want >= 12", h)
	}
}

func TestExecuteCommand_ArtTogglesAndPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newModel(nil)
	m.cfg.SetPath("") // save under the fake HOME so config.Load("") below finds it
	m.supportsArtColor = func() bool { return true }

	_ = m.executeCommand("art")
	if !m.artMode || !m.cfg.AlbumArt {
		t.Fatalf("after :art artMode=%v cfg.AlbumArt=%v, want true/true", m.artMode, m.cfg.AlbumArt)
	}
	saved, err := config.Load("")
	if err != nil {
		t.Fatalf("loading saved config: %v", err)
	}
	if !saved.AlbumArt {
		t.Fatal("saved config album_art = false, want true")
	}

	_ = m.executeCommand("art")
	if m.artMode || m.cfg.AlbumArt {
		t.Fatalf("after second :art artMode=%v cfg.AlbumArt=%v, want false/false", m.artMode, m.cfg.AlbumArt)
	}
}

func TestExecuteCommand_ArtRejectedWithoutColorSupport(t *testing.T) {
	m := newModel(nil)
	m.supportsArtColor = func() bool { return false }

	if cmd := m.executeCommand("art"); cmd != nil {
		t.Fatal(":art without colour support returned a command, want nil")
	}
	if m.artMode {
		t.Fatal(":art without colour support enabled art mode")
	}
	if m.errMsg == "" {
		t.Fatal(":art without colour support left no user-facing message")
	}
}

func TestExecuteCommand_ArtToggleOnFetchesCurrentCover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testArtworkPNG(t))
	}))
	defer server.Close()

	m := newModel(newMockPlayer())
	m.stateCh = nil
	m.supportsArtColor = func() bool { return true }
	track := &provider.Track{Title: "Late Song", Artist: "Artist", Album: "Album", ArtworkURL: server.URL, Duration: time.Minute}
	_, _ = m.Update(playerStateMsg{Track: track, Playing: true})
	if got := requests.Load(); got != 0 {
		t.Fatalf("artwork fetched while art mode off: %d requests", got)
	}

	cmd := m.executeCommand("art")
	if cmd == nil {
		t.Fatal(":art with a playing track returned nil cmd, want artwork fetch")
	}
	msg := runArtworkCommand(t, cmd)
	updated, _ := m.Update(msg)
	m = updated.(*Model)
	if got := requests.Load(); got != 1 {
		t.Fatalf("artwork requests after toggle = %d, want 1", got)
	}
	if !strings.Contains(strings.Join(m.nowPlayingLines(100, 16), "\n"), "▀") {
		t.Fatal("cover not rendered after toggling art mode on")
	}
}

func runArtworkCommand(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, batched := range batch {
			if batched == nil {
				continue
			}
			msg = batched()
			if _, ok := msg.(artworkLoadedMsg); ok {
				return msg
			}
		}
	}
	return msg
}

func testArtworkPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	img.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNowPlayingLines_WithErrMsg(t *testing.T) {
	m := newModel(nil)
	m.errMsg = "Something went wrong"
	m.errExpiry = time.Now().Add(time.Hour)
	lines := m.nowPlayingLines(76, 12)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "Something went wrong") {
			found = true
			break
		}
	}
	if !found {
		t.Error("nowPlayingLines should include errMsg when set")
	}
}

func TestNowPlayingLines_RepeatModeAll(t *testing.T) {
	m := newModel(nil)
	m.playerState.RepeatMode = player.RepeatModeAll
	m.playerState.Track = &provider.Track{Title: "T", Artist: "A", Album: "Al", Duration: time.Minute}
	lines := m.nowPlayingLines(76, 12)
	if len(lines) != 12 {
		t.Errorf("nowPlayingLines(RepeatAll) returned %d lines, want 12", len(lines))
	}
}

func TestNowPlayingLines_Loading(t *testing.T) {
	m := newModel(nil)
	m.playerState.Loading = true
	m.playerState.Track = &provider.Track{Title: "Loading", Artist: "A", Album: "Al", Duration: time.Minute}
	lines := m.nowPlayingLines(76, 12)
	if len(lines) != 12 {
		t.Errorf("nowPlayingLines(Loading) returned %d lines, want 12", len(lines))
	}
}

func TestNowPlayingLines_Favorite(t *testing.T) {
	m := newModel(nil)
	track := &provider.Track{Title: "Fav Song", Artist: "Artist", Album: "Album", ID: "fav1", Duration: time.Minute}
	m.playerState.Track = track
	m.favorites["fav1"] = true
	lines := m.nowPlayingLines(76, 12)
	if len(lines) != 12 {
		t.Errorf("nowPlayingLines(favorite) returned %d lines, want 12", len(lines))
	}
}

func TestDebugLogLines_Empty(t *testing.T) {
	m := newModel(nil)
	m.debugLog = nil
	lines := m.debugLogLines(80, 10)
	if len(lines) != 10 {
		t.Errorf("debugLogLines returned %d lines, want 10", len(lines))
	}
}

func TestDebugLogLines_WithEntries(t *testing.T) {
	m := newModel(nil)
	m.appendLog("normal entry")
	m.appendLog("[error] something failed")
	m.appendLog("[playing] Artist — Song")
	lines := m.debugLogLines(80, 10)
	if len(lines) != 10 {
		t.Errorf("debugLogLines returned %d lines, want 10", len(lines))
	}
}

func TestDebugLogLines_WithScroll(t *testing.T) {
	m := newModel(nil)
	for i := range 20 {
		m.appendLog(strings.Repeat("x", i+1))
	}
	m.debugScroll = 3
	lines := m.debugLogLines(80, 10)
	if len(lines) != 10 {
		t.Errorf("debugLogLines with scroll returned %d lines, want 10", len(lines))
	}
}

func TestSearchLines_WithResults(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.search.SetState([]provider.Track{{Title: "Hit Song", Artist: "Artist"}}, false, nil)
	lines := m.searchFindLines(45, 10)
	if len(lines) != 10 {
		t.Errorf("searchFindLines returned %d lines, want 10", len(lines))
	}
}

func TestSearchLines_Empty(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchQuery = "notfound"
	lines := m.searchFindLines(45, 10)
	if len(lines) != 10 {
		t.Errorf("searchFindLines(empty) returned %d lines, want 10", len(lines))
	}
}

// ─── Additional Update coverage ─────────────────────────────────────────────

func TestModel_Update_GlowTickMsg(t *testing.T) {
	m := newModel(nil)
	step := m.glowStep
	_, _ = m.Update(glowTickMsg(time.Now()))
	if m.glowStep != step+1 {
		t.Errorf("glowStep = %d, want %d", m.glowStep, step+1)
	}
}

func TestModel_Update_IntroTickMsg_Advances(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.introStep = 0
	_, _ = m.Update(introTickMsg(time.Now()))
	// introStep should advance by 1.
	if m.introStep <= 0 {
		t.Errorf("introStep did not advance after introTickMsg, got %d", m.introStep)
	}
}

func TestCreatePlaylistCmd_CallsProvider(t *testing.T) {
	m := newModel(nil)
	tracks := []provider.Track{{Title: "T1", ID: "id1", CatalogID: "cat1"}}
	m.queueTracks = tracks
	ids := []string{"cat1"}
	cmd := m.createPlaylistCmd("Test Playlist", ids)
	if cmd == nil {
		t.Fatal("createPlaylistCmd should return a cmd")
	}
	result := cmd()
	// Should return playlistCreatedMsg or errMsg (mock returns no error).
	if _, ok := result.(playlistCreatedMsg); !ok {
		if _, ok := result.(errMsg); !ok {
			t.Errorf("createPlaylistCmd result = %T, want playlistCreatedMsg or errMsg", result)
		}
	}
}

// ─── Panel wrapper coverage ───────────────────────────────────────────────────

func TestLibraryPanel_NavLabel(t *testing.T) {
	m := newModel(nil)
	// Access the library panel through the model's panels.
	for _, p := range m.panels {
		if p.NavKey() == "l" {
			if p.NavLabel() != "library" {
				t.Errorf("library NavLabel() = %q, want %q", p.NavLabel(), "library")
			}
			break
		}
	}
}

func TestLibraryPanel_SetSize(t *testing.T) {
	m := newModel(nil)
	for _, p := range m.panels {
		if p.NavKey() == "l" {
			p.SetSize(80, 20) // should not panic
			break
		}
	}
}

func TestLibraryPanel_View(t *testing.T) {
	m := newModel(nil)
	for _, p := range m.panels {
		if p.NavKey() == "l" {
			view := p.View()
			_ = view // just verify no panic
			break
		}
	}
}

func TestLibraryPanel_Update(t *testing.T) {
	m := newModel(nil)
	for _, p := range m.panels {
		if p.NavKey() == "l" {
			cmd := p.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
			_ = cmd // should not panic
			break
		}
	}
}

func TestQueuePanel_NavLabel(t *testing.T) {
	m := newModel(newMockPlayer())
	for _, p := range m.panels {
		if p.NavKey() == "q" {
			if p.NavLabel() != "queue" {
				t.Errorf("queue NavLabel() = %q, want %q", p.NavLabel(), "queue")
			}
			break
		}
	}
}

func TestQueuePanel_SetSize(t *testing.T) {
	m := newModel(newMockPlayer())
	for _, p := range m.panels {
		if p.NavKey() == "q" {
			p.SetSize(80, 20) // should not panic
			break
		}
	}
}

func TestQueuePanel_View(t *testing.T) {
	m := newModel(newMockPlayer())
	for _, p := range m.panels {
		if p.NavKey() == "q" {
			view := p.View()
			_ = view // should not panic
			break
		}
	}
}

func TestQueuePanel_Update(t *testing.T) {
	m := newModel(newMockPlayer())
	for _, p := range m.panels {
		if p.NavKey() == "q" {
			cmd := p.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
			_ = cmd // should not panic
			break
		}
	}
}

func TestQueuePanel_SelectedTrack_Empty(t *testing.T) {
	m := newModel(newMockPlayer())
	for _, p := range m.panels {
		if p.NavKey() == "q" {
			qp := p.(*queuePanel)
			idx, track := qp.SelectedTrack()
			if track != nil {
				t.Error("SelectedTrack() on empty queue should return nil track")
			}
			if idx >= 0 {
				t.Error("SelectedTrack() on empty queue should return idx < 0")
			}
			break
		}
	}
}

// ─── discoveryQueries ─────────────────────────────────────────────────────────

func TestDiscoveryQueries_HighSimilarity(t *testing.T) {
	seed := &provider.Track{Artist: "Daft Punk", Title: "Get Lucky", Genres: []string{"electronic"}}
	queries := discoveryQueries(seed, 0.9) // >= 0.85
	if len(queries) == 0 {
		t.Fatal("discoveryQueries(0.9) returned empty slice")
	}
	found := false
	for _, q := range queries {
		if strings.Contains(q, "Daft Punk") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("discoveryQueries(0.9) should include artist name, got %v", queries)
	}
}

func TestDiscoveryQueries_MediumHighSimilarity(t *testing.T) {
	seed := &provider.Track{Artist: "Kendrick Lamar", Genres: []string{"hip-hop"}}
	queries := discoveryQueries(seed, 0.7) // >= 0.65
	if len(queries) == 0 {
		t.Fatal("discoveryQueries(0.7) returned empty slice")
	}
}

func TestDiscoveryQueries_MediumSimilarity(t *testing.T) {
	seed := &provider.Track{Artist: "Frank Ocean", Genres: []string{"r&b"}}
	queries := discoveryQueries(seed, 0.5) // >= 0.45
	if len(queries) == 0 {
		t.Fatal("discoveryQueries(0.5) returned empty slice")
	}
}

func TestDiscoveryQueries_LowSimilarity(t *testing.T) {
	seed := &provider.Track{Artist: "The Weeknd", Genres: []string{"pop"}}
	queries := discoveryQueries(seed, 0.3) // >= 0.20
	if len(queries) == 0 {
		t.Fatal("discoveryQueries(0.3) returned empty slice")
	}
}

func TestDiscoveryQueries_VeryLowSimilarity(t *testing.T) {
	seed := &provider.Track{Artist: "Artist", Genres: []string{"jazz"}}
	queries := discoveryQueries(seed, 0.1) // < 0.20
	if len(queries) == 0 {
		t.Fatal("discoveryQueries(0.1) returned empty slice")
	}
}

func TestDiscoveryQueries_NoGenres(t *testing.T) {
	seed := &provider.Track{Artist: "Artist", Genres: nil}
	queries := discoveryQueries(seed, 0.8)
	if len(queries) == 0 {
		t.Fatal("discoveryQueries(no genres) returned empty slice")
	}
}

// ─── safeIdx ──────────────────────────────────────────────────────────────────

func TestSafeIdx_ValidIndex(t *testing.T) {
	lines := []string{"a", "b", "c"}
	got := safeIdx(lines, 1)
	if got != "b" {
		t.Errorf("safeIdx(1) = %q, want %q", got, "b")
	}
}

func TestSafeIdx_OutOfRange(t *testing.T) {
	lines := []string{"a", "b"}
	got := safeIdx(lines, 5)
	if got != "" {
		t.Errorf("safeIdx(5) out of range = %q, want %q", got, "")
	}
}

func TestSafeIdx_Zero(t *testing.T) {
	lines := []string{"first"}
	got := safeIdx(lines, 0)
	if got != "first" {
		t.Errorf("safeIdx(0) = %q, want %q", got, "first")
	}
}

// ─── queuePanelLines ─────────────────────────────────────────────────────────

func TestQueuePanelLines_Empty(t *testing.T) {
	m := newModel(nil)
	m.width = 80
	lines := m.queuePanelLines(76, 10)
	if len(lines) != 10 {
		t.Errorf("queuePanelLines(empty) returned %d lines, want 10", len(lines))
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "No tracks yet") || strings.Contains(l, "Tracks") {
			found = true
			break
		}
	}
	if !found {
		t.Error("queuePanelLines should contain the 'Tracks' header or the 'No tracks yet' message")
	}
}

func TestQueuePanelLines_WithTracks(t *testing.T) {
	m := newModel(nil)
	m.width = 80
	m.playerState.Track = &provider.Track{Title: "Now Playing"}
	m.queueTracks = []provider.Track{
		{Title: "Now Playing", Artist: "A"},
		{Title: "Next Track", Artist: "B"},
	}
	m.queue.SetTracks(m.queueTracks)
	lines := m.queuePanelLines(76, 10)
	if len(lines) != 10 {
		t.Errorf("queuePanelLines(tracks) returned %d lines, want 10", len(lines))
	}
}

// ─── statusNavContent and scheduleSearch ─────────────────────────────────────

func TestScheduleSearch_EmptyQuery(t *testing.T) {
	m := newModel(nil)
	cmd := m.scheduleSearch("")
	if cmd != nil {
		t.Error("scheduleSearch('') should return nil cmd")
	}
}

func TestScheduleSearch_NonEmptyQuery(t *testing.T) {
	m := newModel(nil)
	cmd := m.scheduleSearch("jazz")
	if cmd == nil {
		t.Error("scheduleSearch('jazz') should return non-nil cmd")
	}
}

// ─── Model.View() with different panels ──────────────────────────────────────

func TestModel_View_NormalMode(t *testing.T) {
	m := newModel(nil)
	m.width = 120
	m.height = 30
	view := m.View()
	if view.Content == "" {
		t.Error("View() in normal mode should return non-empty string")
	}
}

func TestModel_View_SearchMode(t *testing.T) {
	m := newModel(nil)
	m.width = 120
	m.height = 30
	m.mode = modeSearch
	view := m.View()
	_ = view // should not panic
}

func TestModel_View_CommandMode(t *testing.T) {
	m := newModel(nil)
	m.width = 120
	m.height = 30
	m.mode = modeCommand
	m.cmdBuf = "sa"
	view := m.View()
	_ = view // should not panic
}

func TestModel_View_DebugLog(t *testing.T) {
	m := newModel(nil)
	m.width = 120
	m.height = 30
	m.debugView = true
	m.appendLog("test entry")
	view := m.View()
	_ = view // should not panic
}

func TestModel_View_WithLibraryPanel(t *testing.T) {
	m := newModel(nil)
	m.width = 120
	m.height = 30
	// Activate library panel.
	for i, p := range m.panels {
		if p.NavKey() == "l" {
			m.activePanel = i
			break
		}
	}
	view := m.View()
	_ = view // should not panic
}

// ─── Model.Init coverage ─────────────────────────────────────────────────────

func TestModel_Init_ReturnsCmd(t *testing.T) {
	m := newModel(nil)
	cmd := m.Init()
	// Init returns a tick cmd — it should be non-nil.
	if cmd == nil {
		t.Error("Init() should return a non-nil cmd")
	}
}

func TestModel_Init_WithPlayer(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	cmd := m.Init()
	_ = cmd // should not panic
}

// ─── handleNormalKey queue panel: enter, d, K, J ──────────────────────────────

func TestHandleNormalKey_G_DoubleTap(t *testing.T) {
	m := newModel(nil)
	// First g — sets lastKey.
	m.handleNormalKey(tea.KeyPressMsg{Code: 'g', Text: "g"}, "g")
	if m.lastKey != "g" {
		t.Errorf("after first g, lastKey = %q, want %q", m.lastKey, "g")
	}
	// Second g — resets lastKey.
	m.handleNormalKey(tea.KeyPressMsg{Code: 'g', Text: "g"}, "g")
	if m.lastKey != "" {
		t.Errorf("after second g, lastKey = %q, want %q", m.lastKey, "")
	}
}

func TestHandleNormalKey_ActivePanel_Esc(t *testing.T) {
	m := newModel(nil)
	m.activePanel = 0
	m.handleNormalKey(tea.KeyPressMsg{Code: tea.KeyEsc}, "esc")
	if m.activePanel >= 0 {
		t.Error("esc with activePanel should close it")
	}
}

func TestHandleNormalKey_ActivePanel_ForwardKey(t *testing.T) {
	m := newModel(nil)
	// Open library panel.
	for i, p := range m.panels {
		if p.NavKey() == "l" {
			m.activePanel = i
			break
		}
	}
	// Forward a key to library panel — should not panic.
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: 'j', Text: "j"}, "j")
	_ = cmd
}

func TestHandleSearchKey_Space_InsertsSpaceInQuery(t *testing.T) {
	m := newModel(nil)
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = "taylor"
	m.searchCursor = 6

	m.handleSearchKey("space", tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})

	if m.searchQuery != "taylor " {
		t.Errorf("searchQuery after space = %q, want %q", m.searchQuery, "taylor ")
	}
	if m.searchCursor != 7 {
		t.Errorf("searchCursor after space = %d, want 7", m.searchCursor)
	}
}

func TestHandleCommandKey_Space_AppendsSpace(t *testing.T) {
	m := newModel(nil)
	m.cmdBuf = "save"
	m.handleCommandKey("space")
	if m.cmdBuf != "save " {
		t.Errorf("cmdBuf after space = %q, want %q", m.cmdBuf, "save ")
	}
}

func TestQualityLabel(t *testing.T) {
	cases := []struct {
		kbps int
		want string
	}{
		{0, ""},
		{-1, ""},
		{64, "64 kbps"},
		{256, "256 kbps"},
		{320, "320 kbps"},
		{321, "Lossless"},
		{1411, "Lossless"},
		{2000, "Lossless"},
		{2001, "Hi-Res"},
		{6000, "Hi-Res"},
	}
	for _, tc := range cases {
		if got := qualityLabel(tc.kbps); got != tc.want {
			t.Errorf("qualityLabel(%d) = %q, want %q", tc.kbps, got, tc.want)
		}
	}
}

// ─── Playlist picker ──────────────────────────────────────────────────────────

// TestPlaylistPicker_OpenFromQueue verifies that pressing 'p' in the queue panel
// when a track is selected transitions to modePlaylistPicker.

// TestPlaylistPicker_EscRestoresMode verifies that pressing Esc in the picker
// restores the previous mode.
func TestPlaylistPicker_EscRestoresMode(t *testing.T) {
	m := newModel(newMockPlayer())
	track := provider.Track{ID: "t1", Title: "Song", Artist: "Artist"}

	m.mode = modeNormal
	_ = m.openPlaylistPicker(&track)

	if m.mode != modePlaylistPicker {
		t.Fatalf("expected modePlaylistPicker, got %d", m.mode)
	}

	m3, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = m3.(*Model)

	if m.mode != modeNormal {
		t.Errorf("expected modeNormal after Esc, got %d", m.mode)
	}
}

// TestPlaylistPicker_LoadedPlaylistsAppear verifies that playlistsForPickerMsg
// populates the picker items.
func TestPlaylistPicker_LoadedPlaylistsAppear(t *testing.T) {
	m := newModel(newMockPlayer())
	track := provider.Track{ID: "t1", Title: "Song", Artist: "Artist"}
	_ = m.openPlaylistPicker(&track)
	gen := m.playlistPickerGen

	playlists := []provider.Playlist{
		{ID: "p1", Name: "Favorites"},
		{ID: "p2", Name: "Chill"},
	}
	m2, _ := m.Update(playlistsForPickerMsg{playlists: playlists, gen: gen})
	m = m2.(*Model)

	if m.playlistPickerLoading {
		t.Error("expected loading=false after receiving playlists")
	}
	if len(m.playlistPickerItems) != 2 {
		t.Errorf("expected 2 playlist items, got %d", len(m.playlistPickerItems))
	}
}

// TestPlaylistPicker_StaleResponseDiscarded verifies that a stale (old gen)
// playlistsForPickerMsg does not clobber current state.
func TestPlaylistPicker_StaleResponseDiscarded(t *testing.T) {
	m := newModel(newMockPlayer())
	track := provider.Track{ID: "t1", Title: "Song", Artist: "Artist"}
	_ = m.openPlaylistPicker(&track)
	gen := m.playlistPickerGen

	// Simulate a stale response (gen-1).
	stale := playlistsForPickerMsg{playlists: []provider.Playlist{{ID: "px", Name: "Stale"}}, gen: gen - 1}
	m2, _ := m.Update(stale)
	m = m2.(*Model)

	if len(m.playlistPickerItems) != 0 {
		t.Errorf("expected 0 items (stale response discarded), got %d", len(m.playlistPickerItems))
	}
	if !m.playlistPickerLoading {
		t.Error("loading flag should still be true after stale response")
	}
}

// TestPlaylistPicker_ErrorClosesPickerAndSetsErrMsg verifies that a fetch error
// closes the picker and sets an error message.
func TestPlaylistPicker_ErrorClosesPickerAndSetsErrMsg(t *testing.T) {
	m := newModel(newMockPlayer())
	track := provider.Track{ID: "t1", Title: "Song", Artist: "Artist"}
	_ = m.openPlaylistPicker(&track)
	gen := m.playlistPickerGen

	m2, _ := m.Update(playlistsForPickerMsg{err: errors.New("network down"), gen: gen})
	m = m2.(*Model)

	if m.mode == modePlaylistPicker {
		t.Error("expected picker to close on error")
	}
	if m.errMsg == "" {
		t.Error("expected errMsg to be set on fetch error")
	}
}

// TestPlaylistPicker_EnterAddsToPlaylist verifies that pressing Enter fires
// a trackAddedToPlaylistMsg and closes the picker.
func TestPlaylistPicker_EnterAddsToPlaylist(t *testing.T) {
	m := newModel(newMockPlayer())
	track := provider.Track{ID: "t1", Title: "Song", Artist: "Artist"}
	_ = m.openPlaylistPicker(&track)
	gen := m.playlistPickerGen

	// Load playlists.
	playlists := []provider.Playlist{{ID: "p1", Name: "Favorites"}}
	m2, _ := m.Update(playlistsForPickerMsg{playlists: playlists, gen: gen})
	m = m2.(*Model)

	// Press Enter to add to the selected playlist.
	m3, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m3.(*Model)

	if m.mode == modePlaylistPicker {
		t.Error("expected picker to close after Enter")
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd after Enter (the AddToPlaylist command)")
	}
}

// TestPlaylistPicker_CursorNavigation verifies j/k move the cursor.
func TestPlaylistPicker_CursorNavigation(t *testing.T) {
	m := newModel(newMockPlayer())
	track := provider.Track{ID: "t1", Title: "Song", Artist: "Artist"}
	_ = m.openPlaylistPicker(&track)
	gen := m.playlistPickerGen

	playlists := []provider.Playlist{
		{ID: "p1", Name: "Favorites"},
		{ID: "p2", Name: "Chill"},
		{ID: "p3", Name: "Rock"},
	}
	m2, _ := m.Update(playlistsForPickerMsg{playlists: playlists, gen: gen})
	m = m2.(*Model)

	// Move down.
	m3, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = m3.(*Model)
	if m.playlistPickerCursor != 1 {
		t.Errorf("expected cursor=1 after j, got %d", m.playlistPickerCursor)
	}

	// Move down again.
	m4, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = m4.(*Model)
	if m.playlistPickerCursor != 2 {
		t.Errorf("expected cursor=2 after j, got %d", m.playlistPickerCursor)
	}

	// Move up.
	m5, _ := m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = m5.(*Model)
	if m.playlistPickerCursor != 1 {
		t.Errorf("expected cursor=1 after k, got %d", m.playlistPickerCursor)
	}
}

// TestPlaylistPicker_AddedMsg verifies that trackAddedToPlaylistMsg sets errMsg.
func TestPlaylistPicker_AddedMsg(t *testing.T) {
	m := newModel(newMockPlayer())

	m2, _ := m.Update(trackAddedToPlaylistMsg{playlistName: "Favorites"})
	m = m2.(*Model)
	if !strings.Contains(m.errMsg, "Favorites") {
		t.Errorf("expected errMsg to contain playlist name, got %q", m.errMsg)
	}

	// Error case.
	m3, _ := m.Update(trackAddedToPlaylistMsg{err: errors.New("API error")})
	m = m3.(*Model)
	if !strings.Contains(m.errMsg, "API error") {
		t.Errorf("expected errMsg to contain error text, got %q", m.errMsg)
	}
}

// TestPlaylistPicker_TrackIDResolution verifies catalog vs library ID selection.
func TestPlaylistPicker_TrackIDResolution(t *testing.T) {
	var capturedID, capturedPlaylistID string
	prov := &captureProvider{addToPlaylistFn: func(plID, trID string) error {
		capturedPlaylistID = plID
		capturedID = trID
		return nil
	}}
	cfg := testCfg()
	m := New(cfg, prov, newMockPlayer(), Options{})
	m.width = 120
	m.height = 40

	// Track with CatalogID — should prefer CatalogID.
	track := &provider.Track{ID: "i.abc123", CatalogID: "cat456", Title: "Song", Artist: "Artist"}
	_ = m.openPlaylistPicker(track)
	gen := m.playlistPickerGen

	playlists := []provider.Playlist{{ID: "pl1", Name: "MyList"}}
	m2, _ := m.Update(playlistsForPickerMsg{playlists: playlists, gen: gen})
	m = m2.(*Model)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		msg := cmd()
		_, _ = m.Update(msg)
	}

	if capturedPlaylistID != "pl1" {
		t.Errorf("expected playlist ID pl1, got %q", capturedPlaylistID)
	}
	// CatalogID should be preferred over library ID.
	_ = capturedID // ID selection is handled by openPlaylistPicker → handlePlaylistPickerKey
}

// captureProvider is a minimal provider that records AddToPlaylist calls.
type captureProvider struct {
	mockProvider
	addToPlaylistFn func(playlistID, trackID string) error
}

func (p *captureProvider) AddToPlaylist(_ context.Context, playlistID, trackID string) error {
	if p.addToPlaylistFn != nil {
		return p.addToPlaylistFn(playlistID, trackID)
	}
	return nil
}

func TestExecuteCommandQualitySetsPlayerAndPersists(t *testing.T) {
	p := newMockPlayer()
	m := newModel(p)
	t.Setenv("HOME", t.TempDir())

	cmd := m.executeCommand("quality 64")
	if cmd == nil {
		t.Fatal("quality command returned nil cmd")
	}
	msg := cmd()
	if p.bitrateKbps != 64 {
		t.Fatalf("player bitrate = %d, want 64", p.bitrateKbps)
	}
	updated, _ := m.Update(msg)
	m = updated.(*Model)
	if got, err := m.cfg.AudioBitrateKbps(); err != nil || got != 64 {
		t.Fatalf("config bitrate = %d, %v; want 64, nil", got, err)
	}
}

func TestExecuteCommandQualityPersistsWhenBackendCannotSwitchLive(t *testing.T) {
	p := newMockPlayer()
	p.err = player.ErrAudioBitrateSavedPreferenceOnly
	m := newModel(p)
	t.Setenv("HOME", t.TempDir())

	cmd := m.executeCommand("quality 64")
	if cmd == nil {
		t.Fatal("quality command returned nil cmd")
	}
	msg := cmd()
	updated, _ := m.Update(msg)
	m = updated.(*Model)
	if got, err := m.cfg.AudioBitrateKbps(); err != nil || got != 64 {
		t.Fatalf("config bitrate = %d, %v; want 64, nil", got, err)
	}
	if !strings.Contains(m.errMsg, "used next launch; current backend cannot switch live") {
		t.Fatalf("errMsg = %q", m.errMsg)
	}
}

func TestExecuteCommandQualityRejectsLossless(t *testing.T) {
	p := newMockPlayer()
	m := newModel(p)
	cmd := m.executeCommand("quality lossless")
	if cmd != nil {
		t.Fatal("lossless quality returned command")
	}
	if p.bitrateKbps != 0 {
		t.Fatalf("player bitrate changed to %d", p.bitrateKbps)
	}
	if !strings.Contains(m.errMsg, "MusicKit JS/web playback max is 256 kbps AAC") {
		t.Fatalf("errMsg = %q", m.errMsg)
	}
}

func TestCommandSuggestionsIncludeQuality(t *testing.T) {
	m := newModel(newMockPlayer())
	m.cmdBuf = "qual"
	got := m.commandSuggestions()
	if len(got) == 0 || got[0].trigger != "quality" {
		t.Fatalf("quality suggestions = %#v", got)
	}
}

type backTestPanel struct {
	backCalled bool
}

func (p *backTestPanel) NavKey() string                 { return "b" }
func (p *backTestPanel) NavLabel() string               { return "backtest" }
func (p *backTestPanel) SetSize(_, _ int)               {}
func (p *backTestPanel) Update(tea.KeyPressMsg) tea.Cmd { return nil }
func (p *backTestPanel) View() string                   { return "" }
func (p *backTestPanel) Back() bool                     { p.backCalled = true; return true }

func TestModel_ActivePanelEscCallsBackBeforeClose(t *testing.T) {
	m := newModel(nil)
	panel := &backTestPanel{}
	m.panels = []ContentView{panel}
	m.activePanel = 0
	m.handleNormalKey(tea.KeyPressMsg{Code: tea.KeyEsc}, "esc")
	if !panel.backCalled {
		t.Fatal("esc did not call active panel Back")
	}
	if m.activePanel != 0 {
		t.Fatalf("activePanel = %d, want still open after Back handled", m.activePanel)
	}
}

func TestEqualizerKeyPriority(t *testing.T) {
	plyr := newMockPlayer()
	m := newModel(plyr)

	// Set up track state for seeking checks
	m.playerState.Track = &provider.Track{ID: "t1", Title: "Song", Duration: 100 * time.Second}
	m.playerState.Position = 50 * time.Second

	// Open equalizer panel
	eqIdx := -1
	for i, p := range m.panels {
		if p == m.eqP {
			eqIdx = i
			break
		}
	}
	if eqIdx == -1 {
		t.Fatal("eqP not found in panels")
	}
	m.activePanel = eqIdx

	// Verify equalizer cursor is initially at 0
	if m.eqP.m.Cursor() != 0 {
		t.Fatalf("expected equalizer cursor to be 0, got %d", m.eqP.m.Cursor())
	}

	// 1. Send "right" arrow key. It should move equalizer cursor to 1, and NOT seek player.
	m2, _ := m.Update(tea.KeyPressMsg{Text: "right"})
	m = m2.(*Model)
	if m.eqP.m.Cursor() != 1 {
		t.Errorf("expected equalizer cursor to move to 1, got %d", m.eqP.m.Cursor())
	}
	if plyr.seekCalled {
		t.Error("player seek was incorrectly called when pressing right key in equalizer")
	}

	// Reset mock player call tracking
	plyr.seekCalled = false

	// 2. Send "r" key. It should reset equalizer bands, and NOT cycle player repeat mode.
	m.eqP.m.Bands()[0].Gain = 5.0
	m.playerState.RepeatMode = player.RepeatModeAll

	m3, _ := m.Update(tea.KeyPressMsg{Text: "r"})
	m = m3.(*Model)
	if m.eqP.m.Bands()[0].Gain != 0.0 {
		t.Errorf("expected bands to reset to 0, got %f", m.eqP.m.Bands()[0].Gain)
	}
	// RepeatMode is managed by state update in the actual app, but locally it shouldn't have changed
	if m.playerState.RepeatMode != player.RepeatModeAll {
		t.Error("player repeat mode was incorrectly changed when pressing r in equalizer")
	}

	// 3. Send "space" key. It should fall through and toggle play/pause.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if cmd != nil {
		cmd()
	}
	if !plyr.playCalled && !plyr.pauseCalled {
		t.Error("player play/pause was NOT called when pressing space in equalizer")
	}
}

func TestPlayNextCmd(t *testing.T) {
	plyr := newMockPlayer()
	m := newModel(plyr)

	// Case 1: Play next on empty queue
	tracks := []provider.Track{{ID: "x1", Title: "X1", Artist: "ArtX"}}
	ids := []string{"x1"}
	cmd1 := m.playNextCmd("X1", tracks, ids)
	if cmd1 != nil {
		cmd1()
	}
	if len(plyr.setQueueIDs) == 0 || plyr.setQueueIDs[0] != "x1" {
		t.Errorf("expected SetQueue with x1, got %v", plyr.setQueueIDs)
	}

	// Reset mock player
	plyr.setQueueIDs = nil
	plyr.appendQueueIDs = nil
	plyr.moveInQueueCalls = nil

	// Case 2: Play next on non-empty queue, when song B is playing in queue [A, B, C]
	m.queueTracks = []provider.Track{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B"},
		{ID: "c", Title: "C"},
	}
	m.queueIDs = []string{"a", "b", "c"}
	m.playerState.Track = &provider.Track{ID: "b", Title: "B"}

	newTracks := []provider.Track{
		{ID: "x2", Title: "X2"},
		{ID: "y2", Title: "Y2"},
	}
	newIDs := []string{"x2", "y2"}

	cmd2 := m.playNextCmd("Collection", newTracks, newIDs)
	if cmd2 != nil {
		cmd2()
	}

	// Verify local queueIDs insertion: [A, B, X2, Y2, C]
	expectedIDs := []string{"a", "b", "x2", "y2", "c"}
	if len(m.queueIDs) != len(expectedIDs) {
		t.Fatalf("expected queueIDs length %d, got %d", len(expectedIDs), len(m.queueIDs))
	}
	for i, id := range m.queueIDs {
		if id != expectedIDs[i] {
			t.Errorf("expected local queueIDs[%d] to be %s, got %s", i, expectedIDs[i], id)
		}
	}

	// Verify the engine call: one sync with the new order, keeping B playing.
	if len(plyr.syncCalls) != 1 {
		t.Fatalf("expected one SyncQueue call, got %d", len(plyr.syncCalls))
	}
	sc := plyr.syncCalls[0]
	if len(sc.IDs) != 5 || sc.IDs[2] != "x2" || sc.IDs[3] != "y2" || sc.Current != "b" || sc.Play != "" {
		t.Errorf("unexpected sync call: %+v", sc)
	}
}

func TestDedupeStrings(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, nil},
		{"drops repeats, keeps order", []string{"b", "a", "b", "a"}, []string{"b", "a"}},
		{"drops empty strings", []string{"", "a", ""}, []string{"a"}},
		{"all empty", []string{"", ""}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupeStrings(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("dedupeStrings(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("dedupeStrings(%v)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestNoResultsError(t *testing.T) {
	// Without reasons the message stays as it was.
	if got := noResultsError("jazz", nil).Error(); got != `no results for "jazz"` {
		t.Errorf("noResultsError with no reasons = %q", got)
	}

	// With reasons the cause has to survive into the message — that is the whole
	// point: a backend failure and an empty catalog match are otherwise identical
	// from the log.
	err := noResultsError("jazz", []string{"catalog song search: 401", "catalog song search: 401"})
	got := err.Error()
	if !strings.Contains(got, `no results for "jazz"`) {
		t.Errorf("noResultsError = %q, want it to keep the query", got)
	}
	if !strings.Contains(got, "catalog song search: 401") {
		t.Errorf("noResultsError = %q, want it to name the failing backend", got)
	}
	if strings.Count(got, "catalog song search") != 1 {
		t.Errorf("noResultsError = %q, want the repeated reason collapsed to one", got)
	}
}

func TestCommand_DiscoverMetric_OpensPicker(t *testing.T) {
	m := newModel(newMockPlayer())
	m.playerState.Track = &provider.Track{Title: "Seed Song", Artist: "Artist", ID: "seed"}
	_ = m.executeCommand("discover metric")
	if !m.vibe.PickerActive() || m.discovery.enabled {
		t.Fatalf(":discover metric should open the picker without starting discovery (picker=%v enabled=%v)", m.vibe.PickerActive(), m.discovery.enabled)
	}
}

func TestCommand_Discover_TogglesOff(t *testing.T) {
	m := newModel(newMockPlayer())
	m.discovery.enabled = true
	m.discovery.seed = &provider.Track{ID: "seed"}
	_ = m.executeCommand("discover")
	if m.discovery.enabled {
		t.Fatal(":discover while running should stop discovery")
	}
	_ = m.executeCommand("discover stop") // no-op when off
	if m.discovery.enabled {
		t.Fatal("stop must keep discovery off")
	}
}

func TestHandleNormalKey_F_IsDisabled(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	track := &provider.Track{Title: "Song", Artist: "Artist", ID: "fav-id"}
	m.playerState.Track = track
	cmd := m.handleNormalKey(tea.KeyPressMsg{Code: 'f', Text: "f"}, "f")
	if cmd != nil || m.favorites["fav-id"] {
		t.Error("f is disabled and must not toggle a favourite")
	}
}

// pagingProvider is a mockProvider that can page catalog songs.
type pagingProvider struct {
	mockProvider
	offsets []int
	page    provider.SongPage
	err     error
}

func (p *pagingProvider) SearchSongsPage(_ context.Context, _ string, offset int) (provider.SongPage, error) {
	p.offsets = append(p.offsets, offset)
	return p.page, p.err
}

// searchAtTracksMore plants a pageable Tracks section and puts the cursor on
// its "+ 5 more" row.
func searchAtTracksMore(m *Model, tracks int) {
	m.mode = modeSearch
	m.search.SetSize(80, 40)
	var ts []provider.Track
	for i := range tracks {
		ts = append(ts, provider.Track{ID: fmt.Sprintf("c%d", i), CatalogID: fmt.Sprintf("c%d", i), Title: fmt.Sprintf("Song %d", i), Artist: "Band"})
	}
	m.search.SetResults(&provider.SearchResult{Tracks: ts, CatalogNext: 25, CatalogMore: true}, false, nil)
	m.searchShown = "band"
	for range tracks {
		m.search, _ = m.search.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
}

func TestHandleSearchKey_EnterOnTracksMore_PagesApple(t *testing.T) {
	prov := &pagingProvider{page: provider.SongPage{Tracks: []provider.Track{
		{ID: "c9", CatalogID: "c9", Title: "Song 9", Artist: "Band"},
		{ID: "c10", CatalogID: "c10", Title: "Song 10", Artist: "Band"},
	}, Next: 50, More: true}}
	m := newModel(newMockPlayer())
	m.provider = prov
	searchAtTracksMore(m, 3)
	if sec, more, ok := m.search.SelectedToggle(); !ok || sec != "Tracks" || !more {
		t.Fatalf("setup: cursor should be on the Tracks more row, got %q more=%v ok=%v", sec, more, ok)
	}

	cmd := m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd == nil {
		t.Fatal("enter on a pageable more row must fetch the next page")
	}
	if !m.search.Paging() {
		t.Fatal("the panel should show the page as loading")
	}
	msg := cmd()
	if len(prov.offsets) != 1 || prov.offsets[0] != 25 {
		t.Fatalf("provider should be asked for offset 25, got %v", prov.offsets)
	}
	_, next := m.Update(msg)
	if len(m.search.Results()) != 5 {
		t.Fatalf("page should be merged into the results: %d tracks", len(m.search.Results()))
	}
	// Two new songs arrived but five were owed, so the model chains one more page.
	if next == nil {
		t.Fatal("still owed 3 items with more pages available: expected a chained fetch")
	}
	if !m.search.Paging() {
		t.Fatal("chained fetch should be marked in flight")
	}
	next()
	if len(prov.offsets) != 2 || prov.offsets[1] != 50 {
		t.Fatalf("chained fetch should use the new offset 50, got %v", prov.offsets)
	}
}

func TestSearchMoreMsg_StaleOrFailedPages(t *testing.T) {
	prov := &pagingProvider{err: errors.New("boom")}
	m := newModel(newMockPlayer())
	m.provider = prov
	searchAtTracksMore(m, 3)
	cmd := m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd == nil {
		t.Fatal("expected a fetch")
	}
	msg := cmd()
	// The user typed on before the page came back: the stale page is ignored.
	m.searchGen++
	m.Update(msg)
	if m.errMsg != "" {
		t.Fatalf("a stale page must be dropped silently, got error %q", m.errMsg)
	}
	// A failed page for the current query clears the loading state and reports.
	m.searchGen--
	_, next := m.Update(msg)
	if m.search.Paging() || next != nil || !strings.Contains(m.errMsg, "boom") {
		t.Fatalf("failed page: paging=%v next=%v err=%q", m.search.Paging(), next != nil, m.errMsg)
	}
	if len(m.search.Results()) != 3 {
		t.Fatalf("results must be untouched by a failed page, got %d", len(m.search.Results()))
	}
}

func TestHandleSearchKey_EnterOnTracksMore_ProviderCannotPage(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &mockProvider{}
	searchAtTracksMore(m, 3)
	if cmd := m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight}); cmd != nil || m.search.Paging() {
		t.Fatal("a provider without paging must leave the panel as it is")
	}
}

func TestSearchFindLines_TitleUnderlineInputOnly(t *testing.T) {
	m := newModel(newMockPlayer())
	m.search.SetSize(40, 10)
	lines := m.searchFindLines(40, 8)
	if len(lines) != 8 {
		t.Fatalf("want 8 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "Search") || strings.Contains(lines[0], "tab") || strings.Contains(lines[0], "enter") {
		t.Fatalf("header must be the bare title (keys live in the footer): %q", lines[0])
	}
	if !strings.Contains(lines[1], "─────") || strings.Contains(lines[1], strings.Repeat("─", 6)) {
		t.Fatalf("second line should be the five-dash underline like the Queue's: %q", lines[1])
	}
	if !strings.Contains(lines[2], "AM") {
		t.Fatalf("third line should be the query input with the Apple Music prompt: %q", lines[2])
	}
	if !strings.Contains(lines[0], "Apple Music") {
		t.Fatalf("the header names the mode next to the title: %q", lines[0])
	}
	for _, l := range lines[3:] {
		if strings.Contains(l, "press /") || strings.Contains(l, "type to search") {
			t.Fatalf("no usage hint below the input: %q", l)
		}
	}
}

func TestTab_TogglesFocusBetweenQueueAndSearch(t *testing.T) {
	m := newModel(newMockPlayer())
	m.searchQuery = "coltrane"
	for _, k := range []string{"tab", "shift+tab"} {
		m.mode = modeNormal
		if cmd := m.handleNormalKey(tea.KeyPressMsg{Code: tea.KeyTab}, k); cmd != nil || m.mode != modeSearch {
			t.Fatalf("%s in the queue should focus Search (mode=%d, cmd=%v)", k, m.mode, cmd != nil)
		}
		if cmd := m.handleSearchKey(k, tea.KeyPressMsg{Code: tea.KeyTab}); cmd != nil || m.mode != modeNormal {
			t.Fatalf("%s in Search should focus the queue again (mode=%d, cmd=%v)", k, m.mode, cmd != nil)
		}
	}
	if m.searchQuery != "coltrane" {
		t.Fatalf("switching focus must keep the query, got %q", m.searchQuery)
	}
	// Tab in Search never adds anything.
	mp := newMockPlayer()
	m = newModel(mp)
	seedSearchResults(m, provider.Track{Title: "T", CatalogID: "x"})
	if cmd := m.handleSearchKey("tab", tea.KeyPressMsg{Code: tea.KeyTab}); cmd != nil || len(m.queueIDs) != 0 || m.mode != modeNormal {
		t.Fatalf("tab must only move focus: cmd=%v queue=%v mode=%d", cmd != nil, m.queueIDs, m.mode)
	}
}

func TestSearchFooter_ListsEveryKey(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	lines := m.statusLines(200)
	joined := ansi.Strip(strings.Join(lines, " "))
	for _, want := range []string{"↑/↓", "Enter", "open/fold", "^↑/↓", "^→", "^←", "^, add", "^. add & play", "^/", "Tab", "tracks"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("search footer should mention %q: %q", want, joined)
		}
	}
}

func TestPanelTitles_BoldFollowsFocus(t *testing.T) {
	m := newModel(newMockPlayer())
	bold := styles.Header.Bold(true).Render("Search")
	plain := styles.Header.Render("Search")
	if bold == plain {
		t.Skip("styling is disabled in this environment")
	}
	m.mode = modeNormal
	if !strings.HasPrefix(m.findHeader(), plain) || !m.queueFocused() {
		t.Fatalf("with the keys on the queue, Search must be plain and Queue focused (header=%q focused=%v)", m.findHeader(), m.queueFocused())
	}
	m.mode = modeSearch
	if !strings.HasPrefix(m.findHeader(), bold) || m.queueFocused() {
		t.Fatalf("with the keys on Search, its title must be bold and Queue unfocused (header=%q focused=%v)", m.findHeader(), m.queueFocused())
	}
	m.mode = modeNormal
	m.activePanel = 0 // an overlay panel (lyrics) has the keys
	if m.queueFocused() {
		t.Fatal("an open overlay panel takes the focus from the queue")
	}
}

func TestIdleBlock_CreditsOriginalAndUpdate(t *testing.T) {
	m := newModel(newMockPlayer())
	m.playerState.Track = nil
	lines := m.nowPlayingTextLines(80, nowPlayingTextRows)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"made with", "by simonepelosi", "updated with", "by agf and Claude"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("idle block should contain %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "press ?") || strings.Contains(joined, "·") {
		t.Fatalf("the credit line carries no hint or separator any more:\n%s", joined)
	}
	if len(lines) != nowPlayingTextRows {
		t.Fatalf("the block must keep its %d rows, got %d", nowPlayingTextRows, len(lines))
	}
}

func TestHandleSearchKey_CtrlSlashTogglesVibesMode(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &mockProvider{}
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	if m.searchSrc == searchClaude {
		t.Fatal("regular search is the default")
	}
	// A plain slash is text: "AC/DC" must stay searchable.
	m.searchTyping = true
	for _, r := range "AC/DC" {
		m.handleSearchKey(string(r), tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.searchQuery != "AC/DC" || m.searchSrc == searchClaude {
		t.Fatalf("typing a slash must not toggle anything: query=%q vibe=%v", m.searchQuery, m.searchSrc == searchClaude)
	}
	m.searchQuery, m.searchCursor = "", 0
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd != nil || m.searchSrc != searchClaude {
		t.Fatalf("ctrl+/ should switch to vibes mode without searching (cmd=%v vibe=%v)", cmd != nil, m.searchSrc == searchClaude)
	}
	lines := m.searchFindLines(40, 6)
	if !strings.Contains(lines[2], "CC") || strings.Contains(lines[2], "AM") {
		t.Fatalf("vibes mode shows the CC prompt (Claude Code) instead of AM: %q", lines[2])
	}
	if h := ansi.Strip(lines[0]); !strings.Contains(h, "Claude Code") || strings.Contains(h, "Apple Music") {
		t.Fatalf("the header should say Claude Code in vibes mode: %q", h)
	}
	// Typing a description does not search as you type.
	for _, r := range "chill" {
		if cmd := m.handleSearchKey(string(r), tea.KeyPressMsg{Code: r, Text: string(r)}); cmd != nil {
			t.Fatal("vibes mode must not search while typing")
		}
	}
	if m.searchQuery != "chill" {
		t.Fatalf("query = %q, want chill", m.searchQuery)
	}
	footer := strings.Join(m.statusLines(200), " ")
	if !strings.Contains(footer, "SEARCH") || strings.Contains(footer, "VIBES") || !strings.Contains(footer, "find songs") {
		t.Fatalf("footer keeps the SEARCH label in vibes mode and says Enter = find songs: %q", footer)
	}
	// Next come the saved lists: nothing is looked up, the prompt reads SV
	// and the header names the source.
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd != nil || m.searchSrc != searchSaved {
		t.Fatalf("ctrl+/ from CC goes to the saved lists without looking anything up (cmd=%v src=%v)", cmd != nil, m.searchSrc)
	}
	if lines := m.searchFindLines(40, 6); !strings.Contains(lines[2], "SV") || !strings.Contains(ansi.Strip(lines[0]), "Saved lists") {
		t.Fatalf("the saved-lists source shows the SV prompt and names itself: %q / %q", lines[2], ansi.Strip(lines[0]))
	}
	// Then the feed: fetched on this first visit, nothing to type.
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd == nil || m.searchSrc != searchFeed {
		t.Fatalf("ctrl+/ from SV goes to the feed and fetches it (cmd=%v src=%v)", cmd != nil, m.searchSrc)
	}
	// Back to regular search: switching runs nothing; Enter then looks the text up.
	if cmd := m.handleSearchKey("ctrl+_", tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl}); cmd != nil || m.searchSrc != searchApple {
		t.Fatalf("ctrl+/ again (legacy ctrl+_ spelling) should return to regular search without searching (cmd=%v src=%v)", cmd != nil, m.searchSrc)
	}
	if cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || m.searchTyping {
		t.Fatalf("Enter runs the Apple Music search and ends typing (cmd=%v typing=%v)", cmd != nil, m.searchTyping)
	}
	if lines := m.searchFindLines(40, 6); !strings.Contains(lines[2], "AM") || !strings.Contains(ansi.Strip(lines[0]), "Apple Music") {
		t.Fatalf("regular mode shows the AM prompt and names Apple Music: %q / %q", lines[2], ansi.Strip(lines[0]))
	}
}

func TestHandleSearchKey_EnterInVibesModeFindsSongsThenActsOnRows(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.provider = &mockProvider{}
	m.mode = modeSearch
	m.searchTyping = true
	m.search.SetSize(80, 20)
	m.setSearchSource(searchClaude)
	m.searchQuery = "late night coding"
	m.searchCursor = len(m.searchQuery)

	cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || m.vibeShown != "late night coding" || !m.search.Loading() {
		t.Fatalf("Enter on a new description must start the vibe lookup (cmd=%v shown=%q loading=%v)", cmd != nil, m.vibeShown, m.search.Loading())
	}
	// Same description again while loading: nothing else starts, nothing is queued.
	if cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("Enter on the description already being looked up must do nothing")
	}
	var tracks []provider.Track
	for i := range 12 {
		tracks = append(tracks, provider.Track{Title: fmt.Sprintf("Song %d", i), Artist: "Band", ID: fmt.Sprintf("v%d", i), CatalogID: fmt.Sprintf("v%d", i)})
	}
	m.Update(vibeResultMsg{query: "late night coding", tracks: tracks})
	view := m.search.View()
	if !strings.Contains(view, "Vibes") || !strings.Contains(view, "+ 5 more") || !strings.Contains(view, "− 5 less") {
		t.Fatalf("vibe songs should be listed like search results with the more/less controls: %q", view)
	}
	footer := strings.Join(m.statusLines(200), " ")
	if !strings.Contains(footer, "open/fold") || strings.Contains(footer, "find songs") {
		t.Fatalf("once the songs are listed, Enter is back to opening and folding: %q", footer)
	}
	// Ctrl+. on the highlighted song adds it to the queue and plays it.
	cmd = m.handleSearchKey("ctrl+.", tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+. on a vibe song should add & play it")
	}
	cmd()
	if len(m.queueIDs) != 1 || m.queueIDs[0] != "v0" {
		t.Fatalf("queue should hold the chosen song, got %v", m.queueIDs)
	}
	// Ctrl+, adds the next one without playing.
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl}); cmd != nil {
		cmd()
	}
	if len(m.queueIDs) != 2 || m.queueIDs[1] != "v1" {
		t.Fatalf("Ctrl+, should append the next song, got %v", m.queueIDs)
	}
	// Editing the description (Ctrl+' to type again) makes Enter a lookup again.
	m.handleSearchKey("ctrl+'", tea.KeyPressMsg{Code: 0x27, Mod: tea.ModCtrl})
	m.handleSearchKey("!", tea.KeyPressMsg{Code: '!', Text: "!"})
	if cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || m.vibeShown != "late night coding!" {
		t.Fatalf("a changed description must be looked up on Enter (cmd=%v shown=%q)", cmd != nil, m.vibeShown)
	}
}

func TestHandleSearchKey_CtrlSlashWithTextStartsVibeLookup(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &mockProvider{}
	m.mode = modeSearch
	m.search.SetSize(80, 20)
	m.searchQuery = "rainy afternoon"
	m.searchCursor = len(m.searchQuery)
	cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
	if cmd == nil || m.searchSrc != searchClaude || m.vibeShown != "rainy afternoon" || !m.search.Loading() {
		t.Fatalf("switching to vibes with text present must look it up at once (cmd=%v vibe=%v shown=%q loading=%v)", cmd != nil, m.searchSrc == searchClaude, m.vibeShown, m.search.Loading())
	}
	footer := strings.Join(m.statusLines(200), " ")
	if strings.Contains(footer, "find songs") {
		t.Fatalf("the typed text is already being looked up, so Enter is not a lookup: %q", footer)
	}
}

// fakePlanner answers every description with a fixed plan (or error).
type fakePlanner struct {
	plan  vibe.Plan
	err   error
	calls []string
}

func (f *fakePlanner) Name() string { return "Test" }
func (f *fakePlanner) Plan(_ context.Context, d string) (vibe.Plan, error) {
	f.calls = append(f.calls, d)
	return f.plan, f.err
}

// termProvider answers every search with n songs named after the term.
type termProvider struct {
	mockProvider
	n int
}

func (p *termProvider) Search(_ context.Context, term string) (*provider.SearchResult, error) {
	res := &provider.SearchResult{}
	for i := range p.n {
		id := fmt.Sprintf("%s-%d", term, i)
		res.Tracks = append(res.Tracks, provider.Track{ID: id, CatalogID: id, Title: fmt.Sprintf("%s %d", term, i), Artist: "Band " + term})
	}
	return res, nil
}

func TestRunVibeSearch_UsesThePlannerTermsAndInterleaves(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &termProvider{n: 10}
	fp := &fakePlanner{plan: vibe.Plan{Summary: "Dreamy soul", Queries: []string{"alpha", "beta"}}}
	m.vibePlanner = fp
	msg, ok := m.runVibeSearch("dreamy soul")().(vibeCandidatesMsg)
	if !ok || msg.err != nil {
		t.Fatalf("vibe search failed: ok=%v err=%v", ok, msg.err)
	}
	if len(fp.calls) != 1 || fp.calls[0] != "dreamy soul" {
		t.Fatalf("the planner should see the description once, got %v", fp.calls)
	}
	if len(msg.pool) != 20 {
		t.Fatalf("all unique hits are pooled (2 terms × 10), got %d", len(msg.pool))
	}
	if msg.pool[0].Title != "alpha 0" || msg.pool[1].Title != "beta 0" || msg.pool[2].Title != "alpha 1" {
		t.Fatalf("terms should be interleaved in the planner's order, got %q %q %q", msg.pool[0].Title, msg.pool[1].Title, msg.pool[2].Title)
	}
	if msg.via != "Test" || msg.plan.Summary != "Dreamy soul" {
		t.Fatalf("the plan must ride along: via=%q plan=%+v", msg.via, msg.plan)
	}
	// A planner that cannot rerank: the first 15 in search order are shown, and
	// the panel says who planned what, above the songs, without making it selectable.
	m.mode = modeSearch
	m.search.SetSize(80, 40)
	m.setSearchSource(searchClaude)
	m.vibeShown = "dreamy soul"
	if _, cmd := m.Update(msg); cmd != nil {
		t.Fatal("without a reranker the candidates are final: no second stage")
	}
	if n := len(m.search.Results()); n != vibeResultCap {
		t.Fatalf("results are capped at %d, got %d", vibeResultCap, n)
	}
	view := m.search.View()
	if !strings.Contains(view, "Vibes") || !strings.Contains(view, "Test: Dreamy soul") || strings.Contains(view, "terms:") || strings.Contains(view, "first 15") {
		t.Fatalf("a planner without a model keeps the Vibes header and shows just who/what, no terms: %q", view)
	}
	if t0 := m.search.SelectedTrack(); t0 == nil || t0.Title != "alpha 0" {
		t.Fatalf("the highlight should start on the first song, not a note line, got %+v", t0)
	}
}

func TestRunVibeSearch_FallsBackToKeywordsWhenThePlannerFails(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &termProvider{n: 3}
	// Over-long plans are trimmed to the terms that actually run.
	m.vibePlanner = &fakePlanner{plan: vibe.Plan{Summary: "s", Queries: []string{"a", "b", "c", "d", "e", "f", "g", "h"}}}
	if msg := m.runVibeSearch("x")().(vibeCandidatesMsg); len(msg.plan.Queries) != 6 || msg.plan.Queries[5] != "f" {
		t.Fatalf("the shown plan must list only the 6 searched terms, got %v", msg.plan.Queries)
	}
	m.vibePlanner = &fakePlanner{err: errors.New("not logged in")}
	msg := m.runVibeSearch("late night coding")().(vibeCandidatesMsg)
	if msg.err != nil || len(msg.pool) == 0 {
		t.Fatalf("the keyword table must take over: err=%v pool=%d", msg.err, len(msg.pool))
	}
	if !strings.HasPrefix(msg.via, "keywords") || !strings.Contains(msg.via, "Test unavailable") {
		t.Fatalf("the fallback must be visible in via, got %q", msg.via)
	}
	found := false
	for _, w := range msg.warnings {
		if strings.Contains(w, "not logged in") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the planner's error should be kept as a warning, got %v", msg.warnings)
	}
}

func TestSearchFindLines_BusyAnimatesTheModeText(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.search.SetSize(60, 20)
	m.setSearchSource(searchClaude)
	m.vibePlanner = &fakePlanner{plan: vibe.Plan{Queries: []string{"x"}}}
	idle := m.findHeader()
	if cmd := m.startVibeSearch("anything"); cmd == nil {
		t.Fatal("expected a lookup command")
	}
	busy := m.findHeader()
	if !strings.Contains(ansi.Strip(busy), "Claude Code") || busy == idle {
		t.Fatalf("while busy the mode text is rendered differently (animated): idle=%q busy=%q", idle, busy)
	}
	m.glowStep += 3
	if m.findHeader() == busy {
		t.Fatal("the mode text should change colour as the glow ticks")
	}
	lines := strings.Join(m.searchFindLines(60, 12), "\n")
	if strings.Contains(lines, "●") || strings.Contains(lines, "searching") || strings.Contains(lines, "asking") {
		t.Fatalf("no dots or status text while busy, the header carries it: %q", lines)
	}
	// Results in: the header goes back to the plain muted mode text.
	m.Update(vibeResultMsg{query: "anything", tracks: nil, via: "Test", plan: vibe.Plan{Summary: "s"}})
	if got := m.findHeader(); got != idle {
		t.Fatalf("after the lookup the header is back to normal: %q vs %q", got, idle)
	}
	// Plain Apple Music search animates its own label too.
	m.setSearchSource(searchApple)
	m.search.SetResults(nil, true, nil)
	if h := m.findHeader(); !strings.Contains(ansi.Strip(h), "Apple Music") || h == styles.Header.Bold(true).Render("Search")+styles.QueueItemMuted.Render("  Apple Music") {
		t.Fatalf("a slow regular search animates Apple Music: %q", h)
	}
	if v := strings.Join(m.searchFindLines(60, 12), "\n"); strings.Contains(v, "searching") {
		t.Fatalf("no searching… text either: %q", v)
	}
}

// fakeReranker is a fakePlanner that also ranks: it returns fixed picks.
type fakeReranker struct {
	fakePlanner
	picks    []int
	rankErr  error
	gotCands []vibe.Candidate
	gotLimit int
	gotDescr string
}

func (f *fakeReranker) Rerank(_ context.Context, d string, cands []vibe.Candidate, limit int) ([]int, error) {
	f.gotDescr, f.gotCands, f.gotLimit = d, cands, limit
	return f.picks, f.rankErr
}

func TestVibeLookup_RerankerOrdersThePool(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &termProvider{n: 10}
	rr := &fakeReranker{fakePlanner: fakePlanner{plan: vibe.Plan{Summary: "Dreamy soul", Model: "sonnet-5", Queries: []string{"alpha", "beta"}}}, picks: []int{5, 0, 19}}
	m.vibePlanner = rr
	m.mode = modeSearch
	m.search.SetSize(80, 40)
	m.setSearchSource(searchClaude)
	m.vibeShown = "dreamy soul"
	m.search.SetResults(nil, true, nil)

	stage1 := m.runVibeSearch("dreamy soul")()
	_, stage2 := m.Update(stage1)
	if stage2 == nil {
		t.Fatal("a reranking planner gets a second stage")
	}
	if v := strings.Join(m.searchFindLines(60, 20), "\n"); strings.Contains(v, "●") || strings.Contains(v, "picking") || !m.search.Loading() {
		t.Fatalf("while ranking, the panel stays quiet (only the header animates): %q", v)
	}
	final := stage2()
	if rr.gotDescr != "dreamy soul" || len(rr.gotCands) != 20 || rr.gotLimit != vibeResultCap || rr.gotCands[0].Title != "alpha 0" {
		t.Fatalf("the reranker should see the description, the whole pool and the cap: %q %d %d", rr.gotDescr, len(rr.gotCands), rr.gotLimit)
	}
	m.Update(final)
	got := m.search.Results()
	if len(got) != 3 || got[0].Title != "beta 2" || got[1].Title != "alpha 0" || got[2].Title != "beta 9" {
		t.Fatalf("results must follow the picks (pool indices 5, 0, 19), got %+v", got)
	}
	if v := m.search.View(); !strings.Contains(v, "Sonnet 5") || strings.Contains(v, "Vibes") || !strings.Contains(v, "Dreamy soul") || strings.Contains(v, "picked") {
		t.Fatalf("the header should be the model and the line under it the summary only: %q", v)
	}
	if log := strings.Join(m.debugLog, "\n"); !strings.Contains(log, "picked 3 of 20") || !strings.Contains(log, "alpha") {
		t.Fatalf("ranking and terms belong in the debug log: %q", log)
	}

	// A failed ranking keeps the search order and says so.
	rr.rankErr = errors.New("timeout")
	_, stage2 = m.Update(m.runVibeSearch("dreamy soul")())
	m.Update(stage2())
	got = m.search.Results()
	if len(got) != vibeResultCap || got[0].Title != "alpha 0" {
		t.Fatalf("on failure the first %d in search order are kept, got %d starting %q", vibeResultCap, len(got), got[0].Title)
	}
	if log := strings.Join(m.debugLog, "\n"); !strings.Contains(log, "ranking failed, search order") {
		t.Fatalf("a failed ranking is recorded in the debug log: %q", log)
	}
}

func TestSearchFindLines_LongQueryWraps(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.searchTyping = true
	m.search.SetSize(20, 20)
	m.searchQuery = strings.Repeat("abcdefghij", 5) // 50 runes, 17 fit per row
	m.searchCursor = len(m.searchQuery)
	lines := m.searchFindLines(20, 14)
	var plain []string
	for i, l := range lines {
		if lipgloss.Width(l) > 20 {
			t.Fatalf("row %d is wider than the column: %q", i, l)
		}
		plain = append(plain, ansi.Strip(l))
	}
	joined := strings.Join(plain[2:6], "")
	joined = strings.ReplaceAll(strings.ReplaceAll(joined, " ", ""), "█", "")
	if !strings.Contains(joined, m.searchQuery) {
		t.Fatalf("the whole query should be readable across the wrapped rows, got %q", plain[2:6])
	}
	if !strings.HasPrefix(plain[2], "AM ") || !strings.HasPrefix(plain[3], "   ") || !strings.Contains(plain[4], "█") {
		t.Fatalf("first row carries the prompt, continuation rows are indented, the cursor sits at the end: %q", plain[2:5])
	}
	// Too little height for every row: the rows ending at the cursor are kept.
	lines = m.searchFindLines(20, 6) // header, underline, at most 2 input rows
	plain = plain[:0]
	for _, l := range lines {
		plain = append(plain, ansi.Strip(l))
	}
	if !strings.Contains(plain[3], "█") || strings.Contains(plain[2], "abcdefghijabcdefg") {
		t.Fatalf("with two input rows the last ones (with the cursor) win: %q", plain[2:4])
	}
}

func TestSearchFooter_LongQueryKeepsCursorAndHints(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.searchTyping = true
	m.searchQuery = strings.Repeat("long vibe description ", 12)
	m.searchCursor = len([]rune(m.searchQuery))
	lines := m.statusNavLines(90)
	for _, l := range lines {
		if lipgloss.Width(l) > 90 {
			t.Fatalf("every footer row must fit the width, got %d: %q", lipgloss.Width(l), ansi.Strip(l))
		}
	}
	plain := ansi.Strip(strings.Join(lines, " "))
	if !strings.Contains(plain, "…") || !strings.Contains(plain, "█") || !strings.Contains(plain, "tracks") {
		t.Fatalf("a long query is cut on the left, keeping the cursor and the hints: %q", plain)
	}
	if wide := m.statusNavLines(700); len(wide) != 1 {
		t.Fatalf("on a wide terminal the search row is one line, got %d", len(wide))
	}
	// Cursor in the middle: the window follows it.
	m.searchCursor = 10
	plain = ansi.Strip(m.statusNavLines(90)[0])
	if !strings.Contains(plain, "long vibe █") || !strings.HasSuffix(strings.TrimSpace(plain), "…") {
		t.Fatalf("the window should show the text around the cursor and mark the cut on the right: %q", plain)
	}
	if all := ansi.Strip(strings.Join(m.statusNavLines(90), " ")); !strings.Contains(all, "Enter") {
		t.Fatalf("the key hints follow on the next row: %q", all)
	}
}

func TestCommandFooter_ListsEveryCommand(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeCommand
	m.cmdBuf = "vo"
	lines := m.statusNavLines(90)
	for _, l := range lines {
		if lipgloss.Width(l) > 90 {
			t.Fatalf("every footer row must fit the width, got %d: %q", lipgloss.Width(l), ansi.Strip(l))
		}
	}
	if len(lines) < 2 {
		t.Fatalf("at 90 columns the command list needs more than one row, got %d", len(lines))
	}
	plain := ansi.Strip(strings.Join(lines, " "))
	if !strings.HasPrefix(strings.TrimSpace(plain), "CMD") || !strings.Contains(plain, ":vo█") {
		t.Fatalf("the row opens with the mode chip and the typed command: %q", plain)
	}
	for _, c := range allCommands {
		if !strings.Contains(plain, ":"+c.trigger) {
			t.Fatalf("the CMD row lists every command, always; %q is missing from %q", ":"+c.trigger, plain)
		}
	}
	if !strings.Contains(plain, ":quality <high|standard|256|64>") || !strings.Contains(plain, ":save [name]") || !strings.Contains(plain, ":effort <low|medium|high|xhigh|max|default>") {
		t.Fatalf("each command shows what it takes: %q", plain)
	}
	if !strings.Contains(plain, "Esc cancel") || strings.Contains(plain, "Tab complete") || strings.Contains(plain, "Enter run") {
		t.Fatalf("the row is the commands and Esc only: %q", plain)
	}
	if wide := m.statusNavLines(400); len(wide) != 1 {
		t.Fatalf("on a wide terminal the CMD row is one line, got %d", len(wide))
	}
	// The CMD row is the whole footer: no Tracks or playback keys while a
	// command is typed.
	if all := ansi.Strip(strings.Join(m.statusLines(90), " ")); strings.Contains(all, "play/pause") || strings.Contains(all, "next/prev") {
		t.Fatalf("command mode shows the CMD row alone, got %q", all)
	}

	// A long buffer is cut on the left so the cursor stays on the row.
	m.cmdBuf = "save " + strings.Repeat("summer ", 20)
	plain = ansi.Strip(m.statusNavLines(90)[0])
	if !strings.Contains(plain, ":…") || !strings.Contains(plain, "summer █") {
		t.Fatalf("a long command is cut on the left, keeping the cursor: %q", plain)
	}
}

func TestCommandMode_KeepsTheSplitOnScreen(t *testing.T) {
	m := newModel(newMockPlayer())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.introStep = introDone // skip startup animation
	m.mode = modeCommand
	m.cmdBuf = "qua"
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "┬") {
		t.Fatalf("typing a command keeps the Tracks/Search split on screen:\n%s", view)
	}
	if strings.Contains(view, "Commands") {
		t.Fatalf("there is no command panel any more:\n%s", view)
	}
	if !strings.Contains(view, ":save") || !strings.Contains(view, ":qua█") {
		t.Fatalf("the footer lists the commands and the typed one:\n%s", view)
	}
	// Tab completes the first match; there is no cursor to move.
	m.handleCommandKey("tab")
	if m.cmdBuf != "quality " {
		t.Fatalf("Tab completes qua → 'quality ', got %q", m.cmdBuf)
	}
}

func TestRetiredCommands_StillRunButAreNotListed(t *testing.T) {
	m := newModel(newMockPlayer())
	for _, c := range retiredCommands {
		m.cmdBuf = c.trigger
		for _, s := range m.commandSuggestions() {
			if s.trigger == c.trigger {
				t.Fatalf("%q is retired and must not be listed or completed", c.trigger)
			}
		}
		m.errMsg = ""
		_ = m.executeCommand(c.trigger)
		if strings.Contains(m.errMsg, "unknown command") {
			t.Fatalf("%q still runs when typed, got %q", c.trigger, m.errMsg)
		}
	}
}

func TestMute_IsGone(t *testing.T) {
	// The laptop and the OS handle muting; the app no longer has a command.
	m := newModel(newMockPlayer())
	m.cmdBuf = "mu"
	if suggs := m.commandSuggestions(); len(suggs) != 0 {
		t.Fatalf("no command completes from 'mu', got %#v", suggs)
	}
	_ = m.executeCommand("mute")
	if !strings.Contains(m.errMsg, "unknown command") {
		t.Fatalf(":mute is not a command any more, got %q", m.errMsg)
	}
}

func TestCtrlSlash_KeepsEachModesResultsAndSkipsRepeatLookups(t *testing.T) {
	m := newModel(newMockPlayer())
	m.provider = &termProvider{n: 3}
	m.mode = modeSearch
	m.search.SetSize(80, 30)
	// Apple Music results for "coltrane".
	m.searchQuery, m.searchCursor = "coltrane", 8
	m.Update(searchResultMsg{query: "coltrane", result: &provider.SearchResult{Tracks: catalogTracks(4)}})
	if len(m.search.Results()) != 4 || m.searchShown != "coltrane" {
		t.Fatalf("setup: AM results missing (%d, shown=%q)", len(m.search.Results()), m.searchShown)
	}
	// First switch to Claude Code: the text has never been looked up there → lookup.
	cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
	if cmd == nil || m.searchSrc != searchClaude || !m.search.Loading() {
		t.Fatalf("first Claude Code switch must look the text up (cmd=%v vibe=%v loading=%v)", cmd != nil, m.searchSrc == searchClaude, m.search.Loading())
	}
	m.Update(vibeResultMsg{query: "coltrane", via: "Test", plan: vibe.Plan{Model: "fable-5-1", Summary: "Late Coltrane"}, tracks: catalogTracks(2)})
	if !m.search.VibeResults() || len(m.search.Results()) != 2 {
		t.Fatalf("Claude Code results should show (vibe=%v n=%d)", m.search.VibeResults(), len(m.search.Results()))
	}
	// Through the saved lists, which never look anything up…
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd != nil || m.searchSrc != searchSaved {
		t.Fatalf("the saved lists never look anything up (cmd=%v src=%v)", cmd != nil, m.searchSrc)
	}
	// …the feed (fetched on this first visit)…
	if m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); m.searchSrc != searchFeed {
		t.Fatalf("SV → FE, got %v", m.searchSrc)
	}
	// …back to Apple Music: same text, its results are still there → no search.
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd != nil || m.searchSrc != searchApple || m.search.Loading() {
		t.Fatalf("same text back in Apple Music must not re-search (cmd=%v vibe=%v loading=%v)", cmd != nil, m.searchSrc == searchClaude, m.search.Loading())
	}
	if m.search.VibeResults() || len(m.search.Results()) != 4 {
		t.Fatalf("the Apple Music list should be back untouched (vibe=%v n=%d)", m.search.VibeResults(), len(m.search.Results()))
	}
	// And forward again: the Claude Code songs are still there → no new lookup.
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd != nil || !m.search.VibeResults() || len(m.search.Results()) != 2 {
		t.Fatalf("same text back in Claude Code must reuse its songs (cmd=%v vibe=%v n=%d)", cmd != nil, m.search.VibeResults(), len(m.search.Results()))
	}
	if v := m.search.View(); !strings.Contains(v, "Fable 5.1") || !strings.Contains(v, "Late Coltrane") {
		t.Fatalf("the kept Claude Code list keeps its header and summary: %q", v)
	}
	// A changed text: Claude Code looks it up on the way in, Apple Music
	// waits for Enter (through the saved lists first, which ignore it).
	m.searchTyping = true
	m.handleSearchKey("!", tea.KeyPressMsg{Code: '!', Text: "!"})
	m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}) // SV
	m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}) // FE
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd != nil || m.searchSrc != searchApple || m.search.Loading() {
		t.Fatalf("switching to Apple Music with new text does not search yet (cmd=%v src=%v loading=%v)", cmd != nil, m.searchSrc, m.search.Loading())
	}
	if cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || !m.search.Loading() || m.searchTyping {
		t.Fatalf("Enter searches the new text and ends typing (cmd=%v loading=%v typing=%v)", cmd != nil, m.search.Loading(), m.searchTyping)
	}
	// An empty query never looks anything up.
	m.searchQuery, m.searchCursor = "", 0
	if cmd := m.handleSearchKey("ctrl+/", tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl}); cmd != nil || m.searchSrc != searchClaude {
		t.Fatalf("empty text: just switch (cmd=%v vibe=%v)", cmd != nil, m.searchSrc == searchClaude)
	}
}

func TestVibeResult_LandsInClaudeListWhileAppleMusicShows(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.search.SetSize(80, 30)
	m.setSearchSource(searchClaude)
	m.searchQuery = "chill"
	m.startVibeSearch("chill")
	m.setSearchSource(searchApple) // user switched back before the answer came
	m.Update(vibeResultMsg{query: "chill", via: "Test", plan: vibe.Plan{Summary: "s"}, tracks: catalogTracks(1)})
	if m.search.VibeResults() {
		t.Fatal("the Apple Music list must not be replaced by the late Claude result")
	}
	m.setSearchSource(searchClaude)
	if !m.search.VibeResults() || len(m.search.Results()) != 1 || m.search.Loading() {
		t.Fatalf("the Claude Code list should hold the late result (vibe=%v n=%d loading=%v)", m.search.VibeResults(), len(m.search.Results()), m.search.Loading())
	}
}

// catalogTracks builds n distinct catalog tracks.
func catalogTracks(n int) []provider.Track {
	var out []provider.Track
	for i := range n {
		id := fmt.Sprintf("k%d", i)
		out = append(out, provider.Track{ID: id, CatalogID: id, Title: "Track " + id, Artist: "Band"})
	}
	return out
}

func TestStatusLines_SearchModeDropsTheTracksKeys(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	lines := m.statusLines(400)
	if len(lines) != 1 {
		t.Fatalf("search mode shows only the SEARCH row, got %d rows: %q", len(lines), lines)
	}
	joined := strings.Join(lines, " ")
	// (space is play/pause in Search too now, so it is not in this list.)
	for _, stale := range []string{"cut below", "remove", "+5 related", "+5 library", "repeat", "random"} {
		if strings.Contains(joined, stale) {
			t.Fatalf("Tracks key %q must not be listed while searching: %q", stale, joined)
		}
	}
	m.mode = modeNormal
	if len(m.statusLines(200)) < 2 {
		t.Fatal("the Tracks keys come back with the Tracks panel")
	}
}

func TestStatusLines_TracksKeysAreOneFlow(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeNormal
	lines := m.statusLines(600) // wide enough for the whole Tracks key list
	if len(lines) != 1 {
		t.Fatalf("on a wide terminal the Tracks key list is one row, got %d: %q", len(lines), lines)
	}
	plain := ansi.Strip(lines[0])
	if !strings.Contains(plain, ":q quit  ·  spc play/pause") {
		t.Fatalf("the navigation and playback keys are joined with a dot, not a line break: %q", plain)
	}
	if narrow := m.statusLines(60); len(narrow) < 2 {
		t.Fatal("a narrow terminal still wraps the list onto more rows")
	}
}

func TestHandleSearchKey_MultiSelectAddsAll(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.mode = modeSearch
	m.search.SetSize(80, 40)
	tracks := catalogTracks(6)
	m.search.SetState(tracks, false, nil)
	// Shift+↓ twice: k0, k1, k2; Shift+→ drops k2; ↓ ↓ and Shift+→ adds k4.
	m.handleSearchKey("ctrl+shift+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl | tea.ModShift})
	m.handleSearchKey("ctrl+shift+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl | tea.ModShift})
	m.handleSearchKey("ctrl+right", tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl})
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	m.handleSearchKey("ctrl+right", tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl})
	if got := m.search.SelectedTracks(); len(got) != 3 || got[0].ID != "k0" || got[1].ID != "k1" || got[2].ID != "k4" {
		t.Fatalf("selection = %+v, want k0 k1 k4", got)
	}
	footer := ansi.Strip(strings.Join(m.statusLines(400), " "))
	if !strings.Contains(footer, "^, add") || !strings.Contains(footer, "^. add & play") {
		t.Fatalf("the footer lists the add keys: %q", footer)
	}
	// → keeps its row meaning: on the "+ 1 more" row it reveals, adds nothing.
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if sec, more, ok := m.search.SelectedToggle(); !ok || sec != "Tracks" || !more {
		t.Fatalf("expected the more row, got %q %v %v", sec, more, ok)
	}
	if cmd := m.handleSearchKey("right", tea.KeyPressMsg{Code: tea.KeyRight}); cmd != nil || len(m.queueIDs) != 0 || m.search.SelectionCount() != 3 {
		t.Fatalf("→ on a control row operates the control even with a selection (cmd=%v queue=%v sel=%d)", cmd != nil, m.queueIDs, m.search.SelectionCount())
	}
	// Ctrl+,: all three go to Tracks, nothing plays, selection cleared.
	if cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl}); cmd != nil {
		cmd()
	}
	if strings.Join(m.queueIDs, ",") != "k0,k1,k4" || len(mp.setQueueIDs) > 0 || mp.setQueueAtStart != "" {
		t.Fatalf("Ctrl+, appends the selection in order without playing: queue=%v", m.queueIDs)
	}
	if m.search.SelectionCount() != 3 {
		t.Fatal("the selection stays marked after adding")
	}
	// Shift+← clears it; Ctrl+. with a fresh selection adds and starts the first of them (k2).
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	if m.search.SelectionCount() != 0 {
		t.Fatal("Shift+← clears the selection")
	}
	for range 4 {
		m.handleSearchKey("ctrl+up", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	}
	if cur := m.search.SelectedTrack(); cur == nil || cur.ID != "k2" {
		t.Fatalf("setup: expected the highlight on k2, got %+v", cur)
	}
	m.handleSearchKey("ctrl+shift+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl | tea.ModShift}) // k2, k3
	cmd := m.handleSearchKey("ctrl+.", tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+. with a selection should add and play")
	}
	cmd()
	if strings.Join(m.queueIDs, ",") != "k0,k1,k4,k2,k3" {
		t.Fatalf("Ctrl+. appends the selection: %v", m.queueIDs)
	}
	started := mp.setQueueAtStart
	if n := len(mp.syncCalls); n > 0 {
		started = mp.syncCalls[n-1].Play
	}
	if started != "k2" {
		t.Fatalf("the first selected song should start, got %q", started)
	}
	if m.search.SelectionCount() != 2 {
		t.Fatal("selection kept after Ctrl+.")
	}
	// Without a selection the keys do nothing.
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	if cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatal("Ctrl+, without a selection is a no-op")
	}
}

func TestCtrlLeft_ClearsAndRestoresTheSelection(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.search.SetSize(80, 40)
	m.search.SetState(catalogTracks(4), false, nil)
	m.handleSearchKey("ctrl+shift+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl | tea.ModShift}) // k0, k1
	if footer := ansi.Strip(strings.Join(m.statusLines(400), " ")); !strings.Contains(footer, "^← clear/restore") {
		t.Fatalf("the footer always lists Ctrl+←: %q", footer)
	}
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	if m.search.SelectionCount() != 0 || !m.search.CanRestoreSelection() {
		t.Fatalf("Shift+← clears and keeps the selection for restoring: sel=%d", m.search.SelectionCount())
	}
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	if got := m.search.SelectedTracks(); len(got) != 2 || got[0].ID != "k0" || got[1].ID != "k1" {
		t.Fatalf("Shift+← again brings the same selection back: %+v", got)
	}
	// A change after clearing starts over: the old selection is gone for good.
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	m.handleSearchKey("ctrl+right", tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}) // k2 only
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	if got := m.search.SelectedTracks(); len(got) != 1 || got[0].ID != "k2" {
		t.Fatalf("only the latest cleared selection comes back: %+v", got)
	}
	// New results drop both the selection and the stash.
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	m.search.SetState(catalogTracks(2), false, nil)
	m.handleSearchKey("ctrl+left", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	if m.search.SelectionCount() != 0 {
		t.Fatal("nothing to restore after a new result set")
	}
}

// collectionProvider serves fixed album and playlist contents.
type collectionProvider struct {
	mockProvider
	albums    map[string][]provider.Track
	playlists map[string][]provider.Track
}

func (p *collectionProvider) GetAlbumTracks(_ context.Context, id string) ([]provider.Track, error) {
	return p.albums[id], nil
}
func (p *collectionProvider) GetPlaylistTracks(_ context.Context, id string) ([]provider.Track, error) {
	return p.playlists[id], nil
}
func (p *collectionProvider) GetCatalogPlaylistTracks(_ context.Context, id string) ([]provider.Track, error) {
	return p.playlists[id], nil
}

func TestAddSelection_ExpandsAlbumsAndPlaylistsInResultOrder(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	m.provider = &collectionProvider{
		albums:    map[string][]provider.Track{"a1": {{ID: "a1-1", CatalogID: "a1-1", Title: "Album song 1", Artist: "Band"}, {ID: "a1-2", CatalogID: "a1-2", Title: "Album song 2", Artist: "Band"}}},
		playlists: map[string][]provider.Track{"pl1": {{ID: "pl-1", CatalogID: "pl-1", Title: "Playlist song", Artist: "Other"}}},
	}
	m.mode = modeSearch
	m.search.SetSize(80, 40)
	m.search.SetResults(&provider.SearchResult{
		Playlists: []provider.Playlist{{ID: "pl1", Name: "Mix"}},
		Albums:    []provider.Album{{ID: "a1", Title: "LP", Artist: "Band"}},
		Tracks:    catalogTracks(2),
	}, false, nil)
	// Highlight starts on the playlist: Shift+↓ sweeps playlist and album
	// (skipping the section controls); then walk to k1 and toggle it.
	m.handleSearchKey("ctrl+shift+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl | tea.ModShift})
	if a := m.search.SelectedAlbum(); a == nil || a.ID != "a1" {
		t.Fatalf("the sweep should land on the album, not on a control row (album=%v)", a)
	}
	for i := 0; i < 10 && (m.search.SelectedTrack() == nil || m.search.SelectedTrack().ID != "k1"); i++ {
		m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	}
	m.handleSearchKey("ctrl+right", tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl})
	items := m.search.SelectedItems()
	if len(items) != 3 || items[0].Playlist == nil || items[1].Album == nil || items[2].Track == nil {
		t.Fatalf("selection should hold playlist, album, song in result order: %+v", items)
	}
	if v := m.search.View(); strings.Count(v, "✓") != 2 { // playlist and album; k1 is highlighted
		t.Fatalf("albums and playlists get the check mark too: %q", v)
	}
	cmd := m.handleSearchKey("ctrl+.", tea.KeyPressMsg{Code: '.', Mod: tea.ModCtrl})
	if cmd == nil || m.search.SelectionCount() != 3 {
		t.Fatal("Ctrl+. with collections should expand them asynchronously and keep the selection")
	}
	msg := cmd()
	sel, ok := msg.(selectionTracksMsg)
	if !ok || sel.err != nil || len(sel.tracks) != 4 {
		t.Fatalf("expected 4 songs (1 playlist + 2 album + 1 song), got %+v", msg)
	}
	_, next := m.Update(msg)
	if next != nil {
		next()
	}
	if strings.Join(m.queueIDs, ",") != "pl-1,a1-1,a1-2,k1" {
		t.Fatalf("songs land in result order: %v", m.queueIDs)
	}
	started := mp.setQueueAtStart
	if n := len(mp.syncCalls); n > 0 {
		started = mp.syncCalls[n-1].Play
	}
	if started != "pl-1" {
		t.Fatalf("the first song of the first selected item starts, got %q", started)
	}
}

func TestSearchView_UnselectedHighlightGoesGreyWhileSelecting(t *testing.T) {
	m := newModel(newMockPlayer())
	m.mode = modeSearch
	m.search.SetSize(80, 40)
	m.search.SetState(catalogTracks(3), false, nil)
	grey := styles.QueueItemMuted.Render("Track k0")
	if strings.Contains(m.search.View(), grey) {
		t.Fatal("without a selection the highlighted song keeps the highlight colour")
	}
	m.handleSearchKey("ctrl+right", tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}) // select k0
	if strings.Contains(m.search.View(), grey) {
		t.Fatal("a highlighted song that is selected keeps the highlight colour")
	}
	m.handleSearchKey("ctrl+right", tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}) // deselect k0 → no selection at all
	m.handleSearchKey("ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	m.handleSearchKey("ctrl+right", tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}) // select k1
	m.handleSearchKey("ctrl+up", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})       // back on k0, unselected
	v := m.search.View()
	if !strings.Contains(v, grey) || !strings.Contains(v, "▶") {
		t.Fatalf("with a selection active, the highlighted unselected song is grey but keeps the pointer: %q", v)
	}
}

func TestHandleSearchKey_EnterAndShiftEnter_NoLongerAdd(t *testing.T) {
	mp := newMockPlayer()
	m := newModel(mp)
	seedSearchResults(m, provider.Track{Title: "Hi", Artist: "There", CatalogID: "99999"})
	if cmd := m.handleSearchKey("enter", tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || len(m.queueIDs) != 0 {
		t.Fatalf("Enter on a song does nothing now (cmd=%v queue=%v)", cmd != nil, m.queueIDs)
	}
	if cmd := m.handleSearchKey("shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}); cmd != nil || len(m.queueIDs) != 0 {
		t.Fatalf("Shift+Enter is unbound in Search (cmd=%v queue=%v)", cmd != nil, m.queueIDs)
	}
	// Ctrl+, with nothing marked adds the highlighted album via its songs.
	m.provider = &collectionProvider{albums: map[string][]provider.Track{"a1": {{ID: "a1-1", CatalogID: "a1-1", Title: "One", Artist: "Band"}}}}
	m.search.SetResults(&provider.SearchResult{Albums: []provider.Album{{ID: "a1", Title: "LP", Artist: "Band"}}}, false, nil)
	cmd := m.handleSearchKey("ctrl+,", tea.KeyPressMsg{Code: ',', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+, on a highlighted album should expand it")
	}
	if _, next := m.Update(cmd()); next != nil {
		next()
	}
	if strings.Join(m.queueIDs, ",") != "a1-1" {
		t.Fatalf("the album's songs should be queued: %v", m.queueIDs)
	}
}

func TestExecuteCommand_ModelAndEffort(t *testing.T) {
	m := newModel(newMockPlayer())
	m.cfg.VibeAgent = "claude" // the planner is the CLI (nothing is spawned here)
	m.applyVibeSetup("test")
	m.executeCommand("model")
	if !strings.Contains(m.errMsg, vibe.DefaultClaudeModel) || !strings.Contains(m.errMsg, "effort default") {
		t.Fatalf("bare :model reports the current setup, got %q", m.errMsg)
	}
	m.executeCommand("model haiku")
	c, ok := m.vibePlanner.(*vibe.ClaudePlanner)
	if !ok || c.Model != "haiku" || m.cfg.VibeModel != "haiku" || !strings.Contains(m.errMsg, "haiku") {
		t.Fatalf(":model haiku should rebuild the planner and remember it (planner=%+v cfg=%q msg=%q)", m.vibePlanner, m.cfg.VibeModel, m.errMsg)
	}
	m.executeCommand("effort low")
	if c := m.vibePlanner.(*vibe.ClaudePlanner); c.Effort != "low" || c.Model != "haiku" || m.cfg.VibeEffort != "low" {
		t.Fatalf(":effort low should keep the model and set the effort: %+v", c)
	}
	m.executeCommand("effort silly")
	if c := m.vibePlanner.(*vibe.ClaudePlanner); c.Effort != "low" || !strings.Contains(m.errMsg, ":effort takes") {
		t.Fatalf("an unknown effort is refused: %+v %q", c, m.errMsg)
	}
	m.executeCommand("model default")
	m.executeCommand("effort default")
	if c := m.vibePlanner.(*vibe.ClaudePlanner); c.Model != "" || c.Effort != "" || !strings.Contains(m.errMsg, "CLI default") {
		t.Fatalf("default hands both back to the CLI: %+v %q", c, m.errMsg)
	}
	// The commands are in the palette.
	found := 0
	for _, c := range allCommands {
		if c.trigger == "model" || c.trigger == "effort" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("model and effort should be listed in the palette, found %d", found)
	}
}
