package views

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// stripANSI removes colour and style escapes so tests can read plain text.
func stripANSI(s string) string { return ansi.Strip(s) }

func TestAboutModel(t *testing.T) {
	m := NewAbout()
	m.SetSize(80, 24)
	plain := stripANSI(m.View())
	for _, want := range []string{"About", "vibezAI ♪", "made with ❤ by simonepelosi", "updated with ❤ by agf and Claude"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expected view to contain %q", want)
		}
	}
	for _, unwanted := range []string{"ko-fi", "Donate", "donation", "browser"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("the About panel must not mention %q: it opens nothing", unwanted)
		}
	}
	// No key does anything here, so nothing can open a browser.
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Text: "d", Code: 'd'}, {Text: "o", Code: 'o'}} {
		if cmd := m.Update(k); cmd != nil {
			t.Fatalf("About.Update(%v) must not produce a command", k)
		}
	}
}
