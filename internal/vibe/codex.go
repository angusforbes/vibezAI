package vibe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// DefaultCodexModel is the model AI lookups ask for unless ai_model says
// otherwise: OpenAI's GPT-5.6 Sol, by its Codex CLI slug.
const DefaultCodexModel = "gpt-5.6-sol"

// CodexPlanner asks ChatGPT, through OpenAI's Codex command-line interface
// (`codex exec`), for search terms and rankings, using the user's ChatGPT
// login; no API key is involved. It is the AI source's planner the way
// ClaudePlanner is CC's, and shares its prompts and answer parsing.
type CodexPlanner struct {
	Bin    string // executable; "codex" on PATH when empty
	Model  string // optional -m (a Codex model slug such as "gpt-5.6-sol")
	Effort string // optional model_reasoning_effort (minimal, low, medium, high, xhigh)
}

// NewAIPlanner is the AI source's planner for the config's ai_model /
// ai_effort: ChatGPT through Codex when the CLI is installed, the keyword
// table otherwise. An empty model means DefaultCodexModel; "default" means
// the CLI's own default.
func NewAIPlanner(model, effort string) Planner {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "":
		model = DefaultCodexModel
	case "default", "cli":
		model = ""
	}
	c := &CodexPlanner{Model: model, Effort: effort}
	if c.Available() {
		return c
	}
	return KeywordPlanner{}
}

func (c *CodexPlanner) Name() string { return "ChatGPT" }

func (c *CodexPlanner) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "codex"
}

// Available reports whether the CLI can be found.
func (c *CodexPlanner) Available() bool {
	_, err := exec.LookPath(c.bin())
	return err == nil
}

// codexEvent is the part of `codex exec --json` we read: one JSON object per
// line. The answer is the text of the last "agent_message" item; "error"
// items and error events explain a missing answer.
type codexEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Item    struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
	} `json:"item"`
}

// codexPreamble goes in front of every prompt: Codex runs an agent with a
// shell, and these lookups want a plain answer.
const codexPreamble = "Answer directly from what you know. Do not run commands, read files or use any tool.\n\n"

// run makes one `codex exec` call and returns the answer text and the model
// asked for (the event stream does not name the model that answered).
func (c *CodexPlanner) run(ctx context.Context, systemPrompt, input string) (answer, model string, err error) {
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--color", "never", "-s", "read-only", "-C", os.TempDir()}
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(c.Effort))
	}
	args = append(args, "-")                          // the prompt comes over stdin
	cmd := exec.CommandContext(ctx, c.bin(), args...) //nolint:gosec // fixed binary, fixed flags; the prompt goes over stdin
	cmd.Stdin = strings.NewReader(codexPreamble + systemPrompt + "\n\n" + input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return "", "", fmt.Errorf("codex: %w", ctx.Err())
	}
	// Read the stream even after a non-zero exit: it may still hold the
	// answer, or the reason it did not come.
	var lastErr string
	sc := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev codexEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Item.Type == "agent_message" && strings.TrimSpace(ev.Item.Text) != "":
			answer = ev.Item.Text
		case ev.Item.Type == "error" && ev.Item.Message != "":
			lastErr = ev.Item.Message
		case ev.Type == "error" && ev.Message != "":
			lastErr = ev.Message
		}
	}
	if answer != "" {
		return answer, c.Model, nil
	}
	detail := lastErr
	if detail == "" {
		detail = strings.TrimSpace(stderr.String())
	}
	if detail == "" {
		detail = strings.TrimSpace(stdout.String())
	}
	if len(detail) > 200 {
		detail = detail[:200] + "…"
	}
	if runErr != nil {
		return "", "", fmt.Errorf("codex: %w: %s", runErr, detail)
	}
	return "", "", fmt.Errorf("codex: no answer: %s", detail)
}

func (c *CodexPlanner) Plan(ctx context.Context, description string) (Plan, error) {
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

// Rerank asks ChatGPT to pick the candidates that fit the description best.
func (c *CodexPlanner) Rerank(ctx context.Context, description string, candidates []Candidate, limit int) ([]int, error) {
	if len(candidates) == 0 {
		return nil, errors.New("codex: no candidates to rank")
	}
	answer, _, err := c.run(ctx, rerankSystemPrompt(limit), formatCandidates(description, candidates))
	if err != nil {
		return nil, err
	}
	return parsePicks(answer, len(candidates), limit)
}

// NameList asks ChatGPT for a 2-4 word name for a list of songs given as
// "artist — title" lines, reduced like ClaudePlanner.NameList's answer.
func (c *CodexPlanner) NameList(ctx context.Context, lines []string) (string, error) {
	answer, _, err := c.run(ctx, nameListSystemPrompt, strings.Join(lines, "\n"))
	if err != nil {
		return "", err
	}
	name := CleanListName(answer)
	if name == "" {
		return "", fmt.Errorf("codex: no usable name in %q", strings.TrimSpace(answer))
	}
	return name, nil
}

// formatCandidates is the reranking input both planners send: the description
// and the numbered "artist — title (album)" candidates.
func formatCandidates(description string, candidates []Candidate) string {
	var sb strings.Builder
	sb.WriteString("Description: " + description + "\n\nCandidates:\n")
	for i, cand := range candidates {
		fmt.Fprintf(&sb, "%d. %s — %s", i+1, cand.Artist, cand.Title)
		if cand.Album != "" {
			fmt.Fprintf(&sb, " (%s)", cand.Album)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
