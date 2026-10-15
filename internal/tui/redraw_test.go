package tui

import (
	"charm.land/lipgloss/v2"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// J545: a layout change or resize asks for a full repaint; anything else doesn't.
func TestRedrawCmd_LayoutChangesRepaint(t *testing.T) {
	m := New(testCfg(), &captureProvider{}, newMockPlayer(), Options{})
	m.width, m.height = 120, 40
	m.introStep = introDone

	before := m.screenKey()
	if cmd := m.redrawCmd(before, nil); cmd != nil {
		t.Fatalf("no layout change: nothing to repaint")
	}
	// a panel switch: no clear (the cover stays), the parity flips so the whole region is rewritten in one frame
	m.sixel.shownKey = "cover"
	m.activePanel = 7
	_ = m.redrawCmd(before, nil) // a wake-up tick, not a clear
	if m.repaintUntil.IsZero() || m.sixel.shownKey != "cover" {
		t.Fatalf("panel switch: the region repaint should start and the cover stay (until %v, cover %q)", m.repaintUntil, m.sixel.shownKey)
	}
	// a resize alone: the renderer repaints it itself
	if cmd := m.redrawCmd(m.screenKey(), tea.WindowSizeMsg{Width: 100, Height: 30}); cmd != nil {
		t.Fatalf("a resize alone is repainted by the renderer itself: no second clear")
	}
	// the intro ending: a full clear, and the cover drawn again
	m.introStep = 0
	b := m.screenKey()
	m.introStep = introDone
	if cmd := m.redrawCmd(b, nil); cmd == nil || m.sixel.shownKey != "" {
		t.Fatalf("intro end should clear and forget the cover")
	}
}

func TestWithRepaintParity(t *testing.T) {
	m := New(testCfg(), &captureProvider{}, newMockPlayer(), Options{})
	m.width = 10
	if out := m.withRepaintParity("top\nx"); out != "top\nx" {
		t.Fatalf("outside a repaint window the frame must be untouched: %q", out)
	}
	m.repaintUntil = time.Now().Add(time.Minute)
	out := m.withRepaintParity("top\n\x1b[31mab\x1b[m\nc")
	lines := strings.Split(out, "\n")
	if lines[0] != "top" {
		t.Fatalf("line above the region changed: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "\x1b[58;5;1m") || !strings.Contains(lines[1], "\x1b[m\x1b[58;5;1m") || lipglossWidth(lines[1]) != 10 {
		t.Fatalf("region line not parity-styled and padded: %q", lines[1])
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

func lipglossWidth(s string) int { return lipgloss.Width(s) }
