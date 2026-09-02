package main

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// setMcpConv sets the subprocess-global conv id for the test's duration.
func setMcpConv(t *testing.T, conv string) {
	t.Helper()
	old := mcpConvID
	mcpConvID = conv
	t.Cleanup(func() { mcpConvID = old })
}

// shrinkApprovalTunables makes the poll/timeout tiny so wait tests run fast, restoring
// the production values afterward.
func shrinkApprovalTunables(t *testing.T) {
	t.Helper()
	to, poll, age := bridgeApprovalTimeout, bridgeApprovalPoll, bridgeApprovalMaxAge
	t.Cleanup(func() { bridgeApprovalTimeout, bridgeApprovalPoll, bridgeApprovalMaxAge = to, poll, age })
	bridgeApprovalTimeout = 500 * time.Millisecond
	bridgeApprovalPoll = 2 * time.Millisecond
}

// TestOperatorTurnMarker: the marker distinguishes a Discord-driven operator turn (armed)
// from Console chat / a stale/expired/other-conv turn (not armed) — the signal the gate
// keys on.
func TestOperatorTurnMarker(t *testing.T) {
	withTempHome(t)

	if operatorTurnArmed("c1") {
		t.Fatal("no marker → not armed")
	}
	armOperatorTurn("c1")
	if !operatorTurnArmed("c1") {
		t.Fatal("armed for its own conv")
	}
	if operatorTurnArmed("c2") {
		t.Fatal("must not arm a different conv")
	}
	if operatorTurnArmed("") {
		t.Fatal("empty conv is never armed")
	}
	disarmOperatorTurn("c1")
	if operatorTurnArmed("c1") {
		t.Fatal("disarmed → not armed")
	}

	// Two unattended turns in parallel (different convs): each holds its own marker, and
	// one turn finishing must not disarm the other (the fail-open bug this test pins).
	armOperatorTurn("c1")
	armOperatorTurn("c2")
	if !operatorTurnArmed("c1") || !operatorTurnArmed("c2") {
		t.Fatal("both parallel turns must be armed")
	}
	disarmOperatorTurn("c1")
	if operatorTurnArmed("c1") {
		t.Fatal("c1 disarmed → not armed")
	}
	if !operatorTurnArmed("c2") {
		t.Fatal("disarming c1 must not disarm c2")
	}
	disarmOperatorTurn("c2")

	// An expired marker (process died without disarming) is treated as not armed.
	_ = operatorTurnStore.Write(operatorTurnKey("c1"), operatorTurnMarker{Conv: "c1", ExpiresAt: chatx.NowMs() - 1})
	if operatorTurnArmed("c1") {
		t.Fatal("expired marker must not arm")
	}
}

// TestBridgeApprovalGateNotArmed: outside a Discord-driven operator turn the gate is a
// transparent no-op (Console chat and non-operator conversations are unaffected).
func TestBridgeApprovalGateNotArmed(t *testing.T) {
	withTempHome(t)
	setMcpConv(t, "c1") // no marker written → not armed
	if err := bridgeApprovalGate("delete_session", "s7"); err != nil {
		t.Fatalf("gate must be a no-op when not armed, got %v", err)
	}
}

// TestBridgeApprovalGateFailClosed: armed but with no operator thread/connection to post
// into, the gate refuses to run the action (fail closed) and leaves no orphan record.
func TestBridgeApprovalGateFailClosed(t *testing.T) {
	withTempHome(t)
	setMcpConv(t, "c1")
	armOperatorTurn("c1") // armed, but no bridge-operator.json / secrets exist

	err := bridgeApprovalGate("delete_session", "s7")
	if !errors.Is(err, errApprovalUndeliverable) {
		t.Fatalf("want errApprovalUndeliverable, got %v", err)
	}
	if entries, _ := readApprovalDir(t); len(entries) != 0 {
		t.Fatalf("fail-closed must leave no record, found %v", entries)
	}
}

// TestWaitApprovalDecision covers the cross-process handshake the subprocess polls: an
// approve resolves to nil, a reject to errApprovalRejected, silence to errApprovalTimeout —
// and every path removes the record.
func TestWaitApprovalDecision(t *testing.T) {
	withTempHome(t)
	shrinkApprovalTunables(t)

	t.Run("approve", func(t *testing.T) {
		id := chatx.RandUUID()
		_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
		go func() {
			time.Sleep(10 * time.Millisecond)
			rec, _ := bridgeApprovals.Read(id)
			rec.Decision = "approve"
			_ = bridgeApprovals.Write(id, rec)
		}()
		if err := waitApprovalDecision(id); err != nil {
			t.Fatalf("approve → %v", err)
		}
		if _, ok := bridgeApprovals.Read(id); ok {
			t.Fatal("record must be removed after decision")
		}
	})

	t.Run("reject", func(t *testing.T) {
		id := chatx.RandUUID()
		_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
		go func() {
			time.Sleep(10 * time.Millisecond)
			rec, _ := bridgeApprovals.Read(id)
			rec.Decision = "reject"
			_ = bridgeApprovals.Write(id, rec)
		}()
		if err := waitApprovalDecision(id); !errors.Is(err, errApprovalRejected) {
			t.Fatalf("reject → %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		id := chatx.RandUUID()
		_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
		if err := waitApprovalDecision(id); !errors.Is(err, errApprovalTimeout) {
			t.Fatalf("timeout → %v", err)
		}
		if _, ok := bridgeApprovals.Read(id); ok {
			t.Fatal("record must be removed after timeout")
		}
	})
}

// TestBridgeApprovalDecision: the daemon-side writer records a first decision, reports
// staleness for a missing record, and refuses to overwrite an already-decided one.
func TestBridgeApprovalDecision(t *testing.T) {
	withTempHome(t)

	// Missing record → expired/handled message, no error.
	if msg, err := bridgeApprovalDecision("nope", "approve", false); err != nil || msg == "" {
		t.Fatalf("missing record: msg=%q err=%v", msg, err)
	}

	id := chatx.RandUUID()
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
	msg, err := bridgeApprovalDecision(id, "approve", false)
	if err != nil || msg == "" {
		t.Fatalf("first decision: msg=%q err=%v", msg, err)
	}
	if rec, _ := bridgeApprovals.Read(id); rec.Decision != "approve" {
		t.Fatalf("decision not persisted: %+v", rec)
	}
	// A second click can't flip it.
	if msg2, _ := bridgeApprovalDecision(id, "reject", false); msg2 == "" {
		t.Fatal("second click should report already-handled")
	}
	if rec, _ := bridgeApprovals.Read(id); rec.Decision != "approve" {
		t.Fatalf("already-decided record must not change: %+v", rec)
	}
}

// TestBridgeApprovalRoundTrip: subprocess waits, daemon approves, subprocess proceeds —
// the full two-process handshake over the shared record.
func TestBridgeApprovalRoundTrip(t *testing.T) {
	withTempHome(t)
	shrinkApprovalTunables(t)

	id := chatx.RandUUID()
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
	done := make(chan error, 1)
	go func() { done <- waitApprovalDecision(id) }() // the subprocess side

	time.Sleep(10 * time.Millisecond)
	if _, err := bridgeApprovalDecision(id, "approve", false); err != nil { // the daemon side
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("round-trip approve → %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subprocess never observed the decision")
	}
}

// TestSessionIsShell: only the raw shell kind gates send_to_session.
func TestSessionIsShell(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "sh1", Dir: t.TempDir(), Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "cl1", Dir: t.TempDir(), Kind: session.KindClaude})
	if !sessionIsShell("sh1") {
		t.Fatal("shell session must gate")
	}
	if sessionIsShell("cl1") {
		t.Fatal("a claude session must not gate")
	}
	if sessionIsShell("ghost") {
		t.Fatal("a missing session must not gate")
	}
}

// TestSweepStaleApprovals: records older than the sweep horizon are reaped; fresh ones stay.
func TestSweepStaleApprovals(t *testing.T) {
	withTempHome(t)
	old := bridgeApprovalMaxAge
	t.Cleanup(func() { bridgeApprovalMaxAge = old })
	bridgeApprovalMaxAge = time.Hour

	stale := chatx.RandUUID()
	fresh := chatx.RandUUID()
	_ = bridgeApprovals.Write(stale, bridgeApprovalRec{ID: stale, CreatedAt: chatx.NowMs() - 2*time.Hour.Milliseconds()})
	_ = bridgeApprovals.Write(fresh, bridgeApprovalRec{ID: fresh, CreatedAt: chatx.NowMs()})

	sweepStaleApprovals()

	if _, ok := bridgeApprovals.Read(stale); ok {
		t.Fatal("stale record must be swept")
	}
	if _, ok := bridgeApprovals.Read(fresh); !ok {
		t.Fatal("fresh record must survive")
	}
}

// readApprovalDir lists the approval record file names (empty when the dir is absent).
func readApprovalDir(t *testing.T) ([]string, error) {
	t.Helper()
	entries, err := os.ReadDir(bridgeApprovals.Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
