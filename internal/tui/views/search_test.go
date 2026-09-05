package views

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/provider"
)

// --- searchRow ---

func TestSearchRow_IsItem_Header(t *testing.T) {
	// Headers are selectable so enter can fold/open their section.
	r := searchRow{header: true, label: "Tracks"}
	if !r.isItem() {
		t.Error("header row should be selectable")
	}
}

func TestSearchRow_IsItem_Track(t *testing.T) {
	tr := provider.Track{Title: "Search Song"}
	row := searchRow{track: &tr}
	if !row.isItem() {
		t.Error("track row should be an item")
	}
}

func TestSearchRow_IsItem_Album(t *testing.T) {
	a := provider.Album{Title: "Search Album"}
	row := searchRow{album: &a}
	if !row.isItem() {
		t.Error("album row should be an item")
	}
}

func TestSearchRow_IsItem_Playlist(t *testing.T) {
	p := provider.Playlist{Name: "Search Playlist"}
	row := searchRow{playlist: &p}
	if !row.isItem() {
		t.Error("playlist row should be an item")
	}
}

func TestSearchRow_View_TrackRendersArtistAndAlbum(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetState([]provider.Track{
		{Title: "Search Song", Artist: "Search Artist", Album: "Search Album"},
	}, false, nil)
	v := s.View()
	if !strings.Contains(v, "Search Artist") {
		t.Errorf("View() should contain artist, got %q", v)
	}
	if !strings.Contains(v, "Search Album") {
		t.Errorf("View() should contain album, got %q", v)
	}
}

// --- searchResultItems ---

func TestSearchResultItems_Empty(t *testing.T) {
	result := &provider.SearchResult{}
	items := searchResultItems(result)
	if len(items) != 0 {
		t.Errorf("searchResultItems(empty) = %d items, want 0", len(items))
	}
}

func TestSearchResultItems_WithTracks(t *testing.T) {
	result := &provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "T1", Artist: "A1"},
			{Title: "T2", Artist: "A2"},
			{Title: "T3", Artist: "A3"},
		},
	}
	items := searchResultItems(result)
	if len(items) != 3 {
		t.Errorf("searchResultItems = %d items, want 3", len(items))
	}
}

// --- SearchModel ---

func TestNewSearch_NilProvider(t *testing.T) {
	s := NewSearch(nil)
	if s == nil {
		t.Fatal("NewSearch(nil) returned nil")
	}
}

func TestSearch_Focus_And_Focused(t *testing.T) {
	s := NewSearch(&mockProvider{})
	// Focus/Focused are no-ops; input is managed by the model
	s.Focus()
	if s.Focused() {
		t.Error("Focused() should always return false (input managed by model)")
	}
}

func TestSearch_SetSize_NoPanic(t *testing.T) {
	s := NewSearch(&mockProvider{})
	s.SetSize(80, 24) // should not panic
}

func TestSearch_Init(t *testing.T) {
	s := NewSearch(&mockProvider{})
	cmd := s.Init()
	if cmd != nil {
		t.Error("Init() should return nil cmd")
	}
}

func TestSearch_View_NonEmpty(t *testing.T) {
	s := NewSearch(&mockProvider{})
	s.SetSize(80, 24)
	s.SetState(nil, true, nil) // loading state → non-empty view
	got := s.View()
	if got == "" {
		t.Error("View() should return non-empty string when loading")
	}
}

func TestSearch_Update_SearchResultMsg(t *testing.T) {
	s := NewSearch(&mockProvider{})
	s.SetSize(80, 24)
	result := &provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "Found Track", Artist: "Found Artist"},
		},
	}
	// In the new design, search results are set via SetState (called by model.go).
	s.SetState(result.Tracks, false, nil)
	got := s.View()
	if got == "" {
		t.Error("View() after search result should return non-empty string")
	}
}

func TestSearch_Update_EscBlursInput(t *testing.T) {
	s := NewSearch(&mockProvider{})
	s.SetSize(80, 24)
	// Esc is now handled by the model; search model Update ignores key msgs
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	// Focused() always returns false in new design
	if s.Focused() {
		t.Error("Focused() should always return false")
	}
}

func TestSearch_Update_NonSearchMsg_NoPanic(t *testing.T) {
	s := NewSearch(&mockProvider{})
	s.SetSize(80, 24)
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // should not panic
}

func TestSearch_ScheduleSearch_NonEmpty(t *testing.T) {
	s := NewSearch(&mockProvider{})
	cmd := s.scheduleSearch("hello")
	if cmd == nil {
		t.Error("scheduleSearch with non-empty query should return non-nil cmd")
	}
}

func TestSearch_ScheduleSearch_Empty(t *testing.T) {
	s := NewSearch(&mockProvider{})
	cmd := s.scheduleSearch("")
	if cmd != nil {
		t.Error("scheduleSearch with empty query should return nil cmd")
	}
}

func TestSearch_Update_TypeWhileFocused(t *testing.T) {
	s := NewSearch(&mockProvider{})
	s.SetSize(80, 24)
	// In the new design, typing is handled by the model, not search.
	// Verify Update handles non-searchResultMsg gracefully (no-op, no panic).
	s, cmd := s.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	_ = cmd // cmd may be nil — that is correct in the new design
	_ = s
}

// --- SelectedTrack ────────────────────────────────────────────────────────────

func TestSearch_SelectedTrack_NoResults(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	if got := s.SelectedTrack(); got != nil {
		t.Errorf("SelectedTrack() with no results = %v, want nil", got)
	}
}

func TestSearch_SelectedTrack_ReturnsFirstByDefault(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	tracks := []provider.Track{
		{Title: "First", Artist: "A", CatalogID: "111"},
		{Title: "Second", Artist: "B", CatalogID: "222"},
	}
	s.SetState(tracks, false, nil)

	got := s.SelectedTrack()
	if got == nil {
		t.Fatal("SelectedTrack() returned nil after SetState with tracks")
	}
	if got.Title != "First" {
		t.Errorf("SelectedTrack().Title = %q, want %q", got.Title, "First")
	}
}

func TestSearch_SelectedTrack_ChangesAfterNavigation(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	tracks := []provider.Track{
		{Title: "Alpha", CatalogID: "1"},
		{Title: "Beta", CatalogID: "2"},
		{Title: "Gamma", CatalogID: "3"},
	}
	s.SetState(tracks, false, nil)

	// Move down once — should select "Beta".
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	got := s.SelectedTrack()
	if got == nil {
		t.Fatal("SelectedTrack() returned nil after navigating down")
	}
	if got.Title != "Beta" {
		t.Errorf("SelectedTrack().Title after Down = %q, want %q", got.Title, "Beta")
	}
}

func TestSearch_SelectedIndex_TracksCursorPosition(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetState([]provider.Track{
		{Title: "A", CatalogID: "1"},
		{Title: "B", CatalogID: "2"},
	}, false, nil)

	if s.SelectedIndex() != 0 {
		t.Errorf("initial SelectedIndex() = %d, want 0", s.SelectedIndex())
	}
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.SelectedIndex() != 1 {
		t.Errorf("SelectedIndex() after Down = %d, want 1", s.SelectedIndex())
	}
}

func TestSearch_SetState_ResetsSelection(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetState([]provider.Track{
		{Title: "X", CatalogID: "x"},
		{Title: "Y", CatalogID: "y"},
	}, false, nil)

	// Navigate to second item.
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	// Replace results — cursor should reset to 0.
	s.SetState([]provider.Track{{Title: "New", CatalogID: "n"}}, false, nil)
	if s.SelectedIndex() != 0 {
		t.Errorf("SelectedIndex() after SetState = %d, want 0 (reset)", s.SelectedIndex())
	}
}

func TestSearch_Loading_View_NonemptyString(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetState(nil, true, nil)
	v := s.View()
	if v == "" {
		t.Error("View() during loading should return non-empty string")
	}
}

func TestSearch_EmptyResults_View_ReturnsEmpty(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetState(nil, false, nil)
	v := s.View()
	if v != "" {
		t.Errorf("View() with no results/loading/error should be empty, got %q", v)
	}
}

func TestSearch_ErrorState_View_ContainsError(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetState(nil, false, errors.New("network timeout"))
	v := s.View()
	if !strings.Contains(v, "network timeout") {
		t.Errorf("View() with error should contain error text, got %q", v)
	}
}

// --- Results, Loading, Focus, SetCursor, Cursor, PlaybackID ---

func TestSearch_Results_Empty(t *testing.T) {
	s := NewSearch(nil)
	if s.Results() != nil {
		t.Error("Results() on fresh SearchModel should be nil")
	}
}

func TestSearch_Results_AfterSetState(t *testing.T) {
	s := NewSearch(nil)
	tracks := []provider.Track{
		{Title: "Track X", CatalogID: "x"},
		{Title: "Track Y", CatalogID: "y"},
	}
	s.SetState(tracks, false, nil)
	got := s.Results()
	if len(got) != 2 {
		t.Errorf("Results() = %d items, want 2", len(got))
	}
}

func TestSearch_Loading_False(t *testing.T) {
	s := NewSearch(nil)
	if s.Loading() {
		t.Error("Loading() should be false on new SearchModel")
	}
}

func TestSearch_Loading_True(t *testing.T) {
	s := NewSearch(nil)
	s.SetState(nil, true, nil)
	if !s.Loading() {
		t.Error("Loading() should be true after SetState(loading=true)")
	}
}

func TestSearch_Focus_NoPanic(t *testing.T) {
	s := NewSearch(nil)
	s.Focus() // no-op, should not panic
}

func TestSearch_Focused_AlwaysFalse(t *testing.T) {
	s := NewSearch(nil)
	s.Focus()
	if s.Focused() {
		t.Error("Focused() should always return false")
	}
}

func TestSearch_SetCursor_NoPanic(t *testing.T) {
	s := NewSearch(nil)
	s.SetCursor(5) // no-op, should not panic
}

func TestSearch_Cursor_ReturnsListIndex(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetState([]provider.Track{
		{Title: "A", CatalogID: "a"},
		{Title: "B", CatalogID: "b"},
	}, false, nil)
	if s.Cursor() != 0 {
		t.Errorf("Cursor() = %d, want 0 initially", s.Cursor())
	}
}

// --- SetResults ---

func TestSearch_SetResults_TracksAlbumsPlaylists(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 40)
	result := &provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "Night Owl", Artist: "Chet Baker", CatalogID: "c1"},
		},
		Albums: []provider.Album{
			{ID: "a1", Title: "Chet", Artist: "Chet Baker", TrackCount: 11},
		},
		Playlists: []provider.Playlist{
			{ID: "pl1", Name: "Jazz Classics", TrackCount: 20},
		},
	}
	s.SetResults(result, false, nil)

	if s.Loading() {
		t.Error("Loading() should be false after SetResults")
	}
	v := s.View()
	if !strings.Contains(v, "Tracks") {
		t.Errorf("View() should contain Tracks header, got: %q", v)
	}
	if !strings.Contains(v, "Albums") {
		t.Errorf("View() should contain Albums header, got: %q", v)
	}
	if !strings.Contains(v, "Playlists") {
		t.Errorf("View() should contain Playlists header, got: %q", v)
	}
	if !strings.Contains(v, "Night Owl") {
		t.Errorf("View() should contain track title, got: %q", v)
	}
	if !strings.Contains(v, "Chet") {
		t.Errorf("View() should contain album title, got: %q", v)
	}
	if !strings.Contains(v, "Jazz Classics") {
		t.Errorf("View() should contain playlist name, got: %q", v)
	}
	if !strings.Contains(v, "[album]") {
		t.Errorf("View() should contain [album] tag, got: %q", v)
	}
	if !strings.Contains(v, "[playlist]") {
		t.Errorf("View() should contain [playlist] tag, got: %q", v)
	}
}

func TestSearch_SetResults_OnlyAlbums(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	result := &provider.SearchResult{
		Albums: []provider.Album{
			{ID: "a1", Title: "Kind of Blue", Artist: "Miles Davis", TrackCount: 5},
			{ID: "a2", Title: "A Love Supreme", Artist: "John Coltrane", TrackCount: 4},
		},
	}
	s.SetResults(result, false, nil)

	v := s.View()
	if !strings.Contains(v, "Albums") {
		t.Errorf("View() should contain Albums header, got: %q", v)
	}
	if strings.Contains(v, "Tracks") {
		t.Errorf("View() should NOT contain Tracks header when there are no tracks, got: %q", v)
	}
	if !strings.Contains(v, "Kind of Blue") {
		t.Errorf("View() should contain album title, got: %q", v)
	}
}

func TestSearch_SetResults_OnlyPlaylists(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	result := &provider.SearchResult{
		Playlists: []provider.Playlist{
			{ID: "pl1", Name: "Morning Mix", TrackCount: 15},
		},
	}
	s.SetResults(result, false, nil)

	v := s.View()
	if !strings.Contains(v, "Playlists") {
		t.Errorf("View() should contain Playlists header, got: %q", v)
	}
	if !strings.Contains(v, "Morning Mix") {
		t.Errorf("View() should contain playlist name, got: %q", v)
	}
	if strings.Contains(v, "Tracks") {
		t.Errorf("View() should NOT contain Tracks header when there are no tracks, got: %q", v)
	}
}

func TestSearch_SetResults_ResetsSelectionToFirstItem(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	// First load: tracks only; navigate down.
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "A", CatalogID: "a"},
			{Title: "B", CatalogID: "b"},
		},
	}, false, nil)
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.SelectedIndex() != 1 {
		t.Fatalf("expected index 1 before reset, got %d", s.SelectedIndex())
	}

	// Second load: new results should reset cursor to first item.
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{{Title: "X", CatalogID: "x"}},
	}, false, nil)
	if s.SelectedIndex() != 0 {
		t.Errorf("SetResults should reset SelectedIndex to 0, got %d", s.SelectedIndex())
	}
}

// --- SelectedAlbum ---

func TestSearch_SelectedAlbum_NoResults(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	if got := s.SelectedAlbum(); got != nil {
		t.Errorf("SelectedAlbum() with no results = %v, want nil", got)
	}
}

func TestSearch_SelectedAlbum_FirstByDefault(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	// Albums only — first item should be auto-selected.
	s.SetResults(&provider.SearchResult{
		Albums: []provider.Album{
			{ID: "a1", Title: "Blue Train", Artist: "Coltrane"},
			{ID: "a2", Title: "Giant Steps", Artist: "Coltrane"},
		},
	}, false, nil)

	got := s.SelectedAlbum()
	if got == nil {
		t.Fatal("SelectedAlbum() returned nil when albums are present")
	}
	if got.Title != "Blue Train" {
		t.Errorf("SelectedAlbum().Title = %q, want %q", got.Title, "Blue Train")
	}
	if s.SelectedTrack() != nil {
		t.Error("SelectedTrack() should be nil when an album is selected")
	}
	if s.SelectedPlaylist() != nil {
		t.Error("SelectedPlaylist() should be nil when an album is selected")
	}
}

func TestSearch_SelectedAlbum_NavigateDown(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetResults(&provider.SearchResult{
		Albums: []provider.Album{
			{ID: "a1", Title: "First Album"},
			{ID: "a2", Title: "Second Album"},
		},
	}, false, nil)

	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	got := s.SelectedAlbum()
	if got == nil {
		t.Fatal("SelectedAlbum() returned nil after navigating down")
	}
	if got.Title != "Second Album" {
		t.Errorf("SelectedAlbum().Title = %q, want %q", got.Title, "Second Album")
	}
}

// --- SelectedPlaylist ---

func TestSearch_SelectedPlaylist_NoResults(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	if got := s.SelectedPlaylist(); got != nil {
		t.Errorf("SelectedPlaylist() with no results = %v, want nil", got)
	}
}

func TestSearch_SelectedPlaylist_FirstByDefault(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetResults(&provider.SearchResult{
		Playlists: []provider.Playlist{
			{ID: "pl1", Name: "Chill Vibes", TrackCount: 10},
			{ID: "pl2", Name: "Workout", TrackCount: 20},
		},
	}, false, nil)

	got := s.SelectedPlaylist()
	if got == nil {
		t.Fatal("SelectedPlaylist() returned nil when playlists are present")
	}
	if got.Name != "Chill Vibes" {
		t.Errorf("SelectedPlaylist().Name = %q, want %q", got.Name, "Chill Vibes")
	}
	if s.SelectedTrack() != nil {
		t.Error("SelectedTrack() should be nil when a playlist is selected")
	}
	if s.SelectedAlbum() != nil {
		t.Error("SelectedAlbum() should be nil when a playlist is selected")
	}
}

// --- Cross-section navigation ---

func TestSearch_Navigation_CrossesSectionBoundary(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "Only Track", CatalogID: "t1"},
		},
		Albums: []provider.Album{
			{ID: "a1", Title: "Only Album"},
		},
	}, false, nil)

	// Rows: Albums header, album, − less, Tracks header, track, − less
	// (everything is shown, so there are no more rows). Headers are selectable.
	if s.SelectedAlbum() == nil {
		t.Fatal("expected album to be selected initially")
	}
	for range 3 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if s.SelectedTrack() == nil || s.SelectedTrack().Title != "Only Track" {
		t.Fatalf("expected the track after passing the albums' less row and the Tracks header")
	}
	for range 3 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if s.SelectedAlbum() == nil || s.SelectedAlbum().Title != "Only Album" {
		t.Fatalf("expected the album to be re-selected after navigating up")
	}
}

func TestSearch_Navigation_AllSections(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "Track One", CatalogID: "t1"},
			{Title: "Mine", ID: "i.lib1"},
		},
		Albums: []provider.Album{
			{ID: "a1", Title: "Album One"},
		},
		Playlists: []provider.Playlist{
			{ID: "pl1", Name: "Playlist One"},
		},
	}, false, nil)

	// Order: Playlists, Albums, Library, Tracks; each fully shown section has
	// its item followed by a single − less row.
	if s.SelectedPlaylist() == nil {
		t.Fatal("step 0: expected playlist")
	}
	down := func(n int) {
		for range n {
			s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		}
	}
	down(1)
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Playlists" || more {
		t.Fatal("step 1: expected the playlists' less row")
	}
	down(1)
	if sec, ok := s.SelectedHeader(); !ok || sec != "Albums" {
		t.Fatal("step 2: expected the Albums header (headers are selectable)")
	}
	down(1)
	if s.SelectedAlbum() == nil {
		t.Fatal("step 3: expected album")
	}
	down(3)
	if s.SelectedTrack() == nil || s.SelectedTrack().ID != "i.lib1" {
		t.Fatal("step 6: expected the library track")
	}
	down(3)
	if s.SelectedTrack() == nil || s.SelectedTrack().Title != "Track One" {
		t.Fatal("step 9: expected the catalog track")
	}
	down(2)
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Tracks" || more {
		t.Fatal("end: expected to stop on the tracks' less row")
	}
}

func TestSearch_SectionsCappedAtFive(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	var tracks []provider.Track
	for i := range 8 {
		tracks = append(tracks, provider.Track{Title: fmt.Sprintf("Cat %d", i), CatalogID: fmt.Sprintf("c%d", i)})
		tracks = append(tracks, provider.Track{Title: fmt.Sprintf("Lib %d", i), ID: fmt.Sprintf("i.l%d", i)})
	}
	var albums []provider.Album
	for i := range 7 {
		albums = append(albums, provider.Album{ID: fmt.Sprintf("a%d", i), Title: fmt.Sprintf("Album %d", i)})
	}
	s.SetResults(&provider.SearchResult{Tracks: tracks, Albums: albums}, false, nil)
	items, headers, toggles := 0, 0, 0
	for _, r := range s.rows {
		switch {
		case r.header:
			headers++
		case r.toggle:
			toggles++
		default:
			items++
		}
	}
	if headers != 3 || items != 15 || toggles != 6 {
		t.Fatalf("expected 3 sections of 5 items with two control rows each, got %d headers / %d items / %d toggles", headers, items, toggles)
	}
	// header, 5 items, more, less = 8 rows per section
	if s.rows[0].label != "Albums" || s.rows[8].label != "Library" || s.rows[16].label != "Tracks" {
		t.Fatalf("unexpected section order: %q %q %q", s.rows[0].label, s.rows[8].label, s.rows[16].label)
	}
	if !s.rows[6].toggle || !s.rows[6].more || s.rows[6].step != 2 || s.rows[7].more || s.rows[7].step != 5 {
		t.Fatalf("control rows should carry their step: more=%+v less=%+v", s.rows[6], s.rows[7])
	}
}

func TestSearch_MoreLessStepsOfFive(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	var albums []provider.Album
	for i := range 12 {
		albums = append(albums, provider.Album{ID: fmt.Sprintf("a%d", i), Title: fmt.Sprintf("Album %d", i)})
	}
	s.SetResults(&provider.SearchResult{Albums: albums}, false, nil)
	shown := func() int { return len(s.rows) - 3 } // header + two control rows
	if shown() != 5 {
		t.Fatalf("default: %d shown, want 5", shown())
	}
	for range 5 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	sec, more, ok := s.SelectedToggle()
	if !ok || sec != "Albums" || !more {
		t.Fatalf("expected the more row after the items, got %q more=%v ok=%v", sec, more, ok)
	}
	s.ShowMore("Albums")
	if shown() != 10 {
		t.Fatalf("after more: %d shown, want 10", shown())
	}
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Albums" || !more {
		t.Fatal("highlight should stay on the more row")
	}
	s.ShowMore("Albums")
	// Everything shown: the more row is gone and the highlight moves to less.
	if len(s.rows) != 1+12+1 || strings.Contains(s.View(), "more") {
		t.Fatalf("after second more: rows=%d, want header + 12 + less only: %q", len(s.rows), s.View())
	}
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Albums" || more {
		t.Fatalf("highlight should move to the less row when more disappears, got %q %v %v", sec, more, ok)
	}
	s.ShowLess("Albums")
	if shown() != 7 || !strings.Contains(s.View(), "− 5 less") || !strings.Contains(s.View(), "+ 5 more") {
		t.Fatalf("less should drop the last five and bring the more row back: %d shown", shown())
	}
	s.ShowLess("Albums")
	if shown() != 2 || !strings.Contains(s.View(), "− 2 less") {
		t.Fatalf("less again: %d shown, want 2", shown())
	}
	s.ShowLess("Albums")
	// Folded to nothing: the header alone, no control rows, and the highlight
	// lands on the header (enter there opens the section again).
	if len(s.rows) != 1 || strings.Contains(s.View(), "less") || strings.Contains(s.View(), "more") {
		t.Fatalf("a folded section should be just its header: rows=%d view %q", len(s.rows), s.View())
	}
	if sec, ok := s.SelectedHeader(); !ok || sec != "Albums" {
		t.Fatalf("highlight should move to the header when the controls disappear, got %q %v", sec, ok)
	}
	s.ToggleSectionOpen("Albums")
	if len(s.rows) != 1+5+2 {
		t.Fatalf("reopening should show five with both controls, got %d rows", len(s.rows))
	}
}

func TestSearch_PlaylistRowsAreOneLine(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{Playlists: []provider.Playlist{{ID: "p1", Name: "Chill", TrackCount: 12}, {ID: "p2", Name: "Focus"}}}, false, nil)
	if rowLines(s.rows[1]) != 1 || rowLines(s.rows[2]) != 1 {
		t.Fatal("playlist rows should take one line")
	}
	lines := strings.Split(strings.TrimRight(s.View(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("header + 2 playlists + a less row should render as 4 lines, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[1], "Chill") || !strings.Contains(lines[1], "12 tracks") {
		t.Fatalf("playlist line should carry the count inline: %q", lines[1])
	}
}

func TestSearch_Navigation_PgDown_SkipsMultipleItems(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	tracks := make([]provider.Track, 8)
	for i := range tracks {
		tracks[i] = provider.Track{Title: fmt.Sprintf("Track %d", i+1), CatalogID: fmt.Sprintf("t%d", i+1)}
	}
	s.SetResults(&provider.SearchResult{Tracks: tracks}, false, nil)

	startIdx := s.SelectedIndex()
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s.SelectedIndex() <= startIdx {
		t.Errorf("PgDown should advance cursor; got index %d (was %d)", s.SelectedIndex(), startIdx)
	}
}

// --- Album track count display ---

func TestSearch_AlbumWithTrackCount_ShowsCount(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetResults(&provider.SearchResult{
		Albums: []provider.Album{
			{ID: "a1", Title: "Thriller", Artist: "Michael Jackson", TrackCount: 9},
		},
	}, false, nil)

	v := s.View()
	if !strings.Contains(v, "9 tracks") {
		t.Errorf("View() should show track count, got: %q", v)
	}
	if !strings.Contains(v, "Michael Jackson") {
		t.Errorf("View() should show artist name, got: %q", v)
	}
}

func TestSearch_AlbumWithoutTrackCount_NoCountShown(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetResults(&provider.SearchResult{
		Albums: []provider.Album{
			{ID: "a1", Title: "Unknown Album", Artist: "Someone", TrackCount: 0},
		},
	}, false, nil)

	v := s.View()
	if strings.Contains(v, "tracks") {
		t.Errorf("View() should NOT show '0 tracks', got: %q", v)
	}
}

func TestSearch_PlaylistWithTrackCount_ShowsCount(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 20)
	s.SetResults(&provider.SearchResult{
		Playlists: []provider.Playlist{
			{ID: "pl1", Name: "Study Mix", TrackCount: 42},
		},
	}, false, nil)

	v := s.View()
	if !strings.Contains(v, "42 tracks") {
		t.Errorf("View() should show track count for playlist, got: %q", v)
	}
}

// --- SelectedIndex with mixed sections ---

// --- Scroll regression tests ---

func TestSearch_ScrollUp_SectionHeaderRemainsVisible(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 6) // tight height: only room for header + 2 items
	tracks := []provider.Track{
		{Title: "Alpha", CatalogID: "t1"},
		{Title: "Beta", CatalogID: "t2"},
		{Title: "Gamma", CatalogID: "t3"},
		{Title: "Delta", CatalogID: "t4"},
	}
	s.SetResults(&provider.SearchResult{Tracks: tracks}, false, nil)

	// Navigate down far enough that the Tracks header scrolls out of view.
	for range 3 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	// Now navigate back up to the first item.
	for range 3 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}

	// The scroll position must be 0 so the "Tracks" header is visible.
	if s.scroll != 0 {
		t.Errorf("after scrolling up to first item, scroll = %d, want 0 (header must be visible)", s.scroll)
	}
	v := s.View()
	if !strings.Contains(v, "Tracks") {
		t.Errorf("section header should be visible after scrolling back to the top, view: %q", v)
	}
}

func TestSearch_ScrollUp_AlbumSectionHeaderRemainsVisible(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 6)
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "T1", CatalogID: "t1"},
		},
		Albums: []provider.Album{
			{ID: "a1", Title: "Album One"},
			{ID: "a2", Title: "Album Two"},
			{ID: "a3", Title: "Album Three"},
		},
	}, false, nil)

	// Albums first: a1 a2 a3, less, Tracks header, then the track. Down to the
	// track, then back up to the first album.
	for range 5 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if s.SelectedTrack() == nil {
		t.Fatal("expected the track at the end of the list")
	}
	for range 5 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if s.SelectedAlbum() == nil || s.SelectedAlbum().Title != "Album One" {
		t.Fatal("expected the first album")
	}
	if v := s.View(); !strings.Contains(v, "Albums") {
		t.Errorf("Albums header should be visible after scrolling back up, view: %q", v)
	}
}

func TestSearch_ColorSeeding_TracksWhenHeaderScrolledPast(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 6)
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "T1", CatalogID: "t1"},
			{Title: "T2", CatalogID: "t2"},
			{Title: "T3", CatalogID: "t3"},
		},
		Playlists: []provider.Playlist{
			{ID: "pl1", Name: "My Playlist", TrackCount: 5},
		},
	}, false, nil)

	if s.SelectedPlaylist() == nil {
		t.Fatal("expected the playlist to be selected initially")
	}
	if v := s.View(); !strings.Contains(v, "My Playlist") {
		t.Errorf("playlist name should be visible in the view, got: %q", v)
	}
	// playlist, less, Tracks header, T1, T2, T3
	for range 5 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if s.SelectedTrack() == nil || s.SelectedTrack().Title != "T3" {
		t.Fatal("expected T3 to be selected after navigating down")
	}
	if v := s.View(); !strings.Contains(v, "T3") {
		t.Errorf("T3 should be visible in the view, got: %q", v)
	}
}

func TestSearch_ColorSeeding_TracksAfterAlbums(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 4) // very tight: forces headers to scroll past
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "T1", CatalogID: "t1"},
			{Title: "T2", CatalogID: "t2"},
			{Title: "T3", CatalogID: "t3"},
		},
		Albums: []provider.Album{
			{ID: "a1", Title: "Album Visible"},
		},
	}, false, nil)

	if s.SelectedAlbum() == nil {
		t.Fatal("expected the album to be selected initially")
	}
	if v := s.View(); !strings.Contains(v, "Album Visible") {
		t.Errorf("album title should be visible, got: %q", v)
	}
	// album, less, Tracks header, T1, T2, T3
	for range 5 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if s.SelectedTrack() == nil || s.SelectedTrack().Title != "T3" {
		t.Fatal("expected T3 after navigating past the albums")
	}
	if v := s.View(); !strings.Contains(v, "T3") {
		t.Errorf("T3 should be visible, got: %q", v)
	}
}

func TestSearch_SelectedIndex_MixedSections(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{
		Tracks: []provider.Track{
			{Title: "T1", CatalogID: "t1"},
			{Title: "T2", CatalogID: "t2"},
		},
		Albums: []provider.Album{
			{ID: "a1", Title: "A1"},
		},
	}, false, nil)

	// Sections run Playlists, Albums, Library, Tracks. SelectedIndex counts
	// items and control rows but not headers: A1(0), less(1), T1(2), T2(3);
	// the arrow keys still stop on the Tracks header in between.
	if s.SelectedIndex() != 0 || s.SelectedAlbum() == nil {
		t.Errorf("initial SelectedIndex = %d (album=%v), want 0 on the album", s.SelectedIndex(), s.SelectedAlbum() != nil)
	}
	for range 3 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if s.SelectedIndex() != 2 || s.SelectedTrack() == nil || s.SelectedTrack().Title != "T1" {
		t.Errorf("after 3 down SelectedIndex = %d, want 2 on T1", s.SelectedIndex())
	}
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.SelectedIndex() != 3 || s.SelectedTrack() == nil || s.SelectedTrack().Title != "T2" {
		t.Errorf("after 3 down SelectedIndex = %d, want 3 on T2", s.SelectedIndex())
	}
}

func TestPlaybackID_CatalogID(t *testing.T) {
	// Non-library track with a CatalogID set: return catalog ID.
	track := provider.Track{ID: "library-id", CatalogID: "catalog-id"}
	got := PlaybackID(track)
	if got != "catalog-id" {
		t.Errorf("PlaybackID(with catalogID) = %q, want %q", got, "catalog-id")
	}
}

func TestPlaybackID_LibraryTrackWithCatalogMatch(t *testing.T) {
	// Library track (i. prefix) that has been matched to a catalog entry:
	// must return the library ID to avoid CONTENT_RESTRICTED on the catalog copy.
	track := provider.Track{ID: "i.library-id", CatalogID: "catalog-id"}
	got := PlaybackID(track)
	if got != "i.library-id" {
		t.Errorf("PlaybackID(library+catalogID) = %q, want %q", got, "i.library-id")
	}
}

func TestPlaybackID_LibraryID(t *testing.T) {
	track := provider.Track{ID: "i.library-id", CatalogID: ""}
	got := PlaybackID(track)
	if got != "i.library-id" {
		t.Errorf("PlaybackID(no catalogID) = %q, want %q", got, "i.library-id")
	}
}

// catalogPage builds n catalog tracks named from `from` for paging tests.
func catalogPage(from, n int) []provider.Track {
	var out []provider.Track
	for i := from; i < from+n; i++ {
		out = append(out, provider.Track{ID: fmt.Sprintf("c%d", i), CatalogID: fmt.Sprintf("c%d", i), Title: fmt.Sprintf("Song %d", i), Artist: "Band"})
	}
	return out
}

func tracksSection(s *SearchModel) (items int, more, less bool) {
	for _, r := range s.rows {
		if r.label != "Tracks" && r.track == nil {
			continue
		}
		switch {
		case r.track != nil && !isLibraryTrack(*r.track):
			items++
		case r.toggle && r.more:
			more = true
		case r.toggle && !r.more:
			less = true
		}
	}
	return
}

func TestSearch_TracksKeepMoreRowWhileApplePagesRemain(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 3), CatalogNext: 25, CatalogMore: true}, false, nil)
	if items, more, less := tracksSection(s); items != 3 || !more || !less {
		t.Fatalf("all 3 shown but a further page exists: want more+less rows, got items=%d more=%v less=%v", items, more, less)
	}
	for range 3 {
		s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Tracks" || !more {
		t.Fatalf("cursor should be on the Tracks more row, got %q more=%v ok=%v", sec, more, ok)
	}
	// Nothing loaded is hidden, so all five have to come from the next page.
	if wanted := s.ShowMore("Tracks"); wanted != 5 {
		t.Fatalf("ShowMore should ask for 5 from the next page, got %d", wanted)
	}
	offset, ok := s.BeginPaging(5)
	if !ok || offset != 25 || !s.Paging() {
		t.Fatalf("BeginPaging: offset=%d ok=%v paging=%v, want 25/true/true", offset, ok, s.Paging())
	}
	if !strings.Contains(s.View(), "loading more") {
		t.Fatalf("the more row should say it is loading: %q", s.View())
	}
	if _, ok := s.BeginPaging(5); ok {
		t.Fatal("a second BeginPaging while one is in flight must be refused")
	}
	// The page repeats one song by artist/title and one by id; both are skipped.
	page := append(catalogPage(3, 7), provider.Track{ID: "x", CatalogID: "x", Title: "song 1", Artist: "band"}, provider.Track{ID: "c4", CatalogID: "c4", Title: "Other", Artist: "Band"})
	if still := s.AppendCatalogTracks(provider.SongPage{Tracks: page, Next: 50, More: true}); still != 0 {
		t.Fatalf("the page carried enough songs; still wanted %d", still)
	}
	if len(s.results.Tracks) != 10 {
		t.Fatalf("3 + 7 new tracks expected, got %d", len(s.results.Tracks))
	}
	if items, more, less := tracksSection(s); items != 8 || !more || !less || s.Paging() {
		t.Fatalf("want 3+5 shown with both controls after the page, got items=%d more=%v less=%v paging=%v", items, more, less, s.Paging())
	}
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Tracks" || !more {
		t.Fatal("highlight should stay on the Tracks more row after the page arrives")
	}
	// "+ 5 more" now reveals the two loaded ones and owes three from the next page.
	if wanted := s.ShowMore("Tracks"); wanted != 3 {
		t.Fatalf("ShowMore should reveal 2 loaded and want 3 more, got %d", wanted)
	}
	if _, ok := s.BeginPaging(3); !ok {
		t.Fatal("BeginPaging should start the next page")
	}
	// The last page is short: everything is revealed and the more row goes away.
	if still := s.AppendCatalogTracks(provider.SongPage{Tracks: catalogPage(20, 2), Next: 52, More: false}); still != 0 {
		t.Fatalf("no further page exists, nothing can still be owed: %d", still)
	}
	if items, more, less := tracksSection(s); items != 12 || more || !less {
		t.Fatalf("want all 12 shown and no more row, got items=%d more=%v less=%v", items, more, less)
	}
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Tracks" || more {
		t.Fatalf("vanished more row should hand the highlight to less, got %q more=%v ok=%v", sec, more, ok)
	}
}

func TestSearch_ShowMoreRevealsLoadedTracksBeforePaging(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 12), CatalogNext: 25, CatalogMore: true}, false, nil)
	if wanted := s.ShowMore("Tracks"); wanted != 0 {
		t.Fatalf("5 hidden tracks are loaded, no page needed, got wanted=%d", wanted)
	}
	if items, _, _ := tracksSection(s); items != 10 {
		t.Fatalf("10 shown expected, got %d", items)
	}
	if wanted := s.ShowMore("Tracks"); wanted != 3 {
		t.Fatalf("2 revealed, 3 owed from the next page, got %d", wanted)
	}
}

func TestSearch_NoPagingWithoutFurtherPage(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 3)}, false, nil)
	if _, more, _ := tracksSection(s); more {
		t.Fatal("all shown and no further page: no more row")
	}
	if wanted := s.ShowMore("Tracks"); wanted != 0 {
		t.Fatalf("nothing to page, got wanted=%d", wanted)
	}
	if _, ok := s.BeginPaging(5); ok {
		t.Fatal("BeginPaging must refuse when no page can exist")
	}
	// Only the catalog Tracks section pages.
	s.SetResults(&provider.SearchResult{Tracks: []provider.Track{{ID: "i.1", Title: "Mine"}}, CatalogNext: 25, CatalogMore: true}, false, nil)
	if wanted := s.ShowMore("Library"); wanted != 0 {
		t.Fatalf("Library never pages, got wanted=%d", wanted)
	}
	if items, more, _ := tracksSection(s); items != 0 || !more {
		t.Fatalf("an empty Tracks section with a further page should still offer more, got items=%d more=%v", items, more)
	}
}

func TestSearch_PageAfterNewQueryIsDropped(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 3), CatalogNext: 25, CatalogMore: true}, false, nil)
	if _, ok := s.BeginPaging(5); !ok {
		t.Fatal("BeginPaging should start")
	}
	s.SetResults(nil, true, nil) // the user typed on: results cleared, loading again
	if still := s.AppendCatalogTracks(provider.SongPage{Tracks: catalogPage(3, 5), More: true}); still != 0 || s.Paging() || s.results != nil {
		t.Fatalf("a late page must not resurrect old results: still=%d paging=%v results=%v", still, s.Paging(), s.results)
	}
}

func TestSearch_VibeResultsAreOneSection(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetVibeResults(catalogPage(0, 12), "")
	if !s.VibeResults() {
		t.Fatal("SetVibeResults should mark the list as a vibe result set")
	}
	headers, items, toggles := 0, 0, 0
	for _, r := range s.rows {
		switch {
		case r.header:
			headers++
			if r.label != "Vibes" {
				t.Fatalf("the only section is Vibes, got %q", r.label)
			}
		case r.toggle:
			toggles++
		case r.track != nil:
			items++
		}
	}
	if headers != 1 || items != 5 || toggles != 2 {
		t.Fatalf("want Vibes header, 5 songs, more+less; got %d/%d/%d", headers, items, toggles)
	}
	if t0 := s.SelectedTrack(); t0 == nil || t0.ID != "c0" {
		t.Fatalf("first song should be highlighted, got %+v", t0)
	}
	s.ShowMore("Vibes")
	if n := len(s.rows); n != 1+10+2 {
		t.Fatalf("after more: %d rows, want header + 10 + two controls", n)
	}
	// A regular result set replaces the vibe one.
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 2)}, false, nil)
	if s.VibeResults() || !strings.Contains(s.View(), "Tracks") {
		t.Fatal("SetResults should return to the regular sections")
	}
}

func TestSearch_VibeNotesAreShownButNotSelectable(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 40)
	s.SetVibeResults(catalogPage(0, 3), "Fable 5.1", "Dreamy soul", "second line")
	if len(s.rows) < 4 || !s.rows[0].header || s.rows[1].note == "" || s.rows[2].note == "" || s.rows[3].track == nil {
		t.Fatalf("want header, two notes, then songs; got %+v", s.rows[:4])
	}
	if s.cursor != 3 {
		t.Fatalf("highlight should start on the first song (row 3), got %d", s.cursor)
	}
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if s.cursor != 0 {
		t.Fatalf("moving up skips the notes and lands on the header, got %d", s.cursor)
	}
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if s.cursor != 3 {
		t.Fatalf("moving down skips the notes again, got %d", s.cursor)
	}
	if v := s.View(); !strings.Contains(v, "Fable 5.1") || strings.Contains(v, "Vibes") || !strings.Contains(v, "Dreamy soul") || !strings.Contains(v, "second line") {
		t.Fatalf("the header shows the title and the notes are rendered: %q", v)
	}
	// Nothing found: the header (default "Vibes") and the note still say what was tried.
	s.SetVibeResults(nil, "", "Nothing")
	if v := s.View(); !strings.Contains(v, "Vibes") || !strings.Contains(v, "Nothing") {
		t.Fatalf("an empty vibe result should still show the note: %q", v)
	}
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 1)}, false, nil)
	if v := s.View(); strings.Contains(v, "Nothing") || strings.Contains(v, "Vibes") {
		t.Fatal("a regular result set drops the vibe title and notes")
	}
}

func TestSearch_MultiSelect(t *testing.T) {
	s := NewSearch(nil)
	s.SetSize(80, 60)
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 6)}, false, nil)
	if s.SelectionCount() != 0 {
		t.Fatal("no selection to start with")
	}
	// Shift+↓ twice from the first song: c0, c1, c2 are selected, cursor on c2.
	s.SelectAndMove(1)
	s.SelectAndMove(1)
	if got := s.SelectedTracks(); len(got) != 3 || got[0].ID != "c0" || got[2].ID != "c2" {
		t.Fatalf("a sweep selects every song passed over: %+v", got)
	}
	if cur := s.SelectedTrack(); cur == nil || cur.ID != "c2" {
		t.Fatalf("the highlight moved with the sweep, got %+v", cur)
	}
	// Shift+→ toggles: c2 out, then move down twice normally and toggle c4 in.
	s.ToggleSelected()
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s.ToggleSelected()
	got := s.SelectedTracks()
	if len(got) != 3 || got[0].ID != "c0" || got[1].ID != "c1" || got[2].ID != "c4" {
		t.Fatalf("toggling makes the selection non-contiguous, in result order: %+v", got)
	}
	v := s.View()
	if strings.Count(v, "✓") != 2 { // c0 and c1; c4 carries the ▶ as the highlighted row
		t.Fatalf("selected rows other than the highlighted one carry a check mark: %q", v)
	}
	// Headers and controls are never selected.
	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // + 1 more? no: only 5 shown of 6 → the more row
	if sec, more, ok := s.SelectedToggle(); !ok || sec != "Tracks" || !more {
		t.Fatalf("expected the more row under the highlight, got %q %v %v", sec, more, ok)
	}
	s.ToggleSelected()
	s.SelectAndMove(1)
	if s.SelectionCount() != 3 {
		t.Fatalf("controls do not join the selection, still 3, got %d", s.SelectionCount())
	}
	s.ClearSelection()
	if s.SelectionCount() != 0 || strings.Contains(s.View(), "✓") {
		t.Fatal("ClearSelection empties it")
	}
	// New results reset the selection.
	s.SelectAndMove(-1)
	s.SetResults(&provider.SearchResult{Tracks: catalogPage(0, 2)}, false, nil)
	if s.SelectionCount() != 0 {
		t.Fatal("a new result set starts without a selection")
	}
	// Albums and playlists are selectable too and come back in result order.
	s.SetResults(&provider.SearchResult{
		Playlists: []provider.Playlist{{ID: "p1", Name: "Mix"}},
		Albums:    []provider.Album{{ID: "a1", Title: "LP"}},
		Tracks:    catalogPage(0, 1),
	}, false, nil)
	s.SelectAndMove(1) // playlist + album
	s.SelectAndMove(1) // album (again) + song
	items := s.SelectedItems()
	if len(items) != 3 || items[0].Playlist == nil || items[1].Album == nil || items[2].Track == nil {
		t.Fatalf("want playlist, album, song; got %+v", items)
	}
	if got := s.SelectedTracks(); len(got) != 1 || got[0].ID != "c0" {
		t.Fatalf("SelectedTracks lists only the songs: %+v", got)
	}
}

func TestView_HighlightIsPointerOnlyUnlessPicked(t *testing.T) {
	m := NewSearch(nil)
	m.SetSize(60, 20)
	m.SetState([]provider.Track{{ID: "1", Title: "Alpha", Artist: "A"}, {ID: "2", Title: "Beta", Artist: "B"}}, false, nil)
	cursorLine := func() string {
		for _, l := range strings.Split(m.View(), "\n") {
			if strings.Contains(l, "▶ ") {
				return l
			}
		}
		t.Fatal("no highlighted row rendered")
		return ""
	}
	bold := func(l string) bool { return strings.Contains(l, "\x1b[1") }
	// Nothing selected: the pointer marks the highlight, the text stays plain.
	if l := cursorLine(); bold(l) {
		t.Fatalf("with nothing selected the highlighted row has plain text: %q", l)
	}
	// Picked (ctrl+→): the row takes the accent.
	m.ToggleSelected()
	if l := cursorLine(); !bold(l) {
		t.Fatalf("a picked row is accented: %q", l)
	}
	// Cleared (ctrl+←): plain again, nothing reads as selected.
	m.ClearSelection()
	if l := cursorLine(); bold(l) || strings.Contains(m.View(), "✓") {
		t.Fatalf("after clearing, nothing is accented: %q", l)
	}
}
