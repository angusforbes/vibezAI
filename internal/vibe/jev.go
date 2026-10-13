package vibe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"time"
)

const (
	// JevAPIEndpoint is the TypeSafe System One evaluation URL.
	JevAPIEndpoint = "https://api.typesafe.ai/v1/systemone"
	jevModel       = "jev-latest"
	jevTimeout     = 30 * time.Second
)

// JevReranker reranks music candidates using TypeSafe's Jev model via the HTTP
// API. It sends one Score question per candidate in a single batched request.
//
// The TYPESAFE_API_KEY environment variable must be set; the key is read at
// construction time and used solely in the Authorization header. It is never
// written to logs, config, or error messages.
type JevReranker struct {
	apiKey     string
	httpClient *http.Client
	endpoint   string // overrideable for tests; empty means JevAPIEndpoint
}

// NewJevReranker constructs a JevReranker from the TYPESAFE_API_KEY environment
// variable. A missing key is not an error at construction time; Rerank returns
// an explicit, user-visible error on first use so the AJ search panel shows
// the failure rather than silently falling back to another ranker.
func NewJevReranker() *JevReranker {
	return &JevReranker{
		apiKey:     os.Getenv("TYPESAFE_API_KEY"),
		httpClient: &http.Client{Timeout: jevTimeout},
	}
}

// jevScoreCriteria are the concrete ordered levels used for every per-candidate
// Score question. Level 0 is the lowest (no match); level 4 is the highest
// (perfect match). Levels describe observable properties of the song relative
// to the listener's description rather than abstract quality, so the model
// evaluates each candidate consistently.
var jevScoreCriteria = []string{
	"No match: wrong genre, wrong era, or clearly unrelated to the described vibe",
	"Weak match: loosely connected by era or genre only; missing the mood or style described",
	"Moderate match: fits the general genre or mood but not the specific feel, artist, or request",
	"Strong match: clearly fits the described mood, style, and era — a well-chosen pick",
	"Perfect match: exactly what was requested — the specific artist, song, or precise vibe described",
}

// jevState is the shared state sent to Jev alongside all candidate questions.
// Keeping the description and candidate list together lets each per-candidate
// question reference them with backtick paths without repeating the data.
type jevState struct {
	Description string         `json:"description"`
	Candidates  []jevCandidate `json:"candidates"`
}

type jevCandidate struct {
	Index  int    `json:"index"`
	Artist string `json:"artist"`
	Title  string `json:"title"`
	Album  string `json:"album,omitempty"`
}

// jevScoreQuestion builds one Score question for the candidate at idx.
// Instructions reference the candidate by its state path so Jev evaluates each
// song against the description independently. The context field preserves any
// version or edition constraints the description implies.
func jevScoreQuestion(idx int) map[string]any {
	return map[string]any{
		"type": "score",
		"instructions": map[string]any{
			"question": fmt.Sprintf(
				"How well does the song at `candidates[%d]` (`candidates[%d].artist` — `candidates[%d].title`) fit the listener's description in `description`?",
				idx, idx, idx,
			),
			"context": "Prefer original studio recordings over live versions, karaoke covers, tributes, and remixes unless the description explicitly requests them. Preserve any version, format, or edition constraints the listener describes (acoustic, instrumental, extended, etc.).",
		},
		"criteria": jevScoreCriteria,
	}
}

// jevRequest is the full HTTP POST body.
type jevRequest struct {
	State     jevState                  `json:"state"`
	Model     string                    `json:"model"`
	Questions map[string]map[string]any `json:"questions"`
}

// jevScoreAnswer is the expected answer shape for a Score question from Jev.
// Score is a pointer so that a missing or null field is caught as an error
// rather than silently defaulting to 0 (which is a valid level value).
type jevScoreAnswer struct {
	Type          string             `json:"type"`
	Score         *float64           `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// jevResponse is the top-level API response.
type jevResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
}

// questionKey returns the per-candidate question id used as a map key in both
// the request and the response. The id is not sent to the Jev model; it only
// routes answers back to the right candidate.
func questionKey(i int) string { return fmt.Sprintf("s%d", i) }

// activeEndpoint returns the endpoint to use, defaulting to JevAPIEndpoint.
func (j *JevReranker) activeEndpoint() string {
	if j.endpoint != "" {
		return j.endpoint
	}
	return JevAPIEndpoint
}

// Rerank implements vibe.Reranker using TypeSafe Jev Score questions.
//
// All candidates are evaluated in a single API request. Each candidate's score
// drives the final order: candidates are sorted descending by score with a
// stable sort (preserving pool order for ties). Unlike the Claude reranker,
// this mode does not apply an artist-diversity preference.
//
// Validation rejects non-finite or out-of-range score values and missing answer
// keys. HTTP and context errors are returned as plain errors; the API key is
// never included in error text.
// maxJevCandidates is a safety cap on the number of candidates sent in one
// request; 40 is vibePoolCap so normal usage is well below this.
const maxJevCandidates = 200

// maxJevResponseBytes limits how many bytes of the API response are read;
// a 1 MiB cap guards against unexpectedly large payloads.
const maxJevResponseBytes = 1 << 20

func (j *JevReranker) Rerank(ctx context.Context, description string, candidates []Candidate, limit int) ([]int, error) {
	if j.apiKey == "" {
		return nil, errors.New("jev: TYPESAFE_API_KEY is not set")
	}
	if len(candidates) == 0 {
		return nil, errors.New("jev: no candidates to rank")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("jev: limit must be positive, got %d", limit)
	}
	if len(candidates) > maxJevCandidates {
		return nil, fmt.Errorf("jev: too many candidates (%d, max %d)", len(candidates), maxJevCandidates)
	}

	// Build shared state and one Score question per candidate.
	state := jevState{
		Description: description,
		Candidates:  make([]jevCandidate, len(candidates)),
	}
	questions := make(map[string]map[string]any, len(candidates))
	for i, c := range candidates {
		state.Candidates[i] = jevCandidate{Index: i, Artist: c.Artist, Title: c.Title, Album: c.Album}
		questions[questionKey(i)] = jevScoreQuestion(i)
	}

	body, err := json.Marshal(jevRequest{State: state, Model: jevModel, Questions: questions})
	if err != nil {
		return nil, fmt.Errorf("jev: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.activeEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+j.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := j.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("jev: %w", ctx.Err())
		}
		return nil, fmt.Errorf("jev: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Do not read or echo the response body: it may contain sensitive
		// diagnostics, and the status code is the only safe signal here.
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil, fmt.Errorf("jev: HTTP %d", resp.StatusCode)
	}

	var result jevResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJevResponseBytes)).Decode(&result); err != nil {
		return nil, fmt.Errorf("jev: decode response: %w", err)
	}

	return j.scoreAndRank(result, candidates, limit)
}

// scoreAndRank extracts scores from the API response, validates them, sorts
// candidates by score, and applies artist diversity. Separated for testability.
func (j *JevReranker) scoreAndRank(result jevResponse, candidates []Candidate, limit int) ([]int, error) {
	type scoredCandidate struct {
		idx   int
		score float64
	}
	top := float64(len(jevScoreCriteria) - 1)
	all := make([]scoredCandidate, 0, len(candidates))
	for i := range candidates {
		key := questionKey(i)
		raw, ok := result.Answers[key]
		if !ok {
			return nil, fmt.Errorf("jev: missing answer for candidate %d", i)
		}
		var ans jevScoreAnswer
		if err := json.Unmarshal(raw, &ans); err != nil {
			return nil, fmt.Errorf("jev: decode answer %d: %w", i, err)
		}
		if ans.Type != "score" {
			return nil, fmt.Errorf("jev: answer %d has unexpected type %q, want \"score\"", i, ans.Type)
		}
		// Score is a pointer to distinguish missing/null (error) from 0 (valid level 0).
		if ans.Score == nil {
			return nil, fmt.Errorf("jev: answer %d score is missing or null", i)
		}
		score := *ans.Score
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > top+0.001 {
			return nil, fmt.Errorf("jev: answer %d score %v is not in [0, %.0f]", i, score, top)
		}
		all = append(all, scoredCandidate{idx: i, score: score})
	}

	// Sort descending by score; stable preserves pool order for equal scores.
	sort.SliceStable(all, func(a, b int) bool {
		return all[a].score > all[b].score
	})

	// Take the top limit candidates in pure score order. No artist-diversity
	// pass is applied: determining whether an artist was "requested" requires
	// semantic understanding that cannot be done reliably by inspecting the
	// description string. Jev's per-song scores already reflect the description,
	// so the ordering follows the model rather than a post-hoc heuristic.
	n := min(limit, len(all))
	if n == 0 {
		return nil, errors.New("jev: no valid picks after scoring")
	}
	idx := make([]int, n)
	for i, s := range all[:n] {
		idx[i] = s.idx
	}
	return idx, nil
}
