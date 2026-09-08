package vibe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCodex(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil { //nolint:gosec // test helper script
		t.Fatal(err)
	}
	return path
}

func TestCodexPlanner_PlanParsesEvents(t *testing.T) {
	// The fake records its arguments and stdin, then answers like `codex exec
	// --json`: a config warning item, the agent message, the turn summary.
	dir := t.TempDir()
	bin := fakeCodex(t, `printf '%s\n' "$@" > "`+dir+`/args"; cat > "`+dir+`/stdin"
printf '%s\n' '{"type":"thread.started","thread_id":"t1"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Configured value for approval_policy is disallowed; falling back"}}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"{\"summary\":\"Dreamy 70s soul\",\"queries\":[\"Roberta Flack\",\" Marvin Gaye \",\"roberta flack\",\"70s soul playlist\"]}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":13307,"output_tokens":16}}'`)
	p := &CodexPlanner{Bin: bin, Model: "gpt-5.6-sol", Effort: "low"}
	plan, err := p.Plan(context.Background(), "dreamy 70s soul for a rainy sunday")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Summary != "Dreamy 70s soul" {
		t.Fatalf("summary = %q", plan.Summary)
	}
	if plan.Model != "gpt-5.6-sol" {
		t.Fatalf("the configured model is reported (the stream does not name it), got %q", plan.Model)
	}
	want := []string{"Roberta Flack", "Marvin Gaye", "70s soul playlist"}
	if strings.Join(plan.Queries, "|") != strings.Join(want, "|") {
		t.Fatalf("queries = %q, want %q", plan.Queries, want)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, flag := range []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "-m", "gpt-5.6-sol", "-c", `model_reasoning_effort="low"`, "-"} {
		if !strings.Contains(string(args), flag+"\n") {
			t.Fatalf("missing argument %q in %q", flag, args)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if !strings.HasPrefix(string(stdin), codexPreamble) || !strings.Contains(string(stdin), claudeSystemPrompt) || !strings.HasSuffix(string(stdin), "\n\ndreamy 70s soul for a rainy sunday") {
		t.Fatalf("the prompt is preamble + system prompt + description over stdin, got %q", stdin)
	}
}

func TestCodexPlanner_ErrorsAreReported(t *testing.T) {
	bin := fakeCodex(t, `printf '%s\n' '{"type":"error","message":"Not logged in. Run codex login"}'`)
	if _, err := (&CodexPlanner{Bin: bin}).Plan(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("an error event without an answer must be reported, got %v", err)
	}
	bin = fakeCodex(t, `echo "boom" >&2; exit 3`)
	if _, err := (&CodexPlanner{Bin: bin}).Plan(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failing CLI must be reported with its stderr, got %v", err)
	}
	// A warning item next to a real answer is not an error.
	bin = fakeCodex(t, `printf '%s\n' '{"type":"item.completed","item":{"type":"error","message":"warning"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"summary\":\"s\",\"queries\":[\"a\",\"b\"]}"}}'`)
	if plan, err := (&CodexPlanner{Bin: bin}).Plan(context.Background(), "x"); err != nil || len(plan.Queries) != 2 {
		t.Fatalf("the answer wins over a warning item: %v %+v", err, plan)
	}
}

func TestCodexPlanner_RerankParsesPicks(t *testing.T) {
	dir := t.TempDir()
	bin := fakeCodex(t, `printf '%s\n' "$@" > "`+dir+`/args"; cat > "`+dir+`/stdin"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"picks\":[3,1]}"}}'`)
	p := &CodexPlanner{Bin: bin}
	cands := []Candidate{{Artist: "A", Title: "1"}, {Artist: "B", Title: "2", Album: "Two"}, {Artist: "C", Title: "3"}}
	idx, err := p.Rerank(context.Background(), "late night", cands, 2)
	if err != nil || len(idx) != 2 || idx[0] != 2 || idx[1] != 0 {
		t.Fatalf("Rerank = %v, %v", idx, err)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if !strings.Contains(string(stdin), "2. B — 2 (Two)") || !strings.Contains(string(stdin), "Description: late night") {
		t.Fatalf("the candidates go numbered over stdin, got %q", stdin)
	}
}

func TestNewAIPlanner(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no codex anywhere
	if _, ok := NewAIPlanner("", "").(KeywordPlanner); !ok {
		t.Fatal("without the CLI the keyword table answers")
	}
	dir := filepath.Dir(fakeCodex(t, `exit 0`))
	t.Setenv("PATH", dir)
	c, ok := NewAIPlanner("", "low").(*CodexPlanner)
	if !ok || c.Model != DefaultCodexModel || c.Effort != "low" {
		t.Fatalf("an empty model means %s, got %+v", DefaultCodexModel, c)
	}
	if c := NewAIPlanner("default", "").(*CodexPlanner); c.Model != "" {
		t.Fatalf("\"default\" leaves the CLI's own model, got %q", c.Model)
	}
	if c := NewAIPlanner("gpt-5.5", "").(*CodexPlanner); c.Model != "gpt-5.5" {
		t.Fatalf("an explicit slug is passed through, got %q", c.Model)
	}
}

func TestPrettyModel_GPT(t *testing.T) {
	for in, want := range map[string]string{"gpt-5.6-sol": "GPT 5.6 Sol", "gpt-5.5": "GPT 5.5", "gpt-5.3-codex-spark": "GPT 5.3 Codex Spark"} {
		if got := PrettyModel(ShortModel(in)); got != want {
			t.Errorf("PrettyModel(%q) = %q, want %q", in, got, want)
		}
	}
}
