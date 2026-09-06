package views

import (
	"errors"
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
	for _, want := range []string{"vibezAI ♪", "made with ❤ by simonepelosi", "updated with ❤ by agf and Claude"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expected view to contain %q", want)
		}
	}
	// No "About" header and no rule under it; nothing about donating until
	// the key is pressed.
	for _, unwanted := range []string{"About", "─", "ko-fi", "Donate", "donation", "browser"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("the About panel must not show %q", unwanted)
		}
	}
	// The block is centred vertically in the panel, so it starts with padding.
	lines := strings.Split(plain, "\n")
	if lines[0] != "" || len(lines) < 12 {
		t.Errorf("the credits are padded down to the middle, got %d lines starting %q", len(lines), lines[0])
	}
}

func TestAbout_OnlyCtrlShiftDOpensTheDonationPage(t *testing.T) {
	m := NewAbout()
	m.SetSize(80, 24)
	var opened []string
	m.OpenURL = func(u string) error { opened = append(opened, u); return nil }

	// Every other key does nothing and opens nothing.
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter}, {Text: "d", Code: 'd'}, {Text: "D", Code: 'D', Mod: tea.ModShift},
		{Code: 'd', Mod: tea.ModCtrl}, {Text: "o", Code: 'o'}, {Code: tea.KeySpace, Text: " "},
	} {
		if cmd := m.Update(k); cmd != nil {
			t.Fatalf("About.Update(%v) must not produce a command", k)
		}
	}
	if len(opened) != 0 {
		t.Fatalf("no browser before Ctrl+Shift+D, got %v", opened)
	}

	cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl | tea.ModShift})
	if cmd == nil {
		t.Fatal("Ctrl+Shift+D returns the command that opens the page")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("a successful open reports nothing, got %#v", msg)
	}
	if len(opened) != 1 || opened[0] != DonateURL {
		t.Fatalf("opened %v, want [%s]", opened, DonateURL)
	}
	if plain := stripANSI(m.View()); !strings.Contains(plain, "Opening "+DonateURL) {
		t.Errorf("the panel says what it is doing, got %q", plain)
	}

	// A failing opener is reported in the panel instead of vanishing.
	m.OpenURL = func(string) error { return errors.New("xdg-open: not found") }
	msg, ok := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl | tea.ModShift})().(AboutOpenErrMsg)
	if !ok {
		t.Fatalf("a failed open returns AboutOpenErrMsg, got %#v", msg)
	}
	m.SetOpenError(msg.Err)
	if plain := stripANSI(m.View()); !strings.Contains(plain, "Could not open a browser: xdg-open: not found") {
		t.Errorf("the failure is shown, got %q", plain)
	}
}
