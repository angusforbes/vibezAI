package tui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPersistSearchTimingTo_WritesJSONL verifies that persistSearchTimingTo
// appends valid JSONL records to the given path and creates parent directories
// with restrictive permissions. No writes go to the real home directory.
func TestPersistSearchTimingTo_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "search-timings.jsonl")

	rec1 := searchTimingRecord{
		Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Mode:      "vibe",
		PlanMs:    1200,
		AppleMs:   3400,
		RankMs:    800,
		TotalMs:   5400,
		Success:   true,
	}
	rec2 := searchTimingRecord{
		Timestamp: time.Date(2026, 1, 2, 3, 4, 10, 0, time.UTC),
		Mode:      "aj",
		PlanMs:    900,
		AppleMs:   2100,
		RankMs:    600,
		TotalMs:   3700,
		Success:   false,
	}

	persistSearchTimingTo(path, rec1)
	persistSearchTimingTo(path, rec2)

	// File must exist.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("timing file not created: %v", err)
	}
	// Restrictive file permissions (0600).
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file permissions = %o, want 0600", got)
	}
	// Parent directory must have restrictive permissions (0700).
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("timing dir stat: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("dir permissions = %o, want 0700", got)
	}

	// Parse JSONL: two valid records.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var records []searchTimingRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r searchTimingRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("invalid JSON line %q: %v", sc.Text(), err)
		}
		records = append(records, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("want 2 records, got %d", len(records))
	}
	if records[0].Mode != "vibe" || records[0].PlanMs != 1200 || !records[0].Success {
		t.Errorf("record[0] mismatch: %+v", records[0])
	}
	if records[1].Mode != "aj" || records[1].AppleMs != 2100 || records[1].Success {
		t.Errorf("record[1] mismatch: %+v", records[1])
	}
}

// TestPersistSearchTimingTo_Appends verifies that multiple calls append to the
// same file rather than truncating it.
func TestPersistSearchTimingTo_Appends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timings.jsonl")
	for i := range 5 {
		persistSearchTimingTo(path, searchTimingRecord{
			Timestamp: time.Now(),
			Mode:      "vibe",
			TotalMs:   int64(i * 1000),
			Success:   true,
		})
	}
	f, _ := os.Open(path)
	defer f.Close()
	var n int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	if n != 5 {
		t.Fatalf("want 5 lines, got %d", n)
	}
}

// TestPersistSearchTimingTo_NoQueryOrTitles verifies the record fields hold no
// user-visible text content (the struct simply has no such fields).
func TestPersistSearchTimingTo_NoQueryOrTitles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timings.jsonl")
	persistSearchTimingTo(path, searchTimingRecord{
		Timestamp: time.Now(),
		Mode:      "vibe",
		PlanMs:    500,
		AppleMs:   1000,
		TotalMs:   1600,
		Success:   true,
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The written JSON must contain only the known fields.
	var raw map[string]any
	if err := json.Unmarshal([]byte(string(data)[:len(data)-1]), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"ts", "mode", "plan_ms", "apple_ms", "total_ms", "success"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing expected field %q", k)
		}
	}
	// rank_ms is omitempty (0 omitted), so it might not be present. That's fine.
	// But no other unexpected fields should be present.
	allowed := map[string]bool{"ts": true, "mode": true, "plan_ms": true, "apple_ms": true, "rank_ms": true, "total_ms": true, "success": true}
	for k := range raw {
		if !allowed[k] {
			t.Errorf("unexpected field %q in timing record", k)
		}
	}
}

// TestPersistSearchTiming_XDGStateHome verifies that when XDG_STATE_HOME is
// set, timing records are written there rather than to the real home directory.
func TestPersistSearchTiming_XDGStateHome(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	path := searchTimingsPath()
	want := filepath.Join(stateDir, "vibezAI", "search-timings.jsonl")
	if path != want {
		t.Fatalf("searchTimingsPath() = %q, want %q", path, want)
	}

	persistSearchTiming(searchTimingRecord{
		Timestamp: time.Now(),
		Mode:      "aj",
		PlanMs:    300,
		AppleMs:   700,
		TotalMs:   1100,
		Success:   true,
	})

	if _, err := os.Stat(want); err != nil {
		t.Fatalf("record not written to XDG_STATE_HOME path: %v", err)
	}
}

// TestPersistSearchTimingTo_BadDirIsSilent verifies that an unwritable
// directory does not panic or return an error (failures are silently dropped).
func TestPersistSearchTimingTo_BadDirIsSilent(t *testing.T) {
	// Use a path whose parent is a file (not a directory), making MkdirAll fail.
	parent := filepath.Join(t.TempDir(), "is-a-file")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "subdir", "timings.jsonl")
	// Must not panic.
	persistSearchTimingTo(path, searchTimingRecord{Mode: "vibe", Success: false})
}
