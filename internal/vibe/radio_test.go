package vibe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNextSongs_PromptAndParsing(t *testing.T) {
	dir := t.TempDir()
	bin := fakeClaude(t, `cat > "`+dir+`/stdin"; printf '%s\n' "$@" > "`+dir+`/args"
printf '%s' '{"type":"result","is_error":false,"result":"Sure:\n{\"picks\":[{\"artist\":\"Boards of Canada\",\"title\":\"Roygbiv\"},{\"artist\":\" Four Tet \",\"title\":\"Two Thousand and Seventeen\"},{\"artist\":\"Boards of Canada\",\"title\":\"roygbiv\"},{\"artist\":\"x\",\"title\":\"\"}]}","modelUsage":{}}'`)
	p := &ClaudePlanner{Bin: bin}
	picks, err := p.NextSongs(context.Background(), Candidate{Artist: "Aphex Twin", Title: "Xtal", Album: "SAW 85-92"},
		[]Candidate{{Artist: "Burial", Title: "Archangel"}}, []Candidate{{Artist: "Aphex Twin", Title: "Xtal"}, {Artist: "Burial", Title: "Archangel"}}, 3)
	if err != nil {
		t.Fatalf("NextSongs: %v", err)
	}
	if len(picks) != 2 || picks[0] != (Candidate{Artist: "Boards of Canada", Title: "Roygbiv"}) || picks[1].Artist != "Four Tet" {
		t.Fatalf("picks = %+v (trimmed, deduped case-insensitively, blanks dropped)", picks)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	for _, want := range []string{"Now playing: Aphex Twin — Xtal (SAW 85-92)", "Before it, oldest first:\n1. Burial — Archangel", "Do not pick (already in the list):\n- Aphex Twin — Xtal"} {
		if !strings.Contains(string(stdin), want) {
			t.Fatalf("the prompt lacks %q: %q", want, stdin)
		}
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "radio DJ") || !strings.Contains(string(args), "with 3 real, released songs") {
		t.Fatalf("the DJ instructions go as the system prompt: %q", args)
	}
}

func TestParseNextSongs_Errors(t *testing.T) {
	for _, bad := range []string{"no json here", `{"picks":[]}`, `{"picks":[{"artist":"a","title":""}]}`, `{"picks": [}`} {
		if _, err := parseNextSongs(bad, 3); err == nil {
			t.Fatalf("%q must fail", bad)
		}
	}
}
