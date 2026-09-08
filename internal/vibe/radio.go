package vibe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Suggester chooses songs to play next: the AI radio's question. Both CLI
// planners answer it; the keyword table cannot.
type Suggester interface {
	NextSongs(ctx context.Context, nowPlaying Candidate, recent, avoid []Candidate, n int) ([]Candidate, error)
}

// runner is the one call both CLI planners make: a system prompt and an
// input in, the answer and the model that gave it out.
type runner interface {
	run(ctx context.Context, systemPrompt, input string) (answer, model string, err error)
}

func nextSongsSystemPrompt(n int) string {
	return fmt.Sprintf(`You are a radio DJ choosing what Apple Music plays next. You get the song playing now, the songs that played before it (oldest first) and songs that must not be picked.
Reply with JSON only, no prose and no code fences: {"picks": [{"artist": "...", "title": "..."}, ...]} with %d real, released songs, best first, that would follow the playing song well.
Match its mood, era and style and the drift of the recent run; prefer a different artist from the last few songs unless the run is all one artist; use exact artist names and song titles as Apple Music lists them; never pick a song from the do-not-pick list, nor live, karaoke or tribute versions.`, n)
}

// nextSongs asks r for n songs to follow nowPlaying.
func nextSongs(ctx context.Context, r runner, nowPlaying Candidate, recent, avoid []Candidate, n int) ([]Candidate, error) {
	if n <= 0 {
		n = 1
	}
	var sb strings.Builder
	sb.WriteString("Now playing: " + candidateLine(nowPlaying) + "\n")
	if len(recent) > 0 {
		sb.WriteString("\nBefore it, oldest first:\n")
		for i, c := range recent {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, candidateLine(c))
		}
	}
	if len(avoid) > 0 {
		sb.WriteString("\nDo not pick (already in the list):\n")
		for _, c := range avoid {
			sb.WriteString("- " + candidateLine(c) + "\n")
		}
	}
	answer, _, err := r.run(ctx, nextSongsSystemPrompt(n), sb.String())
	if err != nil {
		return nil, err
	}
	return parseNextSongs(answer, n)
}

func candidateLine(c Candidate) string {
	s := c.Artist + " — " + c.Title
	if c.Album != "" {
		s += " (" + c.Album + ")"
	}
	return s
}

// parseNextSongs reads {"picks":[{"artist","title"}]} out of an answer,
// dropping blanks and repeats, at most n.
func parseNextSongs(answer string, n int) ([]Candidate, error) {
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if start < 0 || end <= start {
		return nil, errors.New("no JSON in the answer")
	}
	var out struct {
		Picks []struct {
			Artist string `json:"artist"`
			Title  string `json:"title"`
		} `json:"picks"`
	}
	if err := json.Unmarshal([]byte(answer[start:end+1]), &out); err != nil {
		return nil, fmt.Errorf("bad JSON in the answer: %w", err)
	}
	seen := map[string]bool{}
	var picks []Candidate
	for _, p := range out.Picks {
		artist, title := strings.TrimSpace(p.Artist), strings.TrimSpace(p.Title)
		key := strings.ToLower(artist + "||" + title)
		if title == "" || seen[key] {
			continue
		}
		seen[key] = true
		picks = append(picks, Candidate{Artist: artist, Title: title})
		if len(picks) == n {
			break
		}
	}
	if len(picks) == 0 {
		return nil, errors.New("no songs in the answer")
	}
	return picks, nil
}

// NextSongs asks Claude what should play next.
func (c *ClaudePlanner) NextSongs(ctx context.Context, nowPlaying Candidate, recent, avoid []Candidate, n int) ([]Candidate, error) {
	return nextSongs(ctx, c, nowPlaying, recent, avoid, n)
}

// NextSongs asks ChatGPT what should play next.
func (c *CodexPlanner) NextSongs(ctx context.Context, nowPlaying Candidate, recent, avoid []Candidate, n int) ([]Candidate, error) {
	return nextSongs(ctx, c, nowPlaying, recent, avoid, n)
}
