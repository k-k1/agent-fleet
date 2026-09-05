package copilot

// Real-binary contract test (opt-in): only with AF_COPILOT_LIVE=1 does it spawn a real
// `copilot --acp` child and check that docs/log/36's managed contract holds against the real
// CLI — spawn → initialize → session/new → prompt(completed) → session/load resume (in a
// separate process) → context preserved. Authentication assumes the environment's GitHub
// connection (gh transparent auth).
// Example: AF_COPILOT_LIVE=1 AGENT_COPILOT_BIN=<path> go test -run TestLive -v ./internal/agents/copilot/
//
// The CLI ships weekly, so this is the first line of drift detection. Whether models.go's
// static catalog is still valid is left to the measured TUI /model probe; a Free plan only
// offers Auto, which makes --model unverifiable, so no model is given here.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("AF_COPILOT_LIVE") != "1" {
		t.Skip("set AF_COPILOT_LIVE=1 to enable the real-CLI contract test")
	}
}

func TestLiveSpawnPromptResume(t *testing.T) {
	liveGate(t)
	// Isolating HOME also hides gh's saved credential (ambient auth breaks), so the token
	// is read from the real HOME first and injected explicitly through the environment.
	tok := Token()
	if tok == "" {
		t.Skip("no gh auth token (GitHub not connected) — skipping the live test")
	}
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // isolate the status / sids stores
	t.Setenv("COPILOT_HOME", home)
	t.Setenv("COPILOT_GITHUB_TOKEN", tok)

	h := &threadHandle{
		name: "live1", dir: work, slotSid: "live-slot-1",
		events: make(chan agents.Event, 64),
	}
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { DropHandle("live1"); Shutdown() })
	handlesMu.Lock()
	handles["live1"] = h
	handlesMu.Unlock()

	if err := h.Send(agents.TurnInput{Prompt: "Reply with exactly: LIVE-OK", ClientMessageID: "lm1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitState(t, h, agents.TurnRunning)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) && h.currentState() != agents.TurnCompleted {
		time.Sleep(500 * time.Millisecond)
	}
	if st := h.currentState(); st != agents.TurnCompleted {
		t.Fatalf("turn did not complete: %s", st)
	}
	sid := h.sid
	if sid == "" || sids.Read("live-slot-1") != sid {
		t.Fatalf("sid not captured: %q store=%q", sid, sids.Read("live-slot-1"))
	}
	// The read side's source of truth: events.jsonl is written and carries the reply text.
	turns := parseEvents(EventsPath(sid))
	found := false
	for _, tn := range turns {
		if tn.Role == "assistant" && strings.Contains(tn.Text, "LIVE-OK") {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant turn missing from events.jsonl: %+v", turns)
	}
	if st := liveStateFromFile(EventsPath(sid)); st != "idle" {
		t.Fatalf("post-turn live state: %q", st)
	}

	// Resume in a separate process (session/load): kill the child, spawn again, and check
	// with a real prompt that the context survived.
	h.mu.Lock()
	oldCmd := h.cmd
	h.alive = false
	h.mu.Unlock()
	stopChild(oldCmd)
	time.Sleep(1 * time.Second)
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if h.sid != sid {
		t.Fatalf("resume changed sid: %q → %q", sid, h.sid)
	}
	if err := h.Send(agents.TurnInput{Prompt: "What exact string did I ask you to reply with before? Answer with just that string.", ClientMessageID: "lm2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	deadline = time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) && h.currentState() != agents.TurnCompleted {
		time.Sleep(500 * time.Millisecond)
	}
	turns = parseEvents(EventsPath(sid))
	last := turns[len(turns)-1]
	if last.Role != "assistant" || !strings.Contains(last.Text, "LIVE-OK") {
		t.Fatalf("resume lost context; last turn: %+v", last)
	}
}

// TestLiveModels is the contract of the real TUI /model scrape. A Free-plan account returns
// an empty catalog (Auto only) and a paid plan returns one or more ids; either way, what the
// test is really asserting is that the probe runs to completion and parses — i.e. rendering
// drift detection.
func TestLiveModels(t *testing.T) {
	liveGate(t)
	if Token() == "" {
		t.Skip("no gh auth token (GitHub not connected) — skipping the live test")
	}
	list, err := probeModels()
	if err != nil {
		t.Fatalf("probeModels: %v", err)
	}
	t.Logf("catalog: %d models", len(list))
	for _, m := range list {
		if m.ID == "" || strings.EqualFold(m.ID, "auto") {
			t.Errorf("bad catalog entry: %+v", m)
		}
	}
}
