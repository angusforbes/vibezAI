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
printf '%s' '{"type":"result","is_error":false,"result":"`+answer+`"}'`)
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
	if _, ok := NewPlanner("keywords").(KeywordPlanner); !ok {
		t.Fatal("keywords → KeywordPlanner")
	}
	if _, ok := NewPlanner("claude").(*ClaudePlanner); !ok {
		t.Fatal("claude → ClaudePlanner")
	}
	if NewPlanner("auto") == nil {
		t.Fatal("auto must pick something")
	}
}
