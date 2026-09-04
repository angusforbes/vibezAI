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
func NewPlanner(kind string) Planner {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "keywords", "keyword":
		return KeywordPlanner{}
	case "claude":
		return &ClaudePlanner{}
	}
	if c := (&ClaudePlanner{}); c.Available() {
		return c
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

// ClaudePlanner asks Claude Code's command-line interface (`claude -p`) for
// search terms, using the user's existing login; no API key is involved.
type ClaudePlanner struct {
	Bin   string // executable; "claude" on PATH when empty
	Model string // optional --model override
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
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

func (c *ClaudePlanner) Plan(ctx context.Context, description string) (Plan, error) {
	args := []string{"-p", "--output-format", "json", "--tools", "", "--no-session-persistence", "--system-prompt", claudeSystemPrompt}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	cmd := exec.CommandContext(ctx, c.bin(), args...) //nolint:gosec // fixed binary, fixed flags; the description goes over stdin
	cmd.Stdin = strings.NewReader(description)
	cmd.Env = withoutNestedClaudeEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Plan{}, fmt.Errorf("claude: %w", ctx.Err())
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if len(detail) > 200 {
			detail = detail[:200] + "…"
		}
		return Plan{}, fmt.Errorf("claude: %w: %s", err, detail)
	}
	var env claudeEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return Plan{}, fmt.Errorf("claude: unexpected output: %w", err)
	}
	if env.IsError {
		return Plan{}, fmt.Errorf("claude: %s", strings.TrimSpace(env.Result))
	}
	return parsePlan(env.Result, description)
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
