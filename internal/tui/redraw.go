package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	tea "charm.land/bubbletea/v2"
)

// Full repaints (J545). The renderer only rewrites the cells it thinks have
// changed, which assumes the terminal still shows exactly the previous frame.
// Inside tmux, with the cover drawn as a sixel picture outside the frame, that
// assumption broke in foot: after switching views (lyrics → tracks) or
// re-attaching, text from the earlier view (the intro credits, the tails of
// longer rows) stayed on screen and rows could land one line off. So whenever
// the layout itself changes — another view or panel, the intro ending, a new
// size (which is also what a tmux attach sends) — the whole screen is cleared
// and painted again, and the cover is drawn again on top of the fresh frame.

// screenKey names the current layout: the active panel (tracks, lyrics,
// about, equalizer…) and whether the intro is over. Ordinary updates (the
// clock, a new track, a picker or prompt over the same layout) keep it. A
// resize is not in it: the renderer erases and repaints the screen on a resize
// itself (J618: a second clear there only doubled the work).
func (m *Model) screenKey() string {
	return fmt.Sprintf("%d/%t", m.activePanel, m.introStep == introDone)
}

// redrawCmd makes the screen right again when the layout changed (J618):
//   - the intro ending: a full clear and repaint (the credits' heart and the
//     animation are where stale text was seen), the cover drawn again after it;
//   - another panel (tracks ↔ lyrics, about, equalizer…): every cell below
//     the cover is repainted in the same frame (withRepaintParity). No clear: the cover stays, nothing blinks, and the
//     frame goes out as one synchronized update.
func (m *Model) redrawCmd(before string, msg tea.Msg) tea.Cmd {
	after := m.screenKey()
	if after == before {
		return nil
	}
	introEnded := !strings.HasSuffix(before, "/true") && strings.HasSuffix(after, "/true")
	if !introEnded {
		m.repaintUntil = time.Now().Add(repaintWindow)
		trace("repaint", fmt.Sprintf("%s->%s region", before, after))
		// one more frame after the window, so the style's removal (a second full rewrite) is flushed too
		return tea.Tick(repaintWindow+20*time.Millisecond, func(time.Time) tea.Msg { return repaintDoneMsg{} })
	}
	m.sixel.shownKey, m.sixel.pendingKey = "", ""
	trace("repaint", fmt.Sprintf("%s->%s full", before, after))
	return tea.ClearScreen
}

// withRepaintParity gives every cell below the cover (or below the top border
// when there is no cover) an underline colour (58;5;1, invisible since nothing
// is underlined) for repaintWindow after a panel switch. The renderer compares
// cells by style, so it rewrites every one of them when the colour appears, and
// again when it goes, blanks included (each line is padded to the full width):
// whatever the terminal had there, stale or not, is overwritten, in frames that
// go out as synchronized updates. Outside that window the frame is untouched
// and costs nothing.
func (m *Model) withRepaintParity(content string) string {
	if m.repaintUntil.IsZero() || time.Now().After(m.repaintUntil) {
		return content
	}
	from := 1 // first line (0-based) to repaint: below the cover, so its picture is never overwritten
	if p, ok := m.sixelAreaReserved(); ok {
		from = p.row - 1 + p.rows
	}
	lines := strings.Split(content, "\n")
	if from >= len(lines) {
		return content
	}
	const par = "\x1b[58;5;1m"
	for i := from; i < len(lines); i++ {
		l := sgrRe.ReplaceAllStringFunc(lines[i], func(s string) string { return s + parityAfter(s, par) })
		if pad := m.width - lipgloss.Width(lines[i]); pad > 0 {
			l += strings.Repeat(" ", pad)
		}
		lines[i] = par + l + "\x1b[59m"
	}
	return strings.Join(lines, "\n")
}

// repaintWindow is how long the repaint style is kept after a switch: a few of
// the renderer's frames (it flushes at ~60 fps), so one of them surely goes out.
const repaintWindow = 120 * time.Millisecond

// parityAfter is what follows an SGR sequence inside the repaint region: the parity underline colour, except
// where that SGR turns underlining on (the Lyrics heading). There the underline is visible, so its colour is set
// to the text's own colour (an underline's default), which the renderer still sees as a change (RedrawCheck618).
func parityAfter(sgr, par string) string {
	params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sgr, "\x1b["), "m"), ";")
	underlined := false
	for _, p := range params {
		if p == "4" || (strings.HasPrefix(p, "4:") && p != "4:0") {
			underlined = true
		}
	}
	if !underlined {
		return par
	}
	for i := 0; i < len(params); i++ {
		if params[i] == "38" && i+2 < len(params) && params[i+1] == "5" {
			return "\x1b[58;5;" + params[i+2] + "m"
		}
		if params[i] == "38" && i+4 < len(params) && params[i+1] == "2" {
			return "\x1b[58;2;" + params[i+2] + ";" + params[i+3] + ";" + params[i+4] + "m"
		}
	}
	return "\x1b[59m" // underlined in the default colour: keep it so (no parity on those cells)
}

var sgrRe = regexp.MustCompile("\x1b\\[[0-9;:]*m")

// Timing trace for redraw tests (J618): VIBEZ_TRACE=FILE appends "unix-µs event detail" lines for key presses, layout
// repaints and cover draws, so a test can measure key → repainted frame → cover without screen scraping. Off by default.
var traceFile = func() *os.File {
	p := os.Getenv("VIBEZ_TRACE")
	if p == "" {
		return nil
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}()

func trace(event, detail string) {
	if traceFile != nil {
		fmt.Fprintf(traceFile, "%d %s %s\n", time.Now().UnixMicro(), event, detail)
	}
}

// repaintDoneMsg only wakes the program after the repaint window, so the frame without the repaint style is rendered.
type repaintDoneMsg struct{}
