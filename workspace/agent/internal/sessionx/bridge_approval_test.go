package sessionx

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// setMcpConv sets the subprocess-global conv id for the test's duration.
func setMcpConv(t *testing.T, conv string) {
	t.Helper()
	// mcpConvID is now read through a seam rather than a var, so what a test substitutes is the
	// wiring itself (deps.MCPConvID). Never add a test-only path around it: unless this goes
	// through the single opening production uses, a mutation that breaks the wiring passes here.
	old := deps.MCPConvID
	deps.MCPConvID = func() string { return conv }
	t.Cleanup(func() { deps.MCPConvID = old })
}

// Approval windows for the wait tests. A case that expects a decision gets the generous
// one: it returns as soon as the decision is observed, so the size costs nothing when green,
// while under load a short one fails the case: the decision write can land late, or the
// waiter can stall between its last pending read and the deadline check (seen: a 500 ms
// window read as a timeout under package-parallel load). Only the case that expects the
// timeout uses the short one, which keeps that path fast.
const (
	approvalDecidedWindow = 30 * time.Second
	approvalTimeoutWindow = 500 * time.Millisecond
)

// shrinkApprovalTunables sets the wait window and a tiny poll so wait tests run fast,
// restoring the production values afterward.
func shrinkApprovalTunables(t *testing.T, timeout time.Duration) {
	t.Helper()
	to, poll, age := bridgeApprovalTimeout, bridgeApprovalPoll, bridgeApprovalMaxAge
	t.Cleanup(func() { bridgeApprovalTimeout, bridgeApprovalPoll, bridgeApprovalMaxAge = to, poll, age })
	bridgeApprovalTimeout = timeout
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
	ArmOperatorTurn("c1")
	if !operatorTurnArmed("c1") {
		t.Fatal("armed for its own conv")
	}
	if operatorTurnArmed("c2") {
		t.Fatal("must not arm a different conv")
	}
	if operatorTurnArmed("") {
		t.Fatal("empty conv is never armed")
	}
	DisarmOperatorTurn("c1")
	if operatorTurnArmed("c1") {
		t.Fatal("disarmed → not armed")
	}

	// Two unattended turns in parallel (different convs): each holds its own marker, and
	// one turn finishing must not disarm the other (the fail-open bug this test pins).
	ArmOperatorTurn("c1")
	ArmOperatorTurn("c2")
	if !operatorTurnArmed("c1") || !operatorTurnArmed("c2") {
		t.Fatal("both parallel turns must be armed")
	}
	DisarmOperatorTurn("c1")
	if operatorTurnArmed("c1") {
		t.Fatal("c1 disarmed → not armed")
	}
	if !operatorTurnArmed("c2") {
		t.Fatal("disarming c1 must not disarm c2")
	}
	DisarmOperatorTurn("c2")

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
	if err := BridgeApprovalGate("delete_session", "s7"); err != nil {
		t.Fatalf("gate must be a no-op when not armed, got %v", err)
	}
}

// TestBridgeApprovalGateFailClosed: armed but with no operator thread/connection to post
// into, the gate refuses to run the action (fail closed) and leaves no orphan record.
func TestBridgeApprovalGateFailClosed(t *testing.T) {
	withTempHome(t)
	setMcpConv(t, "c1")
	ArmOperatorTurn("c1") // armed, but no bridge-operator.json / secrets exist

	err := BridgeApprovalGate("delete_session", "s7")
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

	t.Run("approve", func(t *testing.T) {
		shrinkApprovalTunables(t, approvalDecidedWindow)
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
		shrinkApprovalTunables(t, approvalDecidedWindow)
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
		shrinkApprovalTunables(t, approvalTimeoutWindow)
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
	shrinkApprovalTunables(t, approvalDecidedWindow)

	id := chatx.RandUUID()
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
	done := make(chan error, 1)
	go func() { done <- waitApprovalDecision(id) }() // the subprocess side

	time.Sleep(10 * time.Millisecond)
	if _, err := bridgeApprovalDecision(id, "approve", false); err != nil { // the daemon side
		t.Fatal(err)
	}
	// No wall-clock guard of its own: the waiter is bounded by the approval window, and a
	// shorter guard here would reintroduce the flake.
	if err := <-done; err != nil {
		t.Fatalf("round-trip approve → %v", err)
	}
}

// TestSessionIsShell: only the raw shell kind gates send_to_session.
func TestSessionIsShell(t *testing.T) {
	withTempHome(t)
	session.WriteMeta(session.Meta{Name: "sh1", Dir: t.TempDir(), Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "cl1", Dir: t.TempDir(), Kind: session.KindClaude})
	if !SessionIsShell("sh1") {
		t.Fatal("shell session must gate")
	}
	if SessionIsShell("cl1") {
		t.Fatal("a claude session must not gate")
	}
	if SessionIsShell("ghost") {
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
		if e.IsDir() { // fstore.StagingSubdir: a record is a file
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

// TestBridgeApprovalDecisionRacesDeadline pins the interleaving behind #2964: the waiter's
// deadline lands between the daemon's pending check and its write. Whatever the order the
// lock settles on, the user's message and the gate's result must agree: "approved" only if
// the waiter returned nil, "expired" only if it timed out — and an expired record is never
// re-created.
func TestBridgeApprovalDecisionRacesDeadline(t *testing.T) {
	withTempHome(t)

	for _, choice := range []string{"approve", "reject"} {
		t.Run(choice, func(t *testing.T) {
			id := chatx.RandUUID()
			_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})

			waiter := make(chan error, 1)
			locked := make(chan struct{})
			bridgeApprovalBetweenReadAndWrite = func(id string) {
				go func() {
					waiter <- expireApproval(id)
				}()
				// With the lock the waiter blocks until the decision is written, so this
				// wait only runs out on the fixed path; without it the waiter removes the
				// record right here, which is the bug.
				select {
				case err := <-waiter:
					waiter <- err
				case <-time.After(100 * time.Millisecond):
				}
				close(locked)
			}
			t.Cleanup(func() { bridgeApprovalBetweenReadAndWrite = nil })

			msg, err := bridgeApprovalDecision(id, choice, true)
			if err != nil {
				t.Fatal(err)
			}
			<-locked
			werr := <-waiter

			approved := strings.Contains(msg, "Approved")
			rejected := strings.Contains(msg, "Rejected")
			expired := strings.Contains(msg, "expired")
			switch {
			case approved && werr != nil, rejected && !errors.Is(werr, errApprovalRejected):
				t.Fatalf("user told %q but the gate returned %v", msg, werr)
			case expired && !errors.Is(werr, errApprovalTimeout):
				t.Fatalf("user told %q but the gate returned %v", msg, werr)
			case !approved && !rejected && !expired:
				t.Fatalf("unexpected message %q", msg)
			}
			if _, ok := bridgeApprovals.Read(id); ok {
				t.Fatal("record must not outlive the gate's result")
			}
		})
	}
}

// A click that arrives after the waiter gave up reports "expired" and leaves no record.
func TestBridgeApprovalDecisionAfterTimeout(t *testing.T) {
	withTempHome(t)
	id := chatx.RandUUID()
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
	if err := expireApproval(id); !errors.Is(err, errApprovalTimeout) {
		t.Fatalf("expire → %v", err)
	}
	for _, choice := range []string{"approve", "reject"} {
		msg, err := bridgeApprovalDecision(id, choice, true)
		if err != nil || !strings.Contains(msg, "expired") {
			t.Fatalf("%s after timeout: msg=%q err=%v", choice, msg, err)
		}
		if _, ok := bridgeApprovals.Read(id); ok {
			t.Fatalf("%s re-created the record", choice)
		}
	}
}

// A decision that landed before the deadline step is honored rather than discarded.
func TestExpireApprovalHonorsEarlierDecision(t *testing.T) {
	withTempHome(t)
	id := chatx.RandUUID()
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs(), Decision: "approve"})
	if err := expireApproval(id); err != nil {
		t.Fatalf("decided-before-deadline → %v", err)
	}
	if _, ok := bridgeApprovals.Read(id); ok {
		t.Fatal("record must be removed")
	}
}

func failApprovalLock(t *testing.T) {
	t.Helper()
	orig := lockApprovals
	t.Cleanup(func() { lockApprovals = orig })
	lockApprovals = func() (func(), error) { return nil, errors.New("injected lock failure") }
}

// When the waiter cannot take the lock it must not remove the record (that is the race):
// it still times out, and the record's Deadline makes a later click report "expired".
func TestExpireApprovalLockFailureKeepsRecord(t *testing.T) {
	withTempHome(t)
	failApprovalLock(t)
	id := chatx.RandUUID()
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs(), Deadline: chatx.NowMs() - 1})
	if err := expireApproval(id); !errors.Is(err, errApprovalTimeout) {
		t.Fatalf("expire → %v", err)
	}
	if _, ok := bridgeApprovals.Read(id); !ok {
		t.Fatal("record must be left alone without the lock")
	}
	lockApprovals = lockApprovalsFile
	msg, err := bridgeApprovalDecision(id, "approve", true)
	if err != nil || !strings.Contains(msg, "expired") {
		t.Fatalf("late click: msg=%q err=%v", msg, err)
	}
	if rec, _ := bridgeApprovals.Read(id); rec.Decision != "" {
		t.Fatalf("late click wrote a decision: %+v", rec)
	}
}

// The daemon fails closed when it cannot take the lock: error, nothing written.
func TestBridgeApprovalDecisionLockFailure(t *testing.T) {
	withTempHome(t)
	failApprovalLock(t)
	id := chatx.RandUUID()
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs()})
	if _, err := bridgeApprovalDecision(id, "approve", true); err == nil {
		t.Fatal("want an error")
	}
	if rec, _ := bridgeApprovals.Read(id); rec.Decision != "" {
		t.Fatalf("decision written without the lock: %+v", rec)
	}
}

// A holder that never lets go must not stall the acquirer past the bounded wait.
func TestLockApprovalsIsBounded(t *testing.T) {
	withTempHome(t)
	hold, err := lockApprovalsFile()
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	w := bridgeApprovalLockWait
	t.Cleanup(func() { bridgeApprovalLockWait = w })
	bridgeApprovalLockWait = 50 * time.Millisecond
	start := time.Now()
	if unlock, err := lockApprovalsFile(); err == nil {
		unlock()
		t.Fatal("second acquisition must fail while held")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("acquisition was not bounded")
	}
}

// A daemon stalled between its check and its write past the deadline must report
// "expired" and leave no decision behind.
func TestBridgeApprovalDecisionStalledPastDeadline(t *testing.T) {
	withTempHome(t)
	id := chatx.RandUUID()
	dl := chatx.NowMs() + 20
	_ = bridgeApprovals.Write(id, bridgeApprovalRec{ID: id, CreatedAt: chatx.NowMs(), Deadline: dl})
	bridgeApprovalBetweenReadAndWrite = func(string) {
		for chatx.NowMs() < dl {
		}
	}
	t.Cleanup(func() { bridgeApprovalBetweenReadAndWrite = nil })
	msg, err := bridgeApprovalDecision(id, "approve", true)
	if err != nil || !strings.Contains(msg, "expired") {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
	if _, ok := bridgeApprovals.Read(id); ok {
		t.Fatal("record must not keep a late decision")
	}
}
