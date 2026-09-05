package vibe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil { //nolint:gosec // test helper script
		t.Fatal(err)
	}
	return path
}

func TestClaudePlanner_PlanParsesEnvelope(t *testing.T) {
	// The fake records its arguments, stdin and environment, then answers like the CLI.
	dir := t.TempDir()
	answer := `Here you go:\n{\"summary\": \"Dreamy 70s soul\", \"queries\": [\"Roberta Flack\", \" Marvin Gaye \", \"roberta flack\", \"\", \"70s soul playlist\"]}\nEnjoy!`
	bin := fakeClaude(t, `printf '%s\n' "$@" > "`+dir+`/args"; cat > "`+dir+`/stdin"; env > "`+dir+`/env"
printf '%s' '{"type":"result","is_error":false,"result":"`+answer+`","modelUsage":{"claude-haiku-4-5-20251001":{"outputTokens":20},"claude-sonnet-5[1m]":{"outputTokens":135}}}'`)
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "abc")
	p := &ClaudePlanner{Bin: bin}
	plan, err := p.Plan(context.Background(), "dreamy 70s soul for a rainy sunday")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Summary != "Dreamy 70s soul" {
		t.Fatalf("summary = %q", plan.Summary)
	}
	if plan.Model != "sonnet-5" {
		t.Fatalf("the answering model (most output tokens) should be reported short, got %q", plan.Model)
	}
	want := []string{"Roberta Flack", "Marvin Gaye", "70s soul playlist"}
	if strings.Join(plan.Queries, "|") != strings.Join(want, "|") {
		t.Fatalf("queries = %q, want %q (trimmed, deduped case-insensitively, blanks dropped)", plan.Queries, want)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, flag := range []string{"-p", "--output-format", "json", "--tools", "--no-session-persistence", "--system-prompt"} {
		if !strings.Contains(string(args), flag+"\n") {
			t.Fatalf("missing flag %q in %q", flag, args)
		}
	}
	if stdin, _ := os.ReadFile(filepath.Join(dir, "stdin")); string(stdin) != "dreamy 70s soul for a rainy sunday" {
		t.Fatalf("the description must go over stdin, got %q", stdin)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	if strings.Contains(string(env), "CLAUDECODE=") || strings.Contains(string(env), "CLAUDE_CODE_SESSION_ID=") {
		t.Fatal("nested Claude Code variables must be stripped or the CLI refuses to start")
	}
}

func TestClaudePlanner_ErrorsAreReported(t *testing.T) {
	bin := fakeClaude(t, `printf '%s' '{"is_error":true,"result":"Not logged in · Please run /login"}'`)
	if _, err := (&ClaudePlanner{Bin: bin}).Plan(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("an is_error envelope must surface its message, got %v", err)
	}
	bin = fakeClaude(t, `echo "boom" >&2; exit 3`)
	if _, err := (&ClaudePlanner{Bin: bin}).Plan(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failing process must report stderr, got %v", err)
	}
	if _, err := (&ClaudePlanner{Bin: filepath.Join(t.TempDir(), "missing")}).Plan(context.Background(), "x"); err == nil {
		t.Fatal("a missing binary must fail")
	}
}

func TestParsePlan_NeedsTerms(t *testing.T) {
	if _, err := parsePlan("no json here", "d"); err == nil {
		t.Fatal("prose without JSON must fail")
	}
	if _, err := parsePlan(`{"summary":"x","queries":[]}`, "d"); err == nil {
		t.Fatal("an empty query list must fail")
	}
	p, err := parsePlan(`{"queries":["a","b","c","d","e","f","g","h","i","j"]}`, "my description")
	if err != nil || len(p.Queries) != 8 || p.Summary != "my description" {
		t.Fatalf("expected 8 capped queries and the description as summary, got %+v (%v)", p, err)
	}
}

func TestKeywordPlanner_Plan(t *testing.T) {
	plan, err := KeywordPlanner{}.Plan(context.Background(), "late night coding")
	if err != nil || len(plan.Queries) != 3 || plan.Summary == "" || plan.Summary == "unknown" {
		t.Fatalf("keyword plan = %+v (%v)", plan, err)
	}
	plan, _ = KeywordPlanner{}.Plan(context.Background(), "Animals as Leaders")
	if plan.Summary != "plain search" || len(plan.Queries) != 1 || plan.Queries[0] != "animals as leaders" {
		t.Fatalf("unmatched text is passed through as one query: %+v", plan)
	}
}

func TestNewPlanner(t *testing.T) {
	if _, ok := NewPlanner("keywords", "", "").(KeywordPlanner); !ok {
		t.Fatal("keywords → KeywordPlanner")
	}
	if _, ok := NewPlanner("claude", "", "").(*ClaudePlanner); !ok {
		t.Fatal("claude → ClaudePlanner")
	}
	if NewPlanner("auto", "", "") == nil {
		t.Fatal("auto must pick something")
	}
}

func TestClaudePlanner_RerankParsesPicks(t *testing.T) {
	dir := t.TempDir()
	bin := fakeClaude(t, `printf '%s\n' "$@" > "`+dir+`/args"; cat > "`+dir+`/stdin"
printf '%s' '{"is_error":false,"result":"{\"picks\": [3, 1, 3, 99, 0, 2]}"}'`)
	p := &ClaudePlanner{Bin: bin, Model: "haiku", Effort: "low"}
	cands := []Candidate{{"A", "one", "Alb"}, {"B", "two", ""}, {"C", "three", "Alb3"}}
	idx, err := p.Rerank(context.Background(), "soft evening", cands, 2)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(idx) != 2 || idx[0] != 2 || idx[1] != 0 {
		t.Fatalf("picks are 1-based, deduped, range-checked and capped: got %v want [2 0]", idx)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	for _, want := range []string{"Description: soft evening", "1. A — one (Alb)", "2. B — two\n", "3. C — three (Alb3)"} {
		if !strings.Contains(string(stdin), want) {
			t.Fatalf("candidate list should contain %q:\n%s", want, stdin)
		}
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "--model\nhaiku\n") || !strings.Contains(string(args), "--effort\nlow\n") {
		t.Fatalf("model and effort must be passed through: %q", args)
	}
}

func TestParsePicks(t *testing.T) {
	if _, err := parsePicks(`{"picks": []}`, 5, 15); err == nil {
		t.Fatal("no picks must fail so the caller keeps Apple's order")
	}
	if _, err := parsePicks("nope", 5, 15); err == nil {
		t.Fatal("prose must fail")
	}
}

func TestShortModel(t *testing.T) {
	for in, want := range map[string]string{
		"claude-sonnet-5[1m]":       "sonnet-5",
		"claude-haiku-4-5-20251001": "haiku-4-5",
		"claude-fable-5-1":          "fable-5-1",
		"":                          "",
	} {
		if got := ShortModel(in); got != want {
			t.Errorf("ShortModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewPlanner_ModelDefaults(t *testing.T) {
	if c := NewPlanner("claude", "", "").(*ClaudePlanner); c.Model != DefaultClaudeModel {
		t.Fatalf("an empty model means %s, got %q", DefaultClaudeModel, c.Model)
	}
	if c := NewPlanner("claude", "default", "low").(*ClaudePlanner); c.Model != "" || c.Effort != "low" {
		t.Fatalf("\"default\" leaves the CLI's model alone; got model=%q effort=%q", c.Model, c.Effort)
	}
	if c := NewPlanner("claude", "haiku", "").(*ClaudePlanner); c.Model != "haiku" {
		t.Fatalf("an explicit model is kept, got %q", c.Model)
	}
}

func TestPrettyModel(t *testing.T) {
	for in, want := range map[string]string{
		"fable-5-1": "Fable 5.1",
		"sonnet-5":  "Sonnet 5",
		"haiku-4-5": "Haiku 4.5",
		"opus":      "Opus",
		"":          "",
	} {
		if got := PrettyModel(in); got != want {
			t.Errorf("PrettyModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanListName(t *testing.T) {
	if got := CleanListName("  Late-Night Jazz!\nsecond line"); got != "late night jazz" {
		t.Fatalf("CleanListName = %q", got)
	}
	if got := CleanListName("one two three four five six seven"); got != "one two three four five" {
		t.Fatalf("at most five words: %q", got)
	}
	if got := CleanListName("!!!"); got != "" {
		t.Fatalf("nothing usable: %q", got)
	}
}
