package muse

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func toolItem(id, tool string, status msp.ItemStatus, rev int64) msp.Item {
	return msp.Item{ItemID: id, Kind: msp.ItemKindToolCall, Tool: &tool, Status: status, Revision: rev}
}

// waitBg blocks until the session's background answer is want. Items travel the fake host's
// pipe to the read goroutine, so the answer lands a moment after Notify returns.
func waitBg(t *testing.T, name string, want bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		busy, reason := BackgroundWork(name)
		if busy == want {
			return reason
		}
		if time.Now().After(deadline) {
			t.Fatalf("BackgroundWork(%s) = %v, want %v", name, busy, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// bgSession is a live handle with one finished turn that left a `bash` running, registered
// under a Meta WireLive can read.
func bgSession(t *testing.T) (*threadHandle, *msptest.Host, session.Meta) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Kind: session.KindMuse, Name: "bg-" + t.Name(), Dir: t.TempDir(), Driver: session.DriverManaged}
	h := &threadHandle{name: m.Name, slotSid: slotSid(m)}
	host := newTestHandle(t, h)
	registerHandle(t, m.Name, h)

	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-1", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	host.Notify(msp.NotificationItemStarted, msp.ItemStartedParams{Item: toolItem("bash-1", "bash", msp.ItemStatusInProgress, 1)})
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: "t-1", SessionID: h.sid, Terminal: msp.TurnTerminalCompleted})
	waitEvent(t, h, agents.TurnCompleted)
	waitBg(t, m.Name, true)
	return h, host, m
}

// The measured case: a `bash` the model left running (yield_time_ms, polled by later turns)
// stays inProgress after its turn ended. The session list must say so, with the shell wording.
func TestIdleWithARunningToolCallIsBackgroundBusy(t *testing.T) {
	_, _, m := bgSession(t)
	li := New().WireLive(m, true)
	if li.State != "idle" {
		t.Fatalf("state = %q, want idle", li.State)
	}
	if !li.BackgroundBusy || li.BackgroundBusyReason != bgReasonShell {
		t.Errorf("WireLive = busy %v reason %q, want busy with %q", li.BackgroundBusy, li.BackgroundBusyReason, bgReasonShell)
	}
	// The /messages header asks through the same interface; the two must not disagree.
	br, ok := New().(agents.BackgroundReporter)
	if !ok {
		t.Fatal("muse does not implement agents.BackgroundReporter")
	}
	if busy, _ := br.BackgroundWork(m); !busy {
		t.Error("BackgroundReporter says not busy while WireLive says busy")
	}
}

func TestTerminalItemClearsBackgroundBusy(t *testing.T) {
	for _, st := range []msp.ItemStatus{msp.ItemStatusCompleted, msp.ItemStatusFailed, msp.ItemStatusCancelled, msp.ItemStatusTimedOut} {
		t.Run(string(st), func(t *testing.T) {
			_, host, m := bgSession(t)
			host.Notify(msp.NotificationItemCompleted, msp.ItemCompletedParams{Item: toolItem("bash-1", "bash", st, 2)})
			waitBg(t, m.Name, false)
			if li := New().WireLive(m, true); li.BackgroundBusy {
				t.Errorf("WireLive still busy after %s", st)
			}
		})
	}
}

// A dead host takes its tasks with it; nothing will ever send their terminal revision, so the
// set has to go with the host or the session reads busy for good.
func TestHostLossClearsBackgroundBusy(t *testing.T) {
	h, _, m := bgSession(t)
	h.hostLost(h.cl)
	if busy, _ := BackgroundWork(m.Name); busy {
		t.Error("still busy after the host was lost")
	}
	// The set itself must go, not only the answer: alive=false hides it, but the next spawn
	// sets alive again, and a leftover entry would then stand for a task of the dead host.
	h.mu.Lock()
	n := len(h.bg)
	h.mu.Unlock()
	if n != 0 {
		t.Errorf("%d entries of the dead host's work survived", n)
	}
}

// session/closed is broadcast to every connection: another session's close must not clear
// this one, its own must.
func TestSessionClosedClearsOnlyItsOwnSession(t *testing.T) {
	h, host, m := bgSession(t)
	host.Notify(msp.NotificationSessionClosed, msp.SessionClosedParams{SessionID: "01a0c1d6-0000-7000-8000-0000000000ff", Reason: "idle"})
	// A marker item after the foreign close proves the close was processed before we read.
	host.Notify(msp.NotificationItemStarted, msp.ItemStartedParams{Item: toolItem("marker", "read_file", msp.ItemStatusInProgress, 1)})
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		_, seen := h.bg["marker"]
		_, kept := h.bg["bash-1"]
		h.mu.Unlock()
		if seen {
			if !kept {
				t.Fatal("another session's close cleared this session's work")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("marker item never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	host.Notify(msp.NotificationSessionClosed, msp.SessionClosedParams{SessionID: h.sid, Reason: "idle"})
	waitBg(t, m.Name, false)
}

func TestBackgroundReasonByTool(t *testing.T) {
	for _, tc := range []struct {
		tool, want string
	}{
		{"bash", bgReasonShell},
		{"bash_input", bgReasonShell},
		{"read_file", bgReasonProcess},
		{"mcp__af__send_to_peer_session", bgReasonProcess},
		// A pending question holds the turn; it is not work behind an idle prompt.
		{"request_user_input", ""},
	} {
		if got := bgReason(toolItem("x", tc.tool, msp.ItemStatusInProgress, 1)); got != tc.want {
			t.Errorf("bgReason(%s) = %q, want %q", tc.tool, got, tc.want)
		}
	}
	if got := bgReason(msp.Item{ItemID: "m", Kind: msp.ItemKindAgentMessage, Status: msp.ItemStatusInProgress}); got != "" {
		t.Errorf("an agent message counted as background work: %q", got)
	}
}

// A resume rebuilds from the HOST's fold in the session/resume result — AF's own store can hold
// an inProgress revision whose terminal one it never received.
func TestResumeRebuildsFromTheHostsHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		hist func(items []msp.Item) any
	}{
		{"inline", func(items []msp.Item) any {
			return map[string]any{"mode": "inline", "items": items}
		}},
		{"snapshot", func(items []msp.Item) any {
			return map[string]any{"mode": "snapshot", "snapshot": map[string]any{
				"schemaVersion": 1, "viewCursor": "c1",
				"state": map[string]any{"items": items, "approvalMode": map[string]any{}, "tokenUsage": map[string]any{},
					"pendingApprovals": []any{}, "pendingUserInputs": []any{}, "queuedTurns": []any{}},
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000b1"}
			host := newTestHandle(t, h)
			name := "bg-resume-" + tc.name
			registerHandle(t, name, h)
			writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000b2", Path: "/tmp/old.jsonl"})
			items := []msp.Item{
				toolItem("running", "bash", msp.ItemStatusInProgress, 1),
				toolItem("done", "bash", msp.ItemStatusCompleted, 2),
				toolItem("question", "request_user_input", msp.ItemStatusInProgress, 1),
			}
			sess := map[string]any{"sessionId": "01a0c1d6-0000-7000-8000-0000000000b2", "path": "/tmp/s.jsonl", "status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 1}
			host.Handle(msp.MethodSessionResume, func(msptest.Message) (any, *msp.Error) {
				return map[string]any{"session": sess, "history": tc.hist(items), "pendingRequests": []any{}, "viewCursor": "c1"}, nil
			})
			if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
				t.Fatalf("openSession: %v", err)
			}
			h.mu.Lock()
			got := len(h.bg)
			_, running := h.bg["running"]
			h.mu.Unlock()
			if got != 1 || !running {
				t.Fatalf("rebuilt set = %d entries (running=%v), want only the running bash", got, running)
			}
			if busy, reason := BackgroundWork(name); !busy || reason != bgReasonShell {
				t.Errorf("BackgroundWork = %v %q after resume", busy, reason)
			}
		})
	}
}

// A host that crashed never wrote its tasks' terminal records ("a crash emits nothing"), so its
// fold can show a dead task inProgress on the next host. A rebuilt entry the new host has not
// mentioned by the end of a turn is dropped; one it has updated since is live and stays.
func TestResumedEntriesTheHostNeverMentionsDropAtTurnEnd(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	name := "bg-orphan"
	registerHandle(t, name, h)
	h.mu.Lock()
	h.rebuildBgLocked(msp.SessionHistory{Mode: msp.HistoryModeInline, Items: []msp.Item{
		toolItem("orphan", "bash", msp.ItemStatusInProgress, 1),
		toolItem("alive", "bash", msp.ItemStatusInProgress, 1),
	}})
	h.mu.Unlock()
	host.Notify(msp.NotificationItemUpdated, msp.ItemCompletedParams{Item: toolItem("alive", "bash", msp.ItemStatusInProgress, 2)})
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{TurnID: "t-2", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: "t-2", SessionID: h.sid, Terminal: msp.TurnTerminalCompleted})
	waitEvent(t, h, agents.TurnCompleted)
	h.mu.Lock()
	_, orphan := h.bg["orphan"]
	_, alive := h.bg["alive"]
	h.mu.Unlock()
	if orphan {
		t.Error("an orphan from the previous host survived a turn on the new one")
	}
	if !alive {
		t.Error("a task the new host updated was dropped")
	}
}

// A member who resumes and sends nothing produces no turn, so the turn-end drop never runs:
// the grace alone has to retire an orphan, or the session reads busy for good. A task the new
// host confirms live is no longer a rebuilt entry and outlives the grace.
func TestResumedEntriesExpireWithoutATurnUnlessConfirmedLive(t *testing.T) {
	old := resumeBgGrace
	resumeBgGrace = 50 * time.Millisecond
	t.Cleanup(func() { resumeBgGrace = old })

	h := &threadHandle{}
	host := newTestHandle(t, h)
	name := "bg-grace"
	registerHandle(t, name, h)
	h.mu.Lock()
	h.rebuildBgLocked(msp.SessionHistory{Mode: msp.HistoryModeInline, Items: []msp.Item{
		toolItem("orphan", "bash", msp.ItemStatusInProgress, 1),
		toolItem("alive", "read_file", msp.ItemStatusInProgress, 1),
	}})
	h.mu.Unlock()
	if busy, reason := BackgroundWork(name); !busy || reason != bgReasonShell {
		t.Fatalf("inside the grace: BackgroundWork = %v %q, want busy shell", busy, reason)
	}

	host.Notify(msp.NotificationItemUpdated, msp.ItemCompletedParams{Item: toolItem("alive", "read_file", msp.ItemStatusInProgress, 2)})
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		e, ok := h.bg["alive"]
		h.mu.Unlock()
		if ok && e.rebuiltAt.IsZero() {
			break // the confirmation has landed
		}
		if time.Now().After(deadline) {
			t.Fatal("the live confirmation never reached the set")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// No turn from here on: only time passes.
	time.Sleep(3 * resumeBgGrace)
	busy, reason := BackgroundWork(name)
	if !busy || reason != bgReasonProcess {
		t.Errorf("after the grace: BackgroundWork = %v %q, want only the confirmed %q", busy, reason, bgReasonProcess)
	}
	h.mu.Lock()
	_, orphan := h.bg["orphan"]
	h.mu.Unlock()
	if orphan {
		t.Error("the unconfirmed orphan survived its grace with no turn")
	}

	// Once the confirmed task finishes, nothing is left: the session reads idle again.
	host.Notify(msp.NotificationItemCompleted, msp.ItemCompletedParams{Item: toolItem("alive", "read_file", msp.ItemStatusCompleted, 3)})
	waitBg(t, name, false)
}

// A resume with only an orphan and no turn afterwards: busy at first, idle once the grace ends.
func TestResumeWithNoTurnGoesIdleAfterTheGrace(t *testing.T) {
	old := resumeBgGrace
	resumeBgGrace = 50 * time.Millisecond
	t.Cleanup(func() { resumeBgGrace = old })

	h := &threadHandle{}
	newTestHandle(t, h)
	name := "bg-grace-orphan"
	registerHandle(t, name, h)
	h.mu.Lock()
	h.rebuildBgLocked(msp.SessionHistory{Mode: msp.HistoryModeInline, Items: []msp.Item{
		toolItem("orphan", "bash", msp.ItemStatusInProgress, 1),
	}})
	h.mu.Unlock()
	if busy, _ := BackgroundWork(name); !busy {
		t.Fatal("inside the grace the rebuilt entry does not count")
	}
	waitBg(t, name, false)
}

// A stopped session has no work running, whatever the handle last saw.
func TestNoBackgroundBusyWithoutALiveHost(t *testing.T) {
	_, _, m := bgSession(t)
	if li := New().WireLive(m, false); li.BackgroundBusy {
		t.Error("a stopped session reads busy")
	}
	if busy, _ := BackgroundWork("no-such-session"); busy {
		t.Error("an unknown session reads busy")
	}
}
