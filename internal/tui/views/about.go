package views

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/simone-vibes/vibez/internal/openurl"
	"github.com/simone-vibes/vibez/internal/tui/styles"
	"github.com/simone-vibes/vibez/internal/version"
)

// DonateURL is the page Ctrl+Shift+D opens from the About panel: the original
// author's Ko-fi. It is the only website the player itself ever opens.
const DonateURL = "https://ko-fi.com/pelpsi"

// AboutModel renders the credits. Its one key, Ctrl+Shift+D, opens DonateURL
// through OpenURL, which is openurl.Open unless a test swaps in a recorder, so
// `go test` never starts a browser.
type AboutModel struct {
	width   int
	height  int
	status  string
	OpenURL func(url string) error
}

// AboutOpenErrMsg reports that the browser could not be started for DonateURL.
type AboutOpenErrMsg struct{ Err error }

func NewAbout() *AboutModel {
	return &AboutModel{OpenURL: openurl.Open}
}

func (a *AboutModel) SetSize(w, h int) {
	a.width = w
	a.height = h
}

// Update handles Ctrl+Shift+D, the only key About owns: it opens DonateURL in
// the browser and says so in the panel. Every other key does nothing; the
// model drops them before they get here, so no player command runs from About.
func (a *AboutModel) Update(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+shift+d", "ctrl+shift+D":
		if a.OpenURL == nil {
			a.status = "✗ No browser opener is available"
			return nil
		}
		a.status = "Opening " + DonateURL + " in your browser…"
		open := a.OpenURL
		return func() tea.Msg {
			if err := open(DonateURL); err != nil {
				return AboutOpenErrMsg{Err: err}
			}
			return nil
		}
	}
	return nil
}

// SetOpenError shows why the browser did not open (the AboutOpenErrMsg path).
func (a *AboutModel) SetOpenError(err error) {
	a.status = "✗ Could not open a browser: " + err.Error()
}

func (a *AboutModel) View() string {
	muted := styles.QueueItemMuted
	normal := lipgloss.NewStyle().Foreground(styles.ColorFg)
	primary := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
	secondary := lipgloss.NewStyle().Foreground(styles.ColorSecondary)

	// No header or rule: the panel is the centred credits alone.
	contentLines := []string{
		primary.Render("vibezAI ♪"),
		muted.Render(fmt.Sprintf("version %s", version.Version)),
		"",
		normal.Render("Apple Music in your terminal."),
		normal.Render("Vibe-driven. Keyboard-first."),
		"",
		secondary.Render("made with ") + styles.FavoriteActive.Render("❤") + secondary.Render(" by simonepelosi"),
		secondary.Render("updated with ") + styles.FavoriteActive.Render("❤") + secondary.Render(" by agf and Claude"),
		"",
		muted.Render("vibez is MIT licensed, © Simone Pelosi; this fork keeps the license."),
	}

	if a.status != "" {
		contentLines = append(contentLines, "", styles.ControlActive.Render(a.status))
	}

	var sb strings.Builder
	topPad := max(0, (a.height-len(contentLines))/2)
	for range topPad {
		sb.WriteByte('\n')
	}
	for _, line := range contentLines {
		sb.WriteString(centerStrAbout(line, a.width) + "\n")
	}
	return sb.String()
}

func centerStrAbout(s string, width int) string {
	w := lipgloss.Width(s)
	pad := max(0, (width-w)/2)
	return strings.Repeat(" ", pad) + s
}
