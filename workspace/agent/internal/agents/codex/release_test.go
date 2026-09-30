package codex

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
				t.Errorf("the probe sent %v: it must only read thread/loaded/list", m["method"])
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// A Terminal launch that resumes a thread the shared app-server still has loaded is refused
// (and the observer told to let go of it), not waited for; one that is free launches as usual.
// Nothing is probed for a fresh launch.
func TestBuildLaunchRefusesWhileTheAppServerHoldsTheThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var released []string
	prev := ReleaseObservedThread
	ReleaseObservedThread = func(tid string) { mu.Lock(); released = append(released, tid); mu.Unlock() }
	t.Cleanup(func() { ReleaseObservedThread = prev })
	gotReleased := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), released...) }

	loaded := true
	var lmu sync.Mutex
	// The thread sits on the second page: a probe that read only the first would let it through.
	addr, calls := loadedListServer(t, func(_ int, cursor any) ([]string, any) {
		if cursor == nil {
			return []string{"other"}, "c1"
		}
		lmu.Lock()
		defer lmu.Unlock()
		if loaded {
			return []string{"cx-own"}, nil
		}
		return []string{}, nil
	})
	t.Setenv(appServerAddrEnv, addr)

	m := session.Meta{Name: "release-probe", Kind: session.KindCodex, Dir: t.TempDir()}
	if _, err := New().BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatalf("fresh launch: %v", err)
	}
	if n := calls(); n != 0 {
		t.Fatalf("a fresh launch probed the app-server %d times: there is no thread to hold", n)
	}

	sids.Write(session.UUID(m.Dir, m.Name), "cx-own")
	if _, err := New().BuildLaunch(m, agents.LaunchOpts{}); !errors.Is(err, ErrThreadReleasing) {
		t.Fatalf("err = %v, want ErrThreadReleasing while the thread is loaded", err)
	}
	if got := gotReleased(); len(got) != 1 || got[0] != "cx-own" {
		t.Fatalf("released = %v, want [cx-own]: a stop that never reached DropHandle must still let go", got)
	}

	lmu.Lock()
	loaded = false
	lmu.Unlock()
	plan, err := New().BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatalf("launch after the unload: %v", err)
	}
	if !strings.HasPrefix(plan.Program, "codex resume 'cx-own'") {
		t.Fatalf("expected a direct codex resume, got %q", plan.Program)
	}
	if got := gotReleased(); len(got) != 1 {
		t.Fatalf("released = %v: a free thread was released again", got)
	}

	// No daemon holds nothing: the launch goes ahead.
	t.Setenv(appServerAddrEnv, "ws://127.0.0.1:1")
	if _, err := New().BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatalf("launch with no daemon: %v", err)
	}
}

// A stop is what releases the thread: DropHandle unsubscribes the writer and has the observer
// let go too, or the observer keeps the thread loaded and the Terminal route stays locked out.
func TestDropHandleReleasesTheObservedThread(t *testing.T) {
	_, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-drop-release")
	registerCodexTestHandle(t, h)
	var released []string
	prev := ReleaseObservedThread
	ReleaseObservedThread = func(tid string) { released = append(released, tid) }
	t.Cleanup(func() { ReleaseObservedThread = prev })

	DropHandle(h.name)
	if len(released) != 1 || released[0] != "thr_test" {
		t.Fatalf("released = %v, want [thr_test]", released)
	}
}
