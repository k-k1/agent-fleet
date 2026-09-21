package muse

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Live verification against a real `muse serve`, run only with MUSE_LIVE=1 (the shape kiro's
// live_test.go and codex's live_drift_test.go already use). Not run in CI: the binary is
// proprietary and not in the image.
//
// It covers exactly what msptest cannot: that the spawn argv, the child environment, the
// handshake and session/start actually work against the vendor's own host. It stops short of
// a turn — `--provider echo` is an exec startup flag with no serve equivalent, so a turn
// costs a member's subscription quota (ADR 0095 P2-1).
func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("MUSE_LIVE") != "1" {
		t.Skip("MUSE_LIVE=1 to run against a real muse serve")
	}
	if os.Getenv("AGENT_MUSE_BIN") == "" {
		if _, err := exec.LookPath("muse"); err != nil {
			t.Skip("no muse binary: set AGENT_MUSE_BIN")
		}
	}
}

// liveMeta builds a session in a throwaway HOME. The throwaway is the point, not tidiness:
// without the foreign-context clamp a real turn ships the member's own ~/.claude/CLAUDE.md to
// Meta, so a live test must never run against the member's home.
func liveMeta(t *testing.T) session.Meta {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	dir := filepath.Join(home, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "muse-live-" + filepath.Base(home)
	t.Cleanup(func() { DropHandle(name) })
	return session.Meta{Kind: session.KindMuse, Name: name, Dir: dir, Driver: session.DriverManaged}
}

func TestLiveResumeStartsAHostAndASession(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !ManagedAlive(m.Name) {
		t.Error("the session is not alive after Resume")
	}

	h := th.(*threadHandle)
	h.mu.Lock()
	sid, path := h.sid, h.path
	h.mu.Unlock()
	if sid == "" {
		t.Fatal("no muse session id after Resume")
	}
	// Decision 4: the path is RECORDED, not computed — the store is partitioned by date, so
	// deriving it means guessing which day the session was created.
	if path == "" {
		t.Fatal("session/start returned no transcript path")
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("the recorded path does not exist: %v", err)
	}

	stored, ok := readSession(slotSid(m))
	if !ok || stored.ID != sid || stored.Path != path {
		t.Errorf("the session was not persisted for the next resume: %+v (ok=%v)", stored, ok)
	}
}

// A second Resume must reattach to the same conversation, not mint a second one — and it must
// reuse the live host rather than spawning a second child for one session.
func TestLiveResumeIsIdempotent(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	first, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	second, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if first != second {
		t.Error("the second Resume built a new handle instead of reusing the live one")
	}
}

// Dropping the handle and resuming again has to RELOAD the stored session, which is the path
// a workspace restart takes. A fresh id here would mean the member's history silently split.
func TestLiveResumeAfterDropReloadsTheSameSession(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	h := th.(*threadHandle)
	h.mu.Lock()
	firstSid := h.sid
	h.mu.Unlock()

	DropHandle(m.Name)
	if ManagedAlive(m.Name) {
		t.Fatal("the session is still alive after DropHandle")
	}

	th2, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume after drop: %v", err)
	}
	h2 := th2.(*threadHandle)
	h2.mu.Lock()
	secondSid := h2.sid
	h2.mu.Unlock()
	if secondSid != firstSid {
		t.Errorf("the reload started a new conversation: %s then %s", firstSid, secondSid)
	}
}

// A dead child must turn into a dead handle, or the Console shows a live session with nothing
// behind it and the next Send hangs until its own timeout.
func TestLiveChildDeathMarksTheHandleDead(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	h := th.(*threadHandle)
	h.mu.Lock()
	cmd := h.cmd
	h.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("no child process")
	}
	_ = cmd.Process.Kill()

	deadline := time.After(10 * time.Second)
	for ManagedAlive(m.Name) {
		select {
		case <-deadline:
			t.Fatal("the handle is still alive 10s after the child was killed")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err == nil {
		t.Error("Send succeeded against a dead host")
	}
}
