package codex

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stopServer plays a daemon whose thread has `running` in-progress turns in a row: each
// turn/interrupt ends one, the next starting (a queued prompt) until none is left.
func stopServer(t *testing.T, running int) (addr string, interrupts func() []string) {
	t.Helper()
	return stopServerOn(t, running, nil)
}

// stopServerOn is stopServer listening on l (a unix socket) when l is not nil.
func stopServerOn(t *testing.T, running int, l net.Listener) (addr string, interrupts func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	turn := 0
	upgrader := websocket.Upgrader{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			var m map[string]any
			if c.ReadJSON(&m) != nil {
				return
			}
			params, _ := m["params"].(map[string]any)
			switch m["method"] {
			case "initialize":
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{}})
			case "thread/turns/list":
				mu.Lock()
				st := "completed"
				if turn < running {
					st = "inProgress"
				}
				id := "turn-" + string(rune('a'+turn))
				mu.Unlock()
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{"data": []map[string]any{{"id": id, "status": st}}}})
			case "turn/interrupt":
				mu.Lock()
				got = append(got, params["threadId"].(string)+"/"+params["turnId"].(string))
				turn++
				mu.Unlock()
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{}})
			}
		}
	}))
	if l != nil {
		srv.Listener = l
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

func TestInterruptThreadEndsTheRunningTurnAndTheOneQueuedBehindIt(t *testing.T) {
	stopSettle = time.Millisecond
	addr, interrupts := stopServer(t, 2)
	if err := interruptThread(addr, "cx-own"); err != nil {
		t.Fatal(err)
	}
	got := interrupts()
	if len(got) != 2 || got[0] != "cx-own/turn-a" || got[1] != "cx-own/turn-b" {
		t.Fatalf("interrupts = %v, want both turns of cx-own", got)
	}
}

func TestInterruptThreadLeavesAnIdleThreadAlone(t *testing.T) {
	stopSettle = time.Millisecond
	addr, interrupts := stopServer(t, 0)
	if err := interruptThread(addr, "cx-own"); err != nil {
		t.Fatal(err)
	}
	if got := interrupts(); len(got) != 0 {
		t.Fatalf("interrupted an idle thread: %v", got)
	}
}

func TestInterruptThreadWithoutADaemonIsAnErrorNotAHang(t *testing.T) {
	if err := interruptThread("unix:///nonexistent/app-server-control.sock", "cx-own"); err == nil {
		t.Fatal("want an error when no daemon listens")
	}
}

// Stopping a Terminal slot interrupts its own thread on the daemon's control socket under
// CODEX_HOME, and asks for nothing when the slot has no captured thread.
func TestGracefulStopInterruptsOnlyTheSlotsThreadOnTheControlSocket(t *testing.T) {
	stopSettle = time.Millisecond
	home, err := os.MkdirTemp("/tmp", "cxh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("CODEX_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "app-server-control"), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(home, "app-server-control", "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	_, interrupts := stopServerOn(t, 1, l)

	m := session.Meta{Name: "stopme", Dir: t.TempDir(), Kind: session.KindCodex}
	if (agentImpl{}).GracefulStop(m) {
		t.Error("the pane still has to be killed by the caller")
	}
	if got := interrupts(); len(got) != 0 {
		t.Fatalf("a slot with no captured thread interrupted %v", got)
	}
	sid := session.UUID(m.Dir, m.Name)
	sids.Write(sid, "cx-own")
	t.Cleanup(func() { sids.Remove(sid) })
	(agentImpl{}).GracefulStop(m)
	if got := interrupts(); len(got) != 1 || got[0] != "cx-own/turn-a" {
		t.Fatalf("interrupts = %v, want the slot's own thread", got)
	}
}
