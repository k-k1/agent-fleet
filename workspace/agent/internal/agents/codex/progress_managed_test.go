package codex

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func progressHandle(t *testing.T, name, tid string) (*threadHandle, session.Meta) {
	t.Helper()
	h := &threadHandle{name: name, tid: tid, alive: true, events: make(chan agents.Event, 8)}
	registerCodexTestHandle(t, h)
	return h, session.Meta{Name: name}
}

func notify(method, params string) {
	dispatchNotification(rpcMsg{Method: method, Params: json.RawMessage(params)})
}

func TestNotificationsNamingTheThreadStampItsHandle(t *testing.T) {
	h, _ := progressHandle(t, "prog-a", "thr_a")
	other, _ := progressHandle(t, "prog-b", "thr_b")

	// The account-wide notifications of the measured run (rate limits, MCP startup) name no
	// thread and must not make any session look alive.
	notify("account/rateLimits/updated", `{"rateLimits":{}}`)
	notify("mcpServer/startupStatus/updated", `{"name":"x"}`)
	if _, ok := h.act.At(); ok {
		t.Fatal("an account-wide notification stamped a session")
	}
	notify("item/completed", `{"threadId":"thr_a","item":{"id":"i1","type":"commandExecution"}}`)
	if _, ok := h.act.At(); !ok {
		t.Fatal("a notification naming the thread did not stamp its handle")
	}
	if _, ok := other.act.At(); ok {
		t.Fatal("a notification stamped another session's handle")
	}
}

// A long single call that sends nothing and is not polled: the turn started on this connection,
// the command item opened and nothing has arrived for two hours.
func TestOpenItemKeepsAStaleRowHolding(t *testing.T) {
	h, m := progressHandle(t, "prog-c", "thr_c")
	notify("turn/started", `{"threadId":"thr_c","turn":{"id":"t1"}}`)
	notify("item/started", `{"threadId":"thr_c","item":{"id":"cmd1","type":"commandExecution"}}`)
	h.act.TouchAt(time.Now().Add(-2 * time.Hour))

	got, ok := (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("open command item, silent 2 h: got %v, %v; want now", got, ok)
	}
	notify("item/completed", `{"threadId":"thr_c","item":{"id":"cmd1","type":"commandExecution"}}`)
	h.act.TouchAt(time.Now().Add(-2 * time.Hour))
	got, ok = (managedDriver{}).ManagedProgress(m)
	if !ok || time.Since(got) < time.Hour {
		t.Fatalf("item closed, silent 2 h: got %v, %v; want the stale stamp so the bound can lapse", got, ok)
	}
}

// A handle that never saw its turn begin (Agent restarted mid-turn, connection replaced) cannot
// know what is open: a stale row keeps holding.
func TestUnknownItemSetKeepsAStaleRowHolding(t *testing.T) {
	h, m := progressHandle(t, "prog-d", "thr_d")
	notify("item/completed", `{"threadId":"thr_d","item":{"id":"x"}}`) // adopted mid-turn
	h.act.TouchAt(time.Now().Add(-2 * time.Hour))
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("answered %v although the open items are unknown", got)
	}

	notify("turn/started", `{"threadId":"thr_d","turn":{"id":"t1"}}`)
	h.act.TouchAt(time.Now().Add(-2 * time.Hour))
	if _, ok := (managedDriver{}).ManagedProgress(m); !ok {
		t.Fatal("no answer after a turn boundary made the set known")
	}
	h.runtimeLost()
	if got, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatalf("answered %v after the connection was lost", got)
	}
}

func TestManagedProgressWithoutHandleOrMessage(t *testing.T) {
	if _, ok := (managedDriver{}).ManagedProgress(session.Meta{Name: "nope"}); ok {
		t.Fatal("answered without a handle")
	}
	_, m := progressHandle(t, "prog-e", "thr_e")
	if _, ok := (managedDriver{}).ManagedProgress(m); ok {
		t.Fatal("answered for a thread never heard from")
	}
}
