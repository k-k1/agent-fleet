package claude

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// stopHookPane is what claude keeps drawing while the Stop hook runs: a busy pane although
// the hook already persisted idle.
var stopHookPane = tmuxx.PaneRead{OK: true, Busy: true}

func ledgerSession(t *testing.T) (session.Meta, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	m := session.Meta{Dir: t.TempDir(), Name: "ledger", Kind: session.KindClaude}
	old := readPane
	readPane = func(string) tmuxx.PaneRead { return stopHookPane }
	t.Cleanup(func() { readPane = old })
	oldCont := stopContinued
	stopContinued = func(string, time.Time) bool { return false }
	t.Cleanup(func() { stopContinued = oldCont })
	return m, session.UUID(m.Dir, m.Name)
}

func TestWireLiveStopHookSpinnerDoesNotReopenClosedTurn(t *testing.T) {
	m, sid := ledgerSession(t)
	status.PersistOpen(sid, "working", "P1")
	status.PersistTurnEndFor(sid, "idle", "", "P1")
	if got := (agentImpl{}).WireLive(m, true).State; got != "idle" {
		t.Fatalf("busy pane after Stop: got %q, want idle", got)
	}
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("record was rewritten: %+v", st)
	}
}

func TestWireLiveBusyPaneWithoutRecordStillGoesWorking(t *testing.T) {
	m, sid := ledgerSession(t)
	if got := (agentImpl{}).WireLive(m, true).State; got != "working" {
		t.Fatalf("no record: got %q, want working", got)
	}
	if st, _ := status.Read(sid); st.State != "working" {
		t.Fatalf("working not persisted: %+v", st)
	}
}

func TestPaneMayReopen(t *testing.T) {
	_, sid := ledgerSession(t)
	cont := false
	stopContinued = func(string, time.Time) bool { return cont }

	// A record without prompt_id (older agent, heal, hook-less kind): the pane decides.
	status.PersistTurnEnd(sid, "idle")
	if !PaneMayReopen(sid) {
		t.Fatal("closed turn without a prompt id must leave the decision to the pane")
	}
	status.Persist(sid, "idle") // not a turn end at all
	if !PaneMayReopen(sid) {
		t.Fatal("idle that is not a turn end must leave the decision to the pane")
	}

	status.PersistTurnEndFor(sid, "idle", "", "P1")
	if PaneMayReopen(sid) {
		t.Fatal("a turn closed by Stop was reopened from the pane")
	}
	cont = true // #1600: another Stop hook blocked this one
	if !PaneMayReopen(sid) {
		t.Fatal("a blocked Stop must keep the turn in progress")
	}

	status.Remove(sid) // a heal removed the record
	if !PaneMayReopen(sid) {
		t.Fatal("no record must leave the decision to the pane")
	}
}

func TestWireLiveBlockedStopStaysInProgress(t *testing.T) {
	m, sid := ledgerSession(t)
	stopContinued = func(string, time.Time) bool { return true }
	status.PersistTurnEndFor(sid, "idle", "", "P1")
	if got := (agentImpl{}).WireLive(m, true).State; got != "working" {
		t.Fatalf("blocked Stop: got %q, want working", got)
	}
}

func TestNewPromptAfterStopIsWorking(t *testing.T) {
	m, sid := ledgerSession(t)
	status.PersistTurnEndFor(sid, "idle", "", "P1")
	status.PersistOpen(sid, "working", "P2") // the next UserPromptSubmit
	if got := (agentImpl{}).WireLive(m, true).State; got != "working" {
		t.Fatalf("new prompt: got %q, want working", got)
	}
}
