package kiro

// Real-binary contract test for the managed (ACP) route, opt-in through KIRO_LIVE=1. It
// starts a real `kiro-cli acp` as a child and verifies that docs/log/43 Track A2's contract
// holds against the real CLI: spawn, initialize, session/new, prompt (completed), the
// transcript being built in memory, then a session/load resume from a SEPARATE process with
// the context kept and the transcript rebuilt from the replay. Shares the read layer's
// liveGate (live_test.go, KIRO_LIVE plus PATH). Authentication assumes the environment's
// ambient kiro login (~/.local/share/kiro-cli).
//
// The CLI updates weekly, so this is the drift detection line for the managed contract: a
// change to the sessionUpdate discriminator, the session/load replay shape, the release of
// .lock, or stopReason makes it fail here.
//   KIRO_LIVE=1 go test -run TestLiveManaged -v ./internal/agents/kiro/

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// isolateHomeKeepKiroAuth points HOME at a tempdir (isolating AF sids/status + a fresh
// session store) while symlinking kiro's ambient auth + settings so the CLI still logs in.
// We never read or copy the credential — the symlink lets kiro-cli read its OWN files.
func isolateHomeKeepKiroAuth(t *testing.T) {
	t.Helper()
	real, _ := os.UserHomeDir()
	home := t.TempDir()
	if real != "" {
		for _, rel := range []string{".local/share/kiro-cli", ".kiro/settings"} {
			src := filepath.Join(real, rel)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			dst := filepath.Join(home, rel)
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Symlink(src, dst)
		}
	}
	t.Setenv("HOME", home)
}

func waitCompleted(t *testing.T, h *threadHandle) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		switch h.currentState() {
		case agents.TurnCompleted:
			return
		case agents.TurnFailed, agents.TurnUnknown, agents.TurnCancelled:
			t.Fatalf("turn ended abnormally: %s", h.currentState())
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("turn did not complete (state %s)", h.currentState())
}

func TestLiveManagedSpawnPromptResume(t *testing.T) {
	liveGate(t)
	if !LoggedIn() {
		t.Fatal("expected a logged-in kiro (Builder ID) for the live managed test")
	}
	isolateHomeKeepKiroAuth(t)
	work := t.TempDir()

	h := &threadHandle{
		name: "livem1", dir: work, slotSid: "livem-slot-1",
		events: make(chan agents.Event, 64),
	}
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { DropHandle("livem1"); Shutdown() })
	handlesMu.Lock()
	handles["livem1"] = h
	handlesMu.Unlock()

	if err := h.Send(agents.TurnInput{Prompt: "Reply with exactly: LIVE-KIRO-OK", ClientMessageID: "lm1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitState(t, h, agents.TurnRunning) // wait for the turn to start, so spawn's initial Completed is not overtaken
	waitCompleted(t, h)

	sid := h.sid
	if sid == "" || sids.Read("livem-slot-1") != sid {
		t.Fatalf("sid not captured: %q store=%q", sid, sids.Read("livem-slot-1"))
	}
	turns := h.buf.snapshot()
	found := false
	for _, tn := range turns {
		if tn.Role == "assistant" && strings.Contains(tn.Text, "LIVE-KIRO-OK") {
			found = true
		}
	}
	if !found {
		t.Fatalf("assistant turn missing from in-memory transcript: %+v", turns)
	}

	// Track D: was the live context% plus credits captured from the real
	// `_kiro.dev/metadata`? Also confirms ContextFill returns non-nil through the
	// pct-to-token conversion and that window is the real catalog value.
	pct, window, credits, model, ok := ManagedContext("livem1")
	if !ok {
		t.Fatalf("ManagedContext: no live usage captured after a completed turn")
	}
	if pct <= 0 || pct > 100 {
		t.Fatalf("contextUsagePercentage out of range: %v", pct)
	}
	if window <= 0 {
		t.Fatalf("model %q context window not resolved (got %d)", model, window)
	}
	if credits < 0 {
		t.Fatalf("credits negative: %v", credits)
	}
	if c := (agentImpl{}).ContextFill(managedMeta("livem1", work)); c == nil || c.Window != window {
		t.Fatalf("ContextFill did not surface the live context: %+v", c)
	}
	t.Logf("Track D live usage: pct=%.2f%% window=%d credits=%.4f model=%s", pct, window, credits, model)
	// Unlike cursor, kiro also persists the transcript to the v2 JSONL; this confirms the
	// stopped-session fallback.
	if fileTranscript(managedMeta("livem1", work)).Turns == nil {
		t.Fatalf("kiro should have persisted the ACP turns to the v2 JSONL")
	}

	// Resume from a separate process (session/load): end the child cleanly through stdin EOF
	// so .lock is released, spawn again, and confirm with a real prompt that the context and
	// the transcript survived.
	h.mu.Lock()
	oldCmd, oldStdin := h.cmd, h.stdin
	h.alive = false
	h.mu.Unlock()
	stopChild(oldCmd, oldStdin)
	time.Sleep(2 * time.Second) // wait for the graceful exit and the .lock release
	if err := h.spawn(agents.ThreadSettings{}); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if h.sid != sid {
		t.Fatalf("resume changed sid: %q -> %q", sid, h.sid)
	}
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
	if last.Role != "assistant" || !strings.Contains(last.Text, "LIVE-KIRO-OK") {
		t.Fatalf("resume lost context; last turn: %+v", last)
	}
}

// managedMeta builds a managed session.Meta for the persisted-fallback check.
func managedMeta(name, dir string) session.Meta {
	return session.Meta{Name: name, Dir: dir, Kind: session.KindKiro, Driver: session.DriverManaged}
}
