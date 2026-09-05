package vibe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Plan is what a planner makes of a listener's description: a short summary
// of the vibe and the concrete search terms to run against Apple Music.
type Plan struct {
	Summary string   `json:"summary"`
	Queries []string `json:"queries"`
	Model   string   `json:"-"` // short name of the model that answered, when known
}

// Candidate is a song offered to a Reranker.
type Candidate struct {
	Artist, Title, Album string
}

// Reranker orders search hits against the listener's description. It returns
// 0-based indices into candidates, best first, at most limit of them.
type Reranker interface {
	Rerank(ctx context.Context, description string, candidates []Candidate, limit int) ([]int, error)
}

// Planner turns a natural-language description into search terms.
type Planner interface {
	// Name is shown to the user next to the plan ("Claude", "keywords").
	Name() string
	Plan(ctx context.Context, description string) (Plan, error)
}

// NewPlanner picks the planner for a config value: "keywords" is the built-in
// table, "claude" the Claude Code CLI, anything else ("auto", "") means Claude
// when the CLI is installed and the keyword table otherwise.
//
// model and effort are passed to the CLI as --model / --effort. An empty
// model means DefaultClaudeModel; "default" means the CLI's own default
// (which an organisation policy may pin to another model).
func NewPlanner(kind, model, effort string) Planner {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "":
		model = DefaultClaudeModel
	case "default", "cli":
		model = ""
	}
	claude := &ClaudePlanner{Model: model, Effort: effort}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "keywords", "keyword":
		return KeywordPlanner{}
	case "claude":
		return claude
	}
	if claude.Available() {
		return claude
	}
	return KeywordPlanner{}
}

// KeywordPlanner wraps the keyword table: one matching rule supplies genre
// and phrase terms, three of which are used per search.
type KeywordPlanner struct{}

func (KeywordPlanner) Name() string { return "keywords" }

func (KeywordPlanner) Plan(_ context.Context, description string) (Plan, error) {
	agent := &KeywordAgent{}
	v := agent.Parse(description)
	queries := agent.ToSearchQueries(v)
	const maxQueries = 3
	if len(queries) > maxQueries {
		queries = queries[:maxQueries]
	}
	if len(queries) == 0 {
		queries = []string{description}
	}
	summary := v.Mood
	if summary == "unknown" {
		summary = "plain search"
	}
	return Plan{Summary: summary, Queries: queries}, nil
}

// DefaultClaudeModel is the model vibes-mode lookups use unless the config
// says otherwise: the same one the user's Claude launcher pins.
const DefaultClaudeModel = "claude-fable-5-1"

// ClaudePlanner asks Claude Code's command-line interface (`claude -p`) for
// search terms, using the user's existing login; no API key is involved.
type ClaudePlanner struct {
	Bin    string // executable; "claude" on PATH when empty
	Model  string // optional --model (alias like "sonnet", "haiku" or a full id)
	Effort string // optional --effort (low, medium, high, xhigh, max)
}

const claudeSystemPrompt = `You turn a listener's natural-language description of what they want to hear into search terms for Apple Music's plain text search, which matches song titles, artist names, album titles and playlist names, not meanings.
Reply with JSON only, no prose and no code fences: {"summary": "<the vibe in at most 8 words>", "queries": ["...", "..."]} with 4 to 6 queries of at most 6 words each.
Prefer concrete artist names, song titles and album titles that fit the description; add at most 2 genre, era or mood phrases.
If the description names artists, songs or albums, put them first and add similar ones.`

func (c *ClaudePlanner) Name() string { return "Claude" }

func (c *ClaudePlanner) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "claude"
}

// Available reports whether the CLI can be found.
func (c *ClaudePlanner) Available() bool {
	_, err := exec.LookPath(c.bin())
	return err == nil
}

// claudeEnvelope is the part of `claude -p --output-format json` we read.
type claudeEnvelope struct {
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	ModelUsage map[string]struct {
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
}

// mainModel is the model that wrote the answer: the one with the most output
// tokens (the CLI also makes small side calls to a lighter model).
func (e claudeEnvelope) mainModel() string {
	best, bestTokens := "", -1
	for name, u := range e.ModelUsage {
		if u.OutputTokens > bestTokens || (u.OutputTokens == bestTokens && name < best) {
			best, bestTokens = name, u.OutputTokens
		}
	}
	return best
}

// ShortModel turns "claude-sonnet-5[1m]" or "claude-haiku-4-5-20251001" into
// "sonnet-5" / "haiku-4-5" for display.
func ShortModel(id string) string {
	id = strings.TrimPrefix(id, "claude-")
	if i := strings.Index(id, "["); i >= 0 {
		id = id[:i]
	}
	if i := strings.LastIndex(id, "-"); i > 0 && len(id)-i-1 == 8 && strings.Trim(id[i+1:], "0123456789") == "" {
		id = id[:i]
	}
	return id
}

// PrettyModel turns a ShortModel name into a display name: "fable-5-1" →
// "Fable 5.1", "haiku-4-5" → "Haiku 4.5", "sonnet-5" → "Sonnet 5".
func PrettyModel(short string) string {
	if short == "" {
		return ""
	}
	parts := strings.Split(short, "-")
	name := strings.ToUpper(parts[0][:1]) + parts[0][1:]
	var version []string
	for _, part := range parts[1:] {
		if part != "" && strings.Trim(part, "0123456789") == "" {
			version = append(version, part)
		} else if part != "" {
			name += " " + strings.ToUpper(part[:1]) + part[1:]
		}
	}
	if len(version) > 0 {
		name += " " + strings.Join(version, ".")
	}
	return name
}

// run makes one tool-free `claude -p` call and returns the answer text and
// the model that wrote it.
func (c *ClaudePlanner) run(ctx context.Context, systemPrompt, input string) (answer, model string, err error) {
	args := []string{"-p", "--output-format", "json", "--tools", "", "--no-session-persistence", "--system-prompt", systemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	cmd := exec.CommandContext(ctx, c.bin(), args...) //nolint:gosec // fixed binary, fixed flags; the input goes over stdin
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = withoutNestedClaudeEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("claude: %w", ctx.Err())
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if len(detail) > 200 {
			detail = detail[:200] + "…"
		}
		return "", "", fmt.Errorf("claude: %w: %s", err, detail)
	}
	var env claudeEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return "", "", fmt.Errorf("claude: unexpected output: %w", err)
	}
	if env.IsError {
		return "", "", fmt.Errorf("claude: %s", strings.TrimSpace(env.Result))
	}
	return env.Result, env.mainModel(), nil
}

func (c *ClaudePlanner) Plan(ctx context.Context, description string) (Plan, error) {
	answer, model, err := c.run(ctx, claudeSystemPrompt, description)
	if err != nil {
		return Plan{}, err
	}
	plan, err := parsePlan(answer, description)
	if err != nil {
		return Plan{}, err
	}
	plan.Model = ShortModel(model)
	return plan, nil
}

// Rerank asks Claude to pick the candidates that fit the description best.
func (c *ClaudePlanner) Rerank(ctx context.Context, description string, candidates []Candidate, limit int) ([]int, error) {
	if len(candidates) == 0 {
		return nil, errors.New("claude: no candidates to rank")
	}
	var sb strings.Builder
	sb.WriteString("Description: " + description + "\n\nCandidates:\n")
	for i, cand := range candidates {
		fmt.Fprintf(&sb, "%d. %s — %s", i+1, cand.Artist, cand.Title)
		if cand.Album != "" {
			fmt.Fprintf(&sb, " (%s)", cand.Album)
		}
		sb.WriteString("\n")
	}
	answer, _, err := c.run(ctx, rerankSystemPrompt(limit), sb.String())
	if err != nil {
		return nil, err
	}
	return parsePicks(answer, len(candidates), limit)
}

func rerankSystemPrompt(limit int) string {
	return fmt.Sprintf(`You pick songs for a listener. You get their description and a numbered list of candidate songs from Apple Music.
Reply with JSON only, no prose and no code fences: {"picks": [n, n, ...]} — the numbers of the candidates that fit the description best, best first, at most %d of them, and at least 5 when the list allows.
Use only numbers from the list. Favour the mood, era and style described; prefer a variety of artists unless one artist is asked for; leave out live versions, karaoke, tributes and remixes unless asked for.`, limit)
}

// parsePicks reads {"picks": [...]} (1-based) into unique 0-based indices.
func parsePicks(answer string, n, limit int) ([]int, error) {
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end <= start {
		return nil, errors.New("claude: no JSON in the answer")
	}
	var out struct {
		Picks []int `json:"picks"`
	}
	if err := json.Unmarshal([]byte(answer[start:end+1]), &out); err != nil {
		return nil, fmt.Errorf("claude: bad JSON in the answer: %w", err)
	}
	seen := make(map[int]bool, len(out.Picks))
	var idx []int
	for _, pick := range out.Picks {
		i := pick - 1
		if i < 0 || i >= n || seen[i] {
			continue
		}
		seen[i] = true
		idx = append(idx, i)
		if len(idx) == limit {
			break
		}
	}
	if len(idx) == 0 {
		return nil, errors.New("claude: no valid picks in the answer")
	}
	return idx, nil
}

// parsePlan extracts the JSON object from the model's answer (tolerating
// prose or code fences around it) and cleans the query list.
func parsePlan(answer, description string) (Plan, error) {
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end <= start {
		return Plan{}, errors.New("claude: no JSON in the answer")
	}
	var p Plan
	if err := json.Unmarshal([]byte(answer[start:end+1]), &p); err != nil {
		return Plan{}, fmt.Errorf("claude: bad JSON in the answer: %w", err)
	}
	seen := map[string]bool{}
	queries := p.Queries[:0]
	for _, q := range p.Queries {
		q = strings.TrimSpace(q)
		key := strings.ToLower(q)
		if q == "" || seen[key] {
			continue
		}
		seen[key] = true
		queries = append(queries, q)
	}
	const maxQueries = 8
	if len(queries) > maxQueries {
		queries = queries[:maxQueries]
	}
	if len(queries) == 0 {
		return Plan{}, errors.New("claude: no search terms in the answer")
	}
	p.Queries = queries
	p.Summary = strings.TrimSpace(p.Summary)
	if p.Summary == "" {
		p.Summary = description
	}
	return p, nil
}

// withoutNestedClaudeEnv drops the variables Claude Code sets in its own
// shells; a nested `claude -p` refuses to start while they are present.
func withoutNestedClaudeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CLAUDECODE", k == "CLAUDE_PID", k == "CLAUDE_JOB_DIR", k == "CLAUDE_EFFORT":
			continue
		case strings.HasPrefix(k, "CLAUDE_CODE_"):
			continue
		}
		out = append(out, kv)
	}
	return out
}
