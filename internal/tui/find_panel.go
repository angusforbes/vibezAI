package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/styles"
)

// The Find panel is the right column of the main view: Apple Music search,
// with the queue on the left staying visible the whole time. "/" focuses the
// search box; esc hands the keys back to the queue but leaves the query and
// results as they are. Tab adds the selected item to the end of the queue
// without playing it and Enter adds it and starts it; a track already in the
// queue is never added twice. Nothing in this panel ever replaces the queue.
// The vibe prompt and the discovery picker borrow the column while they have
// focus. (The library browser is not wired in; search covers the library.)

// findLines renders the right column.
func (m *Model) findLines(w, h int) []string {
	if m.vibe.IsFocused() || m.vibe.PickerActive() {
		return m.vibe.Lines(w, h, m.glowStep)
	}
	return m.searchFindLines(w, h)
}

// findHeader is the column title with the keys that matter.
func (m *Model) findHeader() string {
	muted := styles.QueueItemMuted
	return styles.Header.Render("Search") + muted.Render("     ") +
		styles.KeyName.Render("/") + muted.Render(" type  ") +
		styles.KeyName.Render("tab") + muted.Render(" add  ") +
		styles.KeyName.Render("enter") + muted.Render(" add & play  ") +
		styles.KeyName.Render("esc") + muted.Render(" back")
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
			listView = "  " + muted.Render("press / and type to search Apple Music and your library")
		}
	}
	lines := append([]string{m.findHeader(), inputLine, sep}, toLines(listView, listH)...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

// queueIndexOf returns the queue position of a playback id, or -1.
func (m *Model) queueIndexOf(id string) int {
	for i, qid := range m.queueIDs {
		if qid == id {
			return i
		}
	}
	return -1
}

// withoutQueued drops the tracks that are already in the queue (and repeats
// within the batch itself). A track is never queued twice.
func (m *Model) withoutQueued(tracks []provider.Track, ids []string) ([]provider.Track, []string) {
	if len(ids) != len(tracks) {
		return nil, nil
	}
	seen := make(map[string]bool, len(m.queueIDs)+len(ids))
	for _, id := range m.queueIDs {
		seen[id] = true
	}
	var nt []provider.Track
	var ni []string
	for i, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		nt = append(nt, tracks[i])
		ni = append(ni, id)
	}
	return nt, ni
}

// addToQueue (Tab) appends the tracks that are not queued yet without
// touching playback. When nothing is new, the highlight moves to the first
// of them instead, so "adding" a track twice takes you to it.
func (m *Model) addToQueue(label string, tracks []provider.Track, ids []string) tea.Cmd {
	if len(tracks) == 0 || len(ids) != len(tracks) {
		return nil
	}
	nt, ni := m.withoutQueued(tracks, ids)
	if len(ni) == 0 {
		if idx := m.queueIndexOf(ids[0]); idx >= 0 {
			m.setQueueCursor(idx)
		}
		m.errMsg = "ℹ Already in the queue: " + label
		m.errExpiry = time.Now().Add(3 * time.Second)
		return nil
	}
	m.queueTracks = append(m.queueTracks, nt...)
	m.queueIDs = append(m.queueIDs, ni...)
	m.syncQueue()
	skipped := len(ids) - len(ni)
	if skipped > 0 {
		m.appendLog(fmt.Sprintf("[queue] added %d track(s) (%s); %d already queued", len(ni), label, skipped))
	} else {
		m.appendLog(fmt.Sprintf("[queue] added %d track(s) (%s)", len(ni), label))
	}
	appended := append([]string(nil), ni...)
	return m.playerCmd(func(p player.Player) error { return p.AppendQueue(appended) })
}

// appendAndPlay (Enter) adds the tracks that are not queued yet and starts
// the one at offset. A track that is already queued is not added again: the
// existing entry is played instead. Nothing already queued is ever replaced.
func (m *Model) appendAndPlay(label string, tracks []provider.Track, ids []string, offset int) tea.Cmd {
	if len(tracks) == 0 || len(ids) != len(tracks) {
		return nil
	}
	if offset < 0 || offset >= len(ids) {
		offset = 0
	}
	targetID := ids[offset]
	nt, ni := m.withoutQueued(tracks, ids)
	if len(ni) == 0 {
		if idx := m.queueIndexOf(targetID); idx >= 0 {
			m.appendLog(fmt.Sprintf("[queue] %s is already queued; playing it", label))
			return m.jumpToQueueIndex(idx)
		}
		return nil
	}
	m.queueTracks = append(m.queueTracks, nt...)
	m.queueIDs = append(m.queueIDs, ni...)
	m.syncQueue()
	target := m.queueIndexOf(targetID)
	if target < 0 {
		target = len(m.queueIDs) - len(ni)
		targetID = m.queueIDs[target]
	}
	m.appendLog(fmt.Sprintf("[queue] added %d track(s) (%s) and playing #%d", len(ni), label, target+1))
	m.playerState.Loading = true
	m.playerState.Playing = false
	m.playerState.Position = 0
	m.queueFollow = true
	m.queueCursor = target
	m.ensureQueueCursorVisible()
	if m.playerState.Track == nil {
		all := append([]string(nil), m.queueIDs...)
		m.queueResumeIdx = noQueueCursor
		return m.playerCmd(func(p player.Player) error { return p.SetQueueAt(all, targetID) })
	}
	// One engine step: the list is re-synced by id and the new track starts
	// only once it is in place (an append followed by a play-by-index raced).
	return m.syncEngineQueue(targetID)
}
