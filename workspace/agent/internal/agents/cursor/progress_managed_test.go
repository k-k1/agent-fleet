package cursor

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The shape measured on cursor-agent 2026.10.16 (managed, `sleep 120`): the ACP child leads its
// own group, the shell tool under it is a session leader, and the child sends no message for
// the whole sleep.
func cursorTable(withTool bool) map[int]procx.Info {
	tab := map[int]procx.Info{
		100: {PPID: 1, State: 'S', Comm: "cursor-agent", Pgrp: 100, Sid: 50},
		101: {PPID: 100, State: 'S', Comm: "node", Pgrp: 100, Sid: 50},
	}
	if withTool {
		tab[200] = procx.Info{PPID: 101, State: 'S', Comm: "bash", Pgrp: 200, Sid: 200}
		tab[201] = procx.Info{PPID: 200, State: 'S', Comm: "sleep", Pgrp: 200, Sid: 200}
	}
	return tab
}

// registered puts a handle in the real registry, stamped `silent` ago, and serves `tab` as the
// process table, so the test goes through ManagedProgress as production does.
func registered(t *testing.T, name string, silent time.Duration, tab map[int]procx.Info) session.Meta {
	t.Helper()
	h := &threadHandle{name: name, cmd: &exec.Cmd{Process: &os.Process{Pid: 100}}}
	h.calls.Reset() // the turn began on this connection
	if silent >= 0 {
		h.act.TouchAt(time.Now().Add(-silent))
	}
	handlesMu.Lock()
	handles[name] = h
	handlesMu.Unlock()
	old := procTable
	procTable = func() map[int]procx.Info { return tab }
	t.Cleanup(func() {
		procTable = old
		handlesMu.Lock()
		delete(handles, name)
		handlesMu.Unlock()
	})
	return session.Meta{Name: name}
}

func TestManagedProgressSilentToolKeepsHolding(t *testing.T) {
	m := registered(t, "prog-tool", 2*time.Hour, cursorTable(true))
	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("a live tool under a child silent for 2 h: got %v, %v; want now", got, ok)
	}
}

func TestManagedProgressStaleWithoutToolCanLapse(t *testing.T) {
	m := registered(t, "prog-stale", 2*time.Hour, cursorTable(false))
	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) < time.Hour {
		t.Fatalf("no tool: got %v, %v; want the 2 h old stamp, so the bound can lapse", got, ok)
	}
}

func setCalls(name string, f func(h *threadHandle)) {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	f(handles[name])
}

// A tool that is not a local process (an MCP server's, or work inside a resident helper) leaves
// nothing in the process table: only the protocol's open tool call shows it.
func TestOpenToolCallKeepsAStaleRowHoldingWithAnIdleProcessTable(t *testing.T) {
	m := registered(t, "prog-mcp", 2*time.Hour, cursorTable(false))
	setCalls(m.Name, func(h *threadHandle) {
		h.onNotify("session/update", []byte(`{"update":{"sessionUpdate":"tool_call","toolCallId":"remote_build","status":"in_progress"}}`))
	})
	// the notification stamped the handle; make it silent again
	setCalls(m.Name, func(h *threadHandle) { h.act.TouchAt(time.Now().Add(-2 * time.Hour)) })
	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("open tool call, idle process table, silent 2 h: got %v, %v; want now", got, ok)
	}
	setCalls(m.Name, func(h *threadHandle) {
		h.onNotify("session/update", []byte(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"remote_build","status":"completed"}}`))
		h.act.TouchAt(time.Now().Add(-2 * time.Hour))
	})
	got, ok = (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) < time.Hour {
		t.Fatalf("call completed: got %v, %v; want the stale stamp, so the bound can lapse", got, ok)
	}
}

// A connection that was replaced (or a replay) leaves the open calls unknown until the next
// turn boundary.
func TestUnknownCallSetKeepsAStaleRowHolding(t *testing.T) {
	m := registered(t, "prog-replay", 2*time.Hour, cursorTable(false))
	setCalls(m.Name, func(h *threadHandle) { h.calls.Unknown() })
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("answered %v although the open calls are unknown", got)
	}
}

func TestManagedProgressFreshMessage(t *testing.T) {
	m := registered(t, "prog-fresh", time.Second, cursorTable(false))
	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("a message a second ago: got %v, %v", got, ok)
	}
}

// "Could not observe" is not "no tool": an empty table (a failed /proc read) or a child that is
// not in it keeps the stale row holding.
func TestManagedProgressUnobservableChildKeepsHolding(t *testing.T) {
	for name, tab := range map[string]map[int]procx.Info{
		"empty table":     {},
		"child not in it": {7: {PPID: 1, State: 'S', Comm: "x", Pgrp: 7, Sid: 7}},
	} {
		m := registered(t, "prog-blind", 2*time.Hour, tab)
		if got, ok := (managedDriver{}).ManagedProgress(m); ok {
			t.Errorf("%s: answered %v for a stale row it could not observe", name, got)
		}
	}
}

func TestManagedProgressNeverHeardAndNoToolCannotTell(t *testing.T) {
	m := registered(t, "prog-never", -1, cursorTable(false))
	if _, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatal("answered for a child never heard from")
	}
}

func TestManagedProgressNeedsALiveHandle(t *testing.T) {
	if _, ok := (managedDriver{}).ManagedProgress(session.Meta{Name: "no-such-session"}); ok {
		t.Fatal("answered without a handle")
	}
}

func TestReadLoopMessagesStampTheHandle(t *testing.T) {
	h := &threadHandle{name: "prog"}
	h.onNotify("session/update", nil)
	if _, ok := h.act.At(); !ok {
		t.Fatal("a notification did not stamp the handle")
	}
}
