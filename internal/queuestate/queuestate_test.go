package queuestate_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/queuestate"
)

func sampleTracks(n int) []provider.Track {
	out := make([]provider.Track, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, provider.Track{
			ID:         "i." + string(rune('a'+i%26)),
			CatalogID:  "c" + string(rune('a'+i%26)),
			Title:      "Song",
			Artist:     "Artist",
			Album:      "Album",
			Duration:   3*time.Minute + time.Duration(i)*time.Second,
			ArtworkURL: "https://example.com/a.jpg",
			Genres:     []string{"Pop"},
		})
	}
	return out
}

func TestPathFor(t *testing.T) {
	got := queuestate.PathFor("/home/x/.config/vibez/config.json")
	want := filepath.Join("/home/x/.config/vibez", queuestate.FileName)
	if got != want {
		t.Fatalf("PathFor = %q, want %q", got, want)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	in := queuestate.FromTracks(sampleTracks(3), 1)
	if err := queuestate.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := queuestate.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Version != queuestate.Version || out.CurrentIndex != 1 || len(out.Tracks) != 3 {
		t.Fatalf("unexpected state: %+v", out)
	}
	tracks := out.ProviderTracks()
	if tracks[2].Duration != 3*time.Minute+2*time.Second || tracks[0].ID != "i.a" || tracks[0].Genres[0] != "Pop" {
		t.Fatalf("tracks did not round-trip: %+v", tracks)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestLoad_MissingIsEmpty(t *testing.T) {
	st, err := queuestate.Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(st.Tracks) != 0 || st.CurrentIndex != -1 {
		t.Fatalf("expected empty state, got %+v", st)
	}
}

func TestLoad_BadJSONAndBadIndex(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := queuestate.Load(bad); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
	idx := filepath.Join(dir, "idx.json")
	if err := os.WriteFile(idx, []byte(`{"version":1,"current_index":9,"tracks":[{"id":"x","title":"t"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := queuestate.Load(idx)
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentIndex != -1 {
		t.Fatalf("out-of-range index should clamp to -1, got %d", st.CurrentIndex)
	}
}

func TestLoad_NewerVersionRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"tracks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := queuestate.Load(path); err == nil {
		t.Fatal("expected an error for a newer schema version")
	}
}

func TestFromTracks_CapKeepsCurrent(t *testing.T) {
	n := queuestate.MaxTracks + 50
	st := queuestate.FromTracks(sampleTracks(n), n-10)
	if len(st.Tracks) != queuestate.MaxTracks {
		t.Fatalf("len = %d, want %d", len(st.Tracks), queuestate.MaxTracks)
	}
	if st.CurrentIndex != queuestate.MaxTracks-10 {
		t.Fatalf("current index = %d, want %d", st.CurrentIndex, queuestate.MaxTracks-10)
	}
	st = queuestate.FromTracks(sampleTracks(n), -1)
	if len(st.Tracks) != queuestate.MaxTracks || st.CurrentIndex != -1 {
		t.Fatalf("unexpected cap result: len=%d idx=%d", len(st.Tracks), st.CurrentIndex)
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deeper", "vibez", "queue.json")
	if err := queuestate.Save(path, queuestate.FromTracks(sampleTracks(1), 0)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
