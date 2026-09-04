package cursor

// Real-binary contract test (opt-in): only with AF_CURSOR_LIVE=1 does it start a real
// `cursor-agent acp` as a child process and check that the managed contract of docs/log/40 holds
// against the real CLI - spawn -> initialize -> session/new -> prompt(completed) -> transcript
// built in memory -> (in a separate process) session/load resume -> context kept and the
// transcript rebuilt from the replay. Authentication assumes the environment's Cursor login
// (ambient auth in ~/.config/cursor/auth.json).
// Run with: AF_CURSOR_LIVE=1 go test -run TestLive -v ./internal/agents/cursor/
//
// The CLI ships weekly, so this is the first line of drift detection: it fails here if the ACP
// sessionUpdate discriminator, the session/load replay shape or stopReason changes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("AF_CURSOR_LIVE") != "1" {
		t.Skip("set AF_CURSOR_LIVE=1 to enable the real-CLI contract test")
	}
}

// isolateHomeKeepAuth points HOME at a tempdir so AF state (sids / status) is isolated,
// while symlinking ~/.config/cursor so the CLI still finds its ambient login. We never
// read or copy the token — the symlink lets cursor-agent read its OWN config file.
func isolateHomeKeepAuth(t *testing.T) {
	t.Helper()
	real, _ := os.UserHomeDir()
	home := t.TempDir()
	if real != "" {
		if _, err := os.Stat(filepath.Join(real, ".config", "cursor")); err == nil {
			_ = os.MkdirAll(filepath.Join(home, ".config"), 0o755)
			_ = os.Symlink(filepath.Join(real, ".config", "cursor"), filepath.Join(home, ".config", "cursor"))
		}
	}
	t.Setenv("HOME", home)
}

func waitCompleted(t *testing.T, h *threadHandle) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		st := h.currentState()
		if st == agents.TurnCompleted {
			return
		}
		if st == agents.TurnFailed || st == agents.TurnUnknown || st == agents.TurnCancelled {
			t.Fatalf("turn ended abnormally: %s", st)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("turn did not complete (state %s)", h.currentState())
}

func TestLiveSpawnPromptResume(t *testing.T) {
	liveGate(t)
	isolateHomeKeepAuth(t)
	work := t.TempDir()

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
	waitState(t, h, agents.TurnRunning) // wait for the turn to start so we do not race past spawn's initial Completed
	waitCompleted(t, h)

	sid := h.sid
	if sid == "" || sids.Read("live-slot-1") != sid {
		t.Fatalf("sid not captured: %q store=%q", sid, sids.Read("live-slot-1"))
	}
	// The transcript is built in memory (ACP writes no local file).
	turns := h.buf.snapshot()
	found := false
	for _, tn := range turns {
		if tn.Role == "assistant" && strings.Contains(tn.Text, "LIVE-OK") {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant turn missing from in-memory transcript: %+v", turns)
	}

	// Resume in a separate process (session/load): kill the child, spawn again, and confirm with a
	// real prompt that the context and the transcript survived.
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
	// The session/load replay rebuilt the transcript (the previous turn is still there).
	replayed := h.buf.snapshot()
	if len(replayed) == 0 {
		t.Fatalf("transcript not rebuilt from session/load replay")
	}
	if err := h.Send(agents.TurnInput{Prompt: "What exact string did I ask you to reply with before? Answer with just that string.", ClientMessageID: "lm2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	waitState(t, h, agents.TurnRunning)
	waitCompleted(t, h)
	turns = h.buf.snapshot()
	last := turns[len(turns)-1]
	if last.Role != "assistant" || !strings.Contains(last.Text, "LIVE-OK") {
		t.Fatalf("resume lost context; last turn: %+v", last)
	}
}
