package vibe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// jevFixtureAnswers builds the "answers" map for a Jev fixture response.
// scores[i] is the score for candidate i; candidates beyond len(scores) get 2.0.
func jevFixtureAnswers(n int, scores []float64) map[string]any {
	answers := make(map[string]any, n)
	for i := range n {
		s := 2.0
		if i < len(scores) {
			s = scores[i]
		}
		// Use a pointer-compatible value: marshal the struct with *float64 Score.
		// We encode it inline as a map so the JSON field is always present.
		answers[questionKey(i)] = map[string]any{
			"type":          "score",
			"score":         s, // present, non-null
			"confidence":    0.8,
			"legend":        map[string]string{"0": "No match", "4": "Perfect match"},
			"probabilities": map[string]float64{"0": 0.0, "2": 1.0, "4": 0.0},
		}
	}
	return answers
}

// jevFixtureBody serialises a complete Jev response body.
func jevFixtureBody(n int, scores []float64) []byte {
	b, _ := json.Marshal(map[string]any{
		"model":   "jev-1.13.0",
		"answers": jevFixtureAnswers(n, scores),
	})
	return b
}

// newJevServer starts a test server that replies with the given status and body.
// It records the most recent Authorization header.
func newJevServer(t *testing.T, status int, body []byte) (srv *httptest.Server, lastAuth *string) {
	t.Helper()
	var auth string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

// newTestJev builds a JevReranker wired to a test server URL with a fixed key.
func newTestJev(t *testing.T, srv *httptest.Server) *JevReranker {
	t.Helper()
	return &JevReranker{
		apiKey:     "test-key-xyz",
		httpClient: &http.Client{Timeout: 5 * time.Second},
		endpoint:   srv.URL,
	}
}

// cands builds a slice of n Candidate values with sequential labels.
func cands(n int) []Candidate {
	c := make([]Candidate, n)
	for i := range n {
		c[i] = Candidate{
			Artist: fmt.Sprintf("Artist %d", i),
			Title:  fmt.Sprintf("Track %d", i),
		}
	}
	return c
}

func TestJevReranker_MissingKey(t *testing.T) {
	j := &JevReranker{apiKey: "", httpClient: &http.Client{}}
	_, err := j.Rerank(context.Background(), "chill", cands(1), 5)
	if err == nil || !strings.Contains(err.Error(), "TYPESAFE_API_KEY") {
		t.Fatalf("missing key must return a visible error, got: %v", err)
	}
}

func TestJevReranker_NoCandidates(t *testing.T) {
	j := &JevReranker{apiKey: "k", httpClient: &http.Client{}}
	_, err := j.Rerank(context.Background(), "chill", nil, 5)
	if err == nil || !strings.Contains(err.Error(), "candidates") {
		t.Fatalf("empty candidates must return an error, got: %v", err)
	}
}

func TestJevReranker_HTTP401(t *testing.T) {
	srv, _ := newJevServer(t, http.StatusUnauthorized, []byte(`{"error":"invalid key"}`))
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "upbeat", cands(2), 2)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("401 must return HTTP error, got: %v", err)
	}
	// The error must not echo the API key.
	if strings.Contains(err.Error(), "test-key-xyz") {
		t.Fatal("error message must not include the API key")
	}
}

func TestJevReranker_MalformedJSON(t *testing.T) {
	srv, _ := newJevServer(t, http.StatusOK, []byte(`not json`))
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "jazz", cands(2), 2)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed response must return decode error, got: %v", err)
	}
}

func TestJevReranker_MissingAnswer(t *testing.T) {
	// Return only one answer for two candidates.
	body, _ := json.Marshal(map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			"s0": map[string]any{"type": "score", "score": 3.0, "confidence": 0.9, "legend": map[string]string{}, "probabilities": map[string]float64{}},
			// s1 is missing
		},
	})
	srv, _ := newJevServer(t, http.StatusOK, body)
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "rock", cands(2), 2)
	if err == nil || !strings.Contains(err.Error(), "missing answer") {
		t.Fatalf("missing answer key must return an error, got: %v", err)
	}
}

func TestJevReranker_WrongAnswerType(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			"s0": map[string]any{"type": "noul", "noul": 0.9}, // wrong type
		},
	})
	srv, _ := newJevServer(t, http.StatusOK, body)
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "chill", cands(1), 1)
	if err == nil || !strings.Contains(err.Error(), "unexpected type") {
		t.Fatalf("wrong answer type must return an error, got: %v", err)
	}
}

func TestJevReranker_NonFiniteScore(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		body, _ := json.Marshal(map[string]any{
			"model": "jev-1.13.0",
			"answers": map[string]any{
				"s0": map[string]any{"type": "score", "score": bad, "confidence": 0.5, "legend": map[string]string{}, "probabilities": map[string]float64{}},
			},
		})
		srv, _ := newJevServer(t, http.StatusOK, body)
		j := newTestJev(t, srv)
		_, err := j.Rerank(context.Background(), "pop", cands(1), 1)
		if err == nil {
			t.Fatalf("non-finite score %v must return an error", bad)
		}
	}
}

func TestJevReranker_OutOfRangeScore(t *testing.T) {
	// Score above the level count is invalid.
	top := float64(len(jevScoreCriteria)-1) + 1.5
	body, _ := json.Marshal(map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			"s0": map[string]any{"type": "score", "score": top, "confidence": 0.5, "legend": map[string]string{}, "probabilities": map[string]float64{}},
		},
	})
	srv, _ := newJevServer(t, http.StatusOK, body)
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "metal", cands(1), 1)
	if err == nil || !strings.Contains(err.Error(), "not in [0") {
		t.Fatalf("out-of-range score must return an error, got: %v", err)
	}
}

func TestJevReranker_ContextCancelled(t *testing.T) {
	// Server that blocks until the client gives up.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() { close(block); srv.Close() })

	j := &JevReranker{apiKey: "k", httpClient: &http.Client{Timeout: 10 * time.Second}, endpoint: srv.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := j.Rerank(ctx, "chill", cands(1), 1)
	if err == nil {
		t.Fatal("cancelled context must return an error")
	}
}

func TestJevReranker_ValidResponse_OrdersByScore(t *testing.T) {
	// Candidates 0, 1, 2 with scores 1.0, 3.5, 2.0.
	// Expected order: 1 (3.5), 2 (2.0), 0 (1.0).
	n := 3
	scores := []float64{1.0, 3.5, 2.0}
	srv, lastAuth := newJevServer(t, http.StatusOK, jevFixtureBody(n, scores))
	j := newTestJev(t, srv)
	cs := cands(n)
	idx, err := j.Rerank(context.Background(), "jazzy", cs, n)
	if err != nil {
		t.Fatalf("valid response must not error: %v", err)
	}
	if len(idx) != n {
		t.Fatalf("want %d picks, got %d", n, len(idx))
	}
	if idx[0] != 1 || idx[1] != 2 || idx[2] != 0 {
		t.Fatalf("order should be by descending score: want [1,2,0], got %v", idx)
	}
	// The Authorization header should carry the key.
	if *lastAuth != "Bearer test-key-xyz" {
		t.Fatalf("auth header = %q, want Bearer test-key-xyz", *lastAuth)
	}
}

func TestJevReranker_LimitCaps(t *testing.T) {
	n := 5
	scores := []float64{4.0, 3.5, 3.0, 2.5, 2.0}
	srv, _ := newJevServer(t, http.StatusOK, jevFixtureBody(n, scores))
	j := newTestJev(t, srv)
	idx, err := j.Rerank(context.Background(), "soul", cands(n), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx) != 3 {
		t.Fatalf("limit=3 must return at most 3 picks, got %d", len(idx))
	}
}

func TestJevReranker_PureScoreOrdering(t *testing.T) {
	// AJ applies no artist-diversity cap: results follow Jev scores directly.
	// 5 candidates: first 4 all the same artist (descending scores), last one a different artist.
	// With limit=3 and pure score ordering we expect: 0, 1, 2 (top 3 by score).
	cs := []Candidate{
		{Artist: "BigBand", Title: "Song A"},
		{Artist: "BigBand", Title: "Song B"},
		{Artist: "BigBand", Title: "Song C"},
		{Artist: "BigBand", Title: "Song D"},
		{Artist: "OtherAct", Title: "Song E"},
	}
	scores := []float64{4.0, 3.0, 2.0, 1.0, 0.5}
	srv, _ := newJevServer(t, http.StatusOK, jevFixtureBody(len(cs), scores))
	j := newTestJev(t, srv)
	idx, err := j.Rerank(context.Background(), "big band jazz", cs, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx) != 3 {
		t.Fatalf("want 3 picks, got %d: %v", len(idx), idx)
	}
	if idx[0] != 0 || idx[1] != 1 || idx[2] != 2 {
		t.Errorf("pure score order: want [0 1 2], got %v", idx)
	}
}

func TestJevReranker_StableTieBreaking(t *testing.T) {
	// All candidates have equal scores: pool order should be preserved.
	n := 4
	scores := []float64{2.0, 2.0, 2.0, 2.0}
	srv, _ := newJevServer(t, http.StatusOK, jevFixtureBody(n, scores))
	j := newTestJev(t, srv)
	cs := cands(n)
	idx, err := j.Rerank(context.Background(), "ambient", cs, n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, got := range idx {
		if got != i {
			t.Errorf("tie at equal scores should preserve pool order: idx[%d] = %d, want %d", i, got, i)
		}
	}
}

func TestJevReranker_UniqueIndices(t *testing.T) {
	// Verify no duplicate indices are returned.
	n := 6
	scores := []float64{4.0, 4.0, 3.5, 3.5, 3.0, 2.0}
	srv, _ := newJevServer(t, http.StatusOK, jevFixtureBody(n, scores))
	j := newTestJev(t, srv)
	idx, err := j.Rerank(context.Background(), "funk", cands(n), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seen := make(map[int]bool)
	for _, i := range idx {
		if seen[i] {
			t.Fatalf("duplicate index %d in result %v", i, idx)
		}
		seen[i] = true
		if i < 0 || i >= n {
			t.Fatalf("index %d out of range [0, %d)", i, n)
		}
	}
}

func TestJevReranker_AllSameArtistPureScore(t *testing.T) {
	// With pure score ordering, all 4 candidates of the same artist are
	// returned in score order up to limit.
	cs := []Candidate{
		{Artist: "Solo", Title: "A"},
		{Artist: "Solo", Title: "B"},
		{Artist: "Solo", Title: "C"},
		{Artist: "Solo", Title: "D"},
	}
	scores := []float64{4.0, 3.0, 2.0, 1.0}
	srv, _ := newJevServer(t, http.StatusOK, jevFixtureBody(len(cs), scores))
	j := newTestJev(t, srv)
	idx, err := j.Rerank(context.Background(), "solo artist deep cut", cs, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx) != 4 {
		t.Fatalf("pure score: all 4 candidates returned, got %d: %v", len(idx), idx)
	}
	if idx[0] != 0 || idx[1] != 1 || idx[2] != 2 || idx[3] != 3 {
		t.Errorf("want [0 1 2 3], got %v", idx)
	}
}

func TestJevReranker_AuthHeaderCarriesKey(t *testing.T) {
	n := 2
	srv, lastAuth := newJevServer(t, http.StatusOK, jevFixtureBody(n, nil))
	j := &JevReranker{apiKey: "secret-key-abc", httpClient: &http.Client{Timeout: 5 * time.Second}, endpoint: srv.URL}
	_, err := j.Rerank(context.Background(), "pop", cands(n), n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *lastAuth != "Bearer secret-key-abc" {
		t.Fatalf("Authorization header = %q, want 'Bearer secret-key-abc'", *lastAuth)
	}
}

// TestScoreAndRank exercises the pure ranking logic (no HTTP) directly.
func TestScoreAndRank_BasicOrdering(t *testing.T) {
	j := &JevReranker{}
	answers := make(map[string]json.RawMessage)
	setScore := func(i int, score float64) {
		b, _ := json.Marshal(jevScoreAnswer{Type: "score", Score: &score, Confidence: 0.9, Legend: map[string]string{}, Probabilities: map[string]float64{}})
		answers[questionKey(i)] = b
	}
	setScore(0, 1.5)
	setScore(1, 3.5)
	setScore(2, 2.5)

	cs := cands(3)
	idx, err := j.scoreAndRank(jevResponse{Answers: answers}, cs, 3)
	if err != nil {
		t.Fatalf("scoreAndRank error: %v", err)
	}
	if idx[0] != 1 || idx[1] != 2 || idx[2] != 0 {
		t.Fatalf("want [1 2 0], got %v", idx)
	}
}

// ---------- Tests for specific fixes ----------

func TestJevReranker_NullScore_IsRejected(t *testing.T) {
	// A null JSON score field must be caught as an error, not treated as 0.
	body, _ := json.Marshal(map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			"s0": map[string]any{"type": "score", "score": nil, "confidence": 0.5, "legend": map[string]string{}, "probabilities": map[string]float64{}},
		},
	})
	srv, _ := newJevServer(t, http.StatusOK, body)
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "chill", cands(1), 1)
	if err == nil || !strings.Contains(err.Error(), "missing or null") {
		t.Fatalf("null score must return 'missing or null' error, got: %v", err)
	}
}

func TestJevReranker_AbsentScore_IsRejected(t *testing.T) {
	// A completely absent score field must also be caught as an error.
	body, _ := json.Marshal(map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			// score field omitted entirely
			"s0": map[string]any{"type": "score", "confidence": 0.5, "legend": map[string]string{}, "probabilities": map[string]float64{}},
		},
	})
	srv, _ := newJevServer(t, http.StatusOK, body)
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "chill", cands(1), 1)
	if err == nil || !strings.Contains(err.Error(), "missing or null") {
		t.Fatalf("absent score must return 'missing or null' error, got: %v", err)
	}
}

func TestJevReranker_NegativeLimit_IsRejected(t *testing.T) {
	j := &JevReranker{apiKey: "k", httpClient: &http.Client{}}
	_, err := j.Rerank(context.Background(), "chill", cands(1), 0)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("non-positive limit must return error, got: %v", err)
	}
}

func TestJevReranker_TooManyCandidates_IsRejected(t *testing.T) {
	j := &JevReranker{apiKey: "k", httpClient: &http.Client{}}
	_, err := j.Rerank(context.Background(), "chill", cands(maxJevCandidates+1), 1)
	if err == nil || !strings.Contains(err.Error(), "too many candidates") {
		t.Fatalf("excess candidates must return error, got: %v", err)
	}
}

func TestJevReranker_HTTPErrorBodyNotEchoed(t *testing.T) {
	// The response body (which may contain sensitive diagnostics) must never
	// appear in the error returned by a non-200 HTTP response.
	const secretSentinel = "SUPERSECRET_DIAGNOSTIC_PAYLOAD_XYZ"
	srv, _ := newJevServer(t, http.StatusInternalServerError, []byte(secretSentinel))
	j := newTestJev(t, srv)
	_, err := j.Rerank(context.Background(), "pop", cands(1), 1)
	if err == nil {
		t.Fatal("non-200 response must return an error")
	}
	if strings.Contains(err.Error(), secretSentinel) {
		t.Fatalf("HTTP error response body must not be echoed in the error: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error must include the status code: %v", err)
	}
}
