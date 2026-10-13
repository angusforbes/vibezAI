// Tests for the jev-bench benchmark program.
// All tests are offline: no network connections, no API keys, no credentials.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/simone-vibes/vibez/internal/vibe"
)

// ─── request shape tests ──────────────────────────────────────────────────────

func TestBuildJevRequest_Shape(t *testing.T) {
	// Verify the request JSON matches the TypeSafe API contract and is identical
	// to the production format in internal/vibe/jev.go.
	candidates := []benchCandidate{
		{Index: 0, Artist: "Pearl Jam", Title: "Black", Album: "Ten"},
		{Index: 1, Artist: "Soundgarden", Title: "Black Hole Sun", Album: "Superunknown"},
	}
	req := buildJevRequest("grunge from the 90s", candidates)

	// Top-level fields.
	if req.Model != "jev-latest" {
		t.Errorf("Model = %q, want jev-latest", req.Model)
	}
	if req.State.Description != "grunge from the 90s" {
		t.Errorf("State.Description = %q", req.State.Description)
	}
	if len(req.State.Candidates) != 2 {
		t.Fatalf("State.Candidates len = %d, want 2", len(req.State.Candidates))
	}

	// One question per candidate, keyed s0, s1, ...
	if len(req.Questions) != 2 {
		t.Fatalf("Questions len = %d, want 2", len(req.Questions))
	}
	for i := range 2 {
		key := questionKey(i)
		q, ok := req.Questions[key]
		if !ok {
			t.Errorf("missing question key %q", key)
			continue
		}
		if q["type"] != "score" {
			t.Errorf("question %s type = %v, want score", key, q["type"])
		}
		instr, _ := q["instructions"].(map[string]any)
		if instr == nil {
			t.Errorf("question %s instructions is not a map", key)
		}
		// Criteria must have 5 levels.
		crit, _ := q["criteria"].([]string)
		if len(crit) != 5 {
			t.Errorf("question %s criteria len = %d, want 5", key, len(crit))
		}
	}
}

func TestBuildJevRequest_MatchesProductionFormat(t *testing.T) {
	// Serialize the request and verify that state.candidates and questions
	// have the shape the TypeSafe API expects.
	candidates := []benchCandidate{
		{Index: 0, Artist: "Alice In Chains", Title: "Would?", Album: "Dirt"},
	}
	req := buildJevRequest("heavy 90s rock", candidates)

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Deserialize into a generic map to check field presence.
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Must have state, model, questions at top level.
	for _, field := range []string{"state", "model", "questions"} {
		if _, ok := m[field]; !ok {
			t.Errorf("top-level field %q missing from request JSON", field)
		}
	}

	// state must have description and candidates.
	state, _ := m["state"].(map[string]any)
	for _, field := range []string{"description", "candidates"} {
		if _, ok := state[field]; !ok {
			t.Errorf("state field %q missing from request JSON", field)
		}
	}

	// Each question must have type=score, instructions, and criteria.
	questions, _ := m["questions"].(map[string]any)
	for qk, qv := range questions {
		q, _ := qv.(map[string]any)
		if q["type"] != "score" {
			t.Errorf("question %s type = %v, want score", qk, q["type"])
		}
		if _, ok := q["instructions"]; !ok {
			t.Errorf("question %s missing instructions", qk)
		}
		if _, ok := q["criteria"]; !ok {
			t.Errorf("question %s missing criteria", qk)
		}
	}
}

func TestBuildJevRequest_EmptyAlbumOmitted(t *testing.T) {
	candidates := []benchCandidate{
		{Index: 0, Artist: "X", Title: "Y", Album: ""},
	}
	req := buildJevRequest("test", candidates)
	data, _ := json.Marshal(req)
	// "album" should be absent when empty (omitempty).
	if contains(string(data), `"album":`) {
		t.Error("empty album should be omitted from JSON")
	}
}

// ─── index remapping and chunk merging tests ──────────────────────────────────

func TestMergeChunks_BasicOrdering(t *testing.T) {
	// Chunk 0: pool indices 0-4, scores [1,2,3,4,5] (pool 4 is best)
	// Chunk 1: pool indices 5-9, scores [5,4,3,2,1] (pool 5 is best, ties pool 4)
	// Stable sort: pool 4 appears first in all[] (chunk 0 comes first).
	scores0 := []float64{1, 2, 3, 4, 5}
	scores1 := []float64{5, 4, 3, 2, 1}
	offsets := []int{0, 5}

	idx := mergeChunks([][]float64{scores0, scores1}, offsets, 4)
	if len(idx) != 4 {
		t.Fatalf("want 4 results, got %d: %v", len(idx), idx)
	}
	// Pool 4 (score 5) and pool 5 (score 5) tie; stable sort puts pool 4 first.
	if idx[0] != 4 {
		t.Errorf("idx[0] = %d, want 4 (highest score from chunk 0)", idx[0])
	}
	if idx[1] != 5 {
		t.Errorf("idx[1] = %d, want 5 (tied highest from chunk 1)", idx[1])
	}
	// Pool 3 (score 4) and pool 6 (score 4) tie; pool 3 from chunk 0 first.
	if idx[2] != 3 {
		t.Errorf("idx[2] = %d, want 3", idx[2])
	}
	if idx[3] != 6 {
		t.Errorf("idx[3] = %d, want 6", idx[3])
	}
}

func TestMergeChunks_IndexRemapping(t *testing.T) {
	// Verify that chunk-local indices are correctly remapped to pool indices.
	// Pool of 20: chunk 0 covers pool 0-9, chunk 1 covers pool 10-19.
	// chunk 0 local index 3 → pool index 3
	// chunk 1 local index 7 → pool index 17
	scores0 := make([]float64, 10) // all 0 except idx 3
	scores1 := make([]float64, 10) // all 0 except idx 7
	scores0[3] = 4.0               // pool index 3: best overall
	scores1[7] = 3.0               // pool index 17: second best

	offsets := []int{0, 10}
	idx := mergeChunks([][]float64{scores0, scores1}, offsets, 3)

	if idx[0] != 3 {
		t.Errorf("idx[0] = %d, want 3 (pool index for chunk0 local 3)", idx[0])
	}
	if idx[1] != 17 {
		t.Errorf("idx[1] = %d, want 17 (pool index for chunk1 local 7)", idx[1])
	}
}

func TestMergeChunks_LimitRespected(t *testing.T) {
	scores := []float64{4, 3, 2, 1}
	idx := mergeChunks([][]float64{scores}, []int{0}, 2)
	if len(idx) != 2 {
		t.Errorf("limit=2: got %d results", len(idx))
	}
	if idx[0] != 0 || idx[1] != 1 {
		t.Errorf("want [0 1], got %v", idx)
	}
}

func TestMergeChunks_StableTieBreak(t *testing.T) {
	// All scores equal: stable sort must preserve the order candidates appear
	// in all[] (chunk 0 before chunk 1, within chunks original order).
	scores0 := []float64{2.0, 2.0, 2.0}
	scores1 := []float64{2.0, 2.0, 2.0}
	offsets := []int{0, 3}
	idx := mergeChunks([][]float64{scores0, scores1}, offsets, 6)
	for i, want := range []int{0, 1, 2, 3, 4, 5} {
		if idx[i] != want {
			t.Errorf("tie: idx[%d] = %d, want %d (stable order)", i, idx[i], want)
		}
	}
}

func TestMergeChunks_UnevenLastChunk(t *testing.T) {
	// 13 candidates: chunk 0 has 10, chunk 1 has 3.
	scores0 := make([]float64, 10)
	scores1 := []float64{3.5, 2.5, 1.5} // chunk1 local 0 → pool 10 is best
	scores0[0] = 1.0                     // pool 0 is not the best

	offsets := []int{0, 10}
	idx := mergeChunks([][]float64{scores0, scores1}, offsets, 3)
	if idx[0] != 10 {
		t.Errorf("idx[0] = %d, want 10 (pool index for best in chunk1)", idx[0])
	}
	if idx[1] != 11 {
		t.Errorf("idx[1] = %d, want 11", idx[1])
	}
}

// ─── metrics tests ────────────────────────────────────────────────────────────

func TestCalcMetrics_Basic(t *testing.T) {
	pool := []PoolSong{
		{0, "Pearl Jam", "Black", "Ten"},
		{1, "Soundgarden", "Black Hole Sun", "Superunknown"},
		{2, "Alice In Chains", "Would?", "Dirt"},
		{3, "Pearl Jam", "Alive", "Ten"},
		{4, "Stone Temple Pilots", "Plush", "Core"},
	}
	// Choose indices 0, 1, 3 (Pearl Jam x2, Soundgarden x1).
	indices := []int{0, 1, 3}
	m := calcMetrics(pool, indices, []string{"pearl jam"})

	if m.ChosenCount != 3 {
		t.Errorf("ChosenCount = %d, want 3", m.ChosenCount)
	}
	if m.UniqueArtists != 2 {
		t.Errorf("UniqueArtists = %d, want 2 (Pearl Jam, Soundgarden)", m.UniqueArtists)
	}
	// Pearl Jam has 2 songs in chosen; ref artist count = 2.
	if m.RefArtistCount != 2 {
		t.Errorf("RefArtistCount = %d, want 2", m.RefArtistCount)
	}
	wantFrac := 2.0 / 3.0
	if abs(m.RefArtistFrac-wantFrac) > 0.001 {
		t.Errorf("RefArtistFrac = %.4f, want %.4f", m.RefArtistFrac, wantFrac)
	}
}

func TestCalcMetrics_NoDuplicates(t *testing.T) {
	pool := []PoolSong{
		{0, "Nirvana", "Come As You Are", "Nevermind"},
		{1, "Nirvana", "Lithium", "Nevermind"},
		{2, "Nirvana", "Smells Like Teen Spirit", "Nevermind"},
	}
	// All different titles, same artist, same album.
	indices := []int{0, 1, 2}
	m := calcMetrics(pool, indices, []string{"nirvana"})

	if m.DuplicateCount != 0 {
		t.Errorf("DuplicateCount = %d, want 0 (different titles)", m.DuplicateCount)
	}
	if m.UniqueArtists != 1 {
		t.Errorf("UniqueArtists = %d, want 1", m.UniqueArtists)
	}
	if m.UniqueAlbums != 1 {
		t.Errorf("UniqueAlbums = %d, want 1", m.UniqueAlbums)
	}
	if m.RefArtistFrac != 1.0 {
		t.Errorf("RefArtistFrac = %.2f, want 1.00", m.RefArtistFrac)
	}
}

func TestCalcMetrics_WithDuplicate(t *testing.T) {
	pool := []PoolSong{
		{0, "Nirvana", "Come As You Are", "Nevermind"},
		{1, "Nirvana", "Come as you are", "Nevermind"}, // duplicate via normalisation
		{2, "Foo Fighters", "Everlong", "The Colour and the Shape"},
	}
	indices := []int{0, 1, 2}
	m := calcMetrics(pool, indices, []string{})

	if m.DuplicateCount != 1 {
		t.Errorf("DuplicateCount = %d, want 1 (normalised duplicate)", m.DuplicateCount)
	}
}

func TestCalcMetrics_ZeroRefArtist(t *testing.T) {
	pool := []PoolSong{
		{0, "Nirvana", "Come As You Are", ""},
		{1, "Soundgarden", "Spoonman", ""},
	}
	indices := []int{0, 1}
	m := calcMetrics(pool, indices, []string{"pearl jam"})
	if m.RefArtistCount != 0 {
		t.Errorf("RefArtistCount = %d, want 0", m.RefArtistCount)
	}
	if m.RefArtistFrac != 0 {
		t.Errorf("RefArtistFrac = %.2f, want 0", m.RefArtistFrac)
	}
}

func TestCalcMetrics_EmptyIndices(t *testing.T) {
	pool := []PoolSong{{0, "X", "Y", "Z"}}
	m := calcMetrics(pool, nil, []string{"x"})
	if m.ChosenCount != 0 {
		t.Errorf("ChosenCount = %d, want 0", m.ChosenCount)
	}
	if m.RefArtistFrac != 0 {
		t.Errorf("RefArtistFrac = %f, want 0 for empty selection", m.RefArtistFrac)
	}
}

// ─── normalisation tests ──────────────────────────────────────────────────────

func TestNormField_LowerCase(t *testing.T) {
	if normField("Pearl Jam") != "pearl jam" {
		t.Errorf("normField did not lowercase")
	}
}

func TestNormField_SmartApostrophe(t *testing.T) {
	// Unicode right single quotation mark (\u2019) → ASCII apostrophe.
	got := normField("Stone Temple Pilots\u2019 vibe")
	want := "stone temple pilots' vibe"
	if got != want {
		t.Errorf("smart apostrophe: got %q, want %q", got, want)
	}
}

func TestNormField_EmDash(t *testing.T) {
	// Em dash (\u2014) → hyphen-minus.
	got := normField("Deep Purple \u2014 Smoke")
	want := "deep purple - smoke"
	if got != want {
		t.Errorf("em dash: got %q, want %q", got, want)
	}
}

func TestNormField_CollapseWhitespace(t *testing.T) {
	got := normField("  too   many   spaces  ")
	want := "too many spaces"
	if got != want {
		t.Errorf("whitespace: got %q, want %q", got, want)
	}
}

func TestCanonicalKey_Different(t *testing.T) {
	k1 := canonicalKey("Pearl Jam", "Black")
	k2 := canonicalKey("Pearl Jam", "Alive")
	if k1 == k2 {
		t.Error("different titles should produce different keys")
	}
}

func TestCanonicalKey_Equivalent(t *testing.T) {
	// Same song with punctuation/case variants.
	k1 := canonicalKey("Pearl Jam", "Black")
	k2 := canonicalKey("PEARL JAM", "BLACK")
	if k1 != k2 {
		t.Errorf("case variants should produce the same key: %q vs %q", k1, k2)
	}
}

// ─── trial spec tests ─────────────────────────────────────────────────────────

func TestBuildTrialSpecs_Count(t *testing.T) {
	specs := buildTrialSpecs(benchmarkQueries, poolSizes)
	// 2 queries × 2 pool sizes × 3 variants × 2 repeats = 24
	if len(specs) != 24 {
		t.Errorf("want 24 trial specs, got %d", len(specs))
	}
}

func TestBuildTrialSpecs_BalancedRepeats(t *testing.T) {
	specs := buildTrialSpecs(benchmarkQueries, poolSizes)
	// For each (query, pool), count each variant in each repeat position.
	type key struct {
		query, variant string
		poolSize, repeat int
	}
	counts := map[key]int{}
	for _, sp := range specs {
		counts[key{sp.queryLabel, sp.variant, sp.poolSize, sp.repeat}]++
	}
	for k, n := range counts {
		if n != 1 {
			t.Errorf("spec {%s %s pool=%d repeat=%d} appears %d times, want 1",
				k.query, k.variant, k.poolSize, k.repeat, n)
		}
	}
}

func TestBuildTrialSpecs_BalancedOrder(t *testing.T) {
	// For each (query, pool) block:
	//   repeat 1 must be: claude(1st), jev-full(2nd), jev-chunked(3rd)
	//   repeat 2 must be: jev-chunked(4th), jev-full(5th), claude(6th)
	specs := buildTrialSpecs(benchmarkQueries, poolSizes)
	type groupKey struct {
		query    string
		poolSize int
	}
	groups := map[groupKey][]trialSpec{}
	for _, sp := range specs {
		gk := groupKey{sp.queryLabel, sp.poolSize}
		groups[gk] = append(groups[gk], sp)
	}
	for gk, grp := range groups {
		if len(grp) != 6 {
			t.Errorf("group %v: %d specs, want 6", gk, len(grp))
			continue
		}
		wantVariants := []string{"claude", "jev-full", "jev-chunked", "jev-chunked", "jev-full", "claude"}
		wantRepeats := []int{1, 1, 1, 2, 2, 2}
		for i, sp := range grp {
			if sp.variant != wantVariants[i] {
				t.Errorf("group %v pos %d: variant=%s, want %s", gk, i, sp.variant, wantVariants[i])
			}
			if sp.repeat != wantRepeats[i] {
				t.Errorf("group %v pos %d: repeat=%d, want %d", gk, i, sp.repeat, wantRepeats[i])
			}
		}
	}
}

// ─── poolSlice tests ──────────────────────────────────────────────────────────

func TestPoolSlice_Subset(t *testing.T) {
	pf := PoolFile{Songs: makeSongs(80)}
	s := poolSlice(pf, 40)
	if len(s) != 40 {
		t.Errorf("poolSlice(80songs,40) = %d, want 40", len(s))
	}
	if s[0].Index != 0 || s[39].Index != 39 {
		t.Error("poolSlice must return the first N songs")
	}
}

func TestPoolSlice_WhenSmallerThanN(t *testing.T) {
	pf := PoolFile{Songs: makeSongs(30)}
	s := poolSlice(pf, 80)
	if len(s) != 30 {
		t.Errorf("poolSlice(30songs,80) = %d, want 30 (actual pool)", len(s))
	}
}

// ─── effective model and pool-loading tests ───────────────────────────────

// TestEffectiveModelMatchesDefaultClaudeModel verifies that effectiveClaudeModel
// with an empty config string returns vibe.DefaultClaudeModel, which is the model
// the production app passes to the Claude CLI when no model is configured.
// This is the confound that the original benchmark lacked.
func TestEffectiveModelMatchesDefaultClaudeModel(t *testing.T) {
	got := effectiveClaudeModel("")
	if got != vibe.DefaultClaudeModel {
		t.Errorf("effectiveClaudeModel(\"\") = %q, want DefaultClaudeModel=%q",
			got, vibe.DefaultClaudeModel)
	}
}

func TestEffectiveModelExplicitValue(t *testing.T) {
	got := effectiveClaudeModel("claude-haiku-4-5")
	if got != "claude-haiku-4-5" {
		t.Errorf("effectiveClaudeModel with explicit value = %q, want %q", got, "claude-haiku-4-5")
	}
}

func TestEffectiveModelCLIKeyword(t *testing.T) {
	// "default" and "cli" should yield empty string (no --model flag to CLI).
	for _, kw := range []string{"default", "cli", "DEFAULT", "CLI"} {
		got := effectiveClaudeModel(kw)
		if got != "" {
			t.Errorf("effectiveClaudeModel(%q) = %q, want empty (CLI picks model)", kw, got)
		}
	}
}

func TestLoadPoolIfExists_Found(t *testing.T) {
	dir := t.TempDir()
	pf := PoolFile{
		QueryLabel: "test",
		SongCount:  3,
		Songs:      makeSongs(3),
	}
	data, _ := json.Marshal(pf)
	if err := os.WriteFile(filepath.Join(dir, "pool-test.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, ok := loadPoolIfExists(dir, "test")
	if !ok {
		t.Fatal("loadPoolIfExists: expected ok=true for existing pool")
	}
	if len(loaded.Songs) != 3 {
		t.Errorf("loaded %d songs, want 3", len(loaded.Songs))
	}
	if loaded.QueryLabel != "test" {
		t.Errorf("QueryLabel = %q, want test", loaded.QueryLabel)
	}
}

func TestLoadPoolIfExists_Missing(t *testing.T) {
	dir := t.TempDir()
	_, ok := loadPoolIfExists(dir, "nonexistent")
	if ok {
		t.Error("loadPoolIfExists: expected ok=false for missing pool file")
	}
}

func TestLoadPoolIfExists_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pool-bad.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok := loadPoolIfExists(dir, "bad")
	if ok {
		t.Error("loadPoolIfExists: expected ok=false for invalid JSON")
	}
}

func TestLoadPoolIfExists_EmptySongs(t *testing.T) {
	dir := t.TempDir()
	// Valid JSON but empty Songs slice.
	pf := PoolFile{QueryLabel: "empty", Songs: []PoolSong{}}
	data, _ := json.Marshal(pf)
	os.WriteFile(filepath.Join(dir, "pool-empty.json"), data, 0o644) //nolint:errcheck
	_, ok := loadPoolIfExists(dir, "empty")
	if ok {
		t.Error("loadPoolIfExists: expected ok=false when Songs is empty (not usable for replay)")
	}
}

// TestBuildCorrectedClaudeSpecs verifies the 8-trial corrected run design.
func TestBuildCorrectedClaudeSpecs_Count(t *testing.T) {
	specs := buildCorrectedClaudeSpecs(benchmarkQueries, poolSizes)
	// 2 queries × 2 pool sizes × 2 repeats = 8
	if len(specs) != 8 {
		t.Errorf("want 8 corrected-claude specs, got %d", len(specs))
	}
}

func TestBuildCorrectedClaudeSpecs_AllClaude(t *testing.T) {
	specs := buildCorrectedClaudeSpecs(benchmarkQueries, poolSizes)
	for _, sp := range specs {
		if sp.variant != "claude" {
			t.Errorf("corrected spec has variant=%q, want claude", sp.variant)
		}
	}
}

func TestBuildCorrectedClaudeSpecs_BothRepeats(t *testing.T) {
	specs := buildCorrectedClaudeSpecs(benchmarkQueries, poolSizes)
	type key struct {
		query    string
		poolSize int
	}
	repeatCounts := map[key]map[int]int{}
	for _, sp := range specs {
		k := key{sp.queryLabel, sp.poolSize}
		if repeatCounts[k] == nil {
			repeatCounts[k] = map[int]int{}
		}
		repeatCounts[k][sp.repeat]++
	}
	for k, counts := range repeatCounts {
		if counts[1] != 1 || counts[2] != 1 {
			t.Errorf("group %v: repeat counts %v, want {1:1, 2:1}", k, counts)
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func makeSongs(n int) []PoolSong {
	songs := make([]PoolSong, n)
	for i := range n {
		songs[i] = PoolSong{Index: i, Artist: "Artist", Title: "Song", Album: "Album"}
	}
	return songs
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := range len(s) - len(sub) + 1 {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
