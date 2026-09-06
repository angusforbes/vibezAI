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
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// The Find panel is the right column of the main view: Apple Music search,
// with the queue on the left staying visible the whole time. "/" focuses the
// search box; esc hands the keys back to the queue but leaves the query and
// results as they are. Tab adds the selected item to the end of the queue
// without playing it and Enter adds it and starts it; a track already in the
// queue is never added twice. Nothing in this panel ever replaces the queue.
// (The library browser is not wired in; search covers the library.)

// findLines renders the right column.
func (m *Model) findLines(w, h int) []string {
	return m.searchFindLines(w, h)
}

// findHeader is the column title, underlined like the Queue's and bold while
// Search has the keys, followed by the mode the way the Queue shows its track
// count: "Apple Music" (plain search), "Claude Code" (vibes lookups) or
// "Saved lists" (the track lists kept with :save).
//
// While a lookup is in flight the mode text is the busy indicator: its colours
// run through the glow palette until the results land.
func (m *Model) findHeader() string {
	mode := "Apple Music"
	switch m.searchSrc {
	case searchClaude:
		mode = "Claude Code"
	case searchSaved:
		mode = "Saved lists"
	case searchFeed:
		mode = "Feed"
	}
	label := styles.QueueItemMuted.Render("  " + mode)
	if m.search.Loading() {
		label = "  " + views.RenderGlowTitle(mode, m.glowStep)
	}
	return m.panelTitle("Search", m.mode == modeSearch) + label
}

func (m *Model) searchFindLines(w, h int) []string {
	accent := lipgloss.NewStyle().Foreground(styles.ColorAccent)
	muted := lipgloss.NewStyle().Foreground(styles.ColorMuted)
	textStyle := lipgloss.NewStyle().Foreground(styles.ColorFg)

	glyph := "AM" // Apple Music's own search
	switch m.searchSrc {
	case searchClaude:
		glyph = "CC" // Claude Code plans these lookups
	case searchSaved:
		glyph = "SV" // the saved track lists
	case searchFeed:
		glyph = "FE" // Apple's recommendations
	}
	// The query wraps onto further rows instead of running off the right
	// edge; continuation rows are indented under the text. At most half the
	// column goes to the input, and the rows around the cursor win when even
	// that is not enough.
	runes := []rune(m.searchQuery)
	cur := min(m.searchCursor, len(runes))
	inputRows := wrapQuery(runes, cur, m.mode == modeSearch && m.searchTyping, max(1, w-3), max(1, (h-2)/2), textStyle, accent)
	for i, row := range inputRows {
		if i == 0 {
			inputRows[i] = accent.Render(glyph) + " " + row
		} else {
			inputRows[i] = "   " + row
		}
	}
	sep := styles.QueueItemMuted.Render(strings.Repeat("─", 5))

	listH := max(1, h-2-len(inputRows)) // header + underline + input rows
	m.search.SetSize(w, listH)
	listView := m.search.View()
	if m.search.Loading() {
		listView = "" // the animated mode text in the header says it all
	}
	if listView == "" && m.searchSrc == searchSaved {
		listView = "  " + muted.Render("no saved lists yet; :save makes one")
	}
	if listView == "" && m.searchSrc == searchFeed && !m.search.Loading() {
		listView = "  " + muted.Render("no recommendations")
	}
	if listView == "" && !m.search.Loading() && m.searchQuery != "" {
		listView = "  " + muted.Render("no results")
	}
	lines := append(append([]string{m.findHeader(), sep}, inputRows...), toLines(listView, listH)...)
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

// addToQueue (Ctrl+,) appends the tracks that are not queued yet without
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
		m.errMsg = "ℹ Already in Tracks: " + label
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
	if m.playerState.Track == nil {
		// The engine holds nothing yet (a fresh launch, or a restored queue
		// that has not started). Appending there would make it start playing
		// the new songs on its own, with a queue that lacks the rest of
		// Tracks. The add stays in the model; the first play hands the whole
		// of Tracks over, the way a restored queue starts.
		return nil
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

// wrapQuery lays the query out in rows of at most width cells with the cursor
// (a block while focused, a space otherwise) in its place in the text. When
// there are more rows than maxRows, the rows ending at the cursor's row are
// kept so the cursor is always on screen.
func wrapQuery(runes []rune, cur int, focused bool, width, maxRows int, text, accent lipgloss.Style) []string {
	type cell struct {
		r      rune
		cursor bool
	}
	cells := make([]cell, 0, len(runes)+1)
	for i := 0; i <= len(runes); i++ {
		if i == cur {
			cells = append(cells, cell{cursor: true})
		}
		if i < len(runes) {
			cells = append(cells, cell{r: runes[i]})
		}
	}
	var rows [][]cell
	for len(cells) > 0 {
		n := min(width, len(cells))
		rows = append(rows, cells[:n])
		cells = cells[n:]
	}
	cursorRow := 0
	for i, row := range rows {
		for _, c := range row {
			if c.cursor {
				cursorRow = i
			}
		}
	}
	if len(rows) > maxRows {
		end := max(cursorRow+1, maxRows)
		rows = rows[end-maxRows : end]
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var sb strings.Builder
		var run []rune
		flush := func() {
			if len(run) > 0 {
				sb.WriteString(text.Render(string(run)))
				run = run[:0]
			}
		}
		for _, c := range row {
			if c.cursor {
				flush()
				if focused {
					sb.WriteString(accent.Render("█"))
				} else {
					sb.WriteString(" ")
				}
				continue
			}
			run = append(run, c.r)
		}
		flush()
		out = append(out, sb.String())
	}
	return out
}
