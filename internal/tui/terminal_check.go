package tui

import (
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Terminal requirement (J545). vibezAI runs inside tmux (vibez-toggle keeps the
// player alive there when its window closes), and two things only work when the
// terminal outside tmux can carry them through:
//   - keys without a legacy control code (Ctrl+' Ctrl+. Ctrl+, Ctrl+; Ctrl+Shift+D/G):
//     tmux asks the terminal for modifyOtherKeys ("extkeys"), which foot speaks
//     and kitty does not, so in kitty they arrive as plain ' . , ;
//   - the cover picture (sixel): foot draws it, kitty shows tmux's placeholder.
// foot is the supported terminal. When tmux reports a client without one of the
// two, the player says so once in its status line.

type terminalCheckMsg struct{ problem string }

// terminalCheckCmd asks tmux what the attached terminal can do. Outside tmux
// (or without a client yet) it reports nothing.
func terminalCheckCmd() tea.Cmd {
	if os.Getenv("TMUX") == "" {
		return nil
	}
	return func() tea.Msg {
		out, err := exec.Command("tmux", "display-message", "-p", "#{client_termname}|#{client_termfeatures}").Output()
		if err != nil {
			return terminalCheckMsg{}
		}
		return terminalCheckMsg{problem: terminalProblem(strings.TrimSpace(string(out)))}
	}
}

// terminalProblem turns tmux's "termname|feature,feature" for the client into
// a one-line warning, or "" when the terminal has what vibezAI needs.
func terminalProblem(s string) string {
	name, feats, ok := strings.Cut(s, "|")
	if !ok || name == "" {
		return ""
	}
	has := map[string]bool{}
	for _, f := range strings.Split(feats, ",") {
		has[f] = true
	}
	var missing []string
	if !has["extkeys"] || strings.Contains(name, "kitty") {
		missing = append(missing, "Ctrl+' Ctrl+. keys")
	}
	if !has["sixel"] {
		missing = append(missing, "cover pictures")
	}
	if len(missing) == 0 {
		return ""
	}
	return "this terminal (" + name + ") loses " + strings.Join(missing, " and ") + " through tmux — run vibezAI in foot (see README: Requirements)"
}

// applyTerminalCheck shows the warning for a while in the status line.
func (m *Model) applyTerminalCheck(msg terminalCheckMsg) {
	if msg.problem == "" || m.terminalWarned {
		return
	}
	m.terminalWarned = true
	m.errMsg = msg.problem
	m.errExpiry = time.Now().Add(12 * time.Second)
}
