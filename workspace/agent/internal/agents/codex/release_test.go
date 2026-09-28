package codex

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// loadedListServer answers thread/loaded/list from pages(call number), one page per request.
func loadedListServer(t *testing.T, pages func(n int, cursor any) (data []string, next any)) (addr string, calls func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			switch m["method"] {
			case "initialize":
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{}})
			case "initialized":
			case "thread/loaded/list":
				mu.Lock()
				n++
				k := n
				mu.Unlock()
				params, _ := m["params"].(map[string]any)
				data, next := pages(k, params["cursor"])
				_ = c.WriteJSON(map[string]any{"id": m["id"], "result": map[string]any{"data": data, "nextCursor": next}})
			default:
				t.Errorf("the waiter sent %v: it must only read thread/loaded/list", m["method"])
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func TestAwaitThreadUnloadedWaitsForTheUnload(t *testing.T) {
	// Pages: the thread sits on the second page while loaded, then disappears.
	addr, calls := loadedListServer(t, func(n int, cursor any) ([]string, any) {
		if cursor == nil {
			return []string{"other"}, "c1"
		}
		if n < 6 {
			return []string{"thr-1"}, nil
		}
		return []string{}, nil
	})
	if !AwaitThreadUnloaded(addr, "thr-1", 5*time.Second, 10*time.Millisecond) {
		t.Fatal("reported a timeout although the thread was unloaded")
	}
	if got := calls(); got < 6 {
		t.Fatalf("returned after %d list calls - before the thread left the second page", got)
	}
}

func TestAwaitThreadUnloadedGivesUp(t *testing.T) {
	addr, _ := loadedListServer(t, func(int, any) ([]string, any) { return []string{"thr-1"}, nil })
	if AwaitThreadUnloaded(addr, "thr-1", 100*time.Millisecond, 10*time.Millisecond) {
		t.Fatal("reported an unload for a thread that never left")
	}
}

func TestAwaitThreadUnloadedWithoutDaemon(t *testing.T) {
	if !AwaitThreadUnloaded("ws://127.0.0.1:1", "thr-1", time.Second, 10*time.Millisecond) {
		t.Fatal("no daemon holds nothing: the wait must not block the launch")
	}
}

// A Terminal launch that resumes a conversation while the shared app-server is up must release
// the observer's hold and wait in the pane before codex starts; a fresh launch, or one with no
// daemon, must do neither.
func TestBuildLaunchWaitsForTheAppServerToReleaseTheThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var released []string
	prev := ReleaseObservedThread
	ReleaseObservedThread = func(tid string) { released = append(released, tid) }
	t.Cleanup(func() { ReleaseObservedThread = prev })

	dir := t.TempDir()
	m := session.Meta{Name: "await-release", Kind: session.KindCodex, Dir: dir}
	launch := func() string {
		t.Helper()
		plan, err := New().BuildLaunch(m, agents.LaunchOpts{})
		if err != nil {
			t.Fatalf("BuildLaunch: %v", err)
		}
		return plan.Program
	}

	t.Setenv(appServerAddrEnv, "ws://127.0.0.1:1")
	if got := launch(); strings.Contains(got, "codex-await-thread") || len(released) != 0 {
		t.Fatalf("fresh launch waited or released (released=%v): %q", released, got)
	}

	sids.Write(session.UUID(m.Dir, m.Name), "cx-own")
	got := launch()
	if want := " codex-await-thread 'ws://127.0.0.1:1' 'cx-own'; codex resume 'cx-own'"; !strings.Contains(got, want) {
		t.Fatalf("expected %q in %q", want, got)
	}
	if len(released) != 1 || released[0] != "cx-own" {
		t.Fatalf("released = %v, want [cx-own]", released)
	}

	t.Setenv(appServerAddrEnv, "")
	released = nil
	if got := launch(); strings.Contains(got, "codex-await-thread") || len(released) != 0 {
		t.Fatalf("launch without a daemon waited or released (released=%v): %q", released, got)
	}
}

// The marker must track the waiter's life: present while it runs, gone after its cleanup, and
// ignored once its process is dead (a killed pane never runs the cleanup).
func TestAwaitingFollowsTheWaiter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	done := MarkAwaiting("await-mark")
	if !Awaiting("await-mark") {
		t.Fatal("marker written but Awaiting is false")
	}
	done()
	if Awaiting("await-mark") {
		t.Fatal("Awaiting still true after the waiter's cleanup")
	}
	if MarkAwaiting(""); Awaiting("") {
		t.Fatal("a pane without AF_SESSION_NAME must not mark anything")
	}
	// A dead pid: spawn and reap a short process for a pid nobody holds any more.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	p := awaitMarkerPath("await-stale")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if Awaiting("await-stale") {
		t.Fatal("a marker left by a dead waiter still blocks the session")
	}
}
