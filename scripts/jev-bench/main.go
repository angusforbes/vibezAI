// Package main is a standalone controlled ranking benchmark for vibezAI.
// It compares three ranking backends (Claude, Jev full-list, Jev chunked) on
// identical candidate pools fetched once from Apple Music.
//
// Two modes:
//
//	# Full benchmark (original; requires TYPESAFE_API_KEY)
//	bash -ic 'go run ./scripts/jev-bench/'
//
//	# Corrected-Claude only (replay saved pools, 8 Claude trials, no Jev calls)
//	bash -ic 'go run ./scripts/jev-bench/ --corrected-claude'
//
// Output files are written to docs/bench-output/ (no credentials).
// No UI interaction, no binary installs, no app restarts.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/simone-vibes/vibez/internal/config"
	"github.com/simone-vibes/vibez/internal/provider"
	appleProvider "github.com/simone-vibes/vibez/internal/provider/apple"
	"github.com/simone-vibes/vibez/internal/vibe"
)

// ─── constants ──────────────────────────────────────────────────────────────

const (
	jevEndpoint         = "https://api.typesafe.ai/v1/systemone"
	jevModel            = "jev-latest"
	resultCap           = 15 // matches vibeResultCap in tui/model.go
	maxPool             = 80 // target pool size per query
	chunkSize           = 10 // candidates per chunk in the chunked variant
	maxConcurrentChunks = 2  // semaphore width for chunk goroutines
	perRequestTimeout   = 30 * time.Second
	appleSearchTimeout  = 20 * time.Second
	appleParallelism    = 3  // max concurrent Apple search requests
	claudeCallTimeout   = 45 * time.Second // per Claude Rerank call
)

// benchmarkQueries are the two representative queries from the manual test report.
// Queries match the visible text captured from the AJ/AI panels on 2026-09-21.
var benchmarkQueries = []queryDef{
	{
		Label:      "pearl-jam",
		Text:       "songs similar to Pearl Jam's vibe but from the last few years",
		RefArtists: []string{"pearl jam"},
	},
	{
		Label:      "stone-temple-pilots",
		Text:       "songs like Stone Temple Pilots but from a few years before them",
		RefArtists: []string{"stone temple pilots", "stp"},
	},
}

// poolSizes are the nested pool sizes to test; 40 matches production vibePoolCap.
var poolSizes = []int{40, 80}

// benchScoreCriteria are identical to the production jevScoreCriteria in
// internal/vibe/jev.go. Copied here so the benchmark uses exactly the same
// rubric without modifying the production package.
var benchScoreCriteria = []string{
	"No match: wrong genre, wrong era, or clearly unrelated to the described vibe",
	"Weak match: loosely connected by era or genre only; missing the mood or style described",
	"Moderate match: fits the general genre or mood but not the specific feel, artist, or request",
	"Strong match: clearly fits the described mood, style, and era — a well-chosen pick",
	"Perfect match: exactly what was requested — the specific artist, song, or precise vibe described",
}

const benchJevContext = "Prefer original studio recordings over live versions, karaoke covers, tributes, and remixes unless the description explicitly requests them. Preserve any version, format, or edition constraints the listener describes (acoustic, instrumental, extended, etc.)."

// ─── types ──────────────────────────────────────────────────────────────────

type queryDef struct {
	Label      string
	Text       string
	RefArtists []string // lowercase; used to compute ref-artist fraction
}

// PoolSong is one sanitized song entry. No IDs, URLs, or credentials.
type PoolSong struct {
	Index  int    `json:"index"`
	Artist string `json:"artist"`
	Title  string `json:"title"`
	Album  string `json:"album,omitempty"`
}

// PoolFile is the immutable pool saved to JSON for replay.
type PoolFile struct {
	CreatedAt  time.Time  `json:"created_at"`
	QueryLabel string     `json:"query_label"`
	QueryText  string     `json:"query_text"`
	PlanModel  string     `json:"plan_model,omitempty"`
	PlanTerms  []string   `json:"plan_terms"`
	SongCount  int        `json:"song_count"`
	Songs      []PoolSong `json:"songs"`
}

// HTTPPhase captures per-request network-phase wall-clock timings from
// net/http/httptrace. No headers, addresses, or credentials are recorded.
type HTTPPhase struct {
	DNSMS       float64 `json:"dns_ms"`
	ConnectMS   float64 `json:"connect_ms"`
	TLSMS       float64 `json:"tls_ms"`
	FirstByteMS float64 `json:"first_byte_ms"`
	BodyMS      float64 `json:"body_ms"`
	TotalMS     float64 `json:"total_ms"`
}

// ChunkRecord is one chunk's outcome within a chunked-Jev trial.
type ChunkRecord struct {
	ChunkIndex   int        `json:"chunk_index"`
	Offset       int        `json:"offset"`
	Size         int        `json:"size"`
	ElapsedMS    float64    `json:"elapsed_ms"`
	Censored     bool       `json:"censored,omitempty"`
	HTTPStatus   int        `json:"http_status,omitempty"`
	ModelUsed    string     `json:"model_used,omitempty"`
	InputTokens  int        `json:"input_tokens,omitempty"`
	OutputTokens int        `json:"output_tokens,omitempty"`
	Phase        *HTTPPhase `json:"phase,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// TrialMetrics are quality measurements computed from the chosen indices.
type TrialMetrics struct {
	ChosenCount    int     `json:"chosen_count"`
	UniqueArtists  int     `json:"unique_artists"`
	UniqueAlbums   int     `json:"unique_albums"`
	DuplicateCount int     `json:"duplicate_count"` // duplicate (artist,title) in chosen
	RefArtistCount int     `json:"ref_artist_count"`
	RefArtistFrac  float64 `json:"ref_artist_frac"`
}

// TrialRecord is one complete ranking trial written to the JSONL output.
type TrialRecord struct {
	Timestamp    time.Time    `json:"timestamp"`
	QueryLabel   string       `json:"query_label"`
	PoolSize     int          `json:"pool_size"`
	ActualPool   int          `json:"actual_pool"`  // may be less than PoolSize
	Variant      string       `json:"variant"`      // "claude" | "jev-full" | "jev-chunked"
	Repeat       int          `json:"repeat"`       // 1 or 2
	ElapsedMS    float64      `json:"elapsed_ms"`   // wall time for this trial
	Censored     bool         `json:"censored,omitempty"`
	Indices      []int        `json:"indices"`
	Metrics      TrialMetrics `json:"metrics"`
	// Jev-full fields
	HTTPStatus   int        `json:"http_status,omitempty"`
	ModelUsed    string     `json:"model_used,omitempty"`
	InputTokens  int        `json:"input_tokens,omitempty"`
	OutputTokens int        `json:"output_tokens,omitempty"`
	Phase        *HTTPPhase `json:"phase,omitempty"`
	// Jev-chunked fields
	Chunks       []ChunkRecord `json:"chunks,omitempty"`
	SumChunkMS   float64       `json:"sum_chunk_ms,omitempty"`
	// Claude fields
	ClaudeModel  string `json:"claude_model,omitempty"`
	ClaudeEffort string `json:"claude_effort,omitempty"`
	// Failure
	Error string `json:"error,omitempty"`
}

// ─── Jev request/response types (independent; includes usage field) ──────────

type jevBenchRequest struct {
	State     jevBenchState              `json:"state"`
	Model     string                     `json:"model"`
	Questions map[string]map[string]any  `json:"questions"`
}

type jevBenchState struct {
	Description string           `json:"description"`
	Candidates  []benchCandidate `json:"candidates"`
}

type benchCandidate struct {
	Index  int    `json:"index"`
	Artist string `json:"artist"`
	Title  string `json:"title"`
	Album  string `json:"album,omitempty"`
}

type jevBenchResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type jevScoreAnswer struct {
	Type          string             `json:"type"`
	Score         *float64           `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// chunkScore links a pool index to its Jev score, for merging chunk results.
type chunkScore struct {
	poolIdx int
	score   float64
}

// ─── request building ────────────────────────────────────────────────────────

// questionKey returns the per-candidate question ID, matching production.
func questionKey(i int) string { return fmt.Sprintf("s%d", i) }

// buildJevQuestion returns one Score question for the candidate at localIdx
// within the chunk (or full list) state, matching the production format.
func buildJevQuestion(localIdx int) map[string]any {
	return map[string]any{
		"type": "score",
		"instructions": map[string]any{
			"question": fmt.Sprintf(
				"How well does the song at `candidates[%d]` (`candidates[%d].artist` — `candidates[%d].title`) fit the listener's description in `description`?",
				localIdx, localIdx, localIdx,
			),
			"context": benchJevContext,
		},
		"criteria": benchScoreCriteria,
	}
}

// buildJevRequest constructs the full Jev request body for a set of candidates.
// localOffset is the candidates' position within the overall pool (for index
// remapping after a chunk call); it does not affect the request JSON itself.
func buildJevRequest(description string, candidates []benchCandidate) jevBenchRequest {
	questions := make(map[string]map[string]any, len(candidates))
	for i := range candidates {
		questions[questionKey(i)] = buildJevQuestion(i)
	}
	return jevBenchRequest{
		State: jevBenchState{
			Description: description,
			Candidates:  candidates,
		},
		Model:     jevModel,
		Questions: questions,
	}
}

// poolToCandidates converts a slice of PoolSong to benchCandidate, preserving
// pool indices. startIdx is the offset within the overall pool (used by chunked
// calls to number candidates relative to the chunk, not the full pool).
func poolToCandidates(songs []PoolSong) []benchCandidate {
	cs := make([]benchCandidate, len(songs))
	for i, s := range songs {
		cs[i] = benchCandidate{Index: i, Artist: s.Artist, Title: s.Title, Album: s.Album}
	}
	return cs
}

// ─── response parsing ────────────────────────────────────────────────────────

const maxResponseBytes = 1 << 20 // 1 MiB cap on Jev response

// parseJevScores extracts (localIdx → score) from a Jev response.
// The local indices match the candidates that were sent in the request.
func parseJevScores(resp jevBenchResponse, n int) ([]float64, error) {
	top := float64(len(benchScoreCriteria) - 1)
	scores := make([]float64, n)
	for i := range n {
		key := questionKey(i)
		raw, ok := resp.Answers[key]
		if !ok {
			return nil, fmt.Errorf("missing answer for candidate %d", i)
		}
		var ans jevScoreAnswer
		if err := json.Unmarshal(raw, &ans); err != nil {
			return nil, fmt.Errorf("decode answer %d: %w", i, err)
		}
		if ans.Type != "score" {
			return nil, fmt.Errorf("answer %d has type %q, want score", i, ans.Type)
		}
		if ans.Score == nil {
			return nil, fmt.Errorf("answer %d score is missing or null", i)
		}
		s := *ans.Score
		if math.IsNaN(s) || math.IsInf(s, 0) || s < 0 || s > top+0.001 {
			return nil, fmt.Errorf("answer %d score %v out of range [0,%.0f]", i, s, top)
		}
		scores[i] = s
	}
	return scores, nil
}

// scoresToRanking returns 0-based indices into the candidate array, sorted
// descending by score with stable tie-breaking (preserving array order).
func scoresToRanking(scores []float64, limit int) []int {
	type sc struct {
		idx   int
		score float64
	}
	all := make([]sc, len(scores))
	for i, s := range scores {
		all[i] = sc{i, s}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
	n := min(limit, len(all))
	out := make([]int, n)
	for i, s := range all[:n] {
		out[i] = s.idx
	}
	return out
}

// mergeChunks combines per-chunk score slices into a final pool-index ranking.
// chunkScores[c][i] is the score for the candidate at pool index chunkOffsets[c]+i.
// The returned indices are 0-based into the overall pool, sorted by score descending.
func mergeChunks(chunkScores [][]float64, chunkOffsets []int, limit int) []int {
	var all []chunkScore
	for c, scores := range chunkScores {
		off := chunkOffsets[c]
		for i, s := range scores {
			all = append(all, chunkScore{poolIdx: off + i, score: s})
		}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
	n := min(limit, len(all))
	out := make([]int, n)
	for i, cs := range all[:n] {
		out[i] = cs.poolIdx
	}
	return out
}

// ─── HTTP execution with httptrace ──────────────────────────────────────────

type jevCallResult struct {
	resp         jevBenchResponse
	phase        HTTPPhase
	httpStatus   int
	elapsedMS    float64
	censored     bool
	err          error // nil on success
}

// callJev sends one Jev HTTP request with httptrace timing. The API key is
// only ever placed in the Authorization header; it is never written to logs.
// No request or response headers are logged. On non-200 responses the body
// is discarded without echoing.
func callJev(ctx context.Context, apiKey string, reqBody jevBenchRequest) jevCallResult {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return jevCallResult{err: fmt.Errorf("marshal: %w", err)}
	}

	// Attach httptrace to measure network phases.
	var phase HTTPPhase
	var (
		dnsStart     time.Time
		connectStart time.Time
		tlsStart     time.Time
		firstByte    time.Time
		reqStart     = time.Now()
	)
	trace := &httptrace.ClientTrace{
		DNSStart: func(_ httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(_ httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				phase.DNSMS = ms(time.Since(dnsStart))
			}
		},
		ConnectStart: func(_, _ string) {
			connectStart = time.Now()
		},
		ConnectDone: func(_, _ string, _ error) {
			if !connectStart.IsZero() {
				phase.ConnectMS = ms(time.Since(connectStart))
			}
		},
		TLSHandshakeStart: func() {
			tlsStart = time.Now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			if !tlsStart.IsZero() {
				phase.TLSMS = ms(time.Since(tlsStart))
			}
		},
		GotFirstResponseByte: func() {
			firstByte = time.Now()
			phase.FirstByteMS = ms(firstByte.Sub(reqStart))
		},
	}

	// Build request with 30s deadline.
	reqCtx, cancel := context.WithTimeout(httptrace.WithClientTrace(ctx, trace), perRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, jevEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return jevCallResult{err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{} // no global timeout; per-request via context
	resp, err := client.Do(req)
	if err != nil {
		censored := reqCtx.Err() != nil
		total := ms(time.Since(reqStart))
		return jevCallResult{
			phase:     phase,
			elapsedMS: total,
			censored:  censored,
			err:       fmt.Errorf("http do: %w", err),
		}
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	if status != http.StatusOK {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		total := ms(time.Since(reqStart))
		return jevCallResult{
			phase:      phase,
			httpStatus: status,
			elapsedMS:  total,
			err:        fmt.Errorf("HTTP %d", status),
		}
	}

	// Read body, record transfer timing.
	bodyStart := firstByte
	if bodyStart.IsZero() {
		bodyStart = time.Now()
	}
	limited := io.LimitReader(resp.Body, maxResponseBytes)
	respBytes, err := io.ReadAll(limited)
	phase.BodyMS = ms(time.Since(bodyStart))
	phase.TotalMS = ms(time.Since(reqStart))

	if err != nil {
		return jevCallResult{
			phase:      phase,
			httpStatus: status,
			elapsedMS:  phase.TotalMS,
			err:        fmt.Errorf("read body: %w", err),
		}
	}

	var parsed jevBenchResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return jevCallResult{
			phase:      phase,
			httpStatus: status,
			elapsedMS:  phase.TotalMS,
			err:        fmt.Errorf("decode response: %w", err),
		}
	}

	return jevCallResult{
		resp:       parsed,
		phase:      phase,
		httpStatus: status,
		elapsedMS:  phase.TotalMS,
	}
}

// ms returns elapsed duration in milliseconds as float64.
func ms(d time.Duration) float64 { return float64(d.Milliseconds()) }

// ─── ranking backends ────────────────────────────────────────────────────────

// benchClaude runs Claude Rerank on the given pool slice and returns a TrialRecord.
// requestedModel is the --model argument that will be sent to the Claude CLI
// (cp.Model); the actual model the CLI ran cannot be confirmed from Rerank's
// return value because ClaudePlanner.Rerank discards the model field.
func benchClaude(ctx context.Context, cp *vibe.ClaudePlanner, description string, pool []PoolSong, poolSize, repeat int, queryLabel string) TrialRecord {
	candidates := make([]vibe.Candidate, len(pool))
	for i, s := range pool {
		candidates[i] = vibe.Candidate{Artist: s.Artist, Title: s.Title, Album: s.Album}
	}

	callCtx, cancel := context.WithTimeout(ctx, claudeCallTimeout)
	defer cancel()

	start := time.Now()
	idx, err := cp.Rerank(callCtx, description, candidates, resultCap)
	elapsed := ms(time.Since(start))

	rec := TrialRecord{
		Timestamp:    start,
		QueryLabel:   queryLabel,
		PoolSize:     poolSize,
		ActualPool:   len(pool),
		Variant:      "claude",
		Repeat:       repeat,
		ElapsedMS:    elapsed,
		ClaudeModel:  cp.Model, // requested model; actual model unconfirmed (Rerank discards it)
		ClaudeEffort: cp.Effort,
	}
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	rec.Indices = idx
	rec.Metrics = calcMetrics(pool, idx, findQueryDef(queryLabel).RefArtists)
	return rec
}

// benchJevFull sends the full pool in one Jev request and returns a TrialRecord.
func benchJevFull(ctx context.Context, apiKey, description string, pool []PoolSong, poolSize, repeat int, queryLabel string) TrialRecord {
	candidates := poolToCandidates(pool)
	reqBody := buildJevRequest(description, candidates)

	start := time.Now()
	result := callJev(ctx, apiKey, reqBody)
	elapsed := ms(time.Since(start))

	rec := TrialRecord{
		Timestamp:  start,
		QueryLabel: queryLabel,
		PoolSize:   poolSize,
		ActualPool: len(pool),
		Variant:    "jev-full",
		Repeat:     repeat,
		ElapsedMS:  elapsed,
		Censored:   result.censored,
		HTTPStatus: result.httpStatus,
	}
	ph := result.phase
	rec.Phase = &ph

	if result.err != nil {
		rec.Error = result.err.Error()
		return rec
	}

	rec.ModelUsed = result.resp.Model
	rec.InputTokens = result.resp.Usage.InputTokens
	rec.OutputTokens = result.resp.Usage.OutputTokens

	scores, err := parseJevScores(result.resp, len(pool))
	if err != nil {
		rec.Error = fmt.Sprintf("parse scores: %v", err)
		return rec
	}
	idx := scoresToRanking(scores, resultCap)
	rec.Indices = idx
	rec.Metrics = calcMetrics(pool, idx, findQueryDef(queryLabel).RefArtists)
	return rec
}

// benchJevChunked splits the pool into chunks of chunkSize, scores each chunk
// (max maxConcurrentChunks at a time), merges scores, and returns a TrialRecord.
func benchJevChunked(ctx context.Context, apiKey, description string, pool []PoolSong, poolSize, repeat int, queryLabel string) TrialRecord {
	numChunks := (len(pool) + chunkSize - 1) / chunkSize

	type chunkOut struct {
		idx    int
		scores []float64
		offset int
		cr     ChunkRecord
	}

	results := make([]chunkOut, numChunks)
	for i := range numChunks {
		results[i] = chunkOut{idx: i}
	}

	sem := make(chan struct{}, maxConcurrentChunks)
	var wg sync.WaitGroup
	var mu sync.Mutex

	trialStart := time.Now()
	for ci := range numChunks {
		wg.Add(1)
		go func(ci int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			offset := ci * chunkSize
			end := min(offset+chunkSize, len(pool))
			chunk := pool[offset:end]
			candidates := poolToCandidates(chunk)
			reqBody := buildJevRequest(description, candidates)

			chunkStart := time.Now()
			cr := ChunkRecord{
				ChunkIndex: ci,
				Offset:     offset,
				Size:       len(chunk),
			}

			callRes := callJev(ctx, apiKey, reqBody)
			cr.ElapsedMS = ms(time.Since(chunkStart))
			cr.Censored = callRes.censored
			cr.HTTPStatus = callRes.httpStatus
			ph := callRes.phase
			cr.Phase = &ph

			if callRes.err != nil {
				cr.Error = callRes.err.Error()
				mu.Lock()
				results[ci].cr = cr
				mu.Unlock()
				return
			}

			cr.ModelUsed = callRes.resp.Model
			cr.InputTokens = callRes.resp.Usage.InputTokens
			cr.OutputTokens = callRes.resp.Usage.OutputTokens

			scores, err := parseJevScores(callRes.resp, len(chunk))
			if err != nil {
				cr.Error = fmt.Sprintf("parse scores: %v", err)
				mu.Lock()
				results[ci].cr = cr
				mu.Unlock()
				return
			}

			mu.Lock()
			results[ci] = chunkOut{idx: ci, scores: scores, offset: offset, cr: cr}
			mu.Unlock()
		}(ci)
	}
	wg.Wait()
	wallMS := ms(time.Since(trialStart))

	rec := TrialRecord{
		Timestamp:  trialStart,
		QueryLabel: queryLabel,
		PoolSize:   poolSize,
		ActualPool: len(pool),
		Variant:    "jev-chunked",
		Repeat:     repeat,
		ElapsedMS:  wallMS,
	}

	// Collect chunk records and check for errors.
	chunks := make([]ChunkRecord, numChunks)
	var sumChunkMS float64
	var chunkErrors []string
	for _, r := range results {
		chunks[r.idx] = r.cr
		sumChunkMS += r.cr.ElapsedMS
		if r.cr.Error != "" {
			chunkErrors = append(chunkErrors, fmt.Sprintf("chunk %d: %s", r.idx, r.cr.Error))
		}
		if r.cr.Censored {
			rec.Censored = true
		}
	}
	rec.Chunks = chunks
	rec.SumChunkMS = sumChunkMS

	if len(chunkErrors) > 0 {
		rec.Error = strings.Join(chunkErrors, "; ")
		return rec
	}

	// Merge chunk scores to a global ranking.
	var (
		allScores  [][]float64
		allOffsets []int
	)
	for _, r := range results {
		allScores = append(allScores, r.scores)
		allOffsets = append(allOffsets, r.offset)
	}
	idx := mergeChunks(allScores, allOffsets, resultCap)
	rec.Indices = idx
	rec.Metrics = calcMetrics(pool, idx, findQueryDef(queryLabel).RefArtists)
	return rec
}

// ─── quality metrics ─────────────────────────────────────────────────────────

// normField lower-cases s and normalises punctuation for deduplication,
// matching canonicalTrackKey in internal/tui/model.go.
func normField(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case '\u2019', '\u2018', '\u02bc', '\u0060', '\u00b4':
			b.WriteByte('\'')
		case '\u2013', '\u2014', '\u2012', '\u2212':
			b.WriteByte('-')
		case '\u00a0', '\u202f', '\u2009', '\u2008':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// canonicalKey returns a dedup key for (artist, title).
func canonicalKey(artist, title string) string {
	return normField(artist) + "||" + normField(title)
}

// calcMetrics computes quality metrics for a set of chosen indices.
// refArtists should be lowercase.
func calcMetrics(pool []PoolSong, indices []int, refArtists []string) TrialMetrics {
	m := TrialMetrics{ChosenCount: len(indices)}
	artistSet := map[string]bool{}
	albumSet := map[string]bool{}
	dupKeys := map[string]bool{}
	for _, idx := range indices {
		if idx < 0 || idx >= len(pool) {
			continue
		}
		s := pool[idx]
		key := canonicalKey(s.Artist, s.Title)
		if dupKeys[key] {
			m.DuplicateCount++
		}
		dupKeys[key] = true
		na := normField(s.Artist)
		artistSet[na] = true
		if s.Album != "" {
			albumSet[normField(s.Album)] = true
		}
		// Reference-artist check (case-insensitive).
		for _, ref := range refArtists {
			if strings.Contains(na, ref) {
				m.RefArtistCount++
				break
			}
		}
	}
	m.UniqueArtists = len(artistSet)
	m.UniqueAlbums = len(albumSet)
	if m.ChosenCount > 0 {
		m.RefArtistFrac = float64(m.RefArtistCount) / float64(m.ChosenCount)
	}
	return m
}

// ─── pool building ───────────────────────────────────────────────────────────

// interleaveTracks is a local copy of the production function (tui/model.go)
// so we don't import the TUI package (which would pull in webkit2gtk).
func interleaveTracks(perTerm [][]provider.Track, limit int) []provider.Track {
	seenKey := map[string]bool{}
	seenCatalogID := map[string]bool{}
	var merged []provider.Track
	for progress := true; progress && len(merged) < limit; {
		progress = false
		for i := range perTerm {
			for len(perTerm[i]) > 0 {
				t := perTerm[i][0]
				perTerm[i] = perTerm[i][1:]
				key := canonicalKey(t.Artist, t.Title)
				catID := t.CatalogID
				if catID == "" && !strings.HasPrefix(t.ID, "i.") {
					catID = t.ID
				}
				if seenKey[key] || (catID != "" && seenCatalogID[catID]) {
					continue
				}
				seenKey[key] = true
				if catID != "" {
					seenCatalogID[catID] = true
				}
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

// tracksToPool converts provider.Track slices to a PoolFile (sanitized).
func tracksToPool(queryLabel, queryText, planModel string, planTerms []string, tracks []provider.Track) PoolFile {
	songs := make([]PoolSong, len(tracks))
	for i, t := range tracks {
		songs[i] = PoolSong{Index: i, Artist: t.Artist, Title: t.Title, Album: t.Album}
	}
	return PoolFile{
		CreatedAt:  time.Now().UTC(),
		QueryLabel: queryLabel,
		QueryText:  queryText,
		PlanModel:  planModel,
		PlanTerms:  planTerms,
		SongCount:  len(songs),
		Songs:      songs,
	}
}

// loadPoolIfExists loads a saved pool from disk without touching the network.
// Returns the pool and true if the file exists and is valid; false otherwise.
func loadPoolIfExists(outDir, label string) (PoolFile, bool) {
	poolPath := filepath.Join(outDir, "pool-"+label+".json")
	data, err := os.ReadFile(poolPath)
	if err != nil {
		return PoolFile{}, false
	}
	var pf PoolFile
	if err := json.Unmarshal(data, &pf); err != nil || len(pf.Songs) == 0 {
		return PoolFile{}, false
	}
	return pf, true
}

// fetchPool generates a plan with Claude, searches Apple Music for each term
// (bounded parallelism), interleaves and deduplicates results, and returns
// the immutable pool. If a saved pool file exists, it is loaded instead.
func fetchPool(ctx context.Context, qd queryDef, cp *vibe.ClaudePlanner, ap *appleProvider.AppleProvider, outDir string) (PoolFile, error) {
	// Load existing pool if available (replay mode — no network calls).
	if pf, ok := loadPoolIfExists(outDir, qd.Label); ok {
		log.Printf("[%s] loaded pool from disk (%d songs)", qd.Label, pf.SongCount)
		return pf, nil
	}

	// Generate plan (1 Claude call).
	log.Printf("[%s] generating plan with Claude...", qd.Label)
	planStart := time.Now()
	plan, err := cp.Plan(ctx, qd.Text)
	if err != nil {
		return PoolFile{}, fmt.Errorf("plan for %q: %w", qd.Label, err)
	}
	log.Printf("[%s] plan in %.1fs: %d terms (%s)", qd.Label, time.Since(planStart).Seconds(), len(plan.Queries), plan.Model)
	for i, q := range plan.Queries {
		log.Printf("[%s]   term %d: %q", qd.Label, i+1, q)
	}

	// Search Apple Music for each plan term with bounded parallelism.
	perTerm := make([][]provider.Track, len(plan.Queries))
	var warnMu sync.Mutex
	var warnings []string
	sem := make(chan struct{}, appleParallelism)
	var wg sync.WaitGroup

	for i, q := range plan.Queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			searchCtx, cancel := context.WithTimeout(ctx, appleSearchTimeout)
			defer cancel()

			log.Printf("[%s] apple search: %q", qd.Label, q)
			res, err := ap.Search(searchCtx, q)
			if err != nil {
				warnMu.Lock()
				warnings = append(warnings, fmt.Sprintf("term %q: %v", q, err))
				warnMu.Unlock()
				return
			}
			warnMu.Lock()
			perTerm[i] = res.Tracks
			warnMu.Unlock()
		}(i, q)
	}
	wg.Wait()

	for _, w := range warnings {
		log.Printf("[%s] WARNING: %s", qd.Label, w)
	}

	tracks := interleaveTracks(perTerm, maxPool)
	if len(tracks) == 0 {
		return PoolFile{}, fmt.Errorf("[%s] no tracks returned from Apple search; check Apple credentials", qd.Label)
	}

	log.Printf("[%s] pool: %d unique songs after interleave+dedup (target %d)", qd.Label, len(tracks), maxPool)

	pf := tracksToPool(qd.Label, qd.Text, plan.Model, plan.Queries, tracks)

	// Save pool JSON (sanitized: no IDs, URLs, credentials).
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return PoolFile{}, fmt.Errorf("marshal pool: %w", err)
	}
	savePath := filepath.Join(outDir, "pool-"+qd.Label+".json")
	if err := os.WriteFile(savePath, data, 0o644); err != nil {
		log.Printf("[%s] WARNING: could not save pool: %v", qd.Label, err)
	} else {
		log.Printf("[%s] saved pool to %s", qd.Label, savePath)
	}

	return pf, nil
}

// ─── trial runner ────────────────────────────────────────────────────────────

// trialSpec describes one ranking trial.
type trialSpec struct {
	queryLabel string
	poolSize   int // 40 or 80
	variant    string
	repeat     int
}

// buildCorrectedClaudeSpecs returns the 8 Claude-only trial specs for the
// corrected benchmark (replay mode). Repeats 1 and 2 run in the same A→A
// order because there is only one variant; the two repeats provide an
// estimate of within-session latency variance.
func buildCorrectedClaudeSpecs(queries []queryDef, sizes []int) []trialSpec {
	var specs []trialSpec
	for _, q := range queries {
		for _, sz := range sizes {
			specs = append(specs,
				trialSpec{q.Label, sz, "claude", 1},
				trialSpec{q.Label, sz, "claude", 2},
			)
		}
	}
	return specs
}

// buildTrialSpecs returns the 24 ordered trial specs in balanced repeat order.
// For each (query, poolSize), the order is:
//
//	repeat 1: claude → jev-full → jev-chunked
//	repeat 2: jev-chunked → jev-full → claude
//
// This balances warmup/cache effects across variants.
func buildTrialSpecs(queries []queryDef, sizes []int) []trialSpec {
	var specs []trialSpec
	for _, q := range queries {
		for _, sz := range sizes {
			// Repeat 1: A B C
			specs = append(specs,
				trialSpec{q.Label, sz, "claude", 1},
				trialSpec{q.Label, sz, "jev-full", 1},
				trialSpec{q.Label, sz, "jev-chunked", 1},
			)
			// Repeat 2: C B A
			specs = append(specs,
				trialSpec{q.Label, sz, "jev-chunked", 2},
				trialSpec{q.Label, sz, "jev-full", 2},
				trialSpec{q.Label, sz, "claude", 2},
			)
		}
	}
	return specs
}

// poolSlice returns a subpool of at most n songs.
func poolSlice(pf PoolFile, n int) []PoolSong {
	if n >= len(pf.Songs) {
		return pf.Songs
	}
	return pf.Songs[:n]
}

// effectiveClaudeModel applies the same defaulting logic as vibe.NewPlanner:
// an empty cfgModel maps to vibe.DefaultClaudeModel ("claude-fable-5-1"),
// "default" or "cli" passes no --model flag (CLI chooses), anything else
// is used verbatim. The return value is what gets passed to ClaudePlanner.Model.
func effectiveClaudeModel(cfgModel string) string {
	switch strings.ToLower(strings.TrimSpace(cfgModel)) {
	case "":
		return vibe.DefaultClaudeModel
	case "default", "cli":
		return ""
	default:
		return cfgModel
	}
}

// findQueryDef returns the queryDef for a given label.
func findQueryDef(label string) queryDef {
	for _, q := range benchmarkQueries {
		if q.Label == label {
			return q
		}
	}
	return queryDef{}
}

// ─── output ──────────────────────────────────────────────────────────────────

// appendJSONL appends one JSON record (newline-delimited) to a file.
func appendJSONL(path string, rec TrialRecord) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(rec)
}

// generateMarkdown reads all TrialRecords and writes a summary analysis.
func generateMarkdown(path, jsonlPath string, pools map[string]PoolFile) error {
	// Read all records from JSONL.
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return fmt.Errorf("read jsonl: %w", err)
	}
	var records []TrialRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r TrialRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		records = append(records, r)
	}

	var sb strings.Builder
	sb.WriteString("# Jev Ranking Benchmark Analysis\n\n")
	sb.WriteString(fmt.Sprintf("Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("## Context\n\n")
	sb.WriteString("This benchmark was prompted by a recorded observation (2026-09-21 manual test, records 39–40) where Jev ranking took **22.3 s** versus Claude ranking taking **7.4 s** for a Pearl Jam query with ~40 candidates.\n\n")
	sb.WriteString("The goal is to isolate ranking latency from upstream planning and Apple search variance by reusing an identical candidate pool for all backends.\n\n")

	// Pool summaries.
	sb.WriteString("## Pools\n\n")
	for _, qd := range benchmarkQueries {
		pf, ok := pools[qd.Label]
		if !ok {
			sb.WriteString(fmt.Sprintf("- **%s**: pool not available\n", qd.Label))
			continue
		}
		sb.WriteString(fmt.Sprintf("- **%s** (`%s`): %d songs, %d plan terms\n", qd.Label, qd.Text, pf.SongCount, len(pf.PlanTerms)))
		for i, t := range pf.PlanTerms {
			sb.WriteString(fmt.Sprintf("  - term %d: %q\n", i+1, t))
		}
	}
	sb.WriteString("\n")

	// Timing summary table.
	sb.WriteString("## Timing Summary\n\n")
	sb.WriteString("Each cell shows elapsed ms for a single trial (two repeats shown; no meaningful p-value with N=2).\n\n")

	// Group records by (query, poolSize, variant).
	type key struct {
		query, variant string
		poolSize       int
	}
	byKey := map[key][]TrialRecord{}
	for _, r := range records {
		k := key{r.QueryLabel, r.Variant, r.PoolSize}
		byKey[k] = append(byKey[k], r)
	}

	// Print table per query.
	for _, qd := range benchmarkQueries {
		sb.WriteString(fmt.Sprintf("### %s\n\n", qd.Label))
		sb.WriteString("| variant | pool | repeat 1 ms | repeat 2 ms | mean ms | error |\n")
		sb.WriteString("|---------|------|------------|------------|---------|-------|\n")

		for _, sz := range poolSizes {
			for _, v := range []string{"claude", "jev-full", "jev-chunked"} {
				k := key{qd.Label, v, sz}
				recs := byKey[k]
				// Sort by repeat.
				sort.Slice(recs, func(i, j int) bool { return recs[i].Repeat < recs[j].Repeat })

				var r1, r2 string
				var vals []float64
				var errMsg string
				for _, r := range recs {
					cel := fmt.Sprintf("%.0f", r.ElapsedMS)
					if r.Censored {
						cel += " (≥30 000, censored)"
					}
					if r.Error != "" {
						errMsg = r.Error
					}
					switch r.Repeat {
					case 1:
						r1 = cel
					case 2:
						r2 = cel
					}
					vals = append(vals, r.ElapsedMS)
				}
				var mean string
				if len(vals) == 2 {
					mean = fmt.Sprintf("%.0f", (vals[0]+vals[1])/2)
				} else if len(vals) == 1 {
					mean = fmt.Sprintf("%.0f*", vals[0])
				} else {
					mean = "-"
				}
				errCell := ""
				if errMsg != "" {
					n := len(errMsg)
					if n > 60 {
						n = 60
					}
					errCell = errMsg[:n]
				}
				sb.WriteString(fmt.Sprintf("| %s | %d | %s | %s | %s | %s |\n", v, sz, r1, r2, mean, errCell))
			}
		}
		sb.WriteString("\n")
	}

	// Jev phase breakdown.
	sb.WriteString("## Jev HTTP Phase Breakdown\n\n")
	sb.WriteString("DNS, connect, and TLS timings are captured once per new connection (0 on reuse).\n\n")
	sb.WriteString("| query | pool | variant | repeat | dns ms | connect ms | tls ms | first-byte ms | body ms | total ms |\n")
	sb.WriteString("|-------|------|---------|--------|--------|------------|--------|---------------|---------|----------|\n")

	for _, r := range records {
		if r.Variant == "claude" {
			continue
		}
		if r.Phase != nil {
			p := r.Phase
			sb.WriteString(fmt.Sprintf("| %s | %d | %s | %d | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f |\n",
				r.QueryLabel, r.PoolSize, r.Variant, r.Repeat,
				p.DNSMS, p.ConnectMS, p.TLSMS, p.FirstByteMS, p.BodyMS, p.TotalMS))
		}
		// For chunked, also list chunks.
		for _, cr := range r.Chunks {
			if cr.Phase != nil {
				p := cr.Phase
				sb.WriteString(fmt.Sprintf("| %s | %d | jev-chunked[%d@%d] | %d | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f |\n",
					r.QueryLabel, r.PoolSize, cr.ChunkIndex, cr.Offset, r.Repeat,
					p.DNSMS, p.ConnectMS, p.TLSMS, p.FirstByteMS, p.BodyMS, p.TotalMS))
			}
		}
	}
	sb.WriteString("\n")

	// Token usage.
	sb.WriteString("## Jev Token Usage\n\n")
	sb.WriteString("| query | pool | variant | repeat | model | input tokens | output tokens |\n")
	sb.WriteString("|-------|------|---------|--------|-------|-------------|---------------|\n")
	for _, r := range records {
		if r.Variant == "jev-full" && r.ModelUsed != "" {
			sb.WriteString(fmt.Sprintf("| %s | %d | jev-full | %d | %s | %d | %d |\n",
				r.QueryLabel, r.PoolSize, r.Repeat, r.ModelUsed, r.InputTokens, r.OutputTokens))
		}
		if r.Variant == "jev-chunked" {
			for _, cr := range r.Chunks {
				if cr.ModelUsed != "" {
					sb.WriteString(fmt.Sprintf("| %s | %d | chunk %d (offset %d) | %d | %s | %d | %d |\n",
						r.QueryLabel, r.PoolSize, cr.ChunkIndex, cr.Offset, r.Repeat, cr.ModelUsed, cr.InputTokens, cr.OutputTokens))
				}
			}
		}
	}
	sb.WriteString("\n")

	// Quality comparison.
	sb.WriteString("## Quality Comparison\n\n")
	sb.WriteString("Ref-artist fraction measures how many chosen songs are by the query's reference artist (lower is better: the ranker found alternatives rather than returning the artist itself).\n\n")
	sb.WriteString("| query | pool | variant | repeat | chosen | artists | albums | dup | ref-artist frac |\n")
	sb.WriteString("|-------|------|---------|--------|--------|---------|--------|-----|----------------|\n")
	for _, r := range records {
		if r.Error != "" {
			continue
		}
		m := r.Metrics
		sb.WriteString(fmt.Sprintf("| %s | %d | %s | %d | %d | %d | %d | %d | %.2f |\n",
			r.QueryLabel, r.PoolSize, r.Variant, r.Repeat,
			m.ChosenCount, m.UniqueArtists, m.UniqueAlbums, m.DuplicateCount, m.RefArtistFrac))
	}
	sb.WriteString("\n")

	// Discussion.
	sb.WriteString("## Discussion\n\n")
	sb.WriteString("### Latency hypothesis\n\n")
	sb.WriteString("The 22.3 s Jev ranking in the uncontrolled observation (records 39–40) covered one HTTP request for all pool candidates. ")
	sb.WriteString("Two likely causes are: (a) a large batched payload (40 questions in one request inflates input tokens and inference cost), ")
	sb.WriteString("and (b) stochastic service latency (the Jev endpoint is shared infrastructure with no SLA guarantee). ")
	sb.WriteString("The controlled benchmark provides the first-byte timing to separate network+queue+inference from response transfer, ")
	sb.WriteString("and the token counts to estimate payload size differences.\n\n")
	sb.WriteString("With the chunked variant (10 candidates per request, 2 concurrent), inference per-request is reduced but wall time depends on whether concurrency compensates.\n\n")
	sb.WriteString("### Uncertainty\n\n")
	sb.WriteString("Only two repeats were collected. With N=2, no standard error or p-value is meaningful. ")
	sb.WriteString("Observed differences reflect the specific service state during the benchmark run. ")
	sb.WriteString("Repeat variation should be inspected manually: if the two repeats differ by more than 20%, ")
	sb.WriteString("the estimate is unreliable. Censored trials (≥30 s) are recorded as-is and included in averages without imputation.\n\n")
	sb.WriteString("### Quality note\n\n")
	sb.WriteString("Jev scoring reflects `description` + metadata (artist/title/album). ")
	sb.WriteString("Year constraints and instrumentation cannot be verified from Apple catalog metadata alone. ")
	sb.WriteString("Results labeled as recent-year or from a specific era are **unverified** with respect to actual release year. ")
	sb.WriteString("Ref-artist fraction is a proxy for interpretation failure, not a ground-truth quality measure.\n")

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// ─── corrected-claude mode ───────────────────────────────────────────────────

// runCorrectedClaude runs 8 Claude reranking trials using:
//   - Saved immutable pool files (replay; no plan calls, no Apple search calls)
//   - ClaudePlanner with the same effective model the production app uses:
//     effectiveClaudeModel(cfg.VibeModel) which defaults to vibe.DefaultClaudeModel
//     ("claude-fable-5-1") when cfg.VibeModel is empty.
//
// Results are written to corrected-claude-results.jsonl. No Jev API calls are
// made. The actual model run by the Claude CLI is not returned by Rerank; only
// the requested model string is recorded.
func runCorrectedClaude(ctx context.Context, cfg *config.Config, outDir string) error {
	effModel := effectiveClaudeModel(cfg.VibeModel)
	effEffort := cfg.VibeEffort
	cp := &vibe.ClaudePlanner{
		Model:  effModel,
		Effort: effEffort,
	}
	if !cp.Available() {
		return fmt.Errorf("claude CLI not found on PATH")
	}
	log.Printf("corrected run: requested model=%q effort=%q", effModel, effEffort)
	log.Printf("note: actual model used by Claude CLI cannot be confirmed from Rerank return value")

	// Load saved pools — no network calls.
	pools := map[string]PoolFile{}
	for _, qd := range benchmarkQueries {
		pf, ok := loadPoolIfExists(outDir, qd.Label)
		if !ok {
			return fmt.Errorf("pool file missing for %q; run full benchmark first", qd.Label)
		}
		log.Printf("[%s] loaded pool from disk (%d songs)", qd.Label, pf.SongCount)
		pools[qd.Label] = pf
	}

	jsonlPath := filepath.Join(outDir, "corrected-claude-results.jsonl")
	// Truncate for a fresh corrected run.
	if err := os.WriteFile(jsonlPath, nil, 0o644); err != nil {
		return fmt.Errorf("reset corrected-claude-results.jsonl: %w", err)
	}

	specs := buildCorrectedClaudeSpecs(benchmarkQueries, poolSizes)
	log.Printf("corrected-claude trials: %d", len(specs))

	for i, sp := range specs {
		pf := pools[sp.queryLabel]
		pool := poolSlice(pf, sp.poolSize)
		qd := findQueryDef(sp.queryLabel)

		log.Printf("[%d/%d] %s pool=%d actual=%d repeat=%d",
			i+1, len(specs), sp.queryLabel, sp.poolSize, len(pool), sp.repeat)

		rec := benchClaude(ctx, cp, qd.Text, pool, sp.poolSize, sp.repeat, sp.queryLabel)
		rec.Timestamp = time.Now().UTC()

		if rec.Error != "" {
			log.Printf("  ERROR: %s", rec.Error)
		} else {
			log.Printf("  elapsed=%.0fms chosen=%d artists=%d refFrac=%.2f requested-model=%q",
				rec.ElapsedMS, rec.Metrics.ChosenCount, rec.Metrics.UniqueArtists,
				rec.Metrics.RefArtistFrac, rec.ClaudeModel)
		}
		if err := appendJSONL(jsonlPath, rec); err != nil {
			log.Printf("  WARNING: save record: %v", err)
		}
	}

	// Write concise corrected report.
	reportPath := filepath.Join(outDir, "corrected-claude-report.md")
	if err := generateCorrectedReport(reportPath, jsonlPath, pools, effModel, effEffort); err != nil {
		log.Printf("WARNING: generate corrected report: %v", err)
	} else {
		log.Printf("corrected report written to %s", reportPath)
	}
	return nil
}

// generateCorrectedReport writes a concise markdown report for the
// corrected-claude runs. It does NOT modify analysis.md.
func generateCorrectedReport(path, jsonlPath string, pools map[string]PoolFile, requestedModel, requestedEffort string) error {
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return fmt.Errorf("read jsonl: %w", err)
	}
	var records []TrialRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r TrialRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		records = append(records, r)
	}

	var sb strings.Builder
	sb.WriteString("# Corrected Claude Rerank Benchmark\n\n")
	sb.WriteString(fmt.Sprintf("Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339)))

	sb.WriteString("## Confound in Original Benchmark (results.jsonl)\n\n")
	sb.WriteString("The original benchmark constructed `ClaudePlanner` with `cfg.VibeModel` (empty) and `cfg.VibeEffort` (empty) directly, \n")
	sb.WriteString("bypassing the defaulting logic in `vibe.NewPlanner`. When `ClaudePlanner.Model` is empty, no `--model` flag \n")
	sb.WriteString("is passed to the Claude CLI, which picks its own default model. The planning calls in `results.jsonl` reported \n")
	sb.WriteString("the CLI chose **sonnet-5**. The rerank calls are unconfirmed (the model string is not returned by `Rerank()`). \n")
	sb.WriteString("The production app uses `vibe.NewPlanner(\"claude\", cfg.VibeModel, cfg.VibeEffort)`, which defaults an empty \n")
	sb.WriteString("model to `vibe.DefaultClaudeModel` = `\"claude-fable-5-1\"` and passes `--model claude-fable-5-1` to the CLI. \n")
	sb.WriteString("This difference means the original Claude rerank timings used an uncontrolled CLI-default model, \n")
	sb.WriteString("not the configured production model.\n\n")

	sb.WriteString("## Corrected Benchmark Setup\n\n")
	sb.WriteString(fmt.Sprintf("- Requested model: `%s` (same as `vibe.DefaultClaudeModel`)\n", requestedModel))
	if requestedEffort == "" {
		sb.WriteString("- Effort: not set (CLI uses its own default)\n")
	} else {
		sb.WriteString(fmt.Sprintf("- Effort: `%s`\n", requestedEffort))
	}
	sb.WriteString("- Pools: loaded from saved `pool-*.json` files (immutable; no Apple search or plan calls)\n")
	sb.WriteString("- Trials: 8 total (2 queries × 2 pool sizes × 2 repeats, Claude only)\n")
	sb.WriteString("- Context deadline: 45 s per Claude call\n")
	sb.WriteString("- No Jev API calls\n")
	sb.WriteString("- **Note:** the actual model used by the Claude CLI cannot be confirmed from the `Rerank()` return value.\n")
	sb.WriteString(" The requested model string (`cp.Model`) is logged as `claude_model` in the JSONL records.\n\n")

	// Pool summaries.
	sb.WriteString("## Pools (unchanged from original run)\n\n")
	for _, qd := range benchmarkQueries {
		pf, ok := pools[qd.Label]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %d songs, plan model %s\n", qd.Label, pf.SongCount, pf.PlanModel))
	}
	sb.WriteString("\n")

	// Results table.
	sb.WriteString("## Results\n\n")
	sb.WriteString("Elapsed is wall time for the `claude -p` subprocess call (includes spawn, inference, JSON parse). \n")
	sb.WriteString("With N=2 repeats no standard error or p-value is meaningful; repeat variation is listed to show within-session stability.\n\n")
	sb.WriteString("| query | pool | repeat 1 ms | repeat 2 ms | mean ms | chosen | artists | albums | ref-artist frac | error |\n")
	sb.WriteString("|-------|------|------------|------------|---------|--------|---------|--------|----------------|-------|\n")

	type groupKey struct {
		query    string
		poolSize int
	}
	groups := map[groupKey][]TrialRecord{}
	for _, r := range records {
		gk := groupKey{r.QueryLabel, r.PoolSize}
		groups[gk] = append(groups[gk], r)
	}

	for _, qd := range benchmarkQueries {
		for _, sz := range poolSizes {
			gk := groupKey{qd.Label, sz}
			recs := groups[gk]
			sort.Slice(recs, func(i, j int) bool { return recs[i].Repeat < recs[j].Repeat })

			var r1ms, r2ms, meanStr string
			var r1m, r2m TrialMetrics
			var errStr string
			var vals []float64
			for _, r := range recs {
				cel := fmt.Sprintf("%.0f", r.ElapsedMS)
				if r.Censored {
					cel += " (censored)"
				}
				if r.Error != "" && errStr == "" {
					n := len(r.Error)
					if n > 50 {
						n = 50
					}
					errStr = r.Error[:n]
				}
				switch r.Repeat {
				case 1:
					r1ms = cel
					r1m = r.Metrics
				case 2:
					r2ms = cel
					r2m = r.Metrics
				}
				vals = append(vals, r.ElapsedMS)
			}
			switch len(vals) {
			case 2:
				meanStr = fmt.Sprintf("%.0f", (vals[0]+vals[1])/2)
			case 1:
				meanStr = fmt.Sprintf("%.0f*", vals[0])
			default:
				meanStr = "-"
			}
			// Show repeat-1 metrics; repeat-2 in next row if they differ.
			m := r1m
			sb.WriteString(fmt.Sprintf("| %s | %d | %s | %s | %s | %d | %d | %d | %.2f | %s |\n",
				qd.Label, sz, r1ms, r2ms, meanStr,
				m.ChosenCount, m.UniqueArtists, m.UniqueAlbums, m.RefArtistFrac, errStr))
			// If repeat-2 metrics differ materially from repeat-1, show them.
			if r2m.UniqueArtists != r1m.UniqueArtists || r2m.RefArtistFrac != r1m.RefArtistFrac {
				sb.WriteString(fmt.Sprintf("|  | | | | | %d | %d | %d | %.2f | (repeat 2 metrics) |\n",
					r2m.ChosenCount, r2m.UniqueArtists, r2m.UniqueAlbums, r2m.RefArtistFrac))
			}
		}
	}
	sb.WriteString("\n")

	sb.WriteString("## Comparison with Original CLI-default Runs\n\n")
	sb.WriteString("The original `results.jsonl` Claude trials used an uncontrolled CLI-default model (sonnet-5 for plan calls; \n")
	sb.WriteString("rerank model unconfirmed). The corrected trials use `--model claude-fable-5-1` (requested). \n")
	sb.WriteString("Mean elapsed times from `results.jsonl` for reference (same pools, different model):\n\n")
	sb.WriteString("| query | pool | original mean ms (model unconfirmed) |\n")
	sb.WriteString("|-------|------|---------------------------------------|\n")
	// Hard-coded from the original JSONL run; do not re-read that file to avoid
	// conflating the two datasets.
	origMeans := map[string]string{
		"pearl-jam|40":           "3726",
		"pearl-jam|80":           "3610",
		"stone-temple-pilots|40": "3813",
		"stone-temple-pilots|80": "3391",
	}
	for _, qd := range benchmarkQueries {
		for _, sz := range poolSizes {
			key := fmt.Sprintf("%s|%d", qd.Label, sz)
			sb.WriteString(fmt.Sprintf("| %s | %d | %s |\n", qd.Label, sz, origMeans[key]))
		}
	}
	sb.WriteString("\n")

	sb.WriteString("## Limitations\n\n")
	sb.WriteString("- N=2 repeats per condition: no meaningful standard error or statistical test.\n")
	sb.WriteString("- Requested model logged; actual model used by the CLI not confirmed.\n")
	sb.WriteString("- Elapsed time includes Claude CLI subprocess spawn, API round-trip, and JSON parsing; \n")
	sb.WriteString(" these sub-components are not separately measured here.\n")
	sb.WriteString("- Both repeats ran in the same session; cold-start effects for the first call \n")
	sb.WriteString(" cannot be separated from inference variance with this design.\n")
	sb.WriteString("- Quality metrics (artist diversity, ref-artist fraction) are computed from Apple catalog \n")
	sb.WriteString(" metadata. Year constraints are unverified.\n")

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	log.SetFlags(log.Ltime)

	// Detect --corrected-claude mode before doing anything else.
	correctedMode := false
	for _, arg := range os.Args[1:] {
		if arg == "--corrected-claude" {
			correctedMode = true
		}
	}

	if correctedMode {
		log.Println("=== jev-bench: corrected-claude mode ===")
		cfg, err := config.Load("")
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		cp := &vibe.ClaudePlanner{Model: effectiveClaudeModel(cfg.VibeModel), Effort: cfg.VibeEffort}
		if !cp.Available() {
			log.Fatal("claude CLI not found on PATH")
		}
		outDir := "docs/bench-output"
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			log.Fatalf("create output dir: %v", err)
		}
		if err := runCorrectedClaude(context.Background(), cfg, outDir); err != nil {
			log.Fatalf("corrected-claude run failed: %v", err)
		}
		log.Println("=== corrected-claude complete ===")
		return
	}

	log.Println("=== jev-bench: controlled ranking benchmark ===")

	// Read API key from environment (loaded via bash -ic before invocation).
	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		log.Fatal("TYPESAFE_API_KEY is not set; run with: bash -ic 'go run ./scripts/jev-bench/'")
	}

	// Load config (Apple credentials from ~/.config/vibez/config.json).
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if !appleProvider.New(cfg).IsAuthenticated() {
		log.Fatal("Apple Music credentials not set; cannot fetch candidates. Check ~/.config/vibez/config.json.")
	}

	ap := appleProvider.New(cfg)
	// Use the same model-defaulting logic as the production app (vibe.NewPlanner).
	// Note: the original benchmark used &vibe.ClaudePlanner{Model: cfg.VibeModel} directly,
	// which with an empty VibeModel passed no --model flag (CLI chose sonnet-5 for plan calls;
	// rerank model was unconfirmed). This field is kept as-is for the original full-benchmark
	// mode so results.jsonl is reproducible; corrected-claude mode applies effectiveClaudeModel.
	cp := &vibe.ClaudePlanner{
		Model:  cfg.VibeModel,
		Effort: cfg.VibeEffort,
	}
	if !cp.Available() {
		log.Fatal("claude CLI not found on PATH; required for planning and Claude rerank trials")
	}
	log.Printf("Claude model=%q effort=%q (empty = CLI default, not DefaultClaudeModel)", cp.Model, cp.Effort)

	// Output directory.
	outDir := "docs/bench-output"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	jsonlPath := filepath.Join(outDir, "results.jsonl")
	mdPath := filepath.Join(outDir, "analysis.md")

	// Truncate JSONL for a fresh run.
	if err := os.WriteFile(jsonlPath, nil, 0o644); err != nil {
		log.Fatalf("reset results.jsonl: %v", err)
	}

	ctx := context.Background()

	// Phase 1: fetch pools (1 Claude plan call per query).
	log.Println("\n--- Phase 1: pool fetching ---")
	pools := map[string]PoolFile{}
	for _, qd := range benchmarkQueries {
		pf, err := fetchPool(ctx, qd, cp, ap, outDir)
		if err != nil {
			log.Fatalf("fetch pool for %q: %v", qd.Label, err)
		}
		pools[qd.Label] = pf
	}

	// Phase 2: run ranking trials.
	log.Println("\n--- Phase 2: ranking trials ---")
	specs := buildTrialSpecs(benchmarkQueries, poolSizes)
	log.Printf("total trials: %d", len(specs))

	// Count planned Jev HTTP requests for audit.
	jevRequests := 0
	for _, sp := range specs {
		pf := pools[sp.queryLabel]
		actualPool := min(sp.poolSize, len(pf.Songs))
		switch sp.variant {
		case "jev-full":
			jevRequests++
		case "jev-chunked":
			jevRequests += (actualPool + chunkSize - 1) / chunkSize
		}
	}
	log.Printf("planned Jev HTTP requests: %d (cap %d)", jevRequests, 120)
	if jevRequests > 120 {
		log.Fatalf("ABORT: planned Jev requests %d exceeds cap 120", jevRequests)
	}

	for i, sp := range specs {
		pf := pools[sp.queryLabel]
		pool := poolSlice(pf, sp.poolSize)
		qd := findQueryDef(sp.queryLabel)

		log.Printf("[%d/%d] %s pool=%d actual=%d variant=%s repeat=%d",
			i+1, len(specs), sp.queryLabel, sp.poolSize, len(pool), sp.variant, sp.repeat)

		var rec TrialRecord
		switch sp.variant {
		case "claude":
			rec = benchClaude(ctx, cp, qd.Text, pool, sp.poolSize, sp.repeat, sp.queryLabel)
		case "jev-full":
			rec = benchJevFull(ctx, apiKey, qd.Text, pool, sp.poolSize, sp.repeat, sp.queryLabel)
		case "jev-chunked":
			rec = benchJevChunked(ctx, apiKey, qd.Text, pool, sp.poolSize, sp.repeat, sp.queryLabel)
		default:
			log.Fatalf("unknown variant: %s", sp.variant)
		}
		rec.Timestamp = time.Now().UTC()

		if rec.Error != "" {
			log.Printf("  ERROR: %s", rec.Error)
			// Stop on auth failure.
			if strings.Contains(rec.Error, "HTTP 401") || strings.Contains(rec.Error, "TYPESAFE_API_KEY") {
				if err := appendJSONL(jsonlPath, rec); err != nil {
					log.Printf("  WARNING: save record: %v", err)
				}
				log.Fatal("auth failure: stopping benchmark")
			}
		} else {
			log.Printf("  elapsed=%.0fms chosen=%d artists=%d refFrac=%.2f",
				rec.ElapsedMS, rec.Metrics.ChosenCount, rec.Metrics.UniqueArtists, rec.Metrics.RefArtistFrac)
		}

		if err := appendJSONL(jsonlPath, rec); err != nil {
			log.Printf("  WARNING: save record: %v", err)
		}
	}

	// Phase 3: generate analysis markdown (only if file does not already exist;
	// the parent has corrected docs/bench-output/analysis.md and we must not
	// overwrite those corrections on a re-run of the original full benchmark).
	log.Println("\n--- Phase 3: analysis ---")
	if _, statErr := os.Stat(mdPath); statErr == nil {
		log.Printf("skipping markdown generation: %s already exists (preserving parent corrections)", mdPath)
	} else {
		if err := generateMarkdown(mdPath, jsonlPath, pools); err != nil {
			log.Printf("WARNING: generate markdown: %v", err)
		} else {
			log.Printf("analysis written to %s", mdPath)
		}
	}

	log.Printf("results written to %s", jsonlPath)
	log.Println("=== benchmark complete ===")
}
