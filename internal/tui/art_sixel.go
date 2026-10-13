package tui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/simone-vibes/vibez/internal/tui/art"
)

// The cover in the Now Playing block is a real picture when the terminal can
// draw one: a sixel graphic laid over the blank cells the layout reserves at
// the left of the block. Bubble Tea knows nothing about it, so this file keeps
// the picture and the frame in step: it is drawn only once the frame that
// reserves the cells has been flushed, drawn again after a resize (which
// clears the screen), and taken down when the frame stops reserving the area.
//
// The terminal has to say it draws sixels (attribute 4 in its primary device
// attributes: foot does, and tmux ≥ 3.4 built with sixel passes them through)
// and to report its cell size in pixels. VIBEZ_SIXEL=1 or 0 skips the sixel
// probe.
//
// The cell size is the pixel size the image is scaled to, so it must be the
// terminal's real one. Inside tmux the TIOCGWINSZ pixel geometry is tmux's own
// (often wrong: e.g. 18×39 where foot's cell is 14×32), which would size the
// picture ~1.5× too big — over the text and into the panel below. So the true
// size is asked for with a cell-size report (CSI 16 t), which tmux forwards to
// the real terminal; under tmux no picture is drawn until that answer lands.

// sixelEmitDelay lets the renderer flush the frame that lays out the blank
// area before the picture is drawn over it; the renderer ticks at ~60 fps.
const sixelEmitDelay = 80 * time.Millisecond

type sixelState struct {
	enabled   bool // the terminal draws sixel graphics
	probed    bool // the terminal (or VIBEZ_SIXEL) has answered the sixel probe
	underTmux bool // $TMUX set: the seeded cell size is not to be trusted
	cellW     int  // one cell in pixels; zero = unknown, no pictures
	cellH     int
	cellOK    bool // the cell size was confirmed by a CSI 16 t report

	cacheKey string // url and geometry the cached sequence was encoded for
	cache    string

	shownKey   string // the picture on screen; "" = none
	shown      sixelPlacement
	pendingKey string // an emit is scheduled for this key
}

// sixelEmitMsg fires after sixelEmitDelay: draw the picture for key if the
// frame still calls for it.
type sixelEmitMsg struct{ key string }

// clampCellAspect keeps a cell height/width ratio in the same sane band
// cellAspect() uses, so a bogus report cannot distort the square art.
func clampCellAspect(r float64) float64 {
	switch {
	case r < 1.5:
		return 1.5
	case r > 2.6:
		return 2.6
	default:
		return r
	}
}

// sixelPlacement is where the picture goes: the 1-based terminal row and
// column of its top-left cell and its size in cells.
type sixelPlacement struct {
	row, col   int
	cols, rows int
}

func newSixelState() sixelState {
	var s sixelState
	s.underTmux = os.Getenv("TMUX") != ""
	s.cellW, s.cellH = cellPixels()
	switch strings.ToLower(os.Getenv("VIBEZ_SIXEL")) {
	case "1", "true", "on", "yes":
		s.enabled, s.probed = true, true
	case "0", "false", "off", "no":
		s.enabled, s.probed = false, true
	}
	return s
}

// requestCellSize asks the terminal to report its cell size in pixels (CSI
// 16 t); the answer comes back as a uv.CellSizeEvent. tmux forwards it to the
// real terminal, so it is right where TIOCGWINSZ is not.
func requestCellSize() tea.Cmd {
	return tea.Raw(ansi.WindowOp(ansi.RequestCellSizeWinOp))
}

// sixelInitCmd is run once at start: it asks the terminal for its primary
// device attributes (a uv.PrimaryDeviceAttributesEvent listing 4 means it
// draws sixels) unless VIBEZ_SIXEL already decided, and always for the true
// cell size. Both are cheap and ignored by terminals that do not answer.
func (m *Model) sixelInitCmd() tea.Cmd {
	var cmds []tea.Cmd
	if !m.sixel.probed {
		cmds = append(cmds, tea.Raw(ansi.RequestPrimaryDeviceAttributes))
	}
	cmds = append(cmds, requestCellSize())
	return tea.Batch(cmds...)
}

// sixelUsable reports whether covers are drawn as pictures at all. Inside
// tmux it waits for the real cell size (cellOK) before drawing, since the
// seeded TIOCGWINSZ size there is unreliable; the cover shows as half-blocks
// for the few milliseconds until the report lands.
func (m *Model) sixelUsable() bool {
	if !m.sixel.enabled || m.sixel.cellW <= 0 || m.sixel.cellH <= 0 {
		return false
	}
	return m.sixel.cellOK || !m.sixel.underTmux
}

// sixelAreaReserved reports whether the current frame draws the cover's cells
// blank for a picture: the art view is up in the boxed layout (no intro, no
// picker). The picture itself may still be downloading.
func (m *Model) sixelAreaReserved() (sixelPlacement, bool) {
	if !m.sixelUsable() || m.width <= 0 || !m.artModeActive() ||
		m.introStep != introDone || m.mode == modePlaylistPicker {
		return sixelPlacement{}, false
	}
	h := m.nowPlayingHeight()
	if h < nowPlayingArtRows {
		return sixelPlacement{}, false
	}
	rows, cols, ok := m.artGeometry(m.width-4, h)
	if !ok {
		return sixelPlacement{}, false
	}
	// The block's content starts after "│ " on the row under the top border.
	return sixelPlacement{row: 2, col: 3, cols: cols, rows: rows}, true
}

// sixelWanted returns the placement and key of the picture the current frame
// calls for, or false when there is none to draw (yet).
func (m *Model) sixelWanted() (sixelPlacement, string, bool) {
	p, ok := m.sixelAreaReserved()
	if !ok || m.artwork.img == nil {
		return sixelPlacement{}, "", false
	}
	return p, fmt.Sprintf("%s|%d,%d|%dx%d", m.artwork.url, p.row, p.col, p.cols, p.rows), true
}

// sixelSyncCmd runs after every Update and reconciles the picture with the
// frame: a new or moved cover is drawn once the frame has settled, a resize
// (which clears the screen) gets it drawn again, and when the frame stops
// reserving the area the ghost the terminal would keep is removed — by
// blanking the cells while the layout still holds them blank (a cover in
// flight), or by a full repaint when the layout under it changed.
func (m *Model) sixelSyncCmd(msg tea.Msg) tea.Cmd {
	var extra tea.Cmd
	switch msg := msg.(type) {
	case uv.PrimaryDeviceAttributesEvent:
		m.sixel.probed = true
		m.sixel.enabled = slices.Contains([]int(msg), 4)
		if m.sixel.enabled {
			m.artwork.rendered = map[art.Size][]string{} // half-blocks are not needed any more
		}
	case uv.CellSizeEvent:
		// The terminal's real cell size (right even through tmux). Adopt it,
		// re-square the art to it, and redraw at the corrected size.
		if msg.Width > 0 && msg.Height > 0 {
			changed := msg.Width != m.sixel.cellW || msg.Height != m.sixel.cellH
			m.sixel.cellW, m.sixel.cellH, m.sixel.cellOK = msg.Width, msg.Height, true
			m.artCellAsp = clampCellAspect(float64(msg.Height) / float64(msg.Width))
			if changed {
				m.sixel.cacheKey, m.sixel.shownKey = "", ""
				m.artwork.rendered = map[art.Size][]string{}
			}
		}
	case tea.WindowSizeMsg:
		// The renderer clears the screen on resize; the picture went with it.
		// The font (and so the cell size) may have changed too, so ask again.
		m.sixel.shownKey, m.sixel.pendingKey = "", ""
		extra = requestCellSize()
	case sixelEmitMsg:
		m.sixel.pendingKey = ""
		p, key, ok := m.sixelWanted()
		if !ok || key != msg.key {
			return nil
		}
		seq, err := m.sixelSequence(p)
		if err != nil {
			m.appendLog("[art] picture: " + err.Error() + "; using half-blocks")
			m.sixel.enabled = false
			return nil
		}
		m.sixel.shownKey, m.sixel.shown = key, p
		return tea.Raw(seq)
	}

	if !m.sixelUsable() {
		return extra
	}
	_, key, ok := m.sixelWanted()
	if !ok {
		if m.sixel.shownKey == "" {
			return nil
		}
		shown := m.sixel.shown
		m.sixel.shownKey, m.sixel.pendingKey = "", ""
		if _, reserved := m.sixelAreaReserved(); reserved {
			return tea.Batch(extra, tea.Raw(sixelEraseSequence(shown)))
		}
		return tea.Batch(extra, tea.ClearScreen)
	}
	if key == m.sixel.shownKey || key == m.sixel.pendingKey {
		return extra
	}
	m.sixel.pendingKey = key
	return tea.Batch(extra, tea.Tick(sixelEmitDelay, func(time.Time) tea.Msg { return sixelEmitMsg{key: key} }))
}

// sixelSequence is the raw write that draws the cover at p: save the cursor,
// go to the cell, the picture, restore the cursor (and with it the pen), so
// the renderer's idea of both is untouched. The encoding is cached per cover
// and geometry.
func (m *Model) sixelSequence(p sixelPlacement) (string, error) {
	key := fmt.Sprintf("%s|%dx%d|%dx%d", m.artwork.url, p.cols, p.rows, m.sixel.cellW, m.sixel.cellH)
	if m.sixel.cacheKey != key {
		dcs, err := art.Sixel(m.artwork.img, p.cols, p.rows, m.sixel.cellW, m.sixel.cellH)
		if err != nil {
			return "", err
		}
		m.sixel.cacheKey, m.sixel.cache = key, dcs
	}
	return ansi.SaveCursor + ansi.CursorPosition(p.col, p.row) + m.sixel.cache + ansi.RestoreCursor, nil
}

// sixelEraseSequence blanks the cells a picture covered. It is only used
// while the frame holds those cells blank itself, so the renderer's copy of
// the screen stays right; the pen is reset for the spaces and restored after.
func sixelEraseSequence(p sixelPlacement) string {
	var sb strings.Builder
	sb.WriteString(ansi.SaveCursor)
	sb.WriteString(ansi.ResetStyle)
	blank := strings.Repeat(" ", p.cols)
	for r := range p.rows {
		sb.WriteString(ansi.CursorPosition(p.col, p.row+r))
		sb.WriteString(blank)
	}
	sb.WriteString(ansi.RestoreCursor)
	return sb.String()
}
