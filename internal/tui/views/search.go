package views

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/styles"
)

// PlayTracksMsg is emitted when the user selects a track to play.
// Track carries the metadata for an immediate (optimistic) UI update so the
// Now Playing view feels instant — audio startup latency is hidden from the user.
// When PlaylistID is set, the player uses SetPlaylist instead of SetQueue so
// MusicKit resolves the playlist natively without per-song catalog ID lookups.
type PlayTracksMsg struct {
	IDs        []string
	Tracks     []provider.Track // all tracks in the queue, parallel to IDs
	Track      *provider.Track  // first track, for instant UI update (may be nil)
	PlaylistID string           // non-empty → use SetPlaylist
	StartIdx   int              // start position within the playlist
}

// QueueTracksMsg is emitted when the user appends library tracks without
// interrupting playback.
type QueueTracksMsg struct {
	IDs      []string
	Tracks   []provider.Track
	Label    string
	PlayNext bool
}

// PlaybackID returns the best ID to use for MusicKit queue descriptors.
// Library tracks (IDs prefixed with "i.") must use their library ID directly
// so MusicKit never encounters a CONTENT_RESTRICTED error — the catalog copy
// of a track may be region-locked even when the user already owns it in their
// library. Catalog IDs are used for tracks not present in the user's library.
func PlaybackID(t provider.Track) string {
	// Library tracks must use their library ID directly — never CONTENT_RESTRICTED.
	if strings.HasPrefix(t.ID, "i.") {
		return t.ID
	}
	if t.CatalogID != "" {
		return t.CatalogID
	}
	return t.ID
}

// searchResultMsg is used internally by scheduleSearch (views package only).
type searchResultMsg struct {
	result *provider.SearchResult
	err    error
}

// searchRow is one entry in the unified search result list.
// Section headers are non-selectable; item rows (track/album/playlist) are selectable.
type searchRow struct {
	header   bool
	label    string // header text (only when header=true); section name for toggle rows
	track    *provider.Track
	album    *provider.Album
	playlist *provider.Playlist
	toggle   bool       // "+ 5 more" / "− 5 less" control row of a section
	more     bool       // toggle rows: true = the "more" control, false = "less"
	step     int        // toggle rows: how many items the control would add or remove (0 = nothing to do)
	note     string     // muted, non-selectable line (what a vibe lookup searched for)
	title    string     // header display text when it differs from label (the section key)
	list     *SavedList // saved-lists source: the list this header stands for
	group    bool       // feed source: a recommendation group's header
	child    bool       // a track listed under its opened album or playlist (one indented line)
}

// isItem reports whether this row is selectable: an item, a more/less toggle
// or a section header (enter on a header opens or folds the section).
func (r searchRow) isItem() bool {
	return r.header || r.track != nil || r.album != nil || r.playlist != nil || r.toggle
}

// rowLines returns the number of visual lines a row occupies. Playlists and
// the more/less toggles are one line; tracks and albums carry a detail line.
func rowLines(r searchRow) int {
	if r.header || r.toggle || r.playlist != nil || r.note != "" || r.child {
		return 1
	}
	return 2
}

// SavedList is one of the user's saved track lists, shown by the saved-lists
// source as its own foldable section.
type SavedList struct {
	Name   string
	Tracks []provider.Track
}

// feedSection is one recommendation group of the FE source: its albums and
// playlists as rows, in Apple's order, pointing into the backing slices.
type feedSection struct {
	label     string
	albums    []provider.Album
	playlists []provider.Playlist
	entries   []searchRow
}

// buildFeedSections turns the provider's recommendation groups into sections;
// duplicate group titles are numbered, empty groups are dropped.
func buildFeedSections(groups []provider.RecommendationGroup) []feedSection {
	seen := map[string]int{}
	out := make([]feedSection, 0, len(groups))
	for _, g := range groups {
		label := g.Title
		if label == "" {
			label = "Recommendations"
		}
		seen[label]++
		if seen[label] > 1 {
			label = fmt.Sprintf("%s (%d)", label, seen[label])
		}
		sec := feedSection{label: label}
		for _, it := range g.Items {
			switch it.Kind {
			case "album":
				sec.albums = append(sec.albums, provider.Album{ID: it.ID, Title: it.Title, Artist: it.Subtitle})
			case "playlist":
				sec.playlists = append(sec.playlists, provider.Playlist{ID: it.ID, Name: it.Title})
			}
		}
		ai, pi := 0, 0
		for _, it := range g.Items {
			switch it.Kind {
			case "album":
				sec.entries = append(sec.entries, searchRow{album: &sec.albums[ai]})
				ai++
			case "playlist":
				sec.entries = append(sec.entries, searchRow{playlist: &sec.playlists[pi]})
				pi++
			}
		}
		if len(sec.entries) > 0 {
			out = append(out, sec)
		}
	}
	return out
}

// SearchModel holds search results rendered as a unified multi-section list
// (Playlists, Albums, Library, Tracks) with keyboard navigation.
type SearchModel struct {
	provider provider.Provider
	results  *provider.SearchResult
	shown    map[string]int // items shown per section; absent = searchSectionCap
	rows     []searchRow
	cursor   int // row index of the currently highlighted item
	scroll   int // index of the first rendered row
	width    int
	height   int
	loading  bool
	err      error

	// vibe marks a result set produced by a vibe description: one "Vibes"
	// section instead of the Playlists/Albums/Library/Tracks split, with
	// vibeNote lines (planner, summary, terms) under the header.
	vibe      bool
	vibeTitle string // header text for the vibe section ("Fable 5.1"); "" = "Vibes"
	vibeNote  []string

	// saved marks the saved-lists source: one foldable section per list,
	// folded to its header by default and opened whole.
	saved bool
	lists []SavedList

	// feed is the FE source: Apple's personalised recommendations, one
	// section per group, albums and playlists as rows (nil = not this source).
	feed []feedSection

	// selected holds the keys (see selKey) of the songs, albums and playlists
	// picked for a multi-add (Shift+↑/↓ sweep, Shift+→ toggle); cleared when
	// the results change. stash keeps a selection cleared with Shift+← so the
	// same key can bring it back, as long as nothing else changed it.
	selected map[string]bool
	stash    map[string]bool

	// open marks the albums and playlists (by selKey) whose tracks are listed
	// under them; expansions caches what was fetched for each, so folding and
	// reopening costs nothing. Both reset with the results.
	open       map[string]bool
	expansions map[string]*expansion

	// Catalog Tracks paging: Apple answers at most 25 songs per request, so
	// "+ 5 more" fetches the next page once the loaded ones run out.
	catalogNext   int  // offset of the next page
	catalogMore   bool // a further page may exist
	paging        bool // a page fetch is in flight
	pendingReveal int  // items the in-flight page should reveal on arrival
}

func NewSearch(prov provider.Provider) *SearchModel {
	return &SearchModel{provider: prov}
}

func (m *SearchModel) Init() tea.Cmd { return nil }

func (m *SearchModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// Size reports the last SetSize.
func (m *SearchModel) Size() (w, h int) { return m.width, m.height }

// SetResults updates the model with a full search result (tracks + albums + playlists).
func (m *SearchModel) SetResults(result *provider.SearchResult, loading bool, err error) {
	m.loading = loading
	m.err = err
	m.results = result
	m.vibe, m.vibeTitle, m.vibeNote = false, "", nil
	m.saved, m.lists = false, nil
	m.feed = nil
	m.selected, m.stash = nil, nil
	m.open, m.expansions = nil, nil
	m.shown = nil // a new result set starts at the default count per section
	m.catalogNext, m.catalogMore = 0, false
	m.paging, m.pendingReveal = false, 0
	if result != nil {
		m.catalogNext, m.catalogMore = result.CatalogNext, result.CatalogMore
	}
	m.rebuildRows()
	m.cursor = m.firstEntryRow()
	m.scroll = 0
}

// SetVibeResults shows the songs found for a vibe description as a single
// section with the usual "+ 5 more" / "− 5 less" controls. The header reads
// title (the model that planned the lookup; "" means "Vibes") and the note
// lines (the summary) sit under it and cannot be selected.
func (m *SearchModel) SetVibeResults(tracks []provider.Track, title string, note ...string) {
	m.loading = false
	m.err = nil
	m.results = &provider.SearchResult{Tracks: tracks}
	m.vibe = true
	m.vibeTitle = title
	m.vibeNote = note
	m.saved, m.lists = false, nil
	m.feed = nil
	m.selected, m.stash = nil, nil
	m.open, m.expansions = nil, nil
	m.shown = nil
	m.catalogNext, m.catalogMore = 0, false
	m.paging, m.pendingReveal = false, 0
	m.rebuildRows()
	m.cursor = m.firstEntryRow()
	m.scroll = 0
}

// VibeResults reports whether the list shows a vibe result set.
func (m *SearchModel) VibeResults() bool { return m.vibe }

// SetSavedLists shows the user's saved track lists, one foldable section
// each, all folded to their headers. Enter on a header opens the list whole;
// the header stands for the whole list when adding or marking.
func (m *SearchModel) SetSavedLists(lists []SavedList) {
	// A refresh while the lists are already showing keeps what is open and
	// the highlighted list; coming from elsewhere every list starts folded.
	keep, shown := "", m.shown
	if m.saved {
		if l := m.SelectedSavedList(); l != nil {
			keep = l.Name
		}
	} else {
		shown = nil
	}
	m.loading, m.err = false, nil
	m.results = nil
	m.vibe, m.vibeTitle, m.vibeNote = false, "", nil
	m.saved, m.lists = true, lists
	m.feed = nil
	m.selected, m.stash = nil, nil
	m.open, m.expansions = nil, nil
	m.shown = shown
	m.catalogNext, m.catalogMore = 0, false
	m.paging, m.pendingReveal = false, 0
	m.rebuildRows()
	m.cursor = m.advance(-1, 1)
	m.scroll = 0
	if keep != "" {
		for i, r := range m.rows {
			if r.list != nil && r.list.Name == keep {
				m.cursor = i
				break
			}
		}
		m.ensureCursorVisible()
	}
}

// SavedListIndex is the position of the highlighted header's list, or -1.
func (m *SearchModel) SavedListIndex() int {
	l := m.SelectedSavedList()
	if l == nil {
		return -1
	}
	for i := range m.lists {
		if &m.lists[i] == l {
			return i
		}
	}
	return -1
}

// SelectSavedList puts the highlight on the header of list i, clamped to the
// lists there are.
func (m *SearchModel) SelectSavedList(i int) {
	if len(m.lists) == 0 {
		return
	}
	name := m.lists[max(0, min(i, len(m.lists)-1))].Name
	for r, row := range m.rows {
		if row.list != nil && row.list.Name == name {
			m.cursor = r
			m.ensureCursorVisible()
			return
		}
	}
}

// SavedLists reports whether the list shows the saved-lists source.
func (m *SearchModel) SavedLists() bool { return m.saved }

// SetFeed shows the personalised recommendations: one section per group,
// albums and playlists as rows, five at a time with the usual controls, so
// they are browsed, marked and added exactly like search hits.
func (m *SearchModel) SetFeed(groups []provider.RecommendationGroup) {
	m.loading, m.err = false, nil
	m.results = nil
	m.vibe, m.vibeTitle, m.vibeNote = false, "", nil
	m.saved, m.lists = false, nil
	m.feed = buildFeedSections(groups)
	m.selected, m.stash = nil, nil
	m.open, m.expansions = nil, nil
	m.shown = nil
	m.catalogNext, m.catalogMore = 0, false
	m.paging, m.pendingReveal = false, 0
	m.rebuildRows()
	m.cursor = m.firstEntryRow()
	m.scroll = 0
}

// Feed reports whether the list shows the recommendations source.
func (m *SearchModel) Feed() bool { return m.feed != nil }

// SelectedSavedList returns the saved list whose header is highlighted, or nil.
func (m *SearchModel) SelectedSavedList() *SavedList {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].list
}

// firstEntryRow is the first selectable row that is not a header, so a new
// result set starts on its first match rather than on a section title.
func (m *SearchModel) firstEntryRow() int {
	for i, r := range m.rows {
		if r.isItem() && !r.header {
			return i
		}
	}
	return m.advance(-1, 1)
}

// SelectedHeader reports the section whose header is highlighted.
func (m *SearchModel) SelectedHeader() (string, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) || !m.rows[m.cursor].header {
		return "", false
	}
	return m.rows[m.cursor].label, true
}

// ToggleSectionOpen (enter on a header) folds an open section to nothing or
// reopens a folded one at the default count. The highlight stays on the header.
func (m *SearchModel) ToggleSectionOpen(section string) {
	total := m.sectionTotal(section)
	if total == 0 {
		return
	}
	if m.shown == nil {
		m.shown = map[string]int{}
	}
	switch {
	case m.shownCount(section, total) > 0:
		m.shown[section] = 0
	case m.saved:
		m.shown[section] = total // a saved list opens whole
	default:
		m.shown[section] = searchSectionCap
	}
	m.rebuildRows()
	for i, r := range m.rows {
		if r.header && r.label == section {
			m.cursor = i
			break
		}
	}
	m.ensureCursorVisible()
}

// SetState is kept for backward compatibility with callers that only have tracks.
func (m *SearchModel) SetState(tracks []provider.Track, loading bool, err error) {
	var result *provider.SearchResult
	if tracks != nil {
		result = &provider.SearchResult{Tracks: tracks}
	}
	m.SetResults(result, loading, err)
}

// SelectedToggle reports whether a section's "+ 5 more" (more=true) or
// "− 5 less" (more=false) row is highlighted.
func (m *SearchModel) SelectedToggle() (section string, more bool, ok bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) || !m.rows[m.cursor].toggle {
		return "", false, false
	}
	r := m.rows[m.cursor]
	return r.label, r.more, true
}

// sectionTotal returns how many results a section has.
func (m *SearchModel) sectionTotal(section string) int {
	if m.feed != nil {
		for _, sec := range m.feed {
			if sec.label == section {
				return len(sec.entries)
			}
		}
		return 0
	}
	if m.saved {
		for _, l := range m.lists {
			if l.Name == section {
				return len(l.Tracks)
			}
		}
		return 0
	}
	if m.results == nil {
		return 0
	}
	switch section {
	case "Playlists":
		return len(m.results.Playlists)
	case "Albums":
		return len(m.results.Albums)
	case "Vibes":
		return len(m.results.Tracks)
	}
	lib, cat := 0, 0
	for _, t := range m.results.Tracks {
		if isLibraryTrack(t) {
			lib++
		} else {
			cat++
		}
	}
	if section == "Library" {
		return lib
	}
	return cat
}

// shownCount returns how many items of a section are currently shown.
func (m *SearchModel) shownCount(section string, total int) int {
	n, ok := m.shown[section]
	if !ok {
		n = searchSectionCap
		if m.saved {
			n = 0 // a saved list starts folded to its header
		}
	}
	return max(0, min(n, total))
}

// ShowMore reveals up to searchSectionCap further items of a section. For
// the catalog Tracks section it reports how many of those it could not
// reveal because they have to come from Apple's next page (0 when the loaded
// results sufficed or no further page can exist); the caller then starts
// that fetch with BeginPaging.
func (m *SearchModel) ShowMore(section string) (wanted int) {
	total := m.sectionTotal(section)
	before := m.shownCount(section, total)
	m.adjustShown(section, searchSectionCap)
	revealed := m.shownCount(section, total) - before
	if section != "Tracks" || !m.catalogMore || m.paging {
		return 0
	}
	return searchSectionCap - revealed
}

// BeginPaging marks a catalog page fetch as in flight and returns the offset
// to request; ok is false when no page can exist or one is already loading.
func (m *SearchModel) BeginPaging(wanted int) (offset int, ok bool) {
	if !m.catalogMore || m.paging || m.results == nil {
		return 0, false
	}
	m.paging = true
	m.pendingReveal = wanted
	m.rebuildRows()
	return m.catalogNext, true
}

// EndPaging clears the in-flight state after a failed page fetch.
func (m *SearchModel) EndPaging() {
	m.paging, m.pendingReveal = false, 0
	m.rebuildRows()
	m.clampCursor()
}

// Paging reports whether a catalog page fetch is in flight.
func (m *SearchModel) Paging() bool { return m.paging }

// AppendCatalogTracks merges a further page into the Tracks section, skipping
// songs already listed (by id or artist/title), reveals what the user asked
// for with "+ 5 more" and returns how many of those are still owed because
// the page did not carry enough new songs (0 when satisfied or exhausted).
func (m *SearchModel) AppendCatalogTracks(page provider.SongPage) (stillWanted int) {
	m.paging = false
	if m.results == nil { // a new query replaced these results meanwhile
		m.pendingReveal = 0
		return 0
	}
	seen := make(map[string]bool, 2*len(m.results.Tracks))
	for _, t := range m.results.Tracks {
		seen[trackKey(t)] = true
		seen[PlaybackID(t)] = true
	}
	added := 0
	for _, t := range page.Tracks {
		if seen[trackKey(t)] || seen[PlaybackID(t)] || isLibraryTrack(t) {
			continue
		}
		seen[trackKey(t)] = true
		seen[PlaybackID(t)] = true
		m.results.Tracks = append(m.results.Tracks, t)
		added++
	}
	m.catalogNext, m.catalogMore = page.Next, page.More
	if reveal := min(m.pendingReveal, added); reveal > 0 {
		total := m.sectionTotal("Tracks")
		if m.shown == nil {
			m.shown = map[string]int{}
		}
		m.shown["Tracks"] = min(total, m.shownCount("Tracks", total-added)+reveal)
		m.pendingReveal -= reveal
	}
	if !m.catalogMore {
		m.pendingReveal = 0
	}
	key := ""
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		key = m.rows[m.cursor].key()
	}
	m.rebuildRows()
	m.reselect(key)
	return m.pendingReveal
}

// trackKey identifies a song across library and catalog copies.
func trackKey(t provider.Track) string {
	return strings.ToLower(t.Artist) + "§" + strings.ToLower(t.Title)
}

// key names a row so the highlight can find it again after rows are rebuilt.
func (r searchRow) key() string {
	switch {
	case r.header:
		return "h:" + r.label
	case r.toggle:
		if r.more {
			return "+:" + r.label
		}
		return "-:" + r.label
	case r.track != nil:
		return "t:" + PlaybackID(*r.track)
	case r.album != nil:
		return "a:" + r.album.ID
	case r.playlist != nil:
		return "p:" + r.playlist.ID
	}
	return ""
}

// reselect puts the highlight back on the row named by key; a vanished
// "+ more" control falls back to its section's "− less" control, anything
// else stays where it was (clamped).
func (m *SearchModel) reselect(key string) {
	if key != "" {
		for i, r := range m.rows {
			if r.key() == key {
				m.cursor = i
				m.ensureCursorVisible()
				return
			}
		}
		if strings.HasPrefix(key, "+:") {
			for i, r := range m.rows {
				if r.toggle && !r.more && r.label == key[2:] {
					m.cursor = i
					m.ensureCursorVisible()
					return
				}
			}
		}
	}
	m.clampCursor()
}

// clampCursor keeps the highlight on an existing selectable row.
func (m *SearchModel) clampCursor() {
	if len(m.rows) == 0 {
		m.cursor, m.scroll = 0, 0
		return
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if !m.rows[m.cursor].isItem() {
		m.cursor = m.advance(m.cursor, -1)
	}
	m.ensureCursorVisible()
}

// ShowLess hides the last up-to-searchSectionCap items of a section; a
// section can fold down to just its header and controls.
func (m *SearchModel) ShowLess(section string) { m.adjustShown(section, -searchSectionCap) }

func (m *SearchModel) adjustShown(section string, delta int) {
	total := m.sectionTotal(section)
	if total == 0 {
		return
	}
	if m.shown == nil {
		m.shown = map[string]int{}
	}
	m.shown[section] = max(0, min(total, m.shownCount(section, total)+delta))
	more := delta > 0
	m.rebuildRows()
	// Stay on the control that was used; when it has disappeared (nothing
	// left to reveal, or nothing left to hide) land on the section's other
	// control, and when the section folded to its header alone, on the header.
	target := -1
	for i, r := range m.rows {
		if r.label != section {
			continue
		}
		if r.toggle && r.more == more {
			target = i
			break
		}
		if target < 0 && (r.toggle || r.header) {
			target = i
		} else if target >= 0 && m.rows[target].header && r.toggle {
			target = i
		}
	}
	if target >= 0 {
		m.cursor = target
	}
	m.ensureCursorVisible()
}

// sectionRows appends a header, the shown items and the control rows: "+ 5
// more" only while something is hidden and the section is open, "− 5 less"
// only while something is shown. A folded section is just its header; enter
// on it opens it again.
//
// The catalog Tracks section keeps its "+ 5 more" while Apple may hold a
// further page, even when everything loaded so far is on screen.
func (m *SearchModel) sectionRows(label string, total int, item func(i int) searchRow) {
	pageable := label == "Tracks" && m.catalogMore
	if total == 0 && !pageable {
		return
	}
	n := m.shownCount(label, total)
	m.rows = append(m.rows, searchRow{header: true, label: label})
	for i := range n {
		row := item(i)
		m.rows = append(m.rows, row)
		m.rows = append(m.rows, m.childRows(row)...)
	}
	switch {
	case n > 0 && n < total:
		step := min(searchSectionCap, total-n)
		if pageable {
			step = searchSectionCap
		}
		m.rows = append(m.rows, searchRow{toggle: true, label: label, more: true, step: step})
	case pageable && (n > 0 || total == 0):
		m.rows = append(m.rows, searchRow{toggle: true, label: label, more: true, step: searchSectionCap})
	}
	if n > 0 {
		m.rows = append(m.rows, searchRow{toggle: true, label: label, more: false, step: min(searchSectionCap, n)})
	}
}

// searchSectionCap is how many items a section shows by default and the step
// its "+ more" / "− less" controls add or remove.
const searchSectionCap = 5

// expansion is what an opened album or playlist shows under its row.
type expansion struct {
	tracks  []provider.Track
	loading bool
	err     error
}

// ExpandRequest names a collection whose tracks have to be fetched.
type ExpandRequest struct {
	Key      string
	Album    *provider.Album
	Playlist *provider.Playlist
}

// childRows lists the tracks of an opened album or playlist under its row:
// one indented line each while loaded, a muted note while loading or on error.
func (m *SearchModel) childRows(parent searchRow) []searchRow {
	if parent.album == nil && parent.playlist == nil {
		return nil
	}
	key := selKey(parent)
	if !m.open[key] {
		return nil
	}
	exp := m.expansions[key]
	switch {
	case exp == nil || exp.loading:
		return []searchRow{{note: "    loading…"}}
	case exp.err != nil:
		return []searchRow{{note: "    " + exp.err.Error()}}
	case len(exp.tracks) == 0:
		return []searchRow{{note: "    no playable tracks"}}
	}
	out := make([]searchRow, 0, len(exp.tracks))
	for i := range exp.tracks {
		out = append(out, searchRow{track: &exp.tracks[i], child: true})
	}
	return out
}

// ToggleCollection opens the highlighted album or playlist so its tracks are
// listed under it, or folds it again. It reports a fetch request the first
// time a collection is opened; afterwards the tracks are kept and reopening
// is immediate. On any other row nothing happens.
func (m *SearchModel) ToggleCollection() (req ExpandRequest, fetch bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return ExpandRequest{}, false
	}
	row := m.rows[m.cursor]
	if row.album == nil && row.playlist == nil {
		return ExpandRequest{}, false
	}
	key := selKey(row)
	if m.open == nil {
		m.open = map[string]bool{}
	}
	m.open[key] = !m.open[key]
	if m.open[key] && m.expansions[key] == nil {
		if m.expansions == nil {
			m.expansions = map[string]*expansion{}
		}
		m.expansions[key] = &expansion{loading: true}
		fetch = true
		req = ExpandRequest{Key: key, Album: row.album, Playlist: row.playlist}
	}
	m.rebuildRows()
	m.reselect(row.key())
	return req, fetch
}

// SetCollectionTracks delivers what a ToggleCollection fetch found.
func (m *SearchModel) SetCollectionTracks(key string, tracks []provider.Track, err error) {
	if m.expansions == nil || m.expansions[key] == nil {
		return // the results changed meanwhile
	}
	m.expansions[key] = &expansion{tracks: tracks, err: err}
	cur := ""
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		cur = m.rows[m.cursor].key()
	}
	m.rebuildRows()
	m.reselect(cur)
}

// selectedExpansionTracks lists the marked tracks of opened collections, in
// row order, skipping ids already listed.
func (m *SearchModel) selectedExpansionTracks(seen map[string]bool) []SelectedItem {
	var out []SelectedItem
	for i := range m.rows {
		r := m.rows[i]
		if !r.child || r.track == nil {
			continue
		}
		key := PlaybackID(*r.track)
		if m.selected[key] && !seen[key] {
			seen[key] = true
			out = append(out, SelectedItem{Track: m.rows[i].track})
		}
	}
	return out
}

// isLibraryTrack reports whether a search hit is the user's own library copy.
func isLibraryTrack(t provider.Track) bool {
	return strings.HasPrefix(t.ID, "i.")
}

// rebuildRows lays the results out as Playlists, Albums, Library (tracks the
// user owns) and Tracks (catalog). Each section starts at searchSectionCap
// items and grows or shrinks in steps of five through its control rows.
func (m *SearchModel) rebuildRows() {
	m.rows = nil
	if m.feed != nil {
		m.rebuildFeedRows()
		return
	}
	if m.saved {
		m.rebuildSavedRows()
		return
	}
	if m.results == nil {
		return
	}
	res := m.results
	if m.vibe {
		m.sectionRows("Vibes", len(res.Tracks), func(i int) searchRow { return searchRow{track: &res.Tracks[i]} })
		if len(m.rows) == 0 { // nothing found: still say what was tried
			m.rows = append(m.rows, searchRow{header: true, label: "Vibes"})
		}
		m.rows[0].title = m.vibeTitle
		notes := make([]searchRow, 0, len(m.vibeNote)+len(m.rows))
		for _, n := range m.vibeNote {
			notes = append(notes, searchRow{note: n})
		}
		m.rows = append(m.rows[:1], append(notes, m.rows[1:]...)...)
		return
	}
	m.sectionRows("Playlists", len(res.Playlists), func(i int) searchRow { return searchRow{playlist: &res.Playlists[i]} })
	m.sectionRows("Albums", len(res.Albums), func(i int) searchRow { return searchRow{album: &res.Albums[i]} })
	var library, catalog []*provider.Track
	for i := range res.Tracks {
		t := &res.Tracks[i]
		if isLibraryTrack(*t) {
			library = append(library, t)
		} else {
			catalog = append(catalog, t)
		}
	}
	m.sectionRows("Library", len(library), func(i int) searchRow { return searchRow{track: library[i]} })
	m.sectionRows("Tracks", len(catalog), func(i int) searchRow { return searchRow{track: catalog[i]} })
}

// rebuildFeedRows lays the recommendations out as one section per group,
// with the usual five-at-a-time controls.
func (m *SearchModel) rebuildFeedRows() {
	for i := range m.feed {
		sec := &m.feed[i]
		header := len(m.rows)
		m.sectionRows(sec.label, len(sec.entries), func(j int) searchRow { return sec.entries[j] })
		if header < len(m.rows) && m.rows[header].header {
			m.rows[header].group = true
		}
	}
}

// rebuildSavedRows lays the saved lists out as one section each. The header
// names the list and its size; enter opens it to all of its songs or folds it
// back. There are no "+ 5 more" rows here: a list is seen whole.
func (m *SearchModel) rebuildSavedRows() {
	for li := range m.lists {
		l := &m.lists[li]
		size := fmt.Sprintf("%d tracks", len(l.Tracks))
		if len(l.Tracks) == 1 {
			size = "1 track"
		}
		m.rows = append(m.rows, searchRow{header: true, label: l.Name, title: l.Name + "  ·  " + size, list: l})
		if m.shownCount(l.Name, len(l.Tracks)) == 0 {
			continue
		}
		for i := range l.Tracks {
			m.rows = append(m.rows, searchRow{track: &l.Tracks[i]})
		}
	}
}

// advance returns the index of the next selectable row in direction dir (+1/-1)
// starting from `from`. Returns `from` if no selectable row is found (or 0
// when from==-1 and there are no items).
func (m *SearchModel) advance(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].isItem() {
			return i
		}
	}
	if from < 0 {
		return 0
	}
	return from
}

// ensureCursorVisible adjusts m.scroll so the cursor row is fully visible
// within m.height visual lines.
func (m *SearchModel) ensureCursorVisible() {
	h := max(1, m.height)
	// Cursor is above the scroll window: scroll up to it.
	if m.cursor < m.scroll {
		m.scroll = m.cursor
		// If the row directly above the cursor is a section header, pull it
		// into view too — otherwise the first item in a section appears with
		// its header clipped off.
		if m.scroll > 0 && m.rows[m.scroll-1].header {
			m.scroll--
		}
		return
	}
	// Count visual lines from the scroll start through the cursor row (inclusive).
	lines := 0
	for i := m.scroll; i <= m.cursor && i < len(m.rows); i++ {
		lines += rowLines(m.rows[i])
	}
	// Scroll forward until the cursor row fits inside the available height.
	for lines > h && m.scroll < m.cursor {
		lines -= rowLines(m.rows[m.scroll])
		m.scroll++
	}
	// After any scroll adjustment, if the section header for the topmost
	// visible item sits just above the viewport AND there is room for it,
	// pull it in.  This handles the case where cursor == scroll (cursor is
	// exactly at the top of the viewport but its section header got clipped).
	if m.scroll > 0 && m.rows[m.scroll-1].header && lines+1 <= h {
		m.scroll--
	}
}

// Results returns the current track list (backward compat).
func (m *SearchModel) Results() []provider.Track {
	if m.results == nil {
		return nil
	}
	return m.results.Tracks
}

// Loading returns whether a search is in progress.
func (m *SearchModel) Loading() bool { return m.loading }

// SelectedTrack returns the highlighted track, or nil when an album/playlist is selected.
func (m *SearchModel) SelectedTrack() *provider.Track {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].track
}

// SelectedItem is one entry of the multi-selection: exactly one field is set.
type SelectedItem struct {
	Track    *provider.Track
	Album    *provider.Album
	Playlist *provider.Playlist
	List     *SavedList // a whole saved list (its header, in the saved-lists source)
}

// selKey identifies a selectable row across rebuilds.
func selKey(r searchRow) string {
	switch {
	case r.list != nil:
		return "list:" + r.list.Name
	case r.track != nil:
		return PlaybackID(*r.track)
	case r.album != nil:
		return "album:" + r.album.ID
	case r.playlist != nil:
		return "playlist:" + r.playlist.ID
	}
	return ""
}

// IsSelected reports whether a track is part of the multi-selection.
func (m *SearchModel) IsSelected(t provider.Track) bool { return m.selected[PlaybackID(t)] }

// SelectionCount is how many songs, albums and playlists are multi-selected.
func (m *SearchModel) SelectionCount() int { return len(m.selected) }

// ClearSelection drops the multi-selection, keeping it for RestoreSelection.
func (m *SearchModel) ClearSelection() {
	if len(m.selected) > 0 {
		m.stash = m.selected
	}
	m.selected = nil
}

// RestoreSelection brings back the selection last cleared, provided nothing
// has been selected or deselected since. It reports whether it did.
func (m *SearchModel) RestoreSelection() bool {
	if len(m.selected) > 0 || m.stash == nil {
		return false
	}
	m.selected, m.stash = m.stash, nil
	return true
}

// CanRestoreSelection reports whether Shift+← would bring a selection back.
func (m *SearchModel) CanRestoreSelection() bool { return len(m.selected) == 0 && m.stash != nil }

// ToggleSelected adds the highlighted song, album or playlist to the
// multi-selection or takes it out again; headers and controls are ignored.
func (m *SearchModel) ToggleSelected() {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return
	}
	key := selKey(m.rows[m.cursor])
	if key == "" {
		return
	}
	m.stash = nil
	if m.selected[key] {
		delete(m.selected, key)
		return
	}
	if m.selected == nil {
		m.selected = map[string]bool{}
	}
	m.selected[key] = true
}

// SelectAndMove selects the highlighted item, moves the highlight to the next
// song, album or playlist in dir (+1 down, −1 up), skipping headers and
// controls, and selects that too, so a sweep with Shift+↑/↓ covers everything
// passed over.
func (m *SearchModel) SelectAndMove(dir int) {
	m.pick(m.cursor)
	for next := m.advance(m.cursor, dir); next != m.cursor; next = m.advance(m.cursor, dir) {
		m.cursor = next
		if selKey(m.rows[next]) != "" {
			break
		}
	}
	m.ensureCursorVisible()
	m.pick(m.cursor)
}

func (m *SearchModel) pick(row int) {
	if row < 0 || row >= len(m.rows) {
		return
	}
	key := selKey(m.rows[row])
	if key == "" {
		return
	}
	m.stash = nil
	if m.selected == nil {
		m.selected = map[string]bool{}
	}
	m.selected[key] = true
}

// SelectedItems returns the multi-selection in result order — playlists,
// then albums, then songs — folded or not yet revealed entries included.
func (m *SearchModel) SelectedItems() []SelectedItem {
	if len(m.selected) == 0 {
		return nil
	}
	if m.saved {
		return m.selectedSavedItems()
	}
	if m.feed != nil {
		return m.selectedFeedItems()
	}
	if m.results == nil {
		return nil
	}
	res := m.results
	out := make([]SelectedItem, 0, len(m.selected))
	for i := range res.Playlists {
		if m.selected["playlist:"+res.Playlists[i].ID] {
			out = append(out, SelectedItem{Playlist: &res.Playlists[i]})
		}
	}
	for i := range res.Albums {
		if m.selected["album:"+res.Albums[i].ID] {
			out = append(out, SelectedItem{Album: &res.Albums[i]})
		}
	}
	seen := map[string]bool{}
	for i := range res.Tracks {
		if key := PlaybackID(res.Tracks[i]); m.selected[key] {
			seen[key] = true
			out = append(out, SelectedItem{Track: &res.Tracks[i]})
		}
	}
	return append(out, m.selectedExpansionTracks(seen)...)
}

// selectedSavedItems is the multi-selection of the saved-lists source: whole
// lists first, then songs, in list order, a song only once even when it sits
// in several lists.
func (m *SearchModel) selectedSavedItems() []SelectedItem {
	out := make([]SelectedItem, 0, len(m.selected))
	seen := map[string]bool{}
	for li := range m.lists {
		if l := &m.lists[li]; m.selected["list:"+l.Name] {
			out = append(out, SelectedItem{List: l})
		}
	}
	for li := range m.lists {
		for i := range m.lists[li].Tracks {
			t := &m.lists[li].Tracks[i]
			if key := PlaybackID(*t); m.selected[key] && !seen[key] {
				seen[key] = true
				out = append(out, SelectedItem{Track: t})
			}
		}
	}
	return out
}

// selectedFeedItems is the multi-selection of the FE source: the marked
// albums and playlists in the groups' order.
func (m *SearchModel) selectedFeedItems() []SelectedItem {
	out := make([]SelectedItem, 0, len(m.selected))
	for _, sec := range m.feed {
		for _, r := range sec.entries {
			if !m.selected[selKey(r)] {
				continue
			}
			out = append(out, SelectedItem{Album: r.album, Playlist: r.playlist})
		}
	}
	return append(out, m.selectedExpansionTracks(map[string]bool{})...)
}

// SelectedTracks returns just the songs of the multi-selection, in result order.
func (m *SearchModel) SelectedTracks() []provider.Track {
	var out []provider.Track
	for _, it := range m.SelectedItems() {
		if it.Track != nil {
			out = append(out, *it.Track)
		}
	}
	return out
}

// SelectedAlbum returns the highlighted album, or nil.
func (m *SearchModel) SelectedAlbum() *provider.Album {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].album
}

// SelectedPlaylist returns the highlighted playlist, or nil.
func (m *SearchModel) SelectedPlaylist() *provider.Playlist {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor].playlist
}

// SelectedIndex returns the 0-based item index across all sections (headers excluded).
func (m *SearchModel) SelectedIndex() int {
	count := 0
	for i := 0; i < m.cursor && i < len(m.rows); i++ {
		if m.rows[i].isItem() && !m.rows[i].header {
			count++
		}
	}
	return count
}

// Update handles key navigation (↑ ↓ PgUp PgDn).
func (m *SearchModel) Update(msg tea.KeyPressMsg) (*SearchModel, tea.Cmd) {
	switch msg.String() {
	case "up":
		if prev := m.advance(m.cursor, -1); prev != m.cursor {
			m.cursor = prev
			m.ensureCursorVisible()
		}
	case "down":
		if next := m.advance(m.cursor, 1); next != m.cursor {
			m.cursor = next
			m.ensureCursorVisible()
		}
	case "pgup":
		for range 5 {
			prev := m.advance(m.cursor, -1)
			if prev == m.cursor {
				break
			}
			m.cursor = prev
		}
		m.ensureCursorVisible()
	case "pgdown":
		for range 5 {
			next := m.advance(m.cursor, 1)
			if next == m.cursor {
				break
			}
			m.cursor = next
		}
		m.ensureCursorVisible()
	}
	return m, nil
}

// sectionColor returns the accent colour for a given section label.
// Each section uses a distinct warm/cool hue so the three groups are
// immediately distinguishable at a glance.
func sectionColor(label string) color.Color {
	switch label {
	case "Albums":
		return styles.ColorPrimary // violet  #C678DD
	case "Playlists":
		return styles.ColorSecondary // green  #98C379
	case "Library":
		return styles.ColorAccent // the user's own copies
	default: // "Tracks"
		return styles.ColorAccentWarm // warm amber
	}
}

// rowAccent is the accent of a header row: saved lists share one colour, the
// search sections have theirs by label.
func rowAccent(r searchRow) color.Color {
	switch {
	case r.list != nil:
		return styles.ColorSecondary
	case r.group:
		return styles.ColorPrimary
	}
	return sectionColor(r.label)
}

// View renders the multi-section result list within the allocated height.
func (m *SearchModel) View() string {
	if m.loading {
		return styles.QueueItemMuted.Render("  searching…")
	}
	if m.err != nil {
		return styles.ErrorStyle.Render("⚠  " + m.err.Error())
	}
	if len(m.rows) == 0 {
		return ""
	}

	itemTitle := lipgloss.NewStyle().Foreground(styles.ColorFg)
	itemDesc := lipgloss.NewStyle().Foreground(styles.ColorMuted)
	tagStyle := lipgloss.NewStyle().Foreground(styles.ColorMuted)

	var sb strings.Builder
	linesLeft := m.height
	start := max(0, m.scroll)

	// Seed currentAccent from the nearest header that sits above the current
	// scroll window.  Without this, items whose section header has already
	// scrolled out of view would render with the wrong colour (the default
	// "Tracks" amber) until the header scrolls back into the viewport.
	currentAccent := sectionColor("Tracks")
	for i := start - 1; i >= 0; i-- {
		if m.rows[i].header {
			currentAccent = rowAccent(m.rows[i])
			break
		}
	}

	for i := start; i < len(m.rows) && linesLeft > 0; i++ {
		row := m.rows[i]

		if row.header {
			currentAccent = rowAccent(row)
			hs := lipgloss.NewStyle().
				Foreground(currentAccent).
				Bold(true).
				Italic(true)
			prefix := "  "
			switch {
			case i == m.cursor:
				prefix = lipgloss.NewStyle().Foreground(currentAccent).Render("▶ ")
			case row.list != nil && m.selected[selKey(row)]:
				prefix = lipgloss.NewStyle().Foreground(currentAccent).Render("✓ ")
			}
			name := row.label
			if row.title != "" {
				name = row.title
			}
			sb.WriteString(prefix + hs.Render(name) + "\n")
			linesLeft--
			continue
		}

		if row.note != "" {
			sb.WriteString("  " + styles.QueueItemMuted.Render(cutRunes(row.note, max(1, m.width-2))) + "\n")
			linesLeft--
			continue
		}

		// Skip rows that no longer fit.
		if linesLeft < rowLines(row) {
			break
		}

		sel := i == m.cursor
		picked := m.selected[selKey(row)]
		cur := "  "
		switch {
		case sel:
			cur = lipgloss.NewStyle().Foreground(currentAccent).Render("▶ ")
		case picked:
			cur = lipgloss.NewStyle().Foreground(currentAccent).Render("✓ ")
		}
		tStyle := itemTitle
		dStyle := itemDesc
		switch {
		case picked:
			// The accent marks membership of the selection and nothing else:
			// the highlighted row keeps plain text unless it is picked, so
			// after Ctrl+← (clear) nothing reads as selected; the pointer
			// alone says where the highlight is.
			tStyle = lipgloss.NewStyle().Foreground(currentAccent).Bold(true)
			dStyle = lipgloss.NewStyle().Foreground(currentAccent).Faint(true)
		case sel && len(m.selected) > 0:
			// While a selection exists, the highlighted row outside it goes
			// grey (the pointer keeps its colour) so its state is plain.
			tStyle = styles.QueueItemMuted
			dStyle = styles.QueueItemMuted
		}

		switch {
		case row.toggle:
			label := fmt.Sprintf("− %d less", row.step)
			if row.more {
				label = fmt.Sprintf("+ %d more", row.step)
				if m.paging && row.label == "Tracks" {
					label = "⏳ loading more…"
				}
			}
			ts := tagStyle
			if sel {
				ts = lipgloss.NewStyle().Foreground(currentAccent).Bold(true)
			}
			sb.WriteString(cur + ts.Render(label) + "\n")
			linesLeft--
			continue

		case row.track != nil && row.child:
			t := row.track
			sb.WriteString(cur + "  " + tStyle.Render(t.Title) + dStyle.Render(" — "+t.Artist) + "\n")
			linesLeft--
			continue

		case row.track != nil:
			t := row.track
			sb.WriteString(cur + tStyle.Render(t.Title) + "\n")
			sb.WriteString("    " + dStyle.Render(fmt.Sprintf("%s — %s", t.Artist, t.Album)) + "\n")

		case row.album != nil:
			a := row.album
			desc := a.Artist
			if a.TrackCount > 0 {
				desc += fmt.Sprintf("  ·  %d tracks", a.TrackCount)
			}
			sb.WriteString(cur + tStyle.Render(a.Title) + tagStyle.Render(" [album]") + "\n")
			sb.WriteString("    " + dStyle.Render(desc) + "\n")

		case row.playlist != nil:
			// One line per playlist: name, tag and track count together.
			p := row.playlist
			line := cur + tStyle.Render(p.Name) + tagStyle.Render(" [playlist]")
			if p.TrackCount > 0 {
				line += dStyle.Render(fmt.Sprintf("  ·  %d tracks", p.TrackCount))
			}
			sb.WriteString(line + "\n")
			linesLeft--
			continue
		}
		linesLeft -= 2
	}

	return sb.String()
}

// Focus / Focused — kept for backward compatibility (input is managed by the model).
func (m *SearchModel) Focus()        {}
func (m *SearchModel) Focused() bool { return false }

// SetCursor / Cursor — kept for backward compatibility.
func (m *SearchModel) SetCursor(_ int) {}
func (m *SearchModel) Cursor() int     { return m.SelectedIndex() }

func (m *SearchModel) scheduleSearch(query string) tea.Cmd {
	if query == "" {
		return nil
	}
	prov := m.provider
	return func() tea.Msg {
		time.Sleep(300 * time.Millisecond)
		result, err := prov.Search(context.Background(), query)
		return searchResultMsg{result: result, err: err}
	}
}

func searchResultItems(r *provider.SearchResult) []provider.Track {
	return r.Tracks
}

// cutRunes shortens s to at most n runes, marking the cut with an ellipsis.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
