package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// J545: a layout change or resize asks for a full repaint; anything else doesn't.
func TestRedrawCmd_LayoutChangesRepaint(t *testing.T) {
	m := New(testCfg(), &captureProvider{}, newMockPlayer(), Options{})
	m.width, m.height = 120, 40

	before := m.screenKey()
	if cmd := m.redrawCmd(before, nil); cmd != nil {
		t.Fatalf("no layout change should not repaint")
	}
	m.activePanel = (m.activePanel + 1) % max(1, len(m.panels))
	if m.screenKey() == before {
		m.activePanel = -1
	}
	if cmd := m.redrawCmd(before, nil); cmd == nil {
		t.Fatalf("a panel switch should repaint")
	}
	m.sixel.shownKey = "cover"
	if cmd := m.redrawCmd(m.screenKey(), tea.WindowSizeMsg{Width: 120, Height: 40}); cmd == nil {
		t.Fatalf("a resize (tmux attach) should repaint")
	}
	if m.sixel.shownKey != "" {
		t.Fatalf("the cover should be drawn again after a repaint")
	}
}

func TestTerminalProblem(t *testing.T) {
	if p := terminalProblem("xterm-256color|256,extkeys,sixel,sync"); p != "" {
		t.Fatalf("foot-like client should pass, got %q", p)
	}
	if p := terminalProblem("xterm-kitty|bpaste,extkeys,focus"); p == "" {
		t.Fatalf("kitty should be flagged")
	}
	if p := terminalProblem(""); p != "" {
		t.Fatalf("no client: no warning, got %q", p)
	}
}
