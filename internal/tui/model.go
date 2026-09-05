package tui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"math/rand"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/simone-vibes/vibez/internal/audioquality"
	"github.com/simone-vibes/vibez/internal/config"
	"github.com/simone-vibes/vibez/internal/lyrics"
	"github.com/simone-vibes/vibez/internal/openurl"
	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/art"
	"github.com/simone-vibes/vibez/internal/tui/styles"
	"github.com/simone-vibes/vibez/internal/tui/views"
	"github.com/simone-vibes/vibez/internal/vibe"
)

// ── Modal modes (vim-inspired) ────────────────────────────────────────────

type viewMode int

const (
	modeNormal         viewMode = iota
	modeSearch                  // '/' opens — query accumulates in searchQuery
	modeCommand                 // ':' opens — command accumulates in cmdBuf
	modePlaylistPicker          // 'p' in queue / ctrl+p in search
)

// ── ContentView interface ─────────────────────────────────────────────────

// ContentView is the interface every content panel must implement.
// To register a new panel: implement this interface and append to m.panels in New().
// Nothing else in the model needs to change.
type ContentView interface {
	NavKey() string   // normal-mode key to activate this panel
	NavLabel() string // short label shown in the status bar
	SetSize(w, h int)
	Update(msg tea.KeyPressMsg) tea.Cmd
	View() string
	Back() bool
}

// libraryPanel wraps views.LibraryModel to satisfy ContentView.
type libraryPanel struct{ m *views.LibraryModel }

func (p *libraryPanel) NavKey() string   { return "l" }
func (p *libraryPanel) NavLabel() string { return "library" }
func (p *libraryPanel) SetSize(w, h int) { p.m.SetSize(w, h) }
func (p *libraryPanel) Update(msg tea.KeyPressMsg) tea.Cmd {
	updated, cmd := p.m.Update(msg)
	p.m = updated
	return cmd
}
func (p *libraryPanel) View() string  { return p.m.View() }
func (p *libraryPanel) Back() bool    { return p.m.Back() }
func (p *libraryPanel) Init() tea.Cmd { return p.m.Init() }

// queuePanel wraps views.QueueModel to satisfy ContentView.
type queuePanel struct{ m *views.QueueModel }

func (p *queuePanel) NavKey() string   { return "q" }
func (p *queuePanel) NavLabel() string { return "queue" }
func (p *queuePanel) SetSize(w, h int) { p.m.SetSize(w, h) }
func (p *queuePanel) Update(msg tea.KeyPressMsg) tea.Cmd {
	p.m.Update(msg)
	return nil
}
func (p *queuePanel) View() string                          { return p.m.View() }
func (p *queuePanel) Back() bool                            { return false }
func (p *queuePanel) SetTracks(tracks []provider.Track)     { p.m.SetTracks(tracks) }
func (p *queuePanel) SelectedTrack() (int, *provider.Track) { return p.m.SelectedTrack() }
func (p *queuePanel) Select(idx int)                        { p.m.Select(idx) }

// lyricsPanel wraps views.LyricsModel to satisfy ContentView.
type lyricsPanel struct{ m *views.LyricsModel }

func (p *lyricsPanel) NavKey() string                     { return "y" }
func (p *lyricsPanel) NavLabel() string                   { return "lyrics" }
func (p *lyricsPanel) SetSize(w, h int)                   { p.m.SetSize(w, h) }
func (p *lyricsPanel) Update(msg tea.KeyPressMsg) tea.Cmd { return p.m.Update(msg) }
func (p *lyricsPanel) View() string                       { return p.m.View() }
func (p *lyricsPanel) Back() bool                         { return false }

// feedPanel wraps views.FeedModel to satisfy ContentView.
type feedPanel struct{ m *views.FeedModel }

func (p *feedPanel) NavKey() string                     { return "F" }
func (p *feedPanel) NavLabel() string                   { return "feed" }
func (p *feedPanel) SetSize(w, h int)                   { p.m.SetSize(w, h) }
func (p *feedPanel) Update(msg tea.KeyPressMsg) tea.Cmd { return p.m.Update(msg) }
func (p *feedPanel) View() string                       { return p.m.View() }
func (p *feedPanel) Back() bool                         { return false }

// eqPanel wraps views.EQModel to satisfy ContentView.
type eqPanel struct{ m *views.EQModel }

func (p *eqPanel) NavKey() string   { return "e" }
func (p *eqPanel) NavLabel() string { return "equalizer" }
func (p *eqPanel) SetSize(w, h int) { p.m.SetSize(w, h) }
func (p *eqPanel) Update(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "e" {
		return nil
	}
	return p.m.Update(msg)
}
func (p *eqPanel) View() string { return p.m.View() }
func (p *eqPanel) Back() bool   { return false }

// aboutPanel wraps views.AboutModel to satisfy ContentView.
type aboutPanel struct{ m *views.AboutModel }

func (p *aboutPanel) NavKey() string   { return "?" }
func (p *aboutPanel) NavLabel() string { return "about" }
func (p *aboutPanel) SetSize(w, h int) { p.m.SetSize(w, h) }
func (p *aboutPanel) Update(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "?" {
		return nil
	}
	return p.m.Update(msg)
}
func (p *aboutPanel) View() string { return p.m.View() }
func (p *aboutPanel) Back() bool   { return false }

// ── Messages ──────────────────────────────────────────────────────────────

type playerStateMsg player.State
type artworkLoadedMsg struct {
	url string
	gen int
	img image.Image
	err error
}
type searchResultMsg struct {
	result *provider.SearchResult
	query  string
	err    error
}

// searchMoreMsg carries a further page of catalog songs for the Tracks section.
type searchMoreMsg struct {
	gen  int // searchGen the page was requested for; stale pages are dropped
	hops int // pages already chained for one "+ 5 more" press
	page provider.SongPage
	err  error
}

// vibeCandidatesMsg is stage one of a vibe lookup: the plan and the pooled
// search hits, before a Reranker orders them.
type vibeCandidatesMsg struct {
	query    string
	plan     vibe.Plan
	via      string
	pool     []provider.Track
	warnings []string
	err      error
}

// maxSearchPageHops bounds how many pages one "+ 5 more" press may chain
// through when Apple's pages are full of songs already listed (library copies).
const maxSearchPageHops = 3

// searchDebounceMsg is emitted after the debounce delay. The gen field lets
// Update discard messages that belong to earlier keystrokes.
type searchDebounceMsg struct {
	query string
	gen   int
}
type vibeResultMsg struct {
	query     string
	tracks    []provider.Track
	err       error
	plan      vibe.Plan // what the planner made of the description
	via       string    // planner that produced plan ("Claude", "keywords", …)
	ranking   string    // how the final order came about ("picked 15 of 38", "Apple order …")
	warnings  []string  // non-fatal provider failures behind an incomplete result
	discovery bool      // true when result is from a discovery auto-refill
	radio     bool      // true when result is from a radio auto-refill
	radioGen  int       // radio generation that produced this result
}
type loveSongMsg struct {
	title string
	loved bool
	err   error
}
type songRatingMsg struct {
	trackID string
	loved   bool
}
type tickMsg time.Time
type glowTickMsg time.Time
type introTickMsg time.Time
type memTickMsg struct{ stats string }
type errMsg struct{ err error }

type artworkCache struct {
	url      string
	img      image.Image
	rendered map[art.Size][]string
	failed   bool
}

// saveVolumeMsg is returned by volume-change commands to persist the new
// volume to the config file.
type saveVolumeMsg struct{ vol float64 }
type saveAudioQualityMsg struct {
	kbps      int
	savedOnly bool
}
type playlistCreatedMsg struct{ name string }
type SessionExpiredMsg struct{}
type SessionRestoredMsg struct{}
type lyricsResultMsg struct {
	trackID string
	result  *lyrics.Result
	err     error
}
type feedResultMsg struct {
	groups []provider.RecommendationGroup
	err    error
}
type feedTracksMsg struct {
	item     provider.RecommendationItem
	tracks   []provider.Track
	play     bool // true = replace queue & play; false = append
	playNext bool
	err      error
}
type searchCollectionTracksMsg struct {
	label    string // album/playlist name for log messages
	tracks   []provider.Track
	play     bool // true = replace queue & play; false = append
	playNext bool
	err      error
}

// introLogo is the name typed out on the splash.
const introLogo = "♪ vibezAI"

// introFrames: logo types out letter-by-letter, then holds for 8 frames.
var introFrames = func() []string {
	logo := introLogo
	runes := []rune(logo)
	frames := make([]string, 0, len(runes)+16)
	for i := range runes {
		frames = append(frames, string(runes[:i+1]))
	}
	for range 8 {
		frames = append(frames, logo)
	}
	return frames
}()

const introDone = -1

// ── Discovery mode ─────────────────────────────────────────────────────────

// discoveryMode holds state for the continuous-discovery feature.
// When enabled, songs are queued according to autoMode and refillCap.
// The similarity value (0=very different, 1=very similar) controls how
// adventurous the search is and is set via the metric picker (d key).
type discoveryMode struct {
	enabled        bool
	autoMode       bool // true = auto-refill on last song; false = one-shot
	refillCap      int  // songs to add per cycle
	seed           *provider.Track
	similarity     float64         // 0.0–1.0; persists across stop/start
	refilling      bool            // background search in progress
	triggeredForID string          // ID of track for which we already fired a search
	skipped        map[string]bool // IDs/keys of tracks skipped due to unavailability
	retries        int             // consecutive failed refill attempts (circuit breaker)
}

const discoveryMaxRetries = 5 // give up re-arming after this many consecutive failures

const (
	discoverySimilarityStep = 0.1
)

// ── Radio mode ──────────────────────────────────────────────────────────────

// radioMode holds state for the continuous radio feature: an Apple Music
// station seeded from a track, auto-refilled as the queue runs low. Mutually
// exclusive with discoveryMode — starting one stops the other, since both
// compete for the same last-track refill trigger.
type radioMode struct {
	enabled        bool
	seed           *provider.Track
	refilling      bool            // background search in progress
	triggeredForID string          // ID of track for which we already fired a search
	skipped        map[string]bool // IDs/keys of tracks skipped due to unavailability
	retries        int             // consecutive failed refill attempts (circuit breaker)
	generation     int             // increments whenever radio starts/stops to ignore stale results
}

const radioMaxRetries = 5 // give up re-arming after this many consecutive failures

// ── Model ─────────────────────────────────────────────────────────────────

type Model struct {
	cfg      *config.Config
	provider provider.Provider
	player   player.Player

	width, height int

	playerState player.State
	stateCh     <-chan player.State

	// Album art view (:art). artMode mirrors cfg.AlbumArt; the cover is
	// fetched per track and the rendered half-block lines are cached per size
	// so they only re-render on a track change or a resize.
	artMode          bool
	artwork          artworkCache
	artworkGen       int
	artHTTP          *http.Client
	supportsArtColor func() bool
	artCellAsp       float64          // terminal cell height/width ratio, for square art
	queueIDs         []string         // current playback queue (for "add to queue")
	queueTracks      []provider.Track // full track objects parallel to queueIDs
	queueMiniOffset  int              // scroll offset for the mini-queue in the split view
	queueStatePath   string           // file the queue is saved to between runs ("" = off)
	queueDirty       bool             // queue changed since it was last saved
	queueResumeIdx   int              // restored queue: track to start from on first play, -1 = none
	queueCursor      int              // highlighted queue entry (-1 only while the queue is empty)
	queueFollow      bool             // highlight tracks the playing song until the user moves it
	libraryCache     []provider.Track // the whole library, for random picks (T)
	libraryCacheAt   time.Time        // when libraryCache was fetched
	randomGen        int              // generation of the latest random-library pick
	relatedGen       int              // generation of the latest R (related songs) lookup

	// Discovery mode
	discovery discoveryMode

	// Radio mode
	radio radioMode

	// Panels
	panels      []ContentView // registered content panels; add new ones in New()
	activePanel int           // index into panels; -1 = none active
	library     *libraryPanel
	queue       *queuePanel
	lyricsP     *lyricsPanel
	feedP       *feedPanel
	eqP         *eqPanel
	aboutP      *aboutPanel

	// Lyrics
	lyricsClient      *lyrics.Client
	lastLyricsTrackID string // ID of the track for which lyrics were last fetched

	// Vibe panel (always visible, right split)
	vibe *views.VibeModel
	// vibePlanner turns a vibes-mode description into search terms (Claude
	// Code CLI or the keyword table, per config "vibe_agent").
	vibePlanner vibe.Planner

	// Search popup (not a panel)
	search *views.SearchModel

	// Modal state
	mode viewMode

	// Search accumulation (mode == modeSearch)
	searchQuery  string
	searchCursor int          // rune index of the cursor within searchQuery
	searchGen    int          // incremented on every keystroke; used to discard stale results
	searchShown  string       // query whose results the search panel currently lists
	searchSrc    searchSource // what the Search column asks: Apple Music, Claude Code or the saved lists (Ctrl+/ cycles)
	searchTyping bool         // the Search prompt takes keystrokes (Ctrl+' toggles; Enter and Esc end it)
	vibeShown    string       // vibe description whose songs the panel currently lists
	// Each mode keeps its own result list, so Ctrl+/ switches between them
	// without redoing a lookup; m.search points at the active one.
	searchAM *views.SearchModel // Apple Music search results
	searchCC *views.SearchModel // Claude Code (vibes) results
	searchSV *views.SearchModel // the saved track lists, one foldable section each

	// Command accumulation (mode == modeCommand)
	cmdBuf string

	// Double-key tracking (for 'gg')
	lastKey string

	// Errors / status messages
	errMsg    string
	errExpiry time.Time

	// Debug log
	debugLog    []string
	debugView   bool
	debugScroll int // lines scrolled up from tail (0 = show latest)

	// Favorites: track IDs that the user has hearted this session.
	favorites map[string]bool

	// Animation
	glowStep   int
	introStep  int    // introDone (-1) when complete
	initStatus string // status text shown on the loading screen

	// Memory profiling (enabled with --mem-profiling)
	memProfiling bool
	memStats     string
	helperPaths  []string

	// Playlist picker (modePlaylistPicker)
	playlistPickerTrack      *provider.Track
	playlistPickerItems      []provider.Playlist
	playlistPickerCursor     int
	playlistPickerLoading    bool
	playlistPickerReturnMode viewMode
	playlistPickerGen        int
}

func New(cfg *config.Config, prov provider.Provider, plyr player.Player, opts Options) *Model {
	m := &Model{
		cfg:          cfg,
		provider:     prov,
		player:       plyr,
		activePanel:  -1,
		memProfiling: opts.MemProfiling,
		artMode:      cfg.AlbumArt,
		artwork:      artworkCache{rendered: map[art.Size][]string{}},
		artHTTP:      &http.Client{Timeout: 5 * time.Second},
		// Album art needs at least a 256-colour terminal to look reasonable;
		// on 16-colour/ASCII terminals we skip it (and its download) entirely.
		supportsArtColor: art.SupportsColor,
		// Measured cell height/width ratio, so album art renders as a true square.
		artCellAsp: cellAspect(),
	}
	if plyr != nil {
		m.stateCh = plyr.Subscribe()
	}
	m.library = &libraryPanel{m: views.NewLibrary(prov)}
	m.queue = &queuePanel{m: views.NewQueue()}
	m.queueStatePath = opts.QueueStatePath
	m.queueCursor = noQueueCursor
	m.queueFollow = true
	m.lyricsP = &lyricsPanel{m: views.NewLyrics()}
	m.lyricsClient = lyrics.NewClient()
	m.feedP = &feedPanel{m: views.NewFeed()}
	eqBands := configEQBandsToPlayer(cfg.EQBands)
	m.eqP = &eqPanel{m: views.NewEqualizer(eqBands)}
	m.vibe = views.NewVibe()
	m.vibePlanner = vibe.NewPlanner(cfg.VibeAgent, cfg.VibeModel, cfg.VibeEffort)
	m.searchAM = views.NewSearch(prov)
	m.searchCC = views.NewSearch(prov)
	m.searchSV = views.NewSearch(prov)
	m.search = m.searchAM
	m.favorites = make(map[string]bool)
	m.aboutP = &aboutPanel{m: views.NewAbout()}
	// The library browser panel is not offered (search covers the library);
	// m.library is kept for the engine-ready wiring and message forwarding.
	m.panels = []ContentView{m.lyricsP, m.feedP, m.eqP, m.aboutP}
	m.restoreQueue()
	if opts.Backend != "" {
		m.appendLog("[engine] backend: " + opts.Backend)
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tick(),
		glowTick(),
		introTick(),
	}
	if m.provider != nil {
		cmds = append(cmds, m.library.Init())
	}
	if m.stateCh != nil {
		cmds = append(cmds, waitForState(m.stateCh))
	}
	return tea.Batch(cmds...)
}

// ── Timers ────────────────────────────────────────────────────────────────

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func glowTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return glowTickMsg(t) })
}

func introTick() tea.Cmd {
	return tea.Tick(60*time.Millisecond, func(t time.Time) tea.Msg { return introTickMsg(t) })
}

func memTick(helperPaths []string) tea.Cmd {
	return tea.Tick(3*time.Second, func(_ time.Time) tea.Msg {
		return memTickMsg{stats: collectMemStats(helperPaths)}
	})
}

func waitForState(ch <-chan player.State) tea.Cmd {
	return func() tea.Msg {
		s := <-ch
		// Drain any additional buffered states, keeping only the most recent.
		// During a track transition the player fires a rapid burst of events
		// (paused → buffering → playing). Processing each one floods the event
		// loop and starves glowTick, causing the animation to freeze. Collapsing
		// the burst into a single Update cycle keeps the UI responsive.
		for {
			select {
			case newer := <-ch:
				s = newer
			default:
				return playerStateMsg(s)
			}
		}
	}
}

// ── Update ────────────────────────────────────────────────────────────────

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inner := max(0, m.width-2)
		contentW := max(0, inner-2)
		panelH := m.panelHeight()
		m.library.SetSize(contentW, panelH)
		m.search.SetSize(contentW, panelH)
		m.lyricsP.SetSize(contentW, panelH)
		m.feedP.SetSize(contentW, panelH)
		m.eqP.SetSize(contentW, panelH)
		m.aboutP.SetSize(contentW, panelH)

	case tickMsg:
		if m.errMsg != "" && time.Now().After(m.errExpiry) {
			m.errMsg = ""
		}
		m.flushQueueState()
		cmds = append(cmds, tick())

	case glowTickMsg:
		m.glowStep++
		cmds = append(cmds, glowTick())

	case introTickMsg:
		if m.introStep != introDone {
			m.introStep++
			if m.introStep >= len(introFrames) {
				if m.player == nil {
					// Hold at last frame until the engine signals ready.
					m.introStep = len(introFrames) - 1
				} else {
					m.introStep = introDone
				}
			}
			cmds = append(cmds, introTick())
		}

	case playerStateMsg:
		wasPlaying := m.playerState.Playing
		prevTrack := m.playerState.Track
		s := player.State(msg)
		if s.Log != "" {
			m.appendLog(s.Log)
			s.Log = ""
		}
		if s.Error != "" {
			if strings.Contains(s.Error, "CONTENT_RESTRICTED") {
				// Track is region-locked or unavailable in this storefront.
				// Log it silently and skip to the next track — same behaviour
				// as any streaming app encountering a restricted title.
				title := "track"
				if s.Track != nil {
					title = s.Track.Artist + " — " + s.Track.Title
				}
				m.appendLog(fmt.Sprintf("[skip] restricted: %s", title))
				if m.player != nil {
					cmds = append(cmds, m.playerCmd(func(p player.Player) error { return p.Next() }))
				}
			} else {
				m.appendLog("[error] " + s.Error)
				m.errMsg = s.Error
				m.errExpiry = time.Now().Add(4 * time.Second)
			}
			s.Error = ""
		}
		if s.SkippedID != "" {
			// JS silently skipped a track (CONTENT_RESTRICTED / unavailable).
			// Record the ID in the discovery blacklist so it won't be proposed
			// again this session, purge ALL blacklisted entries from the queue
			// (there may be duplicates from earlier discovery cycles), then
			// re-arm discovery so music keeps flowing without interruption.
			skippedID := s.SkippedID
			s.SkippedID = ""
			if m.discovery.skipped == nil {
				m.discovery.skipped = make(map[string]bool)
			}
			m.discovery.skipped[skippedID] = true
			if m.radio.skipped == nil {
				m.radio.skipped = make(map[string]bool)
			}
			m.radio.skipped[skippedID] = true
			m.purgeSkippedFromQueue()
			if m.discovery.enabled && !m.discovery.refilling &&
				m.discovery.retries < discoveryMaxRetries {
				m.discovery.retries++
				m.discovery.triggeredForID = ""
				m.discovery.refilling = true
				m.syncDiscoveryView()
				cmds = append(cmds, m.runDiscoverySearch())
			} else if m.discovery.retries >= discoveryMaxRetries {
				m.appendLog("[discovery] max retries reached — giving up")
			}
			if m.radio.enabled && !m.radio.refilling &&
				m.radio.retries < radioMaxRetries {
				m.radio.retries++
				m.radio.triggeredForID = ""
				m.radio.refilling = true
				cmds = append(cmds, m.runRadioSearch())
			} else if m.radio.retries >= radioMaxRetries {
				m.appendLog("[radio] max retries reached — giving up")
			}
		}
		if s.Track == nil || s.Track.ArtworkURL == "" {
			if m.artwork.url != "" || m.artwork.img != nil || m.artwork.failed {
				m.artworkGen++
			}
			m.artwork = artworkCache{rendered: map[art.Size][]string{}}
		} else if s.Track.ArtworkURL != m.artwork.url {
			m.artworkGen++
			m.artwork = artworkCache{url: s.Track.ArtworkURL, rendered: map[art.Size][]string{}}
			// Only download covers while the art view is active; toggling
			// :art on fetches the current track's cover on demand.
			if m.artMode {
				cmds = append(cmds, m.fetchArtworkCmd(s.Track.ArtworkURL, m.artworkGen))
			}
		}
		if s.Track != nil && (m.playerState.Track == nil || m.playerState.Track.Title != s.Track.Title) {
			m.appendLog("[playing] " + s.Track.Artist + " — " + s.Track.Title)
			// Log playParams so we can confirm which ID path MusicKit will use.
			trackType := "catalog"
			if strings.HasPrefix(s.Track.ID, "i.") {
				trackType = "library"
			}
			pp := fmt.Sprintf("[playParams] id=%s type=%s", s.Track.ID, trackType)
			if s.Track.CatalogID != "" {
				pp += " catalogId=" + s.Track.CatalogID
			}
			m.appendLog(pp)
			// Check whether the new track is already loved on Apple Music.
			cmds = append(cmds, m.checkSongRatingCmd(s.Track))
			// Fetch lyrics for the new track: immediately if the panel is
			// visible, otherwise mark stale so the fetch is deferred until
			// the user opens the panel (lazy loading).
			if id := views.PlaybackID(*s.Track); id != m.lastLyricsTrackID {
				lyricsOpen := m.activePanel >= 0 && m.panels[m.activePanel] == m.lyricsP
				if lyricsOpen {
					m.lastLyricsTrackID = id
					m.lyricsP.m.SetLoading()
					cmds = append(cmds, m.fetchLyricsCmd(s.Track))
				} else {
					m.lastLyricsTrackID = "" // stale; will fetch on panel open
				}
			}
			// While the highlight follows playback, move it (and the view) to
			// the new track; a highlight the user moved stays put.
			if m.queueFollow {
				id := views.PlaybackID(*s.Track)
				for i, t := range m.queueTracks {
					if views.PlaybackID(t) == id || t.Title == s.Track.Title {
						m.queueCursor = i
						visibleRows := max(0, m.panelHeight()-2)
						if visibleRows > 0 && (i < m.queueMiniOffset || i >= m.queueMiniOffset+visibleRows) {
							m.queueMiniOffset = max(0, i-visibleRows/2)
						}
						break
					}
				}
			}
		}
		// Always sync playback position so the current lyrics line stays highlighted.
		if s.Track != nil {
			m.lyricsP.m.SetPosition(s.Position)
		}
		// Discovery: in auto mode, fire as soon as the last track in the queue
		// starts playing. Triggering at the start of the last track gives the
		// search the maximum possible time to complete before the queue runs dry.
		// triggeredForID ensures we fire exactly once per track.
		isLastQueued := len(m.queueTracks) > 0 && s.Track != nil &&
			views.PlaybackID(*s.Track) == views.PlaybackID(m.queueTracks[len(m.queueTracks)-1])
		if m.discovery.enabled && m.discovery.autoMode && !m.discovery.refilling &&
			isLastQueued &&
			m.discovery.triggeredForID != s.Track.ID {
			m.discovery.triggeredForID = s.Track.ID
			m.discovery.refilling = true
			m.syncDiscoveryView()
			cmds = append(cmds, m.runDiscoverySearch())
		}
		// Radio: same last-track refill trigger as discovery, but always
		// continuous (no one-shot mode).
		if m.radio.enabled && !m.radio.refilling &&
			isLastQueued &&
			m.radio.triggeredForID != s.Track.ID {
			m.radio.triggeredForID = s.Track.ID
			m.radio.refilling = true
			cmds = append(cmds, m.runRadioSearch())
		}
		m.playerState = s
		if trackChanged(prevTrack, s.Track) {
			m.queueDirty = true
		}
		if !wasPlaying && m.playerState.Playing {
			m.appendLog("[player] playing")
		} else if wasPlaying && !m.playerState.Playing && !m.playerState.Loading {
			m.appendLog("[player] paused")
		}
		if m.stateCh != nil {
			cmds = append(cmds, waitForState(m.stateCh))
		}

	case artworkLoadedMsg:
		if msg.url != m.artwork.url || msg.gen != m.artworkGen {
			break
		}
		if msg.err != nil {
			m.artwork.failed = true
			m.artwork.img = nil
			m.artwork.rendered = map[art.Size][]string{}
			m.appendLog(fmt.Sprintf("[art] artwork unavailable: %v", msg.err))
			break
		}
		m.artwork.img = msg.img
		m.artwork.failed = false
		m.artwork.rendered = map[art.Size][]string{}

	case songRatingMsg:
		// Update favorite state to match what Apple Music reports.
		if msg.trackID != "" {
			m.favorites[msg.trackID] = msg.loved
		}

	case DebugLogMsg:
		m.appendLog(string(msg))

	case lyricsResultMsg:
		// Discard stale results if the user skipped to a different track.
		if msg.trackID == m.lastLyricsTrackID {
			m.lyricsP.m.SetLyrics(msg.result, msg.err)
			if msg.err != nil {
				m.appendLog(fmt.Sprintf("[lyrics] not found: %v", msg.err))
			} else {
				m.appendLog("[lyrics] loaded")
			}
		}

	case feedResultMsg:
		if msg.err != nil {
			m.feedP.m.SetError(msg.err)
			m.appendLog(fmt.Sprintf("[feed] error: %v", msg.err))
		} else {
			m.feedP.m.SetRecommendations(msg.groups)
			m.appendLog(fmt.Sprintf("[feed] loaded %d groups", len(msg.groups)))
		}

	case views.EQChangeMsg:
		if m.player != nil {
			if err := m.player.SetEqualizer(msg.Bands); err != nil {
				m.appendLog(fmt.Sprintf("[eq] error: %v", err))
			}
		}
		m.cfg.EQBands = playerEQBandsToConfig(msg.Bands)
		if err := m.cfg.Save(""); err != nil {
			m.appendLog(fmt.Sprintf("[eq] config save error: %v", err))
		}

	case feedTracksMsg:
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("feed: %v", msg.err)
			m.errExpiry = time.Now().Add(4 * time.Second)
			m.appendLog(fmt.Sprintf("[feed] track fetch error: %v", msg.err))
			break
		}
		if len(msg.tracks) == 0 {
			m.errMsg = "feed: no playable tracks"
			m.errExpiry = time.Now().Add(3 * time.Second)
			break
		}
		ids := make([]string, len(msg.tracks))
		for i, t := range msg.tracks {
			ids[i] = views.PlaybackID(t)
		}
		if msg.play {
			return m, m.appendAndPlay(msg.item.Title, msg.tracks, ids, 0)
		}
		if msg.playNext {
			return m, m.playNextCmd(msg.item.Title, msg.tracks, ids)
		}
		return m, m.addToQueue(msg.item.Title, msg.tracks, ids)

	case views.DiscoveryMetricSelectedMsg:
		// User confirmed a metric in the picker — store it and immediately start
		// discovery in continuous auto mode (mirrors the old single-key behaviour).
		m.discovery.similarity = msg.Similarity
		m.appendLog(fmt.Sprintf("[discovery] metric set: %.0f%% similarity", msg.Similarity*100))
		if m.playerState.Track != nil {
			cmds = append(cmds, m.startDiscovery(true, 1))
		}

	case vibeCandidatesMsg:
		cmds = append(cmds, m.handleVibeCandidates(msg))

	case vibeResultMsg:
		if !msg.radio && !msg.discovery {
			m.handleVibeSearchResult(msg)
			break
		}
		if msg.radio && (!m.radio.enabled || msg.radioGen != m.radio.generation) {
			break
		}
		if msg.err != nil {
			if msg.radio {
				m.appendLog(fmt.Sprintf("[radio] search error: %v", msg.err))
				m.radio.refilling = false
				break
			}
			m.appendLog(fmt.Sprintf("[discovery] search error: %v", msg.err))
			m.discovery.refilling = false
			m.syncDiscoveryView()
			break
		}
		// A result can succeed and still be incomplete when one provider backend
		// failed. Say so: otherwise a thin result set reads as a sparse catalog
		// rather than a broken backend.
		if len(msg.warnings) > 0 {
			m.appendLog(fmt.Sprintf("[search] partial results: %s", strings.Join(msg.warnings, "; ")))
		}

		// For discovery/radio results, drop any track that arrived in the
		// blacklist while the search was in flight (race between search
		// goroutine and a concurrent goSkipped notification), and also drop
		// any track already present in the queue (dedup by ID and
		// artist||title).
		tracks := msg.tracks
		if msg.discovery || msg.radio {
			skipped := m.discovery.skipped
			if msg.radio {
				skipped = m.radio.skipped
			}
			filtered := tracks[:0]
			for _, t := range tracks {
				id := views.PlaybackID(t)
				key := strings.ToLower(t.Artist + "||" + t.Title)
				if skipped[id] {
					continue
				}
				dup := slices.Contains(m.queueIDs, id)
				if !dup {
					for _, qt := range m.queueTracks {
						if strings.ToLower(qt.Artist+"||"+qt.Title) == key {
							dup = true
							break
						}
					}
				}
				if !dup {
					filtered = append(filtered, t)
				}
			}
			tracks = filtered
		}
		if len(tracks) == 0 {
			switch {
			case msg.discovery && m.discovery.retries < discoveryMaxRetries:
				m.discovery.retries++
				m.discovery.refilling = true
				m.syncDiscoveryView()
				cmds = append(cmds, m.runDiscoverySearch())
			case msg.radio && m.radio.retries < radioMaxRetries:
				m.radio.retries++
				m.radio.refilling = true
				cmds = append(cmds, m.runRadioSearch())
			case msg.discovery:
				m.discovery.refilling = false
				m.syncDiscoveryView()
			case msg.radio:
				m.radio.refilling = false
			}
			break
		}
		ids := make([]string, len(tracks))
		for i, t := range tracks {
			ids[i] = views.PlaybackID(t)
		}
		if m.player != nil {
			if err := m.player.AppendQueue(ids); err != nil {
				break
			}
		}
		m.queueTracks = append(m.queueTracks, tracks...)
		m.queueIDs = append(m.queueIDs, ids...)
		m.syncQueue()
		switch {
		case msg.discovery:
			m.discovery.refilling = false
			m.discovery.retries = 0 // successful refill — reset circuit breaker
			if !m.discovery.autoMode {
				// One-shot: disable discovery after this successful refill.
				m.discovery.enabled = false
				m.discovery.seed = nil
			}
			m.syncDiscoveryView()
			m.appendLog(fmt.Sprintf("[discovery] refilled %d tracks", len(tracks)))
		case msg.radio:
			m.radio.refilling = false
			m.radio.retries = 0 // successful refill — reset circuit breaker
			m.appendLog(fmt.Sprintf("[radio] refilled %d tracks", len(tracks)))
		}

	case loveSongMsg:
		if msg.err != nil {
			m.appendLog(fmt.Sprintf("[fav] ✗ %s: %v", msg.title, msg.err))
		} else {
			state := "♡"
			if msg.loved {
				state = "♥"
			}
			m.appendLog(fmt.Sprintf("[fav] %s %s synced to Apple Music", state, msg.title))
		}

	case searchDebounceMsg:
		// Drop stale debounce ticks — only the latest keystroke wins.
		if msg.gen != m.searchGen || m.provider == nil {
			return m, nil
		}
		m.appendLog(fmt.Sprintf("[search] %q…", msg.query))
		prov := m.provider
		query := msg.query
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result, err := prov.Search(ctx, query)
			return searchResultMsg{result: result, query: query, err: err}
		}

	case searchResultMsg:
		if msg.err != nil {
			m.appendLog(fmt.Sprintf("[search] error: %v", msg.err))
			m.searchAM.SetResults(nil, false, msg.err)
		} else {
			if msg.result != nil {
				if len(msg.result.Warnings) > 0 {
					m.appendLog(fmt.Sprintf("[search] partial results: %s",
						strings.Join(msg.result.Warnings, "; ")))
				}
				m.appendLog(fmt.Sprintf("[search] %d track(s), %d album(s), %d playlist(s)",
					len(msg.result.Tracks), len(msg.result.Albums), len(msg.result.Playlists)))
			}
			m.searchAM.SetResults(msg.result, false, nil)
			m.searchShown = msg.query
		}

	case searchMoreMsg:
		if msg.gen != m.searchGen { // a newer query replaced these results
			return m, nil
		}
		if msg.err != nil {
			m.searchAM.EndPaging()
			m.appendLog(fmt.Sprintf("[search] more tracks: %v", msg.err))
			m.errMsg = fmt.Sprintf("search: %v", msg.err)
			m.errExpiry = time.Now().Add(4 * time.Second)
			return m, nil
		}
		still := m.searchAM.AppendCatalogTracks(msg.page)
		m.appendLog(fmt.Sprintf("[search] +%d catalog track(s) (next offset %d, more=%v)",
			len(msg.page.Tracks), msg.page.Next, msg.page.More))
		if still > 0 && msg.hops < maxSearchPageHops {
			return m, m.fetchMoreTracksCmd(still, msg.hops+1)
		}
		return m, nil

	case selectionTracksMsg:
		if msg.err != nil {
			m.appendLog(fmt.Sprintf("[search] selection: %v", msg.err))
		}
		if len(msg.tracks) == 0 {
			m.errMsg = "no playable tracks found"
			m.errExpiry = time.Now().Add(3 * time.Second)
			break
		}
		if msg.play {
			return m, m.appendAndPlay(msg.label, msg.tracks, playbackIDs(msg.tracks), 0)
		}
		return m, m.addToQueue(msg.label, msg.tracks, playbackIDs(msg.tracks))

	case searchCollectionTracksMsg:
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("search: %v", msg.err)
			m.errExpiry = time.Now().Add(4 * time.Second)
			m.appendLog(fmt.Sprintf("[search] collection fetch error: %v", msg.err))
			break
		}
		if len(msg.tracks) == 0 {
			m.errMsg = "no playable tracks found"
			m.errExpiry = time.Now().Add(3 * time.Second)
			break
		}
		ids := make([]string, len(msg.tracks))
		for i, t := range msg.tracks {
			ids[i] = views.PlaybackID(t)
		}
		if msg.play {
			return m, m.appendAndPlay(msg.label, msg.tracks, ids, 0)
		}
		if msg.playNext {
			return m, m.playNextCmd(msg.label, msg.tracks, ids)
		}
		return m, m.addToQueue(msg.label, msg.tracks, ids)

	case views.QueueTracksMsg:
		if len(msg.Tracks) == 0 || len(msg.IDs) == 0 {
			break
		}
		if msg.PlayNext {
			return m, m.playNextCmd(msg.Label, msg.Tracks, msg.IDs)
		}
		return m, m.addToQueue(msg.Label, msg.Tracks, msg.IDs)

	case views.PlayTracksMsg:
		// Library "play": add to the end of the queue and start there. Never
		// replaces the queue (so playlists go through their track IDs too).
		tracks := msg.Tracks
		if len(tracks) == 0 && msg.Track != nil {
			tracks = []provider.Track{*msg.Track}
		}
		label := "library"
		if msg.Track != nil {
			label = msg.Track.Artist + " — " + msg.Track.Title
		}
		cmds = append(cmds, m.appendAndPlay(label, tracks, msg.IDs, msg.StartIdx))

	case InitStatusMsg:
		m.initStatus = string(msg)

	case EngineReadyMsg:
		m.player = msg.Player
		m.provider = msg.Provider
		m.stateCh = msg.Player.Subscribe()
		m.library.m = views.NewLibrary(msg.Provider)
		// Re-apply the window size that was stored from the earlier WindowSizeMsg,
		// since the new inner LibraryModel starts with zero dimensions.
		if m.width > 0 {
			inner := max(0, m.width-2)
			m.library.SetSize(max(0, inner-2), m.panelHeight())
		}
		m.searchAM = views.NewSearch(msg.Provider)
		m.searchCC = views.NewSearch(msg.Provider)
		m.searchSV = views.NewSearch(msg.Provider)
		m.search = m.searchFor(m.searchSrc)
		if m.searchSrc == searchSaved {
			m.refreshSavedLists()
		}
		m.helperPaths = msg.HelperPaths
		m.appendLog("[engine] backend: " + msg.Backend)
		cmds = append(cmds, waitForState(m.stateCh), m.library.Init())
		if m.cfg.Volume != nil {
			v := m.cfg.VolumeOrDefault()
			cmds = append(cmds, m.playerCmd(func(p player.Player) error {
				return p.SetVolume(v)
			}))
			m.appendLog(fmt.Sprintf("[vol] restored %.0f%% from config", v*100))
		}
		if len(m.cfg.EQBands) > 0 {
			bands := configEQBandsToPlayer(m.cfg.EQBands)
			cmds = append(cmds, m.playerCmd(func(p player.Player) error {
				return p.SetEqualizer(bands)
			}))
			m.appendLog("[eq] restored from config")
		}
		if m.memProfiling {
			cmds = append(cmds, memTick(m.helperPaths))
		}

	case memTickMsg:
		m.memStats = msg.stats
		if m.memProfiling {
			return m, memTick(m.helperPaths)
		}

	case InitErrMsg:
		m.appendLog("[init error] " + msg.Err.Error())
		m.errMsg = msg.Err.Error()
		m.errExpiry = time.Now().Add(30 * time.Second)
		m.introStep = introDone

	case saveVolumeMsg:
		m.errMsg = fmt.Sprintf("♪ %d%%", int(msg.vol*100+0.5))
		m.errExpiry = time.Now().Add(1500 * time.Millisecond)
		m.cfg.SetVolume(msg.vol)
		if err := m.cfg.Save(""); err != nil {
			m.appendLog(fmt.Sprintf("[vol] config save error: %v", err))
		}

	case saveAudioQualityMsg:
		if err := m.cfg.SetAudioBitrate(msg.kbps); err != nil {
			m.appendLog(fmt.Sprintf("[quality] config error: %v", err))
			break
		}
		if err := m.cfg.Save(""); err != nil {
			m.appendLog(fmt.Sprintf("[quality] config save error: %v", err))
			break
		}
		if msg.savedOnly {
			m.errMsg = fmt.Sprintf("✓ Audio quality saved: %d kbps AAC (used next launch; current backend cannot switch live)", msg.kbps)
		} else {
			m.errMsg = fmt.Sprintf("✓ Audio quality saved: %d kbps AAC", msg.kbps)
		}
		m.errExpiry = time.Now().Add(3 * time.Second)

	case errMsg:
		m.appendLog("[error] " + msg.err.Error())
		m.errMsg = msg.err.Error()
		m.errExpiry = time.Now().Add(3 * time.Second)

	case playlistCreatedMsg:
		m.errMsg = "✓ Playlist \"" + msg.name + "\" saved"
		m.errExpiry = time.Now().Add(4 * time.Second)

	case trackListNamedMsg:
		m.finishAutoSave(msg)

	case playlistsForPickerMsg:
		if msg.gen != m.playlistPickerGen {
			break // stale response; a newer fetch is in flight
		}
		m.playlistPickerLoading = false
		if msg.err != nil {
			m.mode = m.playlistPickerReturnMode
			m.errMsg = "could not load playlists"
			m.errExpiry = time.Now().Add(3 * time.Second)
			m.appendLog(fmt.Sprintf("[playlist] fetch error: %v", msg.err))
			break
		}
		m.playlistPickerItems = m.playlistPickerItems[:0]
		for _, pl := range msg.playlists {
			if !pl.ReadOnly {
				m.playlistPickerItems = append(m.playlistPickerItems, pl)
			}
		}
		if m.playlistPickerCursor >= len(m.playlistPickerItems) {
			m.playlistPickerCursor = 0
		}

	case trackAddedToPlaylistMsg:
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("✗ add to playlist: %v", msg.err)
			m.appendLog(fmt.Sprintf("[playlist] add error: %v", msg.err))
		} else {
			m.errMsg = fmt.Sprintf("✓ Added to \"%s\"", msg.playlistName)
			m.appendLog(fmt.Sprintf("[playlist] added to %q", msg.playlistName))
		}
		m.errExpiry = time.Now().Add(3 * time.Second)

	case SessionExpiredMsg:
		m.errMsg = "Session expired — opening browser to re-authenticate…"
		m.errExpiry = time.Now().Add(365 * 24 * time.Hour) // persists until restored

	case SessionRestoredMsg:
		m.errMsg = "✓ Re-authenticated with Apple Music"
		m.errExpiry = time.Now().Add(5 * time.Second)

	case relatedResultMsg:
		cmds = append(cmds, m.handleRelatedResult(msg))

	case randomLibraryResultMsg:
		cmds = append(cmds, m.handleRandomLibraryResult(msg))

	case RestartMsg:
		m.flushQueueState()
		return m, tea.Quit

	case tea.KeyPressMsg:
		cmd := m.handleKey(msg)
		cmds = append(cmds, cmd)

	default:
		// Forward library background loads.
		prevDrillErr := m.library.m.DrillErr()
		prevLoadErr := m.library.m.LoadErr()
		updated, libCmd := m.library.m.Update(msg)
		if err := updated.DrillErr(); err != nil && prevDrillErr == nil {
			m.appendLog("[library] playlist tracks error: " + err.Error())
		}
		if err := updated.LoadErr(); err != nil && prevLoadErr == nil {
			m.appendLog("[library] load error: " + err.Error())
		}
		m.library.m = updated
		cmds = append(cmds, libCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()

	// ctrl+c always quits
	if k == "ctrl+c" {
		m.flushQueueState()
		if m.player != nil {
			_ = m.player.Close()
		}
		return tea.Quit
	}

	switch m.mode {
	case modeSearch:
		return m.handleSearchKey(k, msg)
	case modeCommand:
		return m.handleCommandKey(k)
	case modePlaylistPicker:
		return m.handlePlaylistPickerKey(k)
	default:
		return m.handleNormalKey(msg, k)
	}
}

func (m *Model) handleSearchKey(k string, msg tea.KeyPressMsg) tea.Cmd {
	if !m.searchTyping {
		// Browsing: the prompt takes nothing until Ctrl+' opens it, so keys
		// cannot land in the query by accident. ":" opens command mode as it
		// does from Tracks.
		switch {
		case k == ":":
			m.mode = modeCommand
			m.cmdBuf = ""
			return nil
		case isSearchEditKey(k):
			return nil
		}
	}
	switch k {
	case "ctrl+'", "ctrl+;":
		// Start or stop typing into the prompt (two spellings: the keys sit
		// side by side). The saved lists take no text.
		if m.searchSrc == searchSaved {
			m.flashStatus("the saved lists take no text; ^/ for Apple Music or Claude Code", 3*time.Second)
			return nil
		}
		m.searchTyping = !m.searchTyping
		return nil
	case "esc":
		// Typing: stop, keeping the text. Browsing: hand the keys back to the
		// queue; the query and results stay visible.
		if m.searchTyping {
			m.searchTyping = false
			return nil
		}
		m.mode = modeNormal
		return nil
	case "ctrl+/", "ctrl+_":
		// Cycle the source: Apple Music ("AM", searches on Enter) → Claude
		// Code ("CC", Enter finds songs for a description) → the saved lists
		// ("SV") → Apple Music. A plain "/" is text ("AC/DC"); terminals
		// without the kitty protocol report Ctrl+/ as ctrl+_ (0x1F), so both
		// spellings are accepted.
		m.setSearchSource(m.searchSrc.next())
		// Each source keeps what it showed last time. Claude Code looks new
		// text up on the way in (the same text keeps its songs); Apple Music
		// searches on Enter only and the saved lists never look anything up.
		if m.searchSrc == searchClaude && m.searchQuery != "" && m.searchQuery != m.vibeShown {
			return m.startVibeSearch(m.searchQuery)
		}
		return nil
	case "enter":
		// Typing: Enter ends it and runs the text, an Apple Music search or a
		// Claude Code lookup (unchanged text is not run again). Browsing:
		// Enter acts on rows.
		if m.searchTyping {
			m.searchTyping = false
			switch m.searchSrc {
			case searchApple:
				if m.searchQuery != m.searchShown {
					return m.scheduleSearch(m.searchQuery)
				}
			case searchClaude:
				if m.searchQuery != "" && m.searchQuery != m.vibeShown {
					return m.startVibeSearch(m.searchQuery)
				}
			}
			return nil
		}
		// Enter on a section header folds it or opens it again.
		if section, ok := m.search.SelectedHeader(); ok {
			m.search.ToggleSectionOpen(section)
			return nil
		}
		// "+ 5 more" / "− 5 less" rows grow or shrink their section.
		if section, more, ok := m.search.SelectedToggle(); ok {
			if !more {
				m.search.ShowLess(section)
				return nil
			}
			// "+ 5 more" past the loaded catalog songs pages Apple's results.
			if wanted := m.search.ShowMore(section); wanted > 0 {
				return m.fetchMoreTracksCmd(wanted, 0)
			}
			return nil
		}
		// On a song, album or playlist Enter does nothing: adding is Ctrl+, / Ctrl+.
		return nil
	case "tab", "shift+tab":
		// Hand the keys back to the queue; the query and results stay visible.
		m.mode = modeNormal
		return nil
	case "ctrl+shift+up", "ctrl+shift+down":
		// Sweep: select the highlighted item, move, select the one landed on.
		dir := 1
		if k == "ctrl+shift+up" {
			dir = -1
		}
		m.search.SelectAndMove(dir)
		return nil
	case "ctrl+right":
		// Toggle the highlighted item in or out of the selection, so a
		// selection need not be contiguous.
		m.search.ToggleSelected()
		return nil
	case "ctrl+left":
		// Clear the selection; pressed again before anything else changes
		// it, bring the same selection back.
		if !m.search.RestoreSelection() {
			m.search.ClearSelection()
		}
		return nil
	case "ctrl+delete":
		// SV: the highlighted list goes, from disk and from the panel.
		return m.deleteSavedList()
	case "ctrl+,":
		// The selection (or the highlighted item) goes to Tracks; nothing starts.
		return m.addSelection(false)
	case "ctrl+.":
		// The selection (or the highlighted item) goes to Tracks and its first song starts.
		return m.addSelection(true)
	case "ctrl+up", "ctrl+down":
		// Move the Search highlight; the plain arrows belong to Tracks (below).
		code := tea.KeyUp
		if k == "ctrl+down" {
			code = tea.KeyDown
		}
		_, cmd := m.search.Update(tea.KeyPressMsg{Code: code})
		return cmd
	case "pgup", "pgdown":
		_, cmd := m.search.Update(msg)
		return cmd
	case "up", "down":
		// The Tracks highlight moves without leaving Search: where R and T
		// insert, and what enter plays after Tab, can be set while a search
		// is on screen.
		delta := 1
		if k == "up" {
			delta = -1
		}
		m.moveQueueCursor(delta)
		return nil
	case "left":
		if m.searchCursor > 0 {
			m.searchCursor--
		}
		return nil
	case "right":
		if m.searchCursor < len([]rune(m.searchQuery)) {
			m.searchCursor++
		}
		return nil
	case "home", "ctrl+a":
		m.searchCursor = 0
		return nil
	case "end", "ctrl+e":
		m.searchCursor = len([]rune(m.searchQuery))
		return nil
	case "backspace":
		if m.searchCursor > 0 {
			runes := []rune(m.searchQuery)
			runes = append(runes[:m.searchCursor-1], runes[m.searchCursor:]...)
			m.searchQuery = string(runes)
			m.searchCursor--
			return nil
		}
		return nil
	case "delete":
		runes := []rune(m.searchQuery)
		if m.searchCursor < len(runes) {
			runes = append(runes[:m.searchCursor], runes[m.searchCursor+1:]...)
			m.searchQuery = string(runes)
			return nil
		}
		return nil
	case "ctrl+w":
		// Delete word before cursor.
		if m.searchCursor > 0 {
			runes := []rune(m.searchQuery)
			i := m.searchCursor - 1
			for i > 0 && runes[i-1] != ' ' {
				i--
			}
			runes = append(runes[:i], runes[m.searchCursor:]...)
			m.searchQuery = string(runes)
			m.searchCursor = i
			return nil
		}
		return nil
	case "ctrl+u":
		// Delete everything before cursor.
		runes := []rune(m.searchQuery)
		m.searchQuery = string(runes[m.searchCursor:])
		m.searchCursor = 0
		return nil
	case "space":
		runes := []rune(m.searchQuery)
		runes = append(runes[:m.searchCursor], append([]rune{' '}, runes[m.searchCursor:]...)...)
		m.searchQuery = string(runes)
		m.searchCursor++
		return nil
	default:
		if len(k) == 1 && k[0] >= 32 {
			runes := []rune(m.searchQuery)
			runes = append(runes[:m.searchCursor], append([]rune{rune(k[0])}, runes[m.searchCursor:]...)...)
			m.searchQuery = string(runes)
			m.searchCursor++
			return nil
		}
	}
	return nil
}

// isSearchEditKey reports whether k edits the Search prompt: text, and the
// cursor and deletion keys. While browsing these are ignored.
func isSearchEditKey(k string) bool {
	switch k {
	case "left", "right", "home", "ctrl+a", "end", "ctrl+e", "backspace", "delete", "ctrl+w", "ctrl+u", "space":
		return true
	}
	return len(k) == 1 && k[0] >= 32
}

// ── Command palette ───────────────────────────────────────────────────────

// cmdEntry describes a single command listed in the CMD footer.
type cmdEntry struct {
	trigger     string // prefix matched against cmdBuf
	usage       string // full usage shown to the user
	description string
}

// allCommands is the master list: the CMD footer shows it and Tab completes
// from it, so the two can never disagree.
var allCommands = []cmdEntry{
	{"save", "save [name]", "Save Tracks as a named list; without a name it is dated and named after the songs"},
	{"quality", "quality <high|standard|256|64>", "Set Apple Music AAC bitrate"},
	{"model", "model <fable|sonnet|haiku|default|id>", "Model Claude Code uses for CC lookups; bare :model shows the current one"},
	{"effort", "effort <low|medium|high|xhigh|max|default>", "Effort Claude Code spends on CC lookups"},
	{"about", "about", "Show information about vibez"},
	{"donate", "donate", "Support vibez development by donating"},
	{"debug-logs", "debug-logs", "Toggle debug log panel"},
	{"q", "q", "Quit (:quit works too)"},
}

// retiredCommands still run when typed but are neither listed in the footer
// nor completed: the fork does not use them. Move an entry back into
// allCommands to bring it back.
var retiredCommands = []cmdEntry{
	{"discover", "discover <n>|auto|stop|metric", "Queue n discovered songs now, auto-discover until stopped, or pick the similarity"},
	{"art", "art", "Toggle album-art view (cover + track info instead of the bar)"},
	{"radio", "radio", "Toggle continuous radio seeded by the playing track (R inserts 5 related songs once)"},
	{"shuffle", "shuffle", "Toggle the engine's shuffled play order (s jumps to a random queued song)"},
}

// commandSuggestions returns commands whose trigger starts with the current
// cmdBuf, or all commands when the buffer is empty.
func (m *Model) commandSuggestions() []cmdEntry {
	var out []cmdEntry
	for _, c := range allCommands {
		if m.cmdBuf == "" || strings.HasPrefix(c.trigger, m.cmdBuf) ||
			strings.HasPrefix(c.usage, m.cmdBuf) {
			out = append(out, c)
		}
	}
	return out
}

func (m *Model) handleCommandKey(k string) tea.Cmd {
	switch k {
	case "esc":
		m.mode = modeNormal
		m.cmdBuf = ""
	case "enter":
		cmd := m.cmdBuf
		m.cmdBuf = ""
		m.mode = modeNormal
		return m.executeCommand(cmd)
	case "tab":
		// Complete to the first command matching what is typed, keeping the
		// trailing space when the command takes an argument.
		if suggs := m.commandSuggestions(); len(suggs) > 0 {
			m.cmdBuf = suggs[0].usage
			if idx := strings.Index(m.cmdBuf, " <"); idx >= 0 {
				m.cmdBuf = m.cmdBuf[:idx+1]
			}
		}
	case "backspace":
		if len(m.cmdBuf) > 0 {
			m.cmdBuf = m.cmdBuf[:len(m.cmdBuf)-1]
		}
	case "space":
		m.cmdBuf += " "
	default:
		if len(k) == 1 && k[0] >= 32 {
			m.cmdBuf += k
		}
	}
	return nil
}

func (m *Model) executeCommand(cmd string) tea.Cmd {
	switch {
	case cmd == "q" || cmd == "quit":
		m.flushQueueState()
		if m.player != nil {
			_ = m.player.Close()
		}
		return tea.Quit
	case cmd == "about":
		for i, p := range m.panels {
			if p == m.aboutP {
				m.activePanel = i
				break
			}
		}
		return nil
	case cmd == "donate":
		m.errMsg = "✓ Opening donation link..."
		m.errExpiry = time.Now().Add(5 * time.Second)
		return func() tea.Msg {
			_ = openurl.Open("https://ko-fi.com/pelpsi")
			return nil
		}
	case cmd == "debug-logs":
		m.debugView = !m.debugView
		m.debugScroll = 0
		return nil
	case cmd == "model" || strings.HasPrefix(cmd, "model "):
		arg := strings.TrimSpace(strings.TrimPrefix(cmd, "model"))
		if arg == "" {
			m.flashInfo("ℹ CC lookups: " + m.vibeSetupLabel())
			return nil
		}
		m.cfg.VibeModel = arg
		m.applyVibeSetup("model")
		return nil
	case cmd == "effort" || strings.HasPrefix(cmd, "effort "):
		arg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(cmd, "effort")))
		if arg == "" {
			m.flashInfo("ℹ CC lookups: " + m.vibeSetupLabel())
			return nil
		}
		switch arg {
		case "low", "medium", "high", "xhigh", "max":
			m.cfg.VibeEffort = arg
		case "default", "cli":
			m.cfg.VibeEffort = ""
		default:
			m.errMsg = ":effort takes low, medium, high, xhigh, max or default"
			m.errExpiry = time.Now().Add(3 * time.Second)
			return nil
		}
		m.applyVibeSetup("effort")
		return nil
	case strings.HasPrefix(cmd, "discover"):
		arg := strings.TrimSpace(strings.TrimPrefix(cmd, "discover"))
		if arg == "stop" || (arg == "" && m.discovery.enabled) {
			m.stopDiscovery()
			return nil
		}
		if arg == "metric" {
			// Pick the similarity level; the picker starts discovery when confirmed.
			sim := m.discovery.similarity
			if sim == 0 {
				sim = 0.7
			}
			if m.playerState.Track != nil && !m.vibe.PickerActive() {
				m.vibe.ShowPicker(sim)
			}
			return nil
		}
		if arg == "" || arg == "auto" {
			return m.startDiscovery(true, 1)
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			m.errMsg = ":discover requires a positive number or 'auto'"
			m.errExpiry = time.Now().Add(3 * time.Second)
			return nil
		}
		return m.startDiscovery(false, n)
	case cmd == "art":
		if !m.artMode && (m.supportsArtColor == nil || !m.supportsArtColor()) {
			m.errMsg = "album art needs a terminal with at least 256 colours"
			m.errExpiry = time.Now().Add(3 * time.Second)
			return nil
		}
		m.artMode = !m.artMode
		m.cfg.AlbumArt = m.artMode
		if err := m.cfg.Save(""); err != nil {
			m.appendLog(fmt.Sprintf("[art] config save error: %v", err))
		}
		if m.artMode {
			m.appendLog("[art] album-art view on")
			// Covers aren't downloaded while the art view is off, so fetch
			// the current track's cover now if we don't have it yet.
			return m.fetchArtworkCmd(m.artwork.url, m.artworkGen)
		}
		m.appendLog("[art] album-art view off")
		return nil

	case cmd == "radio":
		if m.radio.enabled {
			m.stopRadio()
			return nil
		}
		return m.startRadioFrom(m.playerState.Track)

	case cmd == "shuffle":
		on := !m.playerState.ShuffleMode
		m.playerState.ShuffleMode = on
		m.appendLog(fmt.Sprintf("[player] shuffle → %v", on))
		return m.playerCmd(func(p player.Player) error { return p.SetShuffle(on) })

	case strings.HasPrefix(cmd, "quality"):
		name, arg, _ := strings.Cut(cmd, " ")
		arg = strings.TrimSpace(arg)
		if arg == "" {
			m.errMsg = ":" + name + " requires high, standard, 256, or 64"
			m.errExpiry = time.Now().Add(3 * time.Second)
			return nil
		}
		kbps, err := audioquality.Parse(arg)
		if err != nil {
			m.errMsg = err.Error()
			m.errExpiry = time.Now().Add(4 * time.Second)
			return nil
		}
		m.appendLog(fmt.Sprintf("[quality] → %d kbps AAC", kbps))
		p := m.player
		return func() tea.Msg {
			if p == nil {
				return errMsg{fmt.Errorf("no player")}
			}
			if err := p.SetAudioBitrate(kbps); err != nil {
				if errors.Is(err, player.ErrAudioBitrateSavedPreferenceOnly) {
					return saveAudioQualityMsg{kbps: kbps, savedOnly: true}
				}
				return errMsg{err}
			}
			return saveAudioQualityMsg{kbps: kbps}
		}

	case cmd == "save" || strings.HasPrefix(cmd, "save "):
		return m.saveTrackList(strings.TrimPrefix(cmd, "save"))

	case strings.HasPrefix(cmd, "save-playlist "):
		// Unlisted: the Tracks panel as a new playlist in Apple Music.
		name := strings.TrimSpace(strings.TrimPrefix(cmd, "save-playlist "))
		if name == "" {
			m.flashStatus(":save-playlist requires a playlist name", 3*time.Second)
			return nil
		}
		ids := make([]string, len(m.queueTracks))
		for i, t := range m.queueTracks {
			ids[i] = views.PlaybackID(t)
		}
		return m.createPlaylistCmd(name, ids)
	}
	m.errMsg = fmt.Sprintf("unknown command: %s", cmd)
	m.errExpiry = time.Now().Add(3 * time.Second)
	return nil
}

func (m *Model) createPlaylistCmd(name string, ids []string) tea.Cmd {
	m.appendLog(fmt.Sprintf("[playlist] creating %q with %d tracks", name, len(ids)))
	return func() tea.Msg {
		_, err := m.provider.CreatePlaylist(context.Background(), name, ids)
		if err != nil {
			return errMsg{fmt.Errorf("save-playlist: %w", err)}
		}
		return playlistCreatedMsg{name: name}
	}
}

func (m *Model) fetchArtworkCmd(url string, gen int) tea.Cmd {
	if url == "" || m.supportsArtColor == nil || !m.supportsArtColor() {
		return nil
	}
	if m.artwork.url == url && gen == m.artworkGen && (m.artwork.img != nil || m.artwork.failed) {
		return nil
	}
	client := m.artHTTP
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		img, err := art.FetchAndDecode(ctx, client, url, 5<<20)
		return artworkLoadedMsg{url: url, gen: gen, img: img, err: err}
	}
}

// fetchLyricsCmd fetches lyrics for t from LRCLIB asynchronously.
func (m *Model) fetchLyricsCmd(t *provider.Track) tea.Cmd {
	trackID := views.PlaybackID(*t)
	artist, title, album, dur := t.Artist, t.Title, t.Album, t.Duration
	client := m.lyricsClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := client.Fetch(ctx, artist, title, album, dur)
		return lyricsResultMsg{trackID: trackID, result: res, err: err}
	}
}

// fetchFeedCmd fetches personalised recommendations from the provider asynchronously.
func (m *Model) fetchFeedCmd() tea.Cmd {
	prov := m.provider
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		groups, err := prov.GetRecommendations(ctx)
		return feedResultMsg{groups: groups, err: err}
	}
}

// fetchSearchCollectionCmd loads all tracks for a search result album or playlist,
// then either replaces the queue (play=true) or appends to it (play=false).
//
// Album routing: search results only contain catalog albums (library albums are
// excluded from Search to avoid returning a subset of the full release), so
// GetAlbumTracks is always used directly with the album's catalog ID.
//
// Playlist routing: IDs starting with "p." are library playlists; everything else
// uses the catalog endpoint.
func (m *Model) fetchSearchCollectionCmd(album *provider.Album, playlist *provider.Playlist, play bool, playNext bool) tea.Cmd {
	prov := m.provider
	if album != nil {
		snap := *album
		m.appendLog(fmt.Sprintf("[search] loading album %q (id=%s)…", snap.Title, snap.ID))
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tracks, err := prov.GetAlbumTracks(ctx, snap.ID)
			return searchCollectionTracksMsg{label: snap.Title, tracks: tracks, play: play, playNext: playNext, err: err}
		}
	}
	if playlist != nil {
		snap := *playlist
		isLibrary := strings.HasPrefix(snap.ID, "p.")
		m.appendLog(fmt.Sprintf("[search] loading playlist %q…", snap.Name))
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var tracks []provider.Track
			var err error
			if isLibrary {
				tracks, err = prov.GetPlaylistTracks(ctx, snap.ID)
			} else {
				tracks, err = prov.GetCatalogPlaylistTracks(ctx, snap.ID)
			}
			return searchCollectionTracksMsg{label: snap.Name, tracks: tracks, play: play, playNext: playNext, err: err}
		}
	}
	return nil
}

// fetchFeedItemTracksCmd loads the tracks for a recommendation item then either
// plays them (play=true) or appends them to the queue.
func (m *Model) fetchFeedItemTracksCmd(item *provider.RecommendationItem, play bool, playNext bool) tea.Cmd {
	snap := *item
	prov := m.provider
	m.appendLog(fmt.Sprintf("[feed] loading %q (%s)…", snap.Title, snap.Kind))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var tracks []provider.Track
		var err error
		switch snap.Kind {
		case "album":
			tracks, err = prov.GetAlbumTracks(ctx, snap.ID)
		case "playlist":
			tracks, err = prov.GetCatalogPlaylistTracks(ctx, snap.ID)
		default:
			err = fmt.Errorf("unknown kind %q", snap.Kind)
		}
		return feedTracksMsg{item: snap, tracks: tracks, play: play, playNext: playNext, err: err}
	}
}

// startDiscovery activates discovery mode with the configured similarity metric.
// autoMode=false is a one-shot: n songs are queued immediately, then discovery stops.
func (m *Model) startDiscovery(autoMode bool, n int) tea.Cmd {
	if m.playerState.Track == nil {
		m.errMsg = "nothing is playing"
		m.errExpiry = time.Now().Add(3 * time.Second)
		return nil
	}
	sim := m.discovery.similarity
	if sim == 0 {
		sim = 0.7 // default if no metric was selected yet
	}
	m.stopRadio() // discovery and radio both refill on the last-track trigger — mutually exclusive
	m.discovery.enabled = true
	m.discovery.autoMode = autoMode
	m.discovery.refillCap = n
	m.discovery.seed = m.playerState.Track
	m.discovery.similarity = sim
	m.discovery.refilling = true // guard against double-trigger while search is in flight
	m.discovery.triggeredForID = ""
	m.syncDiscoveryView()
	mode := "auto"
	if !autoMode {
		mode = fmt.Sprintf("%d songs", n)
	}
	m.appendLog(fmt.Sprintf("[discovery] started from %q (similarity %.0f%%, mode=%s)",
		m.playerState.Track.Title, sim*100, mode))
	return m.runDiscoverySearch()
}

// stopDiscovery turns discovery mode off and clears its session state.
func (m *Model) stopDiscovery() {
	if !m.discovery.enabled {
		return
	}
	m.discovery.enabled = false
	m.discovery.seed = nil
	m.discovery.refilling = false
	m.discovery.triggeredForID = ""
	m.syncDiscoveryView()
	m.appendLog("[discovery] stopped")
}

// startRadioFrom activates radio mode seeded by the given track, replacing
// any radio session already in progress.
func (m *Model) startRadioFrom(seed *provider.Track) tea.Cmd {
	if seed == nil {
		return nil
	}
	m.discovery.enabled = false // discovery and radio are mutually exclusive
	m.radio.generation++
	m.radio.enabled = true
	m.radio.seed = seed
	m.radio.skipped = nil
	m.radio.refilling = true // guard against double-trigger while search is in flight
	m.radio.triggeredForID = ""
	m.radio.retries = 0
	m.appendLog(fmt.Sprintf("[radio] started from %q", seed.Title))
	return tea.Batch(m.ensureSeedQueued(seed), m.runRadioSearch())
}

// ensureSeedQueued makes sure the radio seed is in the queue. A seed that is
// already queued (usually the playing track) is left where it is and nothing
// else is touched: radio picks are appended after whatever is lined up. A seed
// that is not queued (e.g. from a search result) is inserted as the next track.
func (m *Model) ensureSeedQueued(seed *provider.Track) tea.Cmd {
	seedID := views.PlaybackID(*seed)
	for _, t := range m.queueTracks {
		if views.PlaybackID(t) == seedID {
			return nil
		}
	}
	return m.playNextCmd(seed.Artist+" — "+seed.Title, []provider.Track{*seed}, []string{seedID})
}

// stopRadio disables radio mode and clears its state.
func (m *Model) stopRadio() {
	if !m.radio.enabled {
		return
	}
	m.radio.enabled = false
	m.radio.seed = nil
	m.radio.skipped = nil
	m.radio.refilling = false
	m.radio.triggeredForID = ""
	m.radio.generation++
	m.appendLog("[radio] stopped")
}

// runRadioSearch fetches the next batch of tracks for the active radio
// station and returns a vibeResultMsg tagged as a radio refill, mirroring
// runDiscoverySearch's dedup/exclude handling.
func (m *Model) runRadioSearch() tea.Cmd {
	if !m.radio.enabled || m.radio.seed == nil {
		return nil
	}
	seed := m.radio.seed
	catalogID := seed.CatalogID
	if catalogID == "" {
		catalogID = seed.ID
	}
	prov := m.provider
	radioGen := m.radio.generation

	exclude := make(map[string]bool, len(m.radio.skipped)+len(m.queueIDs))
	for k := range m.radio.skipped {
		exclude[k] = true
	}
	for _, id := range m.queueIDs {
		exclude[id] = true
	}
	for _, t := range m.queueTracks {
		exclude[strings.ToLower(t.Artist+"||"+t.Title)] = true
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := prov.GetStationTracks(ctx, catalogID)
		if err != nil {
			return vibeResultMsg{radio: true, radioGen: radioGen, err: err}
		}
		var merged []provider.Track
		seen := map[string]bool{}
		for _, t := range res {
			id := views.PlaybackID(t)
			key := strings.ToLower(t.Artist + "||" + t.Title)
			if exclude[id] || exclude[key] || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, t)
		}
		if len(merged) == 0 {
			return vibeResultMsg{radio: true, radioGen: radioGen}
		}
		return vibeResultMsg{radio: true, radioGen: radioGen, tracks: merged}
	}
}

func (m *Model) forwardToActivePanel(msg tea.KeyPressMsg) tea.Cmd {
	if m.activePanel < 0 {
		return nil
	}
	k := msg.String()
	if m.panels[m.activePanel] == m.feedP {
		switch k {
		case "r":
			m.feedP.m.SetLoading()
			return m.fetchFeedCmd()
		case "enter":
			if item := m.feedP.m.SelectedItem(); item != nil {
				m.playerState.Loading = true
				return m.fetchFeedItemTracksCmd(item, true, false)
			}
		case "tab":
			if item := m.feedP.m.SelectedItem(); item != nil {
				return m.fetchFeedItemTracksCmd(item, false, false)
			}
		case "shift+tab":
			if item := m.feedP.m.SelectedItem(); item != nil {
				return m.fetchFeedItemTracksCmd(item, false, true)
			}
		default:
			return m.feedP.m.Update(msg)
		}
		return nil
	}
	return m.panels[m.activePanel].Update(msg)
}

func (m *Model) handleNormalKey(msg tea.KeyPressMsg, k string) tea.Cmd {
	// When debug log is open, j/k/G scroll it; esc back/closes it.
	if m.debugView {
		switch k {
		case "j", "down":
			if m.debugScroll > 0 {
				m.debugScroll--
			}
			return nil
		case "k", "up":
			m.debugScroll++
			return nil
		case "G":
			m.debugScroll = 0
			return nil
		case "esc":
			m.debugView = false
			return nil
		}
	}

	// While the discovery-metric picker is open it takes all the keys.
	if m.vibe.PickerActive() {
		return m.vibe.Update(msg)
	}

	// Panel nav keys toggle their panel (always checked first).
	for i, p := range m.panels {
		if k == p.NavKey() {
			if m.activePanel == i {
				m.activePanel = -1 // toggle off
			} else {
				m.activePanel = i
				// Trigger a feed load when opening the feed panel for the first time.
				if p == m.feedP && m.feedP.m.NeedsLoad() {
					m.feedP.m.SetLoading()
					m.lastKey = ""
					return m.fetchFeedCmd()
				}
				// Lazy-load lyrics when the panel is opened and the current
				// track hasn't been fetched yet.
				if p == m.lyricsP && m.lastLyricsTrackID == "" && m.playerState.Track != nil {
					id := views.PlaybackID(*m.playerState.Track)
					m.lastLyricsTrackID = id
					m.lyricsP.m.SetLoading()
					m.lastKey = ""
					return m.fetchLyricsCmd(m.playerState.Track)
				}
			}
			m.lastKey = ""
			return nil
		}
	}

	// ':' opens command mode from anywhere — checked before any panel handler.
	if k == ":" {
		m.mode = modeCommand
		m.cmdBuf = ""
		return nil
	}

	if m.activePanel >= 0 && m.panels[m.activePanel] == m.eqP {
		switch k {
		case "esc":
			m.activePanel = -1
			m.lastKey = ""
			return nil
		case "left", "h", "right", "l", "up", "k", "down", "j", "0", "r":
			return m.eqP.Update(msg)
		}
	}

	// Main view with a highlighted queue entry: space/enter start it, d/x/delete
	// remove it, K/J move it (see queue_nav.go). Everything else falls through.
	if m.activePanel < 0 && !m.debugView {
		switch k {
		case "ctrl+up", "ctrl+down", "ctrl+shift+up", "ctrl+shift+down", "ctrl+right", "ctrl+left":
			// The Ctrl+arrows drive the Search list from here too, with the
			// meanings they have in Search, so both lists can be worked
			// without changing columns.
			m.lastKey = ""
			return m.handleSearchKey(k, msg)
		}
		if cmd, handled := m.handleQueueCursorKey(k); handled {
			return cmd
		}
	}

	// Player control keys always work, even when other panels are active.
	switch k {
	case "space":
		return m.togglePlayPause()

	case "n":
		m.lastKey = ""
		m.appendLog("[player] next")
		m.playerState.Loading = true
		return m.playerCmd(func(p player.Player) error { return p.Next() })

	case "p":
		m.lastKey = ""
		m.appendLog("[player] previous")
		m.playerState.Loading = true
		return m.playerCmd(func(p player.Player) error { return p.Previous() })

	case "+", "=":
		m.lastKey = ""
		if m.discovery.enabled {
			// Adjust discovery similarity toward "more similar".
			m.discovery.similarity = min(1.0, m.discovery.similarity+discoverySimilarityStep)
			m.syncDiscoveryView()
			m.appendLog(fmt.Sprintf("[discovery] similarity → %.0f%%", m.discovery.similarity*100))
			return nil
		}
		return m.adjustVolume(0.05)

	case "-":
		m.lastKey = ""
		if m.discovery.enabled {
			// Adjust discovery similarity toward "more different".
			m.discovery.similarity = max(0.0, m.discovery.similarity-discoverySimilarityStep)
			m.syncDiscoveryView()
			m.appendLog(fmt.Sprintf("[discovery] similarity → %.0f%%", m.discovery.similarity*100))
			return nil
		}
		return m.adjustVolume(-0.05)

	case "r":
		m.lastKey = ""
		// Cycle: off → all → one → off
		next := player.RepeatModeOff
		switch m.playerState.RepeatMode {
		case player.RepeatModeOff:
			next = player.RepeatModeAll
		case player.RepeatModeAll:
			next = player.RepeatModeOne
		case player.RepeatModeOne:
			next = player.RepeatModeOff
		}
		m.playerState.RepeatMode = next
		m.appendLog(fmt.Sprintf("[player] repeat → %d", next))
		return m.playerCmd(func(p player.Player) error { return p.SetRepeat(next) })

	case "s":
		m.lastKey = ""
		return m.jumpToRandomQueued()

	// The "f" favourite key is disabled: it toggled m.favorites for the playing
	// track and synced the rating to Apple Music via loveSongCmd. The plumbing
	// (favorites map, loveSongCmd, checkSongRatingCmd) is kept for a possible
	// return of the feature.

	case "R":
		m.lastKey = ""
		seed := m.playerState.Track
		if m.activePanel < 0 && m.queueCursorActive() {
			t := m.queueTracks[m.queueCursor]
			seed = &t
		}
		return m.fetchRelatedCmd(seed)

	case "T":
		m.lastKey = ""
		seed := m.playerState.Track
		if m.activePanel < 0 && m.queueCursorActive() {
			t := m.queueTracks[m.queueCursor]
			seed = &t
		}
		return m.fetchRandomLibraryCmd(seed)

	case "left":
		m.lastKey = ""
		if m.activePanel >= 0 {
			return m.forwardToActivePanel(msg)
		}
		if m.playerState.Track != nil {
			newPos := max(0, m.playerState.Position-10*time.Second)
			m.appendLog(fmt.Sprintf("[player] seek ← %s", views.FormatDuration(newPos)))
			pos := newPos
			return m.playerCmd(func(p player.Player) error { return p.Seek(pos) })
		}

	case "right":
		m.lastKey = ""
		if m.activePanel >= 0 {
			return m.forwardToActivePanel(msg)
		}
		if m.playerState.Track != nil {
			newPos := m.playerState.Position + 10*time.Second
			if dur := m.playerState.Track.Duration; dur > 0 && newPos > dur {
				newPos = dur
			}
			m.appendLog(fmt.Sprintf("[player] seek → %s", views.FormatDuration(newPos)))
			pos := newPos
			return m.playerCmd(func(p player.Player) error { return p.Seek(pos) })
		}
	}

	// Forward remaining keys to other active panels (e.g. library).
	if m.activePanel >= 0 {
		if k == "esc" {
			if m.panels[m.activePanel].Back() {
				m.lastKey = ""
				return nil
			}
			m.activePanel = -1
			m.lastKey = ""
			return nil
		}
		return m.forwardToActivePanel(msg)
	}

	// Keys that only work when no panel is covering the content area.
	switch k {
	case "tab", "shift+tab":
		// Focus the Search column, browsing (Ctrl+' starts typing); Tab there
		// brings the keys back to the queue.
		m.mode = modeSearch
		m.searchTyping = false

	case "j", "down":
		m.lastKey = ""
		m.moveQueueCursor(1)

	case "k", "up":
		m.lastKey = ""
		m.moveQueueCursor(-1)

	case "shift+up":
		m.lastKey = ""
		m.setQueueCursor(0)

	case "shift+down":
		m.lastKey = ""
		m.setQueueCursor(len(m.queueTracks) - 1)

	case "q":
		m.lastKey = ""
		m.followPlayingTrack()

	case "c":
		m.lastKey = ""
		return m.clearQueue()

	case "g":
		if m.lastKey == "g" {
			m.lastKey = ""
			m.setQueueCursor(0)
		} else {
			m.lastKey = "g"
		}

	case "G":
		m.lastKey = ""
		m.setQueueCursor(len(m.queueTracks) - 1)

	case "enter":
		m.lastKey = ""
		if t := m.search.SelectedTrack(); t != nil {
			tc := *t
			m.queueTracks = []provider.Track{tc}
			m.queueIDs = []string{views.PlaybackID(tc)}
			m.playerState.Track = &tc
			m.playerState.Loading = true
			m.playerState.Playing = false
			m.playerState.Position = 0
			m.syncQueue()
			return m.playerCmd(func(p player.Player) error { return p.SetQueue(m.queueIDs) })
		}

	case "a":
		m.lastKey = ""
		if t := m.search.SelectedTrack(); t != nil {
			tc := *t
			return m.addToQueue(tc.Artist+" — "+tc.Title, []provider.Track{tc}, []string{views.PlaybackID(tc)})
		}

	default:
		m.lastKey = ""
	}

	return nil
}

// panelTitle renders a column title; the column that currently has the keys
// is bold, the other one plain, so focus is visible without a mode label.
func (m *Model) panelTitle(label string, focused bool) string {
	if focused {
		return styles.Header.Bold(true).Render(label)
	}
	return styles.Header.Render(label)
}

// queueFocused reports whether keys go to the queue: not while Search has
// them and not while an overlay panel (lyrics, feed, …) is open.
func (m *Model) queueFocused() bool {
	return m.mode != modeSearch && m.activePanel < 0
}

// selectionTracksMsg carries the songs of a multi-selection once every
// selected album and playlist has been expanded.
type selectionTracksMsg struct {
	label  string
	tracks []provider.Track
	play   bool
	err    error
}

// addSelection sends the multi-selection — or, with nothing marked, the
// highlighted item — to Tracks: songs as they are, albums and playlists
// expanded to their songs, all in result order. With play the first song
// starts. The selection stays as it is.
func (m *Model) addSelection(play bool) tea.Cmd {
	items := m.search.SelectedItems()
	label := fmt.Sprintf("%d selected", len(items))
	if len(items) == 0 {
		// Nothing marked: the highlighted song, album or playlist is meant.
		switch {
		case m.search.SelectedTrack() != nil:
			t := m.search.SelectedTrack()
			items, label = []views.SelectedItem{{Track: t}}, t.Artist+" — "+t.Title
		case m.search.SelectedAlbum() != nil:
			a := m.search.SelectedAlbum()
			items, label = []views.SelectedItem{{Album: a}}, a.Title
		case m.search.SelectedPlaylist() != nil:
			pl := m.search.SelectedPlaylist()
			items, label = []views.SelectedItem{{Playlist: pl}}, pl.Name
		case m.search.SelectedSavedList() != nil:
			l := m.search.SelectedSavedList()
			items, label = []views.SelectedItem{{List: l}}, l.Name
		default:
			return nil
		}
	}
	// Saved lists carry their songs already: they unfold here, in place.
	expanded := make([]views.SelectedItem, 0, len(items))
	for _, it := range items {
		if it.List == nil {
			expanded = append(expanded, it)
			continue
		}
		for i := range it.List.Tracks {
			expanded = append(expanded, views.SelectedItem{Track: &it.List.Tracks[i]})
		}
	}
	items = expanded
	// The selection stays marked after the add (Ctrl+← clears it).
	collections := 0
	for _, it := range items {
		if it.Track == nil {
			collections++
		}
	}
	if collections == 0 {
		tracks := make([]provider.Track, 0, len(items))
		for _, it := range items {
			tracks = append(tracks, *it.Track)
		}
		if play {
			return m.appendAndPlay(label, tracks, playbackIDs(tracks), 0)
		}
		return m.addToQueue(label, tracks, playbackIDs(tracks))
	}
	prov := m.provider
	if prov == nil {
		return nil
	}
	m.appendLog(fmt.Sprintf("[search] expanding %d album(s)/playlist(s) of the selection…", collections))
	snap := make([]views.SelectedItem, len(items))
	copy(snap, items)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		type out struct {
			tracks []provider.Track
			err    error
		}
		results := make([]chan out, len(snap))
		for i, it := range snap {
			ch := make(chan out, 1)
			results[i] = ch
			switch {
			case it.Track != nil:
				ch <- out{tracks: []provider.Track{*it.Track}}
			case it.Album != nil:
				go func(id string) {
					tracks, err := prov.GetAlbumTracks(ctx, id)
					ch <- out{tracks: tracks, err: err}
				}(it.Album.ID)
			case it.Playlist != nil:
				go func(id string) {
					var tracks []provider.Track
					var err error
					if strings.HasPrefix(id, "p.") {
						tracks, err = prov.GetPlaylistTracks(ctx, id)
					} else {
						tracks, err = prov.GetCatalogPlaylistTracks(ctx, id)
					}
					ch <- out{tracks: tracks, err: err}
				}(it.Playlist.ID)
			}
		}
		var tracks []provider.Track
		var firstErr error
		for _, ch := range results {
			r := <-ch
			if r.err != nil && firstErr == nil {
				firstErr = r.err
			}
			tracks = append(tracks, r.tracks...)
		}
		return selectionTracksMsg{label: label, tracks: tracks, play: play, err: firstErr}
	}
}

// flashInfo shows a short informational status line.
func (m *Model) flashInfo(text string) {
	m.errMsg = text
	m.errExpiry = time.Now().Add(4 * time.Second)
}

// vibeSetupLabel describes what answers CC lookups: the model and effort the
// Claude CLI is asked for, or the keyword table.
func (m *Model) vibeSetupLabel() string {
	c, ok := m.vibePlanner.(*vibe.ClaudePlanner)
	if !ok {
		return "keyword table (no model)"
	}
	model := c.Model
	if model == "" {
		model = "CLI default"
	}
	effort := c.Effort
	if effort == "" {
		effort = "default"
	}
	return "Claude " + model + ", effort " + effort
}

// applyVibeSetup rebuilds the planner from the config's vibe_agent /
// vibe_model / vibe_effort, saves the config and reports the result.
func (m *Model) applyVibeSetup(what string) {
	m.vibePlanner = vibe.NewPlanner(m.cfg.VibeAgent, m.cfg.VibeModel, m.cfg.VibeEffort)
	if err := m.cfg.Save(""); err != nil {
		m.appendLog(fmt.Sprintf("[vibe] config save error: %v", err))
	}
	m.appendLog(fmt.Sprintf("[vibe] %s set: %s", what, m.vibeSetupLabel()))
	m.flashInfo("✓ CC lookups: " + m.vibeSetupLabel())
}

// playbackIDs lists the playback id of each track, in order.
func playbackIDs(tracks []provider.Track) []string {
	ids := make([]string, len(tracks))
	for i, t := range tracks {
		ids[i] = views.PlaybackID(t)
	}
	return ids
}

// fetchMoreTracksCmd requests the next page of catalog songs for the Tracks
// section so it can grow past Apple's 25-per-request cap. wanted is how many
// items the user is owed from the page; hops counts pages already chained.
func (m *Model) fetchMoreTracksCmd(wanted, hops int) tea.Cmd {
	pager, ok := m.provider.(provider.SongPager)
	if !ok {
		return nil
	}
	offset, ok := m.searchAM.BeginPaging(wanted)
	if !ok {
		return nil
	}
	gen, query := m.searchGen, m.searchShown
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		page, err := pager.SearchSongsPage(ctx, query, offset)
		return searchMoreMsg{gen: gen, hops: hops, page: page, err: err}
	}
}

func (m *Model) scheduleSearch(query string) tea.Cmd {
	if m.searchSrc != searchApple {
		return nil // only Apple Music has a plain search
	}
	if query == "" {
		m.searchAM.SetResults(nil, false, nil)
		m.searchShown = ""
		return nil
	}
	m.searchGen++
	gen := m.searchGen
	m.searchAM.SetResults(nil, true, nil)
	// Searches run on Enter, so there is nothing to debounce; the gen still
	// drops a result that a newer search has overtaken.
	return func() tea.Msg { return searchDebounceMsg{query: query, gen: gen} }
}

// startVibeSearch looks up songs for a vibe description typed into the Search
// panel; the result lands in the panel's "Vibes" section (never in the queue).
func (m *Model) startVibeSearch(query string) tea.Cmd {
	if m.provider == nil {
		return nil
	}
	m.vibeShown = query
	m.searchCC.SetResults(nil, true, nil)
	return m.runVibeSearch(query)
}

// setSearchSource switches the Search column between Apple Music, Claude
// Code and the saved lists, each with its own result list; the active list
// takes over the column size. The saved lists are read again on the way in,
// so a fresh :save is there.
func (m *Model) setSearchSource(src searchSource) {
	m.searchSrc = src
	w, h := m.search.Size()
	m.search = m.searchFor(src)
	if w > 0 && h > 0 {
		m.search.SetSize(w, h)
	}
	if src == searchSaved {
		m.refreshSavedLists()
	}
}

// searchFor is the result list that belongs to a source.
func (m *Model) searchFor(src searchSource) *views.SearchModel {
	switch src {
	case searchClaude:
		return m.searchCC
	case searchSaved:
		return m.searchSV
	}
	return m.searchAM
}

// handleVibeSearchResult shows the songs found for a vibe description in the
// Claude Code list (even while Apple Music search is showing, so switching
// back finds them). Late results for an older description are dropped.
func (m *Model) handleVibeSearchResult(msg vibeResultMsg) {
	if msg.query != m.vibeShown {
		return
	}
	if len(msg.warnings) > 0 {
		m.appendLog(fmt.Sprintf("[vibe] partial results: %s", strings.Join(msg.warnings, "; ")))
	}
	// The header names the model that planned the lookup and the line under it
	// carries its summary; terms and ranking details go to the debug log. When
	// the keyword table did the work (no model), the header stays "Vibes" and
	// the line says so, including a planner failure.
	title, note := "", []string(nil)
	if msg.via != "" {
		summary := strings.TrimSpace(msg.plan.Summary)
		if msg.plan.Model != "" {
			title = vibe.PrettyModel(msg.plan.Model)
			if summary != "" {
				note = append(note, summary)
			}
		} else {
			note = append(note, msg.via+": "+summary)
		}
		m.appendLog(fmt.Sprintf("[vibe] %s (%s) planned %q → %q: %v (%s)", msg.via, msg.plan.Model, msg.query, summary, msg.plan.Queries, msg.ranking))
	}
	if msg.err != nil {
		m.appendLog(fmt.Sprintf("[vibe] search error: %v", msg.err))
		m.searchCC.SetVibeResults(nil, title, note...)
		return
	}
	m.appendLog(fmt.Sprintf("[vibe] %d song(s) for %q", len(msg.tracks), msg.query))
	m.searchCC.SetVibeResults(msg.tracks, title, note...)
}

// vibeResultCap is how many songs a vibe lookup lists; vibePoolCap is how many
// candidates are gathered for the reranker to choose from.
const (
	vibeResultCap = 15
	vibePoolCap   = 40
)

// runVibeSearch is stage one of a vibe lookup: the description goes through
// the configured planner (Claude Code CLI or the keyword table; the table is
// the fallback when the planner fails), the terms run in parallel through the
// provider and the hits are interleaved so every term contributes,
// deduplicated by artist and title, up to vibePoolCap candidates. The plan
// rides along so the panel can show what was searched for.
func (m *Model) runVibeSearch(query string) tea.Cmd {
	planner := m.vibePlanner
	if planner == nil {
		planner = vibe.KeywordPlanner{}
	}
	prov := m.provider

	return func() tea.Msg {
		var reasons []string
		pctx, pcancel := context.WithTimeout(context.Background(), 45*time.Second)
		plan, err := planner.Plan(pctx, query)
		pcancel()
		via := planner.Name()
		if err != nil {
			reasons = append(reasons, err.Error())
			plan, _ = vibe.KeywordPlanner{}.Plan(context.Background(), query)
			via = "keywords (" + planner.Name() + " unavailable)"
		}
		queries := plan.Queries
		const maxQueries = 6 // each term is three parallel Apple requests
		if len(queries) > maxQueries {
			queries = queries[:maxQueries]
		}
		plan.Queries = queries // the panel lists exactly the terms that ran

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		type searchOut struct {
			tracks   []provider.Track
			warnings []string
			err      error
		}
		chs := make([]chan searchOut, len(queries))
		for i, q := range queries {
			ch := make(chan searchOut, 1)
			chs[i] = ch
			go func(term string, out chan searchOut) {
				res, err := prov.Search(ctx, term)
				if err != nil || res == nil {
					out <- searchOut{err: err}
					return
				}
				out <- searchOut{tracks: res.Tracks, warnings: res.Warnings}
			}(q, ch)
		}
		// Failures and partial-result warnings are collected rather than
		// dropped: when the merge comes back empty they are the only
		// explanation available.
		perTerm := make([][]provider.Track, len(queries))
		for i, ch := range chs {
			r := <-ch
			if r.err != nil {
				reasons = append(reasons, r.err.Error())
			}
			reasons = append(reasons, r.warnings...)
			perTerm[i] = r.tracks
		}
		pool := interleaveTracks(perTerm, vibePoolCap)

		if len(pool) == 0 {
			// Fallback to the raw description.
			res, err := prov.Search(ctx, query)
			if err != nil {
				reasons = append(reasons, err.Error())
			}
			if res != nil {
				reasons = append(reasons, res.Warnings...)
			}
			if err != nil || res == nil || len(res.Tracks) == 0 {
				return vibeCandidatesMsg{
					query:    query,
					err:      noResultsError(query, reasons),
					warnings: dedupeStrings(reasons),
					plan:     plan,
					via:      via,
				}
			}
			pool = interleaveTracks([][]provider.Track{res.Tracks}, vibePoolCap)
		}
		return vibeCandidatesMsg{query: query, pool: pool, warnings: dedupeStrings(reasons), plan: plan, via: via}
	}
}

// handleVibeCandidates is stage two: when the planner can rerank, the pool
// goes back to it with the description and the panel says so; otherwise the
// first vibeResultCap candidates are shown in search order.
func (m *Model) handleVibeCandidates(msg vibeCandidatesMsg) tea.Cmd {
	if msg.query != m.vibeShown {
		return nil
	}
	final := vibeResultMsg{query: msg.query, plan: msg.plan, via: msg.via, warnings: msg.warnings, err: msg.err}
	if msg.err != nil || len(msg.pool) == 0 {
		m.handleVibeSearchResult(final)
		return nil
	}
	if rr, ok := m.vibePlanner.(vibe.Reranker); ok && len(msg.pool) > 1 {
		return m.rerankVibeCmd(msg, rr) // the panel keeps animating meanwhile
	}
	final.tracks = msg.pool[:min(len(msg.pool), vibeResultCap)]
	if len(msg.pool) > len(final.tracks) {
		final.ranking = fmt.Sprintf("first %d of %d, search order", len(final.tracks), len(msg.pool))
	}
	m.handleVibeSearchResult(final)
	return nil
}

// rerankVibeCmd asks the reranker to order the pool against the description.
// A failed ranking keeps the search order and says so.
func (m *Model) rerankVibeCmd(msg vibeCandidatesMsg, rr vibe.Reranker) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cands := make([]vibe.Candidate, len(msg.pool))
		for i, t := range msg.pool {
			cands[i] = vibe.Candidate{Artist: t.Artist, Title: t.Title, Album: t.Album}
		}
		final := vibeResultMsg{query: msg.query, plan: msg.plan, via: msg.via, warnings: msg.warnings}
		idx, err := rr.Rerank(ctx, msg.query, cands, vibeResultCap)
		if err != nil {
			final.tracks = msg.pool[:min(len(msg.pool), vibeResultCap)]
			final.ranking = "ranking failed, search order"
			final.warnings = append(final.warnings, err.Error())
			return final
		}
		for _, i := range idx {
			final.tracks = append(final.tracks, msg.pool[i])
		}
		final.ranking = fmt.Sprintf("picked %d of %d", len(final.tracks), len(msg.pool))
		return final
	}
}

// interleaveTracks takes songs from each term's results in turn, skipping
// repeats by artist and title, until limit songs are collected. Term order
// is the planner's priority; within a term Apple's relevance order is kept.
func interleaveTracks(perTerm [][]provider.Track, limit int) []provider.Track {
	seen := map[string]bool{}
	var merged []provider.Track
	for progress := true; progress && len(merged) < limit; {
		progress = false
		for i := range perTerm {
			for len(perTerm[i]) > 0 {
				t := perTerm[i][0]
				perTerm[i] = perTerm[i][1:]
				key := strings.ToLower(t.Artist + "||" + t.Title)
				if seen[key] {
					continue
				}
				seen[key] = true
				merged = append(merged, t)
				progress = true
				break
			}
			if len(merged) >= limit {
				break
			}
		}
	}
	return merged
}

// noResultsError builds the error shown when a search yields nothing. The
// reasons, when present, distinguish an empty catalog match from a backend
// that failed — the two are indistinguishable to a user otherwise.
func noResultsError(query string, reasons []string) error {
	reasons = dedupeStrings(reasons)
	if len(reasons) == 0 {
		return fmt.Errorf("no results for %q", query)
	}
	return fmt.Errorf("no results for %q (%s)", query, strings.Join(reasons, "; "))
}

// dedupeStrings returns s without duplicates, preserving order. Parallel
// searches against one provider report the same backend failure once per
// query, so the raw list is mostly repetition.
func dedupeStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// loveSongCmd calls provider.LoveSong asynchronously and returns a loveSongMsg.
func (m *Model) loveSongCmd(t *provider.Track, loved bool) tea.Cmd {
	if m.provider == nil || t == nil {
		return nil
	}
	catalogID := t.CatalogID
	if catalogID == "" {
		catalogID = t.ID
	}
	title := t.Title
	prov := m.provider
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := prov.LoveSong(ctx, catalogID, loved)
		return loveSongMsg{title: title, loved: loved, err: err}
	}
}

// checkSongRatingCmd checks whether the track is already Loved on Apple Music
// and returns a songRatingMsg so the model can update m.favorites accordingly.
func (m *Model) checkSongRatingCmd(t *provider.Track) tea.Cmd {
	if m.provider == nil || t == nil {
		return nil
	}
	catalogID := t.CatalogID
	if catalogID == "" {
		catalogID = t.ID
	}
	trackID := t.ID
	prov := m.provider
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		loved, _ := prov.GetSongRating(ctx, catalogID)
		return songRatingMsg{trackID: trackID, loved: loved}
	}
}

// syncDiscoveryView pushes the current discovery state into the vibe view.
func (m *Model) syncDiscoveryView() {
	info := views.DiscoveryInfo{}
	if m.discovery.enabled && m.discovery.seed != nil {
		info.Active = true
		info.SeedArtist = m.discovery.seed.Artist
		info.SeedTitle = m.discovery.seed.Title
		info.Similarity = m.discovery.similarity
		info.Refilling = m.discovery.refilling
		info.AutoMode = m.discovery.autoMode
		info.Count = m.discovery.refillCap
	}
	m.vibe.SetDiscovery(info)
}

// purgeSkippedFromQueue removes every queued track whose PlaybackID appears in
// the discovery skip blacklist. It also tells the JS player to remove those
// slots so both sides stay in sync. Iterates from the end so index removal
// doesn't shift the positions of entries not yet processed.
func (m *Model) purgeSkippedFromQueue() {
	if len(m.discovery.skipped) == 0 && len(m.radio.skipped) == 0 {
		return
	}
	changed := false
	for i := len(m.queueIDs) - 1; i >= 0; i-- {
		if !m.discovery.skipped[m.queueIDs[i]] && !m.radio.skipped[m.queueIDs[i]] {
			continue
		}
		m.appendLog(fmt.Sprintf("[skip] removing from queue: %s — %s",
			m.queueTracks[i].Artist, m.queueTracks[i].Title))
		if m.player != nil {
			idx := i // capture for closure
			_ = m.player.RemoveFromQueue(idx)
		}
		m.queueTracks = slices.Delete(m.queueTracks, i, i+1)
		m.queueIDs = slices.Delete(m.queueIDs, i, i+1)
		changed = true
	}
	if changed {
		m.syncQueue()
	}
}

// runDiscoverySearch builds search queries from the seed track and similarity
// value, fetches results in parallel, and returns a vibeResultMsg tagged as a
// discovery refill so the model knows not to update the vibe panel state.
func (m *Model) runDiscoverySearch() tea.Cmd {
	if !m.discovery.enabled || m.discovery.seed == nil {
		return nil
	}
	seed := m.discovery.seed
	similarity := m.discovery.similarity
	prov := m.provider
	refillCap := m.discovery.refillCap
	if refillCap <= 0 {
		refillCap = 1
	}
	queries := discoveryQueries(seed, similarity)
	m.appendLog(fmt.Sprintf("[discovery] queries (sim=%.0f%%): %v", similarity*100, queries))

	// Snapshot both the skip blacklist and the already-queued set so the
	// goroutine can filter without racing on the model's maps/slices.
	exclude := make(map[string]bool, len(m.discovery.skipped)+len(m.queueIDs))
	for k := range m.discovery.skipped {
		exclude[k] = true
	}
	for _, id := range m.queueIDs {
		exclude[id] = true
	}
	// Also exclude by normalised artist||title to catch the same song under
	// a different ID (e.g. library vs catalog copy).
	for _, t := range m.queueTracks {
		exclude[strings.ToLower(t.Artist+"||"+t.Title)] = true
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		type out struct {
			tracks   []provider.Track
			warnings []string
			err      error
		}
		chs := make([]chan out, len(queries))
		for i, q := range queries {
			ch := make(chan out, 1)
			chs[i] = ch
			go func(term string, c chan out) {
				res, err := prov.Search(ctx, term)
				if err != nil || res == nil {
					c <- out{err: err}
					return
				}
				c <- out{tracks: res.Tracks, warnings: res.Warnings}
			}(q, ch)
		}

		seen := map[string]bool{}
		var merged []provider.Track
		var reasons []string
		for _, ch := range chs {
			r := <-ch
			if r.err != nil {
				reasons = append(reasons, r.err.Error())
			}
			reasons = append(reasons, r.warnings...)
			for _, t := range r.tracks {
				id := views.PlaybackID(t)
				key := strings.ToLower(t.Artist + "||" + t.Title)
				if exclude[id] || exclude[key] {
					continue // already queued, skipped, or blacklisted
				}
				if !seen[key] {
					seen[key] = true
					merged = append(merged, t)
				}
			}
		}

		rand.Shuffle(len(merged), func(i, j int) { //nolint:gosec
			merged[i], merged[j] = merged[j], merged[i]
		})
		if len(merged) > refillCap {
			merged = merged[:refillCap]
		}
		if len(merged) == 0 {
			reasons := dedupeStrings(reasons)
			if len(reasons) == 0 {
				return vibeResultMsg{discovery: true, err: errors.New("no results")}
			}
			return vibeResultMsg{
				discovery: true,
				err:       fmt.Errorf("no results (%s)", strings.Join(reasons, "; ")),
				warnings:  reasons,
			}
		}
		return vibeResultMsg{discovery: true, tracks: merged, warnings: dedupeStrings(reasons)}
	}
}

// discoveryQueries returns search terms based on seed + similarity.
// similarity 1.0 = same artist; 0.0 = completely random genre exploration.
//
// Apple Music's catalog search is artist/title indexed; bare genre strings
// ("indie", "r&b playlist") rarely match songs. Instead we always build
// queries from artist names — either the seed artist or curated artists that
// are representative of the seed's genre or adjacent genres.
func discoveryQueries(seed *provider.Track, similarity float64) []string {
	artist := seed.Artist
	album := seed.Album

	// Resolve the primary genre from the track metadata.  Apple Music often
	// appends the catch-all "Music" genre; skip it when a more specific tag
	// is available.
	genre := ""
	for _, g := range seed.Genres {
		if g != "Music" && g != "" {
			genre = g
			break
		}
	}

	// genrePool returns a randomised slice of artist names that are
	// representative of the given Apple Music genre string (case-insensitive
	// prefix match).  Falls back to a broad alternative/indie pool.
	pool := discoveryArtistPool(genre)

	// pick selects n distinct random elements from pool, avoiding `exclude`.
	pick := func(n int, exclude ...string) []string {
		excl := make(map[string]bool, len(exclude))
		for _, e := range exclude {
			excl[strings.ToLower(e)] = true
		}
		// Shuffle a copy so each call produces different results.
		shuffled := make([]string, len(pool))
		copy(shuffled, pool)
		rand.Shuffle(len(shuffled), func(i, j int) { //nolint:gosec
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		var out []string
		for _, a := range shuffled {
			if !excl[strings.ToLower(a)] {
				out = append(out, a)
				if len(out) == n {
					break
				}
			}
		}
		return out
	}

	switch {
	case similarity >= 0.85:
		// Same-artist focus: search the artist directly, plus a specific album
		// when available for breadth across their catalogue.
		qs := []string{artist}
		if album != "" {
			qs = append(qs, artist+" "+album)
		}
		// Add one more artist from the same genre pool for slight variety.
		qs = append(qs, pick(1, artist)...)
		return qs

	case similarity >= 0.65:
		// Seed artist + a couple of related artists from the same genre.
		qs := []string{artist}
		qs = append(qs, pick(2, artist)...)
		return qs

	case similarity >= 0.45:
		// Genre-focused: seed artist as anchor + 2–3 artists from same pool.
		qs := []string{artist}
		qs = append(qs, pick(3, artist)...)
		return qs

	case similarity >= 0.20:
		// Exploration: seed genre pool + one artist from an adjacent genre.
		adj := discoveryArtistPool(discoveryAdjacentGenre(genre))
		qs := pick(2, artist)
		// Pick one from adjacent pool.
		rand.Shuffle(len(adj), func(i, j int) { adj[i], adj[j] = adj[j], adj[i] }) //nolint:gosec
		if len(adj) > 0 {
			qs = append(qs, adj[0])
		}
		return qs

	default:
		// Pure discovery: three artists from two completely random genre pools.
		genres := []string{
			"Electronic", "Jazz", "Hip-Hop/Rap", "R&B/Soul",
			"Folk", "Classical", "Country", "Reggae",
		}
		rand.Shuffle(len(genres), func(i, j int) { genres[i], genres[j] = genres[j], genres[i] }) //nolint:gosec
		p1 := discoveryArtistPool(genres[0])
		p2 := discoveryArtistPool(genres[1])
		rand.Shuffle(len(p1), func(i, j int) { p1[i], p1[j] = p1[j], p1[i] }) //nolint:gosec
		rand.Shuffle(len(p2), func(i, j int) { p2[i], p2[j] = p2[j], p2[i] }) //nolint:gosec
		var qs []string
		if len(p1) > 0 {
			qs = append(qs, p1[0])
		}
		if len(p2) > 0 {
			qs = append(qs, p2[0])
		}
		if len(p1) > 1 {
			qs = append(qs, p1[1])
		}
		return qs
	}
}

// discoveryAdjacentGenre returns a genre that is stylistically adjacent to g.
func discoveryAdjacentGenre(g string) string {
	adjacency := map[string]string{
		"alternative": "Electronic",
		"indie":       "Folk",
		"electronic":  "Alternative",
		"pop":         "R&B/Soul",
		"hip-hop":     "R&B/Soul",
		"r&b":         "Hip-Hop/Rap",
		"folk":        "Alternative",
		"jazz":        "Soul",
		"soul":        "Jazz",
		"rock":        "Alternative",
		"metal":       "Rock",
		"classical":   "Jazz",
		"country":     "Folk",
	}
	low := strings.ToLower(g)
	for k, v := range adjacency {
		if strings.Contains(low, k) {
			return v
		}
	}
	return "Electronic" // safe fallback
}

// discoveryArtistPool returns a curated list of artist names that are
// representative of g (matched by case-insensitive substring).
// Apple Music search resolves artist names to tracks reliably.
func discoveryArtistPool(g string) []string {
	low := strings.ToLower(g)

	type genreEntry struct {
		key     string
		artists []string
	}
	entries := []genreEntry{
		{"alternative", []string{
			"Radiohead", "The National", "Pixies", "Sonic Youth", "Pavement",
			"Dinosaur Jr", "Built to Spill", "Guided by Voices", "Yo La Tengo",
			"My Bloody Valentine", "Slowdive", "Ride", "Mazzy Star", "Neutral Milk Hotel",
		}},
		{"indie", []string{
			"Bon Iver", "Fleet Foxes", "Arcade Fire", "Modest Mouse", "Death Cab for Cutie",
			"Sufjan Stevens", "Bright Eyes", "Phosphorescent", "Big Thief", "Phoebe Bridgers",
			"Waxahatchee", "Angel Olsen", "Sharon Van Etten", "Hand Habits", "Hazel English",
		}},
		{"electronic", []string{
			"Four Tet", "Burial", "Aphex Twin", "Caribou", "James Blake",
			"Boards of Canada", "Massive Attack", "Portishead", "Thom Yorke",
			"Floating Points", "Jon Hopkins", "Nils Frahm", "Nicolas Jaar", "Arca",
		}},
		{"pop", []string{
			"Lorde", "Lana Del Rey", "Grimes", "FKA twigs", "Charli XCX",
			"Caroline Polachek", "Carly Rae Jepsen", "Perfume Genius", "Weyes Blood", "Aldous Harding",
		}},
		{"hip-hop", []string{
			"Kendrick Lamar", "Frank Ocean", "Tyler the Creator", "Danny Brown", "Vince Staples",
			"JPEGMAFIA", "Denzel Curry", "Little Simz", "Injury Reserve", "billy woods",
		}},
		{"r&b", []string{
			"Frank Ocean", "SZA", "Blood Orange", "Solange", "Kelela",
			"Syd", "Moses Sumney", "Sampha", "NAO", "Tirzah",
		}},
		{"folk", []string{
			"Iron & Wine", "Gillian Welch", "Jason Isbell", "Phosphorescent", "Gregory Alan Isakov",
			"Josh Ritter", "Anaïs Mitchell", "The Tallest Man on Earth", "Nick Drake", "John Martyn",
		}},
		{"jazz", []string{
			"Brad Mehldau", "Nubya Garcia", "Kamasi Washington", "Snarky Puppy", "Thundercat",
			"Makaya McCraven", "Sons of Kemet", "Shabaka Hutchings", "Charles Mingus", "Bill Evans",
		}},
		{"soul", []string{
			"Leon Bridges", "Nathaniel Rateliff", "Anderson Paak", "Charles Bradley",
			"Sharon Jones", "Michael Kiwanuka", "Lianne La Havas", "Durand Jones",
		}},
		{"rock", []string{
			"Wilco", "The War on Drugs", "Kurt Vile", "Spoon", "Parquet Courts",
			"Drive-By Truckers", "Steve Gunn", "Jeff Tweedy", "Ty Segall", "Thee Oh Sees",
		}},
		{"metal", []string{
			"Mastodon", "Baroness", "Converge", "Neurosis", "Pallbearer",
			"Inter Arma", "Bell Witch", "Deafheaven", "Wolves in the Throne Room",
		}},
		{"classical", []string{
			"Nils Frahm", "Max Richter", "Johann Johannsson", "Ólafur Arnalds",
			"Lubomyr Melnyk", "Dustin O'Halloran", "Ryuichi Sakamoto",
		}},
		{"country", []string{
			"Sturgill Simpson", "Chris Stapleton", "Jason Isbell", "Colter Wall",
			"Tyler Childers", "Charley Crockett", "Nikki Lane", "Margo Price",
		}},
		{"reggae", []string{
			"Bob Marley", "Toots and the Maytals", "Peter Tosh", "Lee Scratch Perry",
			"Burning Spear", "Steel Pulse", "The Congos",
		}},
	}

	for _, e := range entries {
		if strings.Contains(low, e.key) {
			return e.artists
		}
	}

	// Broad fallback used when the genre is unknown.
	return []string{
		"Radiohead", "Bon Iver", "The National", "Fleet Foxes", "Arcade Fire",
		"Sufjan Stevens", "Bright Eyes", "James Blake", "Four Tet", "Caribou",
		"Big Thief", "Phoebe Bridgers", "Angel Olsen", "Weyes Blood", "Wilco",
	}
}

func (m *Model) togglePlayPause() tea.Cmd {
	if m.player != nil && !m.playerState.Playing && m.playerState.Track == nil && len(m.queueIDs) > 0 {
		// A restored queue exists only in the model so far; load it into the engine now.
		return m.startRestoredQueue()
	}
	return func() tea.Msg {
		if m.player == nil {
			return errMsg{fmt.Errorf("no player")}
		}
		var err error
		if m.playerState.Playing {
			err = m.player.Pause()
		} else {
			err = m.player.Play()
		}
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m *Model) playerCmd(fn func(player.Player) error) tea.Cmd {
	p := m.player
	return func() tea.Msg {
		if p == nil {
			return errMsg{fmt.Errorf("no player")}
		}
		if err := fn(p); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m *Model) playNextCmd(label string, tracks []provider.Track, ids []string) tea.Cmd {
	// A track is never queued twice: drop the ones already in the queue.
	if nt, ni := m.withoutQueued(tracks, ids); len(ni) != len(ids) {
		if len(ni) == 0 {
			if idx := m.queueIndexOf(ids[0]); idx >= 0 {
				m.setQueueCursor(idx)
			}
			m.errMsg = "ℹ Already in Tracks: " + label
			m.errExpiry = time.Now().Add(3 * time.Second)
			return nil
		}
		tracks, ids = nt, ni
	}
	insertIdx := 0
	if m.playerState.Track != nil {
		for i, t := range m.queueTracks {
			if views.PlaybackID(t) == views.PlaybackID(*m.playerState.Track) {
				insertIdx = i + 1
				break
			}
		}
	}

	origLen := len(m.queueTracks)
	if origLen == 0 {
		m.queueTracks = tracks
		m.queueIDs = ids
		m.playerState.Track = &tracks[0]
		m.playerState.Loading = true
		m.playerState.Playing = false
		m.playerState.Position = 0
		m.syncQueue()
		m.appendLog(fmt.Sprintf("[queue] play now: %s", label))
		return m.playerCmd(func(p player.Player) error { return p.SetQueue(ids) })
	}

	if insertIdx > origLen {
		insertIdx = origLen
	}

	// Insert locally
	m.queueTracks = append(m.queueTracks[:insertIdx], append(tracks, m.queueTracks[insertIdx:]...)...)
	m.queueIDs = append(m.queueIDs[:insertIdx], append(ids, m.queueIDs[insertIdx:]...)...)
	m.syncQueue()

	m.appendLog(fmt.Sprintf("[queue] play next: %s (%d track(s))", label, len(tracks)))

	return m.syncEngineQueue("")
}

func (m *Model) adjustVolume(delta float64) tea.Cmd {
	return func() tea.Msg {
		if m.player == nil {
			return errMsg{fmt.Errorf("no player")}
		}
		newVol := clamp(m.playerState.Volume+delta, 0, 1)
		if err := m.player.SetVolume(newVol); err != nil {
			return errMsg{err}
		}
		return saveVolumeMsg{newVol}
	}
}

// ── View ──────────────────────────────────────────────────────────────────

func (m *Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	var content string
	switch {
	case m.mode == modePlaylistPicker:
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.renderPlaylistPickerModal())
	case m.introStep != introDone:
		content = m.renderIntro()
	default:
		content = m.renderBoxLayout()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// renderBoxLayout renders the full boxed UI.
//
//	┌─────────────────────────────────────┐
//	│ ʕ•ᴥ•ʔ vibez ♪               ♪ 72% │
//	├─────────────────────────────────────┤
//	│  Now Playing                        │  (nowPlayingHeight lines)
//	│  …progress bar, controls, bear…     │
//	├──────────────────┬──────────────────┤
//	│ Queue            │ Vibe             │  (panelH lines)
//	├──────────────────┴──────────────────┤
//	│ ʕ•ᴥ•ʔ > / search  n next  :q quit  │
//	└─────────────────────────────────────┘
func (m *Model) renderBoxLayout() string {
	inner := m.width - 2 // visual width between the │ border chars
	npH := m.nowPlayingHeight()
	panelH := m.panelHeight()

	splitW := inner / 2          // left column inner width (includes padding)
	rightW := inner - splitW - 1 // right column inner width (-1 for │ divider)

	lyricsActive := m.activePanel >= 0 && m.panels[m.activePanel] == m.lyricsP
	feedActive := m.activePanel >= 0 && m.panels[m.activePanel] == m.feedP
	eqActive := m.activePanel >= 0 && m.panels[m.activePanel] == m.eqP
	aboutActive := m.activePanel >= 0 && m.panels[m.activePanel] == m.aboutP
	fullWidth := lyricsActive || feedActive || eqActive || aboutActive || m.debugView

	var sb strings.Builder

	// ── Top border ──
	sb.WriteString("┌" + strings.Repeat("─", inner) + "┐\n")

	// ── Now Playing ──
	for _, line := range m.nowPlayingLines(inner-2, npH) {
		sb.WriteString("│ " + padRight(line, inner-2) + " │\n")
	}

	// ── Split or full divider ──
	if fullWidth {
		sb.WriteString("├" + strings.Repeat("─", inner) + "┤\n")
	} else {
		sb.WriteString("├" + strings.Repeat("─", splitW) + "┬" + strings.Repeat("─", rightW) + "┤\n")
	}

	// ── Panel content ──
	switch {
	case m.debugView:
		for _, line := range m.debugLogLines(inner-2, panelH) {
			sb.WriteString("│ " + padRight(line, inner-2) + " │\n")
		}
	case lyricsActive:
		m.lyricsP.SetSize(inner-2, panelH)
		for _, line := range toLines(m.lyricsP.View(), panelH) {
			sb.WriteString("│ " + padRight(line, inner-2) + " │\n")
		}
	case feedActive:
		m.feedP.SetSize(inner-2, panelH)
		for _, line := range toLines(m.feedP.View(), panelH) {
			sb.WriteString("│ " + padRight(line, inner-2) + " │\n")
		}
	case eqActive:
		m.eqP.SetSize(inner-2, panelH)
		for _, line := range toLines(m.eqP.View(), panelH) {
			sb.WriteString("│ " + padRight(line, inner-2) + " │\n")
		}
	case aboutActive:
		m.aboutP.SetSize(inner-2, panelH)
		for _, line := range toLines(m.aboutP.View(), panelH) {
			sb.WriteString("│ " + padRight(line, inner-2) + " │\n")
		}
	default:
		qLines := m.queuePanelLines(splitW-2, panelH)
		vLines := m.findLines(rightW-2, panelH)
		for i := range panelH {
			left := safeIdx(qLines, i)
			right := safeIdx(vLines, i)
			sb.WriteString("│ " + padRight(left, splitW-2) + " │ " + padRight(right, rightW-2) + " │\n")
		}
	}

	// ── Join or full divider ──
	if fullWidth {
		sb.WriteString("├" + strings.Repeat("─", inner) + "┤\n")
	} else {
		sb.WriteString("├" + strings.Repeat("─", splitW) + "┴" + strings.Repeat("─", rightW) + "┤\n")
	}

	// ── Status bar (context/mode then playback, each wrapped as needed) ──
	for _, line := range m.statusLines(inner - 2) {
		sb.WriteString("│ " + padRight(line, inner-2) + " │\n")
	}

	// ── Bottom border ──
	sb.WriteString("└" + strings.Repeat("─", inner) + "┘")

	return sb.String()
}

func (m *Model) renderIntro() string {
	if m.introStep < 0 || m.introStep >= len(introFrames) {
		return ""
	}

	frame := introFrames[m.introStep]
	glowIdx := m.glowStep % len(styles.GlowPalette)

	var logo strings.Builder
	for _, r := range frame {
		logo.WriteString(lipgloss.NewStyle().
			Foreground(styles.GlowPalette[glowIdx]).
			Render(string(r)))
	}

	var subtitle string
	if m.introStep >= len([]rune(introLogo))+2 { // two hold frames after the last letter
		statusText := m.initStatus
		if statusText == "" {
			statusText = "connecting…"
		}
		subtitle = "\n" + centerStr(
			lipgloss.NewStyle().Foreground(styles.ColorMuted).Render(statusText),
			m.width,
		)
	}

	topPad := max(0, (m.height-3)/2)
	return strings.Repeat("\n", topPad) +
		centerStr(logo.String(), m.width) +
		subtitle
}

// renderBoxHeader builds the header line including the border chars.

// artModeActive reports whether the now-playing panel is currently showing
// (or loading) the album-art view rather than the progress bar. Art mode
// falls back to the bar layout when the current track has no artwork, the
// fetch failed, or the terminal can't show enough colours.
func (m *Model) artModeActive() bool {
	if !m.artMode || m.supportsArtColor == nil || !m.supportsArtColor() {
		return false
	}
	t := m.playerState.Track
	// m.artwork.url must match the current track so a cover left over from
	// the previous track is never shown while the next one is in flight.
	return t != nil && t.ArtworkURL != "" && m.artwork.url == t.ArtworkURL && !m.artwork.failed
}

// nowPlayingTextRows is the compact text-mode block: "Artist — Title",
// "Album • elapsed / total", the transport icons and the status line. Every
// row saved here goes to the queue and vibe panels below.
const nowPlayingTextRows = 4

func (m *Model) nowPlayingHeight() int {
	if !m.artModeActive() {
		return nowPlayingTextRows
	}
	// The art view trades panel rows for a bigger cover: grow with the
	// terminal, but always leave the split panels a usable height.
	return min(24, max(12, m.height-14))
}

// nowPlayingLines returns exactly h lines for the Now Playing section.
func (m *Model) nowPlayingLines(contentW, h int) []string {
	if m.artModeActive() && h >= 8 {
		return m.nowPlayingArtLines(contentW, h)
	}
	return m.nowPlayingTextLines(contentW, h)
}

// nowPlayingArtLines renders the album-art view: the cover centred, with the
// track line and elapsed time beneath it — no progress bar or controls. The
// bottom four rows hold a separator, "Artist — Title", "Album • elapsed /
// total", and the status line; the rows above are the cover, vertically
// centred, sized square via the measured cell aspect ratio. While the cover
// is still downloading its rows stay blank and it pops in when loaded.
func (m *Model) nowPlayingArtLines(contentW, h int) []string {
	t := m.playerState.Track
	if t == nil {
		return m.nowPlayingTextLines(contentW, h)
	}

	aspect := m.artCellAsp
	if aspect <= 0 {
		aspect = 2.0
	}
	artColsFor := func(rows int) int { return int(math.Round(float64(rows) * aspect)) }
	artRegion := h - 4
	artRows := artRegion
	artCols := artColsFor(artRows)
	for artRows > 2 && artCols > contentW {
		artRows--
		artCols = artColsFor(artRows)
	}
	if artRows < 2 || artCols < 4 {
		return m.nowPlayingTextLines(contentW, h)
	}

	size := art.Size{Width: artCols, Height: artRows}
	if m.artwork.rendered == nil {
		m.artwork.rendered = map[art.Size][]string{}
	}
	artLines := m.artwork.rendered[size]
	if artLines == nil && m.artwork.img != nil {
		const maxRenderedArtworkSizes = 16
		if len(m.artwork.rendered) >= maxRenderedArtworkSizes {
			m.artwork.rendered = map[art.Size][]string{}
		}
		artLines = art.RenderHalfBlocks(m.artwork.img, size)
		m.artwork.rendered[size] = artLines
	}

	lines := make([]string, 0, h)
	artTop := max(0, (artRegion-artRows)/2)
	for i := range artRegion {
		if ai := i - artTop; ai >= 0 && ai < len(artLines) {
			lines = append(lines, centerStr(artLines[ai], contentW))
		} else {
			lines = append(lines, "")
		}
	}

	muted := styles.QueueItemMuted
	var titleStr string
	if m.playerState.Playing || m.playerState.Loading {
		titleStr = styles.NowPlayingTitlePlaying.Render(t.Title)
	} else {
		titleStr = styles.NowPlayingTitle.Render(t.Title)
	}
	// Long metadata is truncated (ANSI-aware) so no row can overflow the
	// panel and break the box border.
	trackLine := centerStr(
		ansi.Truncate(styles.NowPlayingArtist.Render(t.Artist)+muted.Render(" — ")+titleStr, contentW, "…"),
		contentW,
	)
	elapsed := views.FormatDuration(m.playerState.Position)
	total := views.FormatDuration(t.Duration)
	albumLine := centerStr(
		ansi.Truncate(styles.NowPlayingAlbum.Render(t.Album+" • ")+styles.TimeStyle.Render(elapsed+" / "+total), contentW, "…"),
		contentW,
	)
	lines = append(lines, "", trackLine, albumLine, m.statusLine(contentW))
	return lines
}

// statusLine renders the centred error/status line, or "" when there is no
// message. Messages prefixed with '✓' are rendered as success (green); all
// others are treated as warnings/errors (red with ⚠ prefix).
func (m *Model) statusLine(contentW int) string {
	if m.errMsg == "" {
		return ""
	}
	for _, info := range []string{"✓", "♪", "🔇", "⏳", "ℹ"} {
		if strings.HasPrefix(m.errMsg, info) {
			text := truncateStr(m.errMsg, max(10, contentW))
			return centerStr(styles.ControlActive.Render(text), contentW)
		}
	}
	const prefix = "⚠  "
	errText := truncateStr(m.errMsg, max(10, contentW-len([]rune(prefix))))
	return centerStr(styles.ErrorStyle.Render(prefix+errText), contentW)
}

func (m *Model) nowPlayingTextLines(contentW, h int) []string {
	muted := styles.QueueItemMuted

	t := m.playerState.Track
	if t == nil {
		lines := make([]string, max(h, 1))
		// Idle block: status (if any), "silence is not a vibe", credits.
		sil := max(0, min(h-1, h/2-1))
		credit := h - 2
		if credit <= sil {
			credit = min(h-1, sil+1)
		}
		lines[sil] = centerStr(muted.Render("silence is not a vibe"), contentW)
		if credit > sil {
			// U+2764 without the emoji variation selector: the "❤️" form is measured
			// as one cell but drawn as two by some terminals, which shifted the
			// right border. Coloured like the favourite heart so it stays red.
			lines[credit] = centerStr(muted.Render("made with ")+styles.FavoriteActive.Render("❤")+muted.Render(" by simonepelosi"), contentW)
		}
		if credit+1 < h {
			lines[credit+1] = centerStr(muted.Render("updated with ")+styles.FavoriteActive.Render("❤")+muted.Render(" by agf and Claude"), contentW)
		}
		if m.errMsg != "" {
			var errRendered string
			if strings.HasPrefix(m.errMsg, "✓") {
				errRendered = centerStr(styles.ControlActive.Render(m.errMsg), contentW)
			} else {
				errRendered = centerStr(styles.ErrorStyle.Render("⚠  "+m.errMsg), contentW)
			}
			lines[max(0, sil-1)] = errRendered
		}
		return lines[:h]
	}

	// Title: bright lavender while playing, softer gray while paused.
	var titleStr string
	if m.playerState.Playing || m.playerState.Loading {
		titleStr = styles.NowPlayingTitlePlaying.Render(t.Title)
	} else {
		titleStr = styles.NowPlayingTitle.Render(t.Title)
	}

	// "Artist — Title" — centred
	trackLine := centerStr(
		styles.NowPlayingArtist.Render(t.Artist)+muted.Render(" — ")+titleStr,
		contentW,
	)

	// "Album • elapsed / total" — centred
	elapsed := views.FormatDuration(m.playerState.Position)
	total := views.FormatDuration(t.Duration)
	albumLine := centerStr(
		styles.NowPlayingAlbum.Render(t.Album+" • ")+styles.TimeStyle.Render(elapsed+" / "+total),
		contentW,
	)

	// Controls: ↺  ⇄  ▶/⏸  ♡/♥
	var playIcon string
	var playStyle lipgloss.Style
	switch {
	case m.playerState.Loading:
		spinnerFrames := [10]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		playIcon = spinnerFrames[m.glowStep%10]
		playStyle = styles.ControlActive
	case m.playerState.Playing:
		playIcon = "⏸"
		playStyle = styles.ControlActive
	default:
		playIcon = "▶"
		playStyle = styles.Paused
	}

	repeatIcon, repeatStyle := "↺", muted
	switch m.playerState.RepeatMode {
	case player.RepeatModeAll:
		repeatIcon, repeatStyle = "↺", styles.ControlActive
	case player.RepeatModeOne:
		repeatIcon, repeatStyle = "↻", styles.ControlActive
	}
	shuffleStyle := muted
	if m.playerState.ShuffleMode {
		shuffleStyle = styles.ControlActive
	}

	controlsStr := repeatStyle.Render(repeatIcon) + "   " +
		shuffleStyle.Render("⇄") + "   " +
		playStyle.Render(playIcon)
	controls := centerStr(controlsStr, contentW)

	errLine := m.statusLine(contentW)
	if errLine == "" && m.memStats != "" {
		// --mem-profiling used to live in the header row.
		errLine = centerStr(muted.Render(m.memStats), contentW)
	}

	// Compact block: no "Now Playing" label, rule or progress bar; the icons
	// sit right under the track info and the status line closes the block.
	lines := []string{
		trackLine,
		albumLine,
		controls,
		errLine,
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

// queuePanelLines returns the Queue panel lines for the left split.
func (m *Model) queuePanelLines(w, h int) []string {
	total := len(m.queue.m.Tracks())

	// Header: "Tracks  12 songs"
	var headerLabel string
	if total > 0 {
		countStr := styles.QueueItemMuted.Render(fmt.Sprintf("  %d songs", total))
		headerLabel = m.panelTitle("Tracks", m.queueFocused()) + countStr
	} else {
		headerLabel = m.panelTitle("Tracks", m.queueFocused())
	}
	sep := styles.QueueItemMuted.Render(strings.Repeat("─", 5))

	currentTitle := ""
	if m.playerState.Track != nil {
		currentTitle = m.playerState.Track.Title
	}

	indexW := len(fmt.Sprintf("%d", total)) // digit width for index numbers
	tracks := m.queue.m.Tracks()
	var trackLines []string
	for i, t := range tracks {
		idx := fmt.Sprintf("%*d. ", indexW, i+1)
		label := t.Artist + " — " + t.Title
		line := truncateStr(label, w-2-indexW-2)
		switch {
		case i == m.queueCursor:
			// Highlighted entry: space/enter plays it, d removes it, K/J move it.
			marker := "› "
			if t.Title == currentTitle {
				marker = "▶ "
			}
			trackLines = append(trackLines, styles.ControlActive.Render(idx)+styles.Selected.Render(marker+line))
		case t.Title == currentTitle:
			trackLines = append(trackLines, styles.ControlActive.Render(idx)+styles.ControlActive.Render("▶ "+line))
		default:
			trackLines = append(trackLines, styles.QueueItemMuted.Render(idx)+styles.QueueItem.Render(line))
		}
	}
	if len(trackLines) == 0 {
		trackLines = []string{styles.QueueItemMuted.Render("  No tracks yet")}
	}

	// header + sep occupy 2 lines; remaining rows hold track entries.
	visibleRows := max(0, h-2)
	// Clamp offset so we never scroll past the end.
	maxOffset := max(0, len(trackLines)-visibleRows)
	if m.queueMiniOffset > maxOffset {
		m.queueMiniOffset = maxOffset
	}
	visible := trackLines[m.queueMiniOffset:]
	if len(visible) > visibleRows {
		visible = visible[:visibleRows]
	}

	result := append([]string{headerLabel, sep}, visible...)
	for len(result) < h {
		result = append(result, "")
	}
	return result
}

// searchLines renders the search popup inline (full-width in the split area).
// statusNavLines is the top status line: mode chip + context-aware shortcuts,
// wrapped to fit width w.
func (m *Model) statusNavLines(w int) []string {
	muted := styles.QueueItemMuted
	accent := styles.KeyName
	dot := muted.Render("  ·  ")

	switch m.mode {
	case modeSearch:
		// The mode label is SEARCH for both prompts; the prompt glyph and the
		// panel header say whether Apple Music or Claude Code is answering.
		label, glyph, toggle := "SEARCH", "AM ", accent.Render("^/")+muted.Render(" claude")
		switch m.searchSrc {
		case searchClaude:
			glyph, toggle = "CC ", accent.Render("^/")+muted.Render(" saved lists")
		case searchSaved:
			glyph, toggle = "SV ", accent.Render("^/")+muted.Render(" apple music")
		}
		// Every key that works here, always listed. While typing, Enter runs
		// the text (a search or a lookup) and ends typing; browsing, it acts
		// on rows. The list keys are Ctrl+something or arrows, so they work
		// and are listed either way.
		var actions []string
		if m.searchTyping {
			run := " search"
			if m.searchSrc == searchClaude {
				run = " find songs"
			}
			actions = append(actions,
				accent.Render("Enter")+muted.Render(run),
				accent.Render("^'/Esc")+muted.Render(" stop typing"),
			)
		} else {
			actions = append(actions, accent.Render("Enter")+muted.Render(" open/fold"))
			if m.searchSrc != searchSaved {
				actions = append(actions, accent.Render("^'")+muted.Render(" type"))
			}
		}
		actions = append(actions,
			accent.Render("^↑/↓")+muted.Render(" pick"),
			accent.Render("^⇧↑/↓")+muted.Render(" select"),
			accent.Render("^→")+muted.Render(" toggle select"),
			accent.Render("^←")+muted.Render(" clear/restore"),
			accent.Render("^,")+muted.Render(" add"),
			accent.Render("^.")+muted.Render(" add & play"),
		)
		if m.searchSrc == searchSaved {
			actions = append(actions, accent.Render("^Del")+muted.Render(" delete list"))
		}
		head := styles.ModeSearch.Render(label) + "  " + accent.Render(glyph)
		// The prompt shows the part of the query around the cursor that fits
		// the row, marking what is cut; the block cursor shows only while
		// typing. The key hints follow as dot-separated parts and wrap to
		// further rows only where the width forces it.
		runes := []rune(m.searchQuery)
		cur := min(m.searchCursor, len(runes))
		shown, curIdx, cutLeft, cutRight := queryWindow(runes, cur, max(8, w-lipgloss.Width(head)-1))
		query := muted.Render(string(shown))
		if m.searchTyping {
			query = styles.Header.Render(string(shown[:curIdx])) + accent.Render("█") + styles.Header.Render(string(shown[curIdx:]))
		}
		if cutLeft {
			query = muted.Render("…") + query
		}
		if cutRight {
			query += muted.Render("…")
		}
		parts := append([]string{head + query}, actions...)
		parts = append(parts, toggle, accent.Render("↑/↓")+muted.Render(" move in tracks"), accent.Render("Tab")+muted.Render(" tracks"))
		return wrapFit(parts, dot, w)
	case modeCommand:
		// The one place commands are listed: every command with what it takes,
		// always, like the SEARCH row lists every key. The row is the commands and Esc only;
		// Enter runs what is typed and Tab completes the first match, both
		// unlisted. The buffer is windowed like the search query so a long
		// `:save <name>` keeps its cursor on the row. The split stays on
		// screen underneath, and this row is the whole footer (statusLines).
		label := styles.ModeCommand.Render("CMD") + "  " + muted.Render(":")
		runes := []rune(m.cmdBuf)
		shown, _, cutLeft, _ := queryWindow(runes, len(runes), max(8, w-lipgloss.Width(label)-1))
		// Same prompt look as the SEARCH row: header-styled text, block cursor.
		buf := styles.Header.Render(string(shown))
		if cutLeft {
			buf = muted.Render("…") + buf
		}
		parts := []string{label + buf + accent.Render("█")}
		for _, c := range allCommands {
			// The command and what it takes, from its usage: ":vol <0-100|+n|-n>".
			part := accent.Render(":" + c.trigger)
			if args := strings.TrimPrefix(c.usage, c.trigger); args != "" {
				part += muted.Render(args)
			}
			parts = append(parts, part)
		}
		parts = append(parts, accent.Render("Esc")+muted.Render(" cancel"))
		return wrapFit(parts, dot, w)
	default:
		var parts []string
		switch {
		case m.debugView:
			parts = []string{
				styles.ModeNormal.Render("DEBUG"),
				accent.Render("j/k") + muted.Render(" scroll"),
				accent.Render("esc") + muted.Render(" close"),
			}
		case m.activePanel >= 0 && m.panels[m.activePanel] == m.lyricsP:
			parts = []string{
				styles.ModeNormal.Render("LYRICS"),
				accent.Render("j/k") + muted.Render(" scroll"),
				accent.Render("g/G") + muted.Render(" top/bottom"),
				accent.Render("esc") + muted.Render(" close"),
			}
		case m.activePanel >= 0 && m.panels[m.activePanel] == m.feedP:
			parts = []string{
				styles.ModeNormal.Render("FEED"),
				accent.Render("Enter") + muted.Render(" play"),
				accent.Render("Tab") + muted.Render(" queue"),
				accent.Render("Shift+Tab") + muted.Render(" next"),
				accent.Render("j/k") + muted.Render(" navigate"),
				accent.Render("r") + muted.Render(" refresh"),
				accent.Render("esc") + muted.Render(" close"),
			}
		case m.activePanel >= 0 && m.panels[m.activePanel] == m.eqP:
			parts = []string{
				styles.ModeNormal.Render("EQUALIZER"),
				accent.Render("←/→") + muted.Render(" band"),
				accent.Render("↑/↓") + muted.Render(" gain"),
				accent.Render("0") + muted.Render(" reset band"),
				accent.Render("r") + muted.Render(" reset all"),
				accent.Render("e") + muted.Render(" close"),
			}
		case m.activePanel >= 0 && m.panels[m.activePanel] == m.aboutP:
			parts = []string{
				styles.ModeNormal.Render("ABOUT"),
				accent.Render("Enter/d") + muted.Render(" donate"),
				accent.Render("esc/?") + muted.Render(" close"),
			}
		default:
			parts = m.tracksNavParts()
		}
		return wrapFit(parts, dot, w)
	}
}

// tracksNavParts is the navigation half of the Tracks panel's key list.
func (m *Model) tracksNavParts() []string {
	muted := styles.QueueItemMuted
	accent := styles.KeyName
	return []string{
		styles.ModeNormal.Render("TRACKS"),
		accent.Render(":") + muted.Render(" command"),
		accent.Render("Tab") + muted.Render(" search"),
		accent.Render("y") + muted.Render(" lyrics"),
		accent.Render("F") + muted.Render(" feed"),
		accent.Render("e") + muted.Render(" equalizer"),
		accent.Render("?") + muted.Render(" about"),
		accent.Render(":q") + muted.Render(" quit"),
	}
}

// statusPlayLines is the bottom status line: always shows playback controls,
// wrapped to fit width w.
func (m *Model) statusPlayLines(w int) []string {
	return wrapFit(m.statusPlayParts(), styles.QueueItemMuted.Render("  ·  "), w)
}

// statusPlayParts lists the playback and Tracks-editing keys.
func (m *Model) statusPlayParts() []string {
	muted := styles.QueueItemMuted
	accent := styles.KeyName

	parts := []string{
		accent.Render("spc") + muted.Render(" play/pause"),
		accent.Render("↑/↓") + muted.Render(" pick"),
		accent.Render("enter") + muted.Render(" play picked"),
		accent.Render("q") + muted.Render(" back to playing"),
		accent.Render("n/p") + muted.Render(" next/prev"),
		accent.Render("←/→") + muted.Render(" seek ±10s"),
		accent.Render("d") + muted.Render(" remove"),
		accent.Render("D/^⇧D") + muted.Render(" cut below/above"),
		accent.Render("K/J") + muted.Render(" move"),
		accent.Render("R") + muted.Render(" +5 related"),
		accent.Render("⇧T") + muted.Render(" +5 library"),
		accent.Render("s") + muted.Render(" random"),
		accent.Render("r") + muted.Render(" repeat"),
		accent.Render("c") + muted.Render(" clear"),
		accent.Render("^↑/↓") + muted.Render(" search pick"),
		accent.Render("^⇧↑/↓") + muted.Render(" search select"),
		accent.Render("^→/^←") + muted.Render(" search toggle/clear"),
	}
	if m.discovery.enabled {
		parts = append(parts, accent.Render(":discover")+styles.Playing.Render(" ● on"))
	} else if m.vibe.PickerActive() {
		parts = append(parts, accent.Render(":discover")+styles.Playing.Render(" picking…"))
	}
	if m.radio.enabled {
		parts = append(parts, accent.Render(":radio")+styles.Playing.Render(" 📻 on"))
	}
	return parts
}

// statusLines returns every status row — nav hints then playback controls —
// each already wrapped to fit width w. The count varies with terminal width,
// so panelHeight consults it rather than assuming a fixed two rows.
func (m *Model) statusLines(w int) []string {
	switch {
	case m.mode == modeSearch, m.mode == modeCommand:
		// Keys go to the search input or the command line here, so the Tracks
		// and playback key lists would be noise; the SEARCH and CMD rows
		// already list what works.
		return m.statusNavLines(w)
	case m.mode == modeNormal && !m.debugView && m.activePanel < 0:
		// The Tracks panel: one continuous key list, broken only where the
		// width forces it.
		return wrapFit(append(m.tracksNavParts(), m.statusPlayParts()...), styles.QueueItemMuted.Render("  ·  "), w)
	}
	return append(m.statusNavLines(w), m.statusPlayLines(w)...)
}

// panelHeight returns the number of rows available for the split panel section.
// Fixed overhead = top(1)+hdr(1)+hdrdiv(1)+splitdiv(1)+joindiv(1)+bottom(1) = 6,
// plus the now-playing section, which grows in art mode, and the status rows,
// which grow as hints wrap on a narrow terminal — so both are measured rather
// than assumed.
func (m *Model) panelHeight() int {
	// top border, split divider, join divider, bottom border
	fixedOverhead := 4 + m.nowPlayingHeight() + len(m.statusLines(m.width-4))
	return max(3, m.height-fixedOverhead)
}

// ── Helpers ───────────────────────────────────────────────────────────────

// padRight pads s on the right with spaces to reach visual width w, and clips
// it when it is wider. Clipping is the backstop that keeps an over-long line
// from pushing past the right border and breaking the vertical rule; it is
// ANSI-aware so styled content is never cut mid-escape-sequence.
func padRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := lipgloss.Width(s)
	switch {
	case sw > w:
		return ansi.Truncate(s, w, "…")
	case sw == w:
		return s
	default:
		return s + strings.Repeat(" ", w-sw)
	}
}

// queryWindow picks the stretch of runes around the cursor that fits budget
// cells (cursor block included), preferring to end at the cursor; the flags
// report whether text was cut on either side (the caller marks it with "…").
func queryWindow(runes []rune, cur, budget int) (shown []rune, curIdx int, cutLeft, cutRight bool) {
	budget = max(1, budget-1) // the cursor block takes one cell
	if len(runes) <= budget {
		return runes, cur, false, false
	}
	start := max(0, cur-budget+1)
	end := min(len(runes), start+budget)
	cutLeft, cutRight = start > 0, end < len(runes)
	if cutLeft {
		start++ // room for the leading ellipsis
	}
	if cutRight && end-start >= budget-1 {
		end-- // room for the trailing ellipsis
	}
	if cur > end {
		cur = end
	}
	return runes[start:end], cur - start, cutLeft, cutRight
}

// wrapFit packs parts into as many lines as needed, each at most visual width
// w, joining the parts on a line with sep. Nothing is ever hidden: a narrow
// terminal costs extra rows rather than dropped hints. Parts are never split,
// so styled segments stay intact — a lone part wider than w gets its own line
// and is clipped by padRight. Always returns at least one line.
func wrapFit(parts []string, sep string, w int) []string {
	if len(parts) == 0 || w <= 0 {
		return []string{""}
	}

	sepW := lipgloss.Width(sep)
	var lines []string
	cur, curW := "", 0

	for _, p := range parts {
		pw := lipgloss.Width(p)
		switch {
		case cur == "":
			cur, curW = p, pw
		case curW+sepW+pw <= w:
			cur += sep + p
			curW += sepW + pw
		default:
			lines = append(lines, cur)
			cur, curW = p, pw
		}
	}
	return append(lines, cur)
}

// toLines splits s into exactly h lines, padding/truncating as needed.
func toLines(s string, h int) []string {
	lines := strings.Split(s, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

// safeIdx returns lines[i] or "" if i is out of range.
func safeIdx(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

// truncateStr truncates s to at most maxW runes, adding "…" if cut.
func truncateStr(s string, maxW int) string {
	if maxW <= 1 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxW {
		return s
	}
	return string(runes[:maxW-1]) + "…"
}

// appendLog adds a timestamped entry to the debug log, capped at 500 lines.
func (m *Model) appendLog(line string) {
	const maxLines = 500
	ts := time.Now().Format("15:04:05")
	m.debugLog = append(m.debugLog, ts+"  "+line)
	if len(m.debugLog) > maxLines {
		m.debugLog = m.debugLog[len(m.debugLog)-maxLines:]
	}
}

// debugLogLines renders the debug log as exactly h lines for the split area.
func (m *Model) debugLogLines(w, h int) []string {
	accent := styles.Header
	muted := styles.QueueItemMuted

	header := accent.Render("Debug Log") + "  " + muted.Render("esc / :debug-logs to close · k/j scroll")
	sep := muted.Render(strings.Repeat("─", 9))
	contentH := max(0, h-2)

	total := len(m.debugLog)
	scroll := min(m.debugScroll, max(0, total-contentH))
	start := max(0, total-contentH-scroll)
	end := min(total, start+contentH)

	// Maximum text width: panel width minus the 2-char indent and 1-char safety margin.
	maxW := max(1, w-3)

	truncate := func(s string) string {
		r := []rune(s)
		if len(r) > maxW {
			return string(r[:maxW-1]) + "…"
		}
		return s
	}

	lines := []string{header, sep}
	if total == 0 {
		lines = append(lines, "  "+muted.Render("no log entries yet"))
	} else {
		for _, entry := range m.debugLog[start:end] {
			clipped := truncate(entry)
			var rendered string
			switch {
			case strings.Contains(entry, "[error]"):
				rendered = styles.ErrorStyle.Render(clipped)
			case strings.Contains(entry, "[playing]"):
				rendered = styles.Playing.Render(clipped)
			case strings.Contains(entry, "[js:"):
				rendered = styles.Header.Render(clipped)
			default:
				rendered = muted.Render(clipped)
			}
			lines = append(lines, "  "+rendered)
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

func centerStr(s string, width int) string {
	w := lipgloss.Width(s)
	pad := max(0, (width-w)/2)
	return strings.Repeat(" ", pad) + s
}

// qualityLabel converts a streaming bitrate (kbps) to a human-readable label.
// Apple Music tiers: AAC ≤ 320 kbps, Lossless ~1411 kbps, Hi-Res > 2000 kbps.
// Returns "" when bitrate is 0 (unknown / nothing playing).
func qualityLabel(kbps int) string {
	switch {
	case kbps <= 0:
		return ""
	case kbps > 2000:
		return "Hi-Res"
	case kbps > 320:
		return "Lossless"
	default:
		return fmt.Sprintf("%d kbps", kbps)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ── Playlist picker ───────────────────────────────────────────────────────

const pickerMaxVisible = 8

// openPlaylistPicker enters modePlaylistPicker for the given track, saving the
// current mode so it can be restored on close.
func (m *Model) openPlaylistPicker(t *provider.Track) tea.Cmd {
	m.playlistPickerTrack = t
	m.playlistPickerLoading = true
	m.playlistPickerCursor = 0
	m.playlistPickerItems = nil
	m.playlistPickerReturnMode = m.mode
	m.mode = modePlaylistPicker
	return m.fetchPlaylistsForPickerCmd()
}

// fetchPlaylistsForPickerCmd fires a background fetch of library playlists.
// The generation counter prevents stale responses from landing after the picker
// has been closed and reopened.
func (m *Model) fetchPlaylistsForPickerCmd() tea.Cmd {
	m.playlistPickerGen++
	gen := m.playlistPickerGen
	prov := m.provider
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		playlists, err := prov.GetLibraryPlaylists(ctx)
		return playlistsForPickerMsg{playlists: playlists, err: err, gen: gen}
	}
}

// handlePlaylistPickerKey handles key presses while the playlist picker is open.
func (m *Model) handlePlaylistPickerKey(k string) tea.Cmd {
	switch k {
	case "esc":
		m.mode = m.playlistPickerReturnMode
		return nil
	case "up", "k":
		if m.playlistPickerCursor > 0 {
			m.playlistPickerCursor--
		}
	case "down", "j":
		if m.playlistPickerCursor < len(m.playlistPickerItems)-1 {
			m.playlistPickerCursor++
		}
	case "enter":
		if m.playlistPickerLoading || len(m.playlistPickerItems) == 0 {
			return nil
		}
		if m.playlistPickerCursor >= len(m.playlistPickerItems) {
			m.playlistPickerCursor = len(m.playlistPickerItems) - 1
		}
		pl := m.playlistPickerItems[m.playlistPickerCursor]
		t := m.playlistPickerTrack
		m.mode = m.playlistPickerReturnMode
		prov := m.provider
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Prefer catalog ID for playback-sourced tracks; fall back to library ID.
			trackID := t.ID
			if !strings.HasPrefix(t.ID, "i.") && t.CatalogID != "" {
				trackID = t.CatalogID
			}
			err := prov.AddToPlaylist(ctx, pl.ID, trackID)
			return trackAddedToPlaylistMsg{playlistName: pl.Name, err: err}
		}
	}
	return nil
}

// pickerWindow returns the start and end indices of the visible playlist items
// window, keeping the cursor roughly centred.
func (m *Model) pickerWindow() (start, end int) {
	n := len(m.playlistPickerItems)
	visible := min(pickerMaxVisible, n)
	start = max(0, m.playlistPickerCursor-visible/2)
	end = start + visible
	if end > n {
		end = n
		start = max(0, end-visible)
	}
	return
}

// renderPlaylistPickerModal renders the centered "Add to playlist" modal.
func (m *Model) renderPlaylistPickerModal() string {
	innerW := min(46, m.width-8)
	sep := styles.QueueItemMuted.Render(strings.Repeat("─", innerW))

	var lines []string

	// Title line
	title := "Add to playlist"
	if m.playlistPickerTrack != nil {
		title = "+ " + truncateStr(m.playlistPickerTrack.Title, innerW-3)
	}
	lines = append(lines, styles.NowPlayingTitlePlaying.Render(title))
	lines = append(lines, sep)

	switch {
	case m.playlistPickerLoading:
		lines = append(lines, styles.QueueItemMuted.Render("  Loading playlists…"))
	case len(m.playlistPickerItems) == 0:
		lines = append(lines, styles.QueueItemMuted.Render("  No playlists found"))
	default:
		start, end := m.pickerWindow()
		if start > 0 {
			lines = append(lines, styles.QueueItemMuted.Render(fmt.Sprintf("  ↑ %d more", start)))
		}
		for i := start; i < end; i++ {
			name := truncateStr(m.playlistPickerItems[i].Name, innerW-3)
			if i == m.playlistPickerCursor {
				lines = append(lines, styles.Playing.Render("▶ "+name))
			} else {
				lines = append(lines, styles.QueueItem.Render("  "+name))
			}
		}
		remaining := len(m.playlistPickerItems) - end
		if remaining > 0 {
			lines = append(lines, styles.QueueItemMuted.Render(fmt.Sprintf("  ↓ %d more", remaining)))
		}
	}

	lines = append(lines, sep)
	lines = append(lines, styles.QueueItemMuted.Render("  ↑↓/jk navigate · ⏎ add · esc cancel"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorPrimary).
		Padding(0, 1).
		Width(innerW).
		Render(strings.Join(lines, "\n"))
}

func configEQBandsToPlayer(bands []config.EQBand) []player.EQBand {
	if len(bands) == 0 {
		return nil
	}
	out := make([]player.EQBand, len(bands))
	for i, b := range bands {
		out[i] = player.EQBand{Frequency: b.Frequency, Q: b.Q, Gain: b.Gain}
	}
	return out
}

func playerEQBandsToConfig(bands []player.EQBand) []config.EQBand {
	out := make([]config.EQBand, len(bands))
	for i, b := range bands {
		out[i] = config.EQBand{Frequency: b.Frequency, Q: b.Q, Gain: b.Gain}
	}
	return out
}

// searchSource is what the Search column asks; Ctrl+/ cycles through them.
type searchSource int

const (
	searchApple  searchSource = iota // "AM": Apple Music, searches as you type
	searchClaude                     // "CC": Claude Code finds songs for a description on Enter
	searchSaved                      // "SV": the saved track lists, one foldable section each
)

// next is the source Ctrl+/ moves to.
func (s searchSource) next() searchSource { return (s + 1) % 3 }
