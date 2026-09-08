package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/simone-vibes/vibez/internal/provider"
	"github.com/simone-vibes/vibez/internal/tui/views"
	"github.com/simone-vibes/vibez/internal/vibe"
)

// AI radio (the A key): while it is on, every track that starts playing gets
// one song chosen by the selected AI (Claude Code, or ChatGPT after :ai gpt)
// from what is playing and the run of songs before it, found on Apple Music
// and inserted right after the playing track. Discover (F) asks Apple's own
// station instead; the two exclude each other, and so does :radio.

// aiRadioContext is how many songs before the playing one the AI is shown.
const aiRadioContext = 10

// aiRadioAvoidCap bounds the "already in the list" part of the prompt.
const aiRadioAvoidCap = 40

type aiRadioMode struct {
	on             bool
	triggeredForID string // playback id of the track that already got its pick
	gen            int    // bumped on every off and on, so late results are dropped
}

// aiRadioResultMsg carries the AI's pick for one track, resolved on Apple
// Music, or the reason there is none.
type aiRadioResultMsg struct {
	gen   int
	seed  provider.Track
	track *provider.Track
	via   string
	picks []vibe.Candidate
	err   error
}

// toggleAIRadio is A. Turning it on takes over from discover and radio and
// asks about the track playing right now; turning it off leaves what was
// inserted in place.
func (m *Model) toggleAIRadio() tea.Cmd {
	if m.aiRadio.on {
		m.aiRadioOff()
		return nil
	}
	if m.discover.on {
		m.discover.on = false
		m.discover.triggeredForID = ""
		m.appendLog("[apple radio] off (AI radio on)")
	}
	if m.radio.enabled {
		m.stopRadio()
	}
	m.aiRadio.on = true
	m.aiRadio.gen++
	m.appendLog("[ai radio] on: " + m.lookupPlanner().Name())
	return m.aiRadioFor(m.playerState.Track)
}

func (m *Model) aiRadioOff() {
	if !m.aiRadio.on {
		return
	}
	m.aiRadio.on = false
	m.aiRadio.triggeredForID = ""
	m.aiRadio.gen++
	m.appendLog("[ai radio] off")
}

// aiRadioFor asks the AI what should follow t, once per track: it sees t, up
// to aiRadioContext songs before it and everything already listed, answers
// with a few songs, and the first one found on Apple Music (right title and
// artist, not already listed) comes back to be inserted after t.
func (m *Model) aiRadioFor(t *provider.Track) tea.Cmd {
	if !m.aiRadio.on || t == nil || m.provider == nil {
		return nil
	}
	id := views.PlaybackID(*t)
	if m.aiRadio.triggeredForID == id {
		return nil
	}
	m.aiRadio.triggeredForID = id
	dj, ok := m.lookupPlanner().(vibe.Suggester)
	if !ok {
		m.appendLog("[ai radio] no AI to ask (the keyword table cannot choose songs); :ai cc or :ai gpt")
		return nil
	}
	idx := m.queueIndexOf(id)
	var recent []vibe.Candidate
	if idx > 0 {
		for _, q := range m.queueTracks[max(0, idx-aiRadioContext):idx] {
			recent = append(recent, candidateOf(q))
		}
	}
	// Everything listed is off limits: the AI is told, and the hits are
	// filtered too, by id and by artist||title.
	exclude := make(map[string]bool, 2*len(m.queueTracks))
	var avoid []vibe.Candidate
	for _, q := range m.queueTracks {
		exclude[views.PlaybackID(q)] = true
		exclude[trackKey(q.Artist, q.Title)] = true
		if len(avoid) < aiRadioAvoidCap {
			avoid = append(avoid, candidateOf(q))
		}
	}
	seed, prov, gen, via := *t, m.provider, m.aiRadio.gen, m.lookupPlanner().Name()
	m.appendLog(fmt.Sprintf("[ai radio] asking %s what follows %q (%d recent, %d listed)", via, seed.Title, len(recent), len(avoid)))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		picks, err := dj.NextSongs(ctx, candidateOf(seed), recent, avoid, 3)
		if err != nil {
			return aiRadioResultMsg{gen: gen, seed: seed, via: via, err: err}
		}
		for _, pick := range picks {
			res, err := prov.Search(ctx, pick.Artist+" "+pick.Title)
			if err != nil || res == nil {
				continue
			}
			for _, hit := range res.Tracks {
				if !matchesPick(hit, pick) || exclude[views.PlaybackID(hit)] || exclude[trackKey(hit.Artist, hit.Title)] {
					continue
				}
				found := hit
				return aiRadioResultMsg{gen: gen, seed: seed, via: via, picks: picks, track: &found}
			}
		}
		return aiRadioResultMsg{gen: gen, seed: seed, via: via, picks: picks, err: fmt.Errorf("none of %d picks found on Apple Music", len(picks))}
	}
}

// handleAIRadioResult inserts the pick right after its seed (or at the end
// when the seed has left the list). Failures only log: the radio stays on.
func (m *Model) handleAIRadioResult(msg aiRadioResultMsg) tea.Cmd {
	if !m.aiRadio.on || msg.gen != m.aiRadio.gen {
		m.appendLog(fmt.Sprintf("[ai radio] off before the pick for %q arrived; dropped", msg.seed.Title))
		return nil
	}
	if msg.err != nil {
		m.appendLog(fmt.Sprintf("[ai radio] %v (picks: %s)", msg.err, describeCandidates(msg.picks)))
		return nil
	}
	insertIdx := len(m.queueTracks)
	if i := m.queueIndexOf(views.PlaybackID(msg.seed)); i >= 0 {
		insertIdx = i + 1
	}
	m.appendLog(fmt.Sprintf("[ai radio] %s chose %s — %s after %q (position %d)", msg.via, msg.track.Artist, msg.track.Title, msg.seed.Title, insertIdx))
	return m.insertQueueAt(insertIdx, []provider.Track{*msg.track}, []string{views.PlaybackID(*msg.track)})
}

func candidateOf(t provider.Track) vibe.Candidate {
	return vibe.Candidate{Artist: t.Artist, Title: t.Title, Album: t.Album}
}

func trackKey(artist, title string) string { return strings.ToLower(artist + "||" + title) }

// matchesPick accepts a search hit whose title carries the picked title and
// whose artist and the picked artist carry each other, ignoring case and
// punctuation, so "ROYGBIV" by "Boards Of Canada" matches the pick.
func matchesPick(t provider.Track, pick vibe.Candidate) bool {
	if !containsLoose(t.Title, pick.Title) {
		return false
	}
	return pick.Artist == "" || containsLoose(t.Artist, pick.Artist) || containsLoose(pick.Artist, t.Artist)
}

func containsLoose(have, want string) bool {
	h, w := looseKey(have), looseKey(want)
	return w != "" && strings.Contains(h, w)
}

func looseKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func describeCandidates(cs []vibe.Candidate) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, c.Artist+" — "+c.Title)
	}
	return strings.Join(parts, "; ")
}
