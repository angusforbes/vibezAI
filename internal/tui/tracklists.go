package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/queuestate"
	"github.com/simone-vibes/vibez/internal/tui/views"
)

// Named track lists. `:save <name>` writes the Tracks panel to
// <config dir>/tracklists/<name>.json in the queue.json format, and
// `:load <name>` puts such a list back into Tracks. The automatic queue.json
// (the previous session's Tracks, restored at launch) keeps working alongside:
// a loaded list becomes the queue, so it is what the next launch restores.

const trackListsDirName = "tracklists"

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

// savedTrackLists returns the names of the saved lists, sorted.
func (m *Model) savedTrackLists() []string {
	dir := m.trackListsDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}

// flashStatus shows msg in the status line for d.
func (m *Model) flashStatus(msg string, d time.Duration) {
	m.errMsg = msg
	m.errExpiry = time.Now().Add(d)
}

// saveTrackList is `:save <name>`: the Tracks panel, as it is, to a named list.
func (m *Model) saveTrackList(raw string) tea.Cmd {
	name, err := trackListName(raw)
	if err != nil {
		m.flashStatus(":save "+err.Error(), 3*time.Second)
		return nil
	}
	if m.trackListsDir() == "" {
		m.flashStatus(":save needs a config directory to write to", 3*time.Second)
		return nil
	}
	if len(m.queueTracks) == 0 {
		m.flashStatus("nothing to save: Tracks is empty", 3*time.Second)
		return nil
	}
	path := m.trackListPath(name)
	existed := !fileMissing(path)
	st := queuestate.FromTracks(m.queueTracks, m.currentQueueIndex())
	if err := queuestate.Save(path, st); err != nil {
		m.appendLog("[tracklist] save failed: " + err.Error())
		m.flashStatus(":save failed: "+err.Error(), 4*time.Second)
		return nil
	}
	verb := "saved"
	if existed {
		verb = "replaced"
	}
	m.appendLog(fmt.Sprintf("[tracklist] %s %q with %d track(s)", verb, name, len(st.Tracks)))
	m.flashStatus(fmt.Sprintf("✓ \"%s\" %s: %d tracks", name, verb, len(st.Tracks)), 4*time.Second)
	return nil
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

// listTrackLists is a bare `:load`: name the saved lists.
func (m *Model) listTrackLists() {
	names := m.savedTrackLists()
	if len(names) == 0 {
		m.flashStatus("no saved lists yet; :save <name> makes one", 4*time.Second)
		return
	}
	m.flashStatus("saved lists: "+strings.Join(names, ", "), 6*time.Second)
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
