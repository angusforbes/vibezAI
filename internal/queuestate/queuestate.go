// Package queuestate persists the playback queue between vibez runs, so a
// relaunch shows the queue that was there when vibez was last closed.
//
// The queue lives in its own file next to config.json rather than inside it:
// it changes far more often than the config, and the config holds the Apple
// user token, so it should not be rewritten on every queue edit.
package queuestate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/simone-vibes/vibez/internal/provider"
)

// Version is the on-disk schema version.
const Version = 1

// FileName is the queue file, kept next to config.json.
const FileName = "queue.json"

// MaxTracks caps the saved queue; radio and discovery can grow it without bound.
const MaxTracks = 500

// Track is a queue entry in its stored form.
type Track struct {
	ID         string   `json:"id"`
	CatalogID  string   `json:"catalog_id,omitempty"`
	Title      string   `json:"title"`
	Artist     string   `json:"artist,omitempty"`
	Album      string   `json:"album,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	ArtworkURL string   `json:"artwork_url,omitempty"`
	PreviewURL string   `json:"preview_url,omitempty"`
	Genres     []string `json:"genres,omitempty"`
}

// State is everything saved about the queue.
type State struct {
	Version int       `json:"version"`
	SavedAt time.Time `json:"saved_at"`
	// CurrentIndex is the position of the track that was playing, or -1.
	CurrentIndex int     `json:"current_index"`
	Tracks       []Track `json:"tracks"`
}

// PathFor returns the queue file that belongs to the given config file.
func PathFor(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), FileName)
}

// FromTracks builds a State from the live queue. current is the index of the
// playing track, or -1. Queues longer than MaxTracks are cut down to a window
// that still contains the current track.
func FromTracks(tracks []provider.Track, current int) State {
	if current < 0 || current >= len(tracks) {
		current = -1
	}
	if len(tracks) > MaxTracks {
		start := 0
		if current > len(tracks)-MaxTracks {
			start = len(tracks) - MaxTracks
		}
		tracks = tracks[start : start+MaxTracks]
		if current >= 0 {
			current -= start
		}
	}
	st := State{
		Version:      Version,
		SavedAt:      time.Now().UTC(),
		CurrentIndex: current,
		Tracks:       make([]Track, 0, len(tracks)),
	}
	for _, t := range tracks {
		st.Tracks = append(st.Tracks, Track{
			ID:         t.ID,
			CatalogID:  t.CatalogID,
			Title:      t.Title,
			Artist:     t.Artist,
			Album:      t.Album,
			DurationMS: t.Duration.Milliseconds(),
			ArtworkURL: t.ArtworkURL,
			PreviewURL: t.PreviewURL,
			Genres:     append([]string(nil), t.Genres...),
		})
	}
	return st
}

// ProviderTracks converts the stored entries back into provider tracks.
func (s State) ProviderTracks() []provider.Track {
	out := make([]provider.Track, 0, len(s.Tracks))
	for _, t := range s.Tracks {
		out = append(out, provider.Track{
			ID:         t.ID,
			CatalogID:  t.CatalogID,
			Title:      t.Title,
			Artist:     t.Artist,
			Album:      t.Album,
			Duration:   time.Duration(t.DurationMS) * time.Millisecond,
			ArtworkURL: t.ArtworkURL,
			PreviewURL: t.PreviewURL,
			Genres:     append([]string(nil), t.Genres...),
		})
	}
	return out
}

// Load reads the queue file. A missing file is not an error: it yields an
// empty State with CurrentIndex -1.
func Load(path string) (State, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the config path
	if errors.Is(err, os.ErrNotExist) {
		return State{Version: Version, CurrentIndex: -1}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("reading queue state: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("parsing queue state: %w", err)
	}
	if st.Version > Version {
		return State{}, fmt.Errorf("queue state version %d is newer than this build supports (%d)", st.Version, Version)
	}
	if st.CurrentIndex < -1 || st.CurrentIndex >= len(st.Tracks) {
		st.CurrentIndex = -1
	}
	return st, nil
}

// Save writes the state with owner-only permissions. The file is written to a
// temporary name and renamed into place, so a crash mid-write cannot leave a
// truncated queue behind.
func Save(path string, st State) error {
	st.Version = Version
	if st.SavedAt.IsZero() {
		st.SavedAt = time.Now().UTC()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating queue state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling queue state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary queue state: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("%s queue state: %w", step, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail("securing", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("writing", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing queue state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replacing queue state: %w", err)
	}
	return nil
}
