package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/queuestate"
	"github.com/simone-vibes/vibez/internal/tui/views"
	"github.com/simone-vibes/vibez/internal/vibe"
)

// Named track lists. `:save [name]` writes the Tracks panel to
// <config dir>/tracklists/<name>.json in the queue.json format, and
// `:load [name]` puts such a list back into Tracks; inside `:load` the space
// key steps through the saved lists so nobody has to remember a name. The
// automatic queue.json (the previous session's Tracks, restored at launch)
// keeps working alongside: at launch it is also kept as the list named
// "last session", and a loaded list becomes the queue the next launch restores.

const (
	trackListsDirName = "tracklists"
	// lastSessionList is written at launch from the restored queue: the
	// Tracks panel as it was when the app was last quit.
	lastSessionList = "last session"
	// autoNameStamp prefixes an automatic list name: 2026-09-05_13-10_<name>.
	autoNameStamp = "2006-01-02_15-04"
)

// listNamer is what the Claude planner offers for naming a list of songs.
type listNamer interface {
	Available() bool
	NameList(ctx context.Context, lines []string) (string, error)
}

// trackListNamedMsg brings the automatic name back, with the Tracks snapshot
// taken when :save was typed so later edits do not leak into the saved list.
type trackListNamedMsg struct {
	stamp string
	short string
	state queuestate.State
	err   error
}

// trackListsDir is the directory the named lists live in; "" when the queue
// is not persisted at all, in which case the commands are refused.
func (m *Model) trackListsDir() string {
	if m.queueStatePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(m.queueStatePath), trackListsDirName)
}

// trackListName validates a typed list name, which doubles as the file name.
// Anything that could leave the directory is refused.
func trackListName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", errors.New("requires a name")
	case len(name) > 80:
		return "", errors.New("name is too long (80 characters at most)")
	case strings.HasPrefix(name, "."), strings.ContainsAny(name, "/\\\x00"):
		return "", errors.New(`name can't start with "." or contain / or \`)
	}
	return name, nil
}

func (m *Model) trackListPath(name string) string {
	return filepath.Join(m.trackListsDir(), name+".json")
}

// savedTrackLists returns the saved lists in the order the space key steps
// through them: the previous session's Tracks first, then the newest save
// first, names breaking ties.
func (m *Model) savedTrackLists() []string {
	dir := m.trackListsDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type list struct {
		name string
		mod  time.Time
	}
	var lists []list
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		l := list{name: strings.TrimSuffix(e.Name(), ".json")}
		if info, err := e.Info(); err == nil {
			l.mod = info.ModTime()
		}
		lists = append(lists, l)
	}
	sort.Slice(lists, func(i, j int) bool {
		a, b := lists[i], lists[j]
		if (a.name == lastSessionList) != (b.name == lastSessionList) {
			return a.name == lastSessionList
		}
		if !a.mod.Equal(b.mod) {
			return a.mod.After(b.mod)
		}
		return a.name < b.name
	})
	names := make([]string, len(lists))
	for i, l := range lists {
		names[i] = l.name
	}
	return names
}

// snapshotLastSession keeps the queue restored at launch as the "last
// session" list, or removes that list when there was nothing to restore.
func (m *Model) snapshotLastSession(st queuestate.State) {
	if m.trackListsDir() == "" {
		return
	}
	path := m.trackListPath(lastSessionList)
	if len(st.Tracks) == 0 {
		_ = os.Remove(path)
		return
	}
	if err := queuestate.Save(path, st); err != nil {
		m.appendLog("[tracklist] could not keep the previous session's Tracks: " + err.Error())
	}
}

// flashStatus shows msg in the status line for d.
func (m *Model) flashStatus(msg string, d time.Duration) {
	m.errMsg = msg
	m.errExpiry = time.Now().Add(d)
}

// cycleLoadName is the space key inside `:load`: instead of typing a space it
// steps through the saved lists, the previous session's Tracks first, then
// the newest saves, so nobody has to remember a name. While a name that is
// not a saved list is being typed, space is a space.
func (m *Model) cycleLoadName() bool {
	if m.cmdBuf != "load" && !strings.HasPrefix(m.cmdBuf, "load ") {
		return false
	}
	names := m.savedTrackLists()
	if len(names) == 0 {
		return false
	}
	next := 0
	if cur := strings.TrimSpace(strings.TrimPrefix(m.cmdBuf, "load")); cur != "" {
		i := slices.Index(names, cur)
		if i < 0 {
			return false
		}
		next = (i + 1) % len(names)
	}
	m.cmdBuf = "load " + names[next]
	return true
}

// saveTrackList is `:save [name]`: the Tracks panel, as it is, to a named
// list. Without a name the list names itself, see autoSaveTrackList.
func (m *Model) saveTrackList(raw string) tea.Cmd {
	if strings.TrimSpace(raw) == "" {
		return m.autoSaveTrackList()
	}
	name, err := trackListName(raw)
	if err != nil {
		m.flashStatus(":save "+err.Error(), 3*time.Second)
		return nil
	}
	if name == lastSessionList {
		m.flashStatus(fmt.Sprintf("\"%s\" is reserved for the Tracks of the previous session", lastSessionList), 4*time.Second)
		return nil
	}
	st, ok := m.trackListSnapshot()
	if ok {
		m.writeTrackList(name, st)
	}
	return nil
}

// trackListSnapshot is the Tracks panel as a saveable state, or a reason not.
func (m *Model) trackListSnapshot() (queuestate.State, bool) {
	if m.trackListsDir() == "" {
		m.flashStatus(":save needs a config directory to write to", 3*time.Second)
		return queuestate.State{}, false
	}
	if len(m.queueTracks) == 0 {
		m.flashStatus("nothing to save: Tracks is empty", 3*time.Second)
		return queuestate.State{}, false
	}
	return queuestate.FromTracks(m.queueTracks, m.currentQueueIndex()), true
}

// writeTrackList saves st under name and reports it in the status line.
func (m *Model) writeTrackList(name string, st queuestate.State) {
	path := m.trackListPath(name)
	existed := !fileMissing(path)
	if err := queuestate.Save(path, st); err != nil {
		m.appendLog("[tracklist] save failed: " + err.Error())
		m.flashStatus(":save failed: "+err.Error(), 4*time.Second)
		return
	}
	verb := "saved"
	if existed {
		verb = "replaced"
	}
	m.appendLog(fmt.Sprintf("[tracklist] %s %q with %d track(s)", verb, name, len(st.Tracks)))
	m.flashStatus(fmt.Sprintf("✓ \"%s\" %s: %d tracks", name, verb, len(st.Tracks)), 5*time.Second)
}

// autoSaveTrackList is a bare `:save`: the name is the date and time plus a
// few words about the songs, from Claude Code when the CC planner is in use
// and the CLI is there, otherwise from the artists and genres themselves.
// The snapshot is taken now; the file is written when the name arrives.
func (m *Model) autoSaveTrackList() tea.Cmd {
	st, ok := m.trackListSnapshot()
	if !ok {
		return nil
	}
	stamp := time.Now().Format(autoNameStamp)
	namer, ok := m.vibePlanner.(listNamer)
	if !ok || !namer.Available() {
		m.finishAutoSave(trackListNamedMsg{stamp: stamp, state: st})
		return nil
	}
	lines := trackLines(st.ProviderTracks(), 40)
	m.flashStatus("naming the list…", 45*time.Second)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		short, err := namer.NameList(ctx, lines)
		return trackListNamedMsg{stamp: stamp, short: short, state: st, err: err}
	}
}

// finishAutoSave writes the snapshot under the automatic name, naming it from
// the songs themselves when Claude gave nothing usable.
func (m *Model) finishAutoSave(msg trackListNamedMsg) {
	short := msg.short
	if msg.err != nil {
		m.appendLog("[tracklist] naming failed, naming it from the songs instead: " + msg.err.Error())
	}
	if short == "" {
		short = fallbackListName(msg.state.ProviderTracks())
	}
	m.writeTrackList(msg.stamp+"_"+short, msg.state)
}

// trackLines renders tracks as "artist — title" lines for the namer, at most n.
func trackLines(tracks []provider.Track, n int) []string {
	if len(tracks) > n {
		tracks = tracks[:n]
	}
	lines := make([]string, 0, len(tracks))
	for _, t := range tracks {
		lines = append(lines, strings.TrimSpace(t.Artist+" — "+t.Title))
	}
	return lines
}

// fallbackListName names a list from the songs alone: the artist behind at
// least half of it, else the most common genre (Apple's catch-all "Music"
// aside), else the most common artist "and others".
func fallbackListName(tracks []provider.Track) string {
	artists, genres := map[string]int{}, map[string]int{}
	for _, t := range tracks {
		if a := strings.TrimSpace(t.Artist); a != "" {
			artists[a]++
		}
		for _, g := range t.Genres {
			if g = strings.TrimSpace(g); g != "" && !strings.EqualFold(g, "music") {
				genres[g]++
			}
		}
	}
	artist, n := topKey(artists)
	genre, _ := topKey(genres)
	var candidates []string
	if artist != "" && n*2 >= len(tracks) {
		candidates = append(candidates, artist)
	}
	if genre != "" {
		candidates = append(candidates, genre)
	}
	if artist != "" {
		candidates = append(candidates, artist+" and others")
	}
	for _, c := range candidates {
		if name := vibe.CleanListName(c); name != "" {
			return name
		}
	}
	return "tracks"
}

// topKey is the most frequent key, ties broken alphabetically for stability.
func topKey(counts map[string]int) (string, int) {
	best, n := "", 0
	for k, c := range counts {
		if c > n || (c == n && k < best) {
			best, n = k, c
		}
	}
	return best, n
}

// loadTrackList is `:load <name>`: the saved list replaces Tracks. With the
// engine idle it behaves like the restore at launch: the list is shown and
// the first play starts it at its saved position. While the engine holds a
// track, the list is handed over at once and starts there, because the track
// that was playing is not in the list any more.
func (m *Model) loadTrackList(raw string) tea.Cmd {
	name, err := trackListName(raw)
	if err != nil {
		m.flashStatus(":load "+err.Error(), 3*time.Second)
		return nil
	}
	path := m.trackListPath(name)
	if m.trackListsDir() == "" || fileMissing(path) {
		m.flashStatus(fmt.Sprintf("no track list \"%s\"%s", name, m.savedListsHint()), 4*time.Second)
		return nil
	}
	st, err := queuestate.Load(path)
	if err != nil {
		m.appendLog("[tracklist] load failed: " + err.Error())
		m.flashStatus(":load failed: "+err.Error(), 4*time.Second)
		return nil
	}
	tracks := st.ProviderTracks()
	if len(tracks) == 0 {
		m.flashStatus(fmt.Sprintf("\"%s\" is empty", name), 3*time.Second)
		return nil
	}
	ids := make([]string, len(tracks))
	for i, t := range tracks {
		ids[i] = views.PlaybackID(t)
	}
	start := st.CurrentIndex
	if start < 0 || start >= len(tracks) {
		start = 0
	}
	m.queueTracks = tracks
	m.queueIDs = ids
	m.syncQueue()
	m.appendLog(fmt.Sprintf("[tracklist] loaded %q: %d track(s)", name, len(tracks)))
	m.flashStatus(fmt.Sprintf("✓ \"%s\" loaded: %d tracks", name, len(tracks)), 4*time.Second)
	if m.playerState.Track == nil {
		// Like a restored queue: nothing plays until asked, then from start.
		m.queueResumeIdx = start
		m.followPlayingTrack()
		return nil
	}
	m.queueResumeIdx = noQueueCursor
	m.playerState.Loading = true
	m.playerState.Playing = false
	m.playerState.Position = 0
	m.queueFollow = true
	m.queueCursor = start
	m.ensureQueueCursorVisible()
	return m.syncEngineQueue(ids[start])
}

// listTrackLists is a bare `:load` run with Enter: name the saved lists.
func (m *Model) listTrackLists() {
	names := m.savedTrackLists()
	if len(names) == 0 {
		m.flashStatus("no saved lists yet; :save makes one", 4*time.Second)
		return
	}
	m.flashStatus("saved lists: "+strings.Join(names, ", ")+" (space inside :load steps through them)", 6*time.Second)
}

func (m *Model) savedListsHint() string {
	names := m.savedTrackLists()
	if len(names) == 0 {
		return ""
	}
	return "; saved: " + strings.Join(names, ", ")
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}
