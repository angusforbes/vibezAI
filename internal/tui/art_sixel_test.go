package tui

import (
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/art"
)

// sixelModel is a playing track with its cover loaded on a terminal that
// draws sixels (14×32 px cells) in the boxed layout.
func sixelModel(t *testing.T) *Model {
	t.Helper()
	m := newModel(nil)
	m.width, m.height = 120, 40
	m.introStep = introDone
	m.artMode = true
	m.supportsArtColor = func() bool { return true }
	m.sixel = sixelState{enabled: true, probed: true, cellOK: true, cellW: 14, cellH: 32}
	m.artwork = artworkCache{url: "https://example.invalid/a.png", img: image.NewRGBA(image.Rect(0, 0, 8, 8)), rendered: map[art.Size][]string{}}
	m.playerState = player.State{Playing: true, Track: &provider.Track{Title: "Pic", Artist: "Painter", Album: "Gallery", ArtworkURL: m.artwork.url, Duration: time.Minute}}
	return m
}

func rawString(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a raw write, got no command")
	}
	msg, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatalf("expected tea.RawMsg, got %#v", msg)
	}
	s, ok := msg.Msg.(string)
	if !ok {
		t.Fatalf("raw message is not a string: %#v", msg.Msg)
	}
	return s
}

// On a sixel terminal the frame reserves blank cells for the cover, the
// picture is drawn there once the frame has settled, drawn again after a
// resize, and taken down when the track has no cover.
func TestSixel_FrameReservesCellsAndThePictureFollows(t *testing.T) {
	m := sixelModel(t)
	lines := m.nowPlayingLines(m.width-4, m.nowPlayingHeight())
	if strings.Contains(strings.Join(lines, "\n"), "▀") {
		t.Fatalf("no half-blocks when the cover is a picture: %q", lines)
	}
	rows, cols, _ := m.artGeometry(m.width-4, nowPlayingArtRows)
	if !strings.HasPrefix(lines[0], strings.Repeat(" ", cols+nowPlayingArtGap)) {
		t.Fatalf("row 0 does not start with %d blank cells: %q", cols+nowPlayingArtGap, lines[0])
	}

	// Any update schedules the picture, delayed so the frame lands first.
	cmd := m.sixelSyncCmd(tickMsg(time.Now()))
	if cmd == nil || m.sixel.pendingKey == "" {
		t.Fatal("a wanted picture schedules an emit")
	}
	if again := m.sixelSyncCmd(tickMsg(time.Now())); again != nil {
		t.Fatal("no second emit while one is pending")
	}
	emit, ok := cmd().(sixelEmitMsg)
	if !ok {
		t.Fatalf("the scheduled message is a sixelEmitMsg, got %T", emit)
	}
	seq := rawString(t, m.sixelSyncCmd(emit))
	wantPrefix := "\x1b7\x1b[2;3H\x1bP0;1q"
	if !strings.HasPrefix(seq, wantPrefix) || !strings.HasSuffix(seq, "\x1b\\\x1b8") {
		t.Fatalf("the write saves the cursor, goes to row 2 col 3, draws, restores: %q…%q", seq[:min(len(seq), 14)], seq[len(seq)-4:])
	}
	if m.sixel.shownKey == "" || m.sixel.shown.cols != cols || m.sixel.shown.rows != rows {
		t.Fatalf("shown placement not recorded: %+v", m.sixel)
	}
	if m.sixelSyncCmd(tickMsg(time.Now())) != nil {
		t.Fatal("nothing to do while the picture is up to date")
	}

	// A resize clears the screen: the picture is scheduled again.
	if m.sixelSyncCmd(tea.WindowSizeMsg{Width: 120, Height: 40}) == nil || m.sixel.shownKey != "" {
		t.Fatal("after a resize the picture is drawn again")
	}
	m.sixel.shownKey, m.sixel.pendingKey = m.sixel.cacheKey, "" // pretend it is up

	// A track without a cover changes the layout under the picture: repaint.
	m.sixel.shownKey = fmt.Sprintf("%s|2,3|%dx%d", m.artwork.url, cols, rows)
	m.playerState.Track = &provider.Track{Title: "Plain", Artist: "Nobody", Duration: time.Minute}
	cmd = m.sixelSyncCmd(tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("hiding the picture needs a command")
	}
	if msg := cmd(); fmt.Sprintf("%T", msg) != "tea.clearScreenMsg" {
		t.Fatalf("the layout changed under the picture, want a full repaint, got %T", msg)
	}
	if m.sixel.shownKey != "" {
		t.Fatal("nothing is shown after the repaint")
	}
}

// While the next cover downloads the layout still holds the cells blank, so
// the old picture is wiped in place instead of repainting everything.
func TestSixel_CoverInFlightIsErasedInPlace(t *testing.T) {
	m := sixelModel(t)
	rows, cols, _ := m.artGeometry(m.width-4, nowPlayingArtRows)
	m.sixel.shownKey = "old"
	m.sixel.shown = sixelPlacement{row: 2, col: 3, cols: cols, rows: rows}
	next := "https://example.invalid/b.png"
	m.playerState.Track.ArtworkURL = next
	m.artwork = artworkCache{url: next, rendered: map[art.Size][]string{}} // in flight: no img yet
	seq := rawString(t, m.sixelSyncCmd(tickMsg(time.Now())))
	if !strings.HasPrefix(seq, "\x1b7\x1b[m\x1b[2;3H"+strings.Repeat(" ", cols)) || !strings.HasSuffix(seq, "\x1b8") {
		t.Fatalf("erase writes blanks over the old cells with a reset pen: %q", seq[:min(len(seq), 30)])
	}
	if strings.Count(seq, "\x1b[") != rows+1 { // one CUP per row plus the pen reset
		t.Fatalf("erase covers %d rows, got %d CSI sequences: %q", rows, strings.Count(seq, "\x1b["), seq)
	}
	if m.sixel.shownKey != "" {
		t.Fatal("nothing is shown after the erase")
	}
}

// The terminal's answer to the probe decides: 4 among the primary device
// attributes means sixel; without it the cover stays half-blocks.
func TestSixel_ProbeAnswerEnablesPictures(t *testing.T) {
	m := sixelModel(t)
	m.sixel = sixelState{cellW: 14, cellH: 32, cellOK: true}
	if m.sixelInitCmd() == nil {
		t.Fatal("an unprobed terminal is asked for its attributes and cell size")
	}
	m.sixelSyncCmd(uv.PrimaryDeviceAttributesEvent{62, 22})
	if m.sixel.enabled || !m.sixel.probed {
		t.Fatalf("no 4, no pictures: %+v", m.sixel)
	}
	if !strings.Contains(strings.Join(m.nowPlayingLines(116, nowPlayingArtRows), ""), "▀") {
		t.Fatal("half-blocks when the terminal does not draw sixels")
	}
	m.sixelSyncCmd(uv.PrimaryDeviceAttributesEvent{62, 4, 22})
	if !m.sixel.enabled {
		t.Fatal("4 means sixel")
	}
	if m.sixelInitCmd() == nil {
		t.Fatal("init still asks for the cell size even once probed")
	}
	m.sixel.cellW = 0
	if m.sixelUsable() {
		t.Fatal("an unknown cell size means no pictures")
	}
}

// Inside tmux the seeded (TIOCGWINSZ) cell size is not trusted: the cover is
// half-blocks until a CSI 16 t report confirms the real size, and adopting a
// bigger cell re-squares and enlarges the art footprint.
func TestSixel_TmuxWaitsForTheRealCellSize(t *testing.T) {
	m := sixelModel(t)
	// tmux handed us a wrong, too-large cell size (18×39 where foot is 14×32).
	m.sixel = sixelState{enabled: true, probed: true, underTmux: true, cellW: 18, cellH: 39}
	if m.sixelUsable() {
		t.Fatal("no picture under tmux until the real cell size is confirmed")
	}
	if !strings.Contains(strings.Join(m.nowPlayingLines(m.width-4, nowPlayingArtRows), ""), "▀") {
		t.Fatal("half-blocks while waiting for the cell size")
	}
	m.sixelSyncCmd(uv.CellSizeEvent{Width: 14, Height: 32})
	if !m.sixel.cellOK || m.sixel.cellW != 14 || m.sixel.cellH != 32 {
		t.Fatalf("the report is adopted: %+v", m.sixel)
	}
	if !m.sixelUsable() {
		t.Fatal("pictures are on once the real cell size is known")
	}
	// The picture now fits its cells: 9 cols × 4 rows of 14×32 = 126×126,
	// well inside the 4-row block, not the oversize the wrong cell size gave.
	p, _, ok := m.sixelWanted()
	if !ok {
		t.Fatal("a picture is wanted")
	}
	seq, err := m.sixelSequence(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seq, `"1;1;126;126`) {
		t.Fatalf("the cover is encoded 126×126 for the real cell size: %q", raster(seq))
	}
}

// raster returns the sixel raster attributes (starting at the `"` that
// begins them) for a message, for error output.
func raster(seq string) string {
	i := strings.Index(seq, `"1;`)
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(seq[i:], '#')
	if j < 0 {
		return seq[i:]
	}
	return seq[i : i+j]
}

// The environment overrides the probe both ways.
func TestSixel_EnvOverride(t *testing.T) {
	t.Setenv("VIBEZ_SIXEL", "0")
	if s := newSixelState(); s.enabled || !s.probed {
		t.Fatalf("VIBEZ_SIXEL=0 disables pictures: %+v", s)
	}
	t.Setenv("VIBEZ_SIXEL", "1")
	if s := newSixelState(); !s.enabled || !s.probed {
		t.Fatalf("VIBEZ_SIXEL=1 forces pictures: %+v", s)
	}
}
