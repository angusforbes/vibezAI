package views

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/simone-vibes/vibez/internal/tui/styles"
	"github.com/simone-vibes/vibez/internal/version"
)

// AboutModel renders the About & support information.
type AboutModel struct {
	width  int
	height int
	status string
}

func NewAbout() *AboutModel {
	return &AboutModel{}
}

func (a *AboutModel) SetSize(w, h int) {
	a.width = w
	a.height = h
}

// Update takes no keys: the About panel is read-only and never opens a
// browser (the app only ever reaches the web for Apple Music and Claude Code).
func (a *AboutModel) Update(_ tea.KeyPressMsg) tea.Cmd { return nil }

func (a *AboutModel) View() string {
	muted := styles.QueueItemMuted
	normal := lipgloss.NewStyle().Foreground(styles.ColorFg)
	header := styles.TabActive
	primary := lipgloss.NewStyle().Foreground(styles.ColorPrimary).Bold(true)
	secondary := lipgloss.NewStyle().Foreground(styles.ColorSecondary)

	var sb strings.Builder
	sb.WriteString(header.Render("About") + "\n")
	sb.WriteString(muted.Render(strings.Repeat("─", 5)) + "\n\n")

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

	neededHeight := len(contentLines)
	topPad := max(0, (a.height-3-neededHeight)/2)

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
