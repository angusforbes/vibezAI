package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/styles"
)

// The Find panel is the right column of the main view. It shows either the
// Apple Music search (default) or the library; the queue on the left stays
// visible the whole time. "/" focuses the search box and "l" the library; esc
// hands the keys back to the queue but leaves the panel as it is. In both
// tabs Tab adds the selected item to the end of the queue without playing it
// and Enter adds it and starts it. Nothing in this panel ever replaces the
// queue. The vibe prompt and the discovery picker borrow the column while
// they have focus.

type findTab int

const (
	findSearch findTab = iota
	findLibrary
)

// findLines renders the right column.
func (m *Model) findLines(w, h int) []string {
	if m.vibe.IsFocused() || m.vibe.PickerActive() {
		return m.vibe.Lines(w, h, m.glowStep)
	}
	if m.findTab == findLibrary {
		return m.libraryFindLines(w, h)
	}
	return m.searchFindLines(w, h)
}

// findHeader is the tab strip: "Search · Library" with the active tab lit.
func (m *Model) findHeader() string {
	muted := styles.QueueItemMuted
	search, library := muted, muted
	if m.findTab == findLibrary {
		library = styles.Header
	} else {
		search = styles.Header
	}
	return search.Render("Search") + muted.Render("  ·  ") + library.Render("Library") +
		muted.Render("     ") + styles.KeyName.Render("/") + muted.Render(" search  ") + styles.KeyName.Render("l") + muted.Render(" library")
}

func (m *Model) searchFindLines(w, h int) []string {
	accent := lipgloss.NewStyle().Foreground(styles.ColorAccent)
	muted := lipgloss.NewStyle().Foreground(styles.ColorMuted)
	textStyle := lipgloss.NewStyle().Foreground(styles.ColorFg)

	runes := []rune(m.searchQuery)
	cur := min(m.searchCursor, len(runes))
	before := textStyle.Render(string(runes[:cur]))
	after := textStyle.Render(string(runes[cur:]))
	cursor := " "
	if m.mode == modeSearch {
		cursor = accent.Render("█")
	}
	inputLine := accent.Render("/") + " " + before + cursor + after
	sep := muted.Render(strings.Repeat("─", max(1, w)))

	listH := max(1, h-3) // header + input + rule
	m.search.SetSize(w, listH)
	listView := m.search.View()
	if listView == "" && !m.search.Loading() {
		if m.searchQuery != "" {
			listView = "  " + muted.Render("no results")
		} else {
			listView = "  " + muted.Render("press / and type to search Apple Music")
		}
	}
	lines := append([]string{m.findHeader(), inputLine, sep}, toLines(listView, listH)...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

func (m *Model) libraryFindLines(w, h int) []string {
	bodyH := max(1, h-1)
	m.library.SetSize(w, bodyH)
	lines := append([]string{m.findHeader()}, toLines(m.library.View(), bodyH)...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

// appendAndPlay adds tracks to the end of the queue and starts the one at
// offset within them, leaving everything already queued in place. When the
// engine has nothing loaded yet the whole queue is handed over and started at
// that track.
func (m *Model) appendAndPlay(label string, tracks []provider.Track, ids []string, offset int) tea.Cmd {
	if len(tracks) == 0 || len(ids) != len(tracks) {
		return nil
	}
	if offset < 0 || offset >= len(ids) {
		offset = 0
	}
	start := len(m.queueTracks)
	m.queueTracks = append(m.queueTracks, tracks...)
	m.queueIDs = append(m.queueIDs, ids...)
	m.syncQueue()
	target := start + offset
	m.appendLog(fmt.Sprintf("[queue] added %d track(s) (%s) and playing #%d", len(ids), label, target+1))
	m.playerState.Loading = true
	m.playerState.Playing = false
	m.playerState.Position = 0
	m.queueFollow = true
	m.queueCursor = target
	m.ensureQueueCursorVisible()
	targetID := ids[offset]
	if m.playerState.Track == nil {
		all := append([]string(nil), m.queueIDs...)
		m.queueResumeIdx = noQueueCursor
		return m.playerCmd(func(p player.Player) error { return p.SetQueueAt(all, targetID) })
	}
	// One engine step: the list is re-synced by id and the new track starts
	// only once it is in place (an append followed by a play-by-index raced).
	return m.syncEngineQueue(targetID)
}
