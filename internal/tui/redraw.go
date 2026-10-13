package tui

import (
	"fmt"

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
// about, equalizer…), the intro step and the size. Ordinary updates (the
// clock, a new track, a picker or prompt over the same layout) keep it.
func (m *Model) screenKey() string {
	return fmt.Sprintf("%d/%d/%dx%d", m.activePanel, m.introStep, m.width, m.height)
}

// redrawCmd returns a full clear-and-repaint when the update changed the
// layout (or was a resize), nil otherwise. The cover picture is forgotten so
// it is drawn again over the repainted frame.
func (m *Model) redrawCmd(before string, msg tea.Msg) tea.Cmd {
	_, resized := msg.(tea.WindowSizeMsg)
	if !resized && m.screenKey() == before {
		return nil
	}
	m.sixel.shownKey, m.sixel.pendingKey = "", ""
	return tea.ClearScreen
}
