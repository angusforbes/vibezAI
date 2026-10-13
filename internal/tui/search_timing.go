package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// searchTimingRecord is one append-only entry in the search-timings JSONL file.
// It captures only timing metadata and outcome — never query text, song titles,
// album names, or any credential.
type searchTimingRecord struct {
	Timestamp time.Time `json:"ts"`
	Mode      string    `json:"mode"`              // "vibe" or "aj"
	PlanMs    int64     `json:"plan_ms"`           // planning stage wall time in milliseconds
	AppleMs   int64     `json:"apple_ms"`          // Apple provider-search stage wall time in milliseconds
	RankMs    int64     `json:"rank_ms,omitempty"` // reranking stage wall time (0 = not reranked)
	TotalMs   int64     `json:"total_ms"`          // total wall time from query submission to result
	Success   bool      `json:"success"`           // false when the search produced an error
}

// searchTimingsPath returns the path of the JSONL timing log.
// It uses $XDG_STATE_HOME when set, otherwise falls back to
// ~/.local/state. Returns "" if the home directory cannot be resolved.
func searchTimingsPath() string {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "vibezAI", "search-timings.jsonl")
}

// persistSearchTiming appends rec to the timing log. Failures are silently
// dropped: the timing log is diagnostic-only and must never affect the search
// outcome. The directory and file are created with restrictive permissions
// (0700 / 0600) so only the owning user can read the file.
func persistSearchTiming(rec searchTimingRecord) {
	path := searchTimingsPath()
	if path == "" {
		return
	}
	persistSearchTimingTo(path, rec)
}

// persistSearchTimingTo is the testable inner implementation; it writes rec as
// a JSONL line to path, creating parent directories as needed.
func persistSearchTimingTo(path string, rec searchTimingRecord) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}
