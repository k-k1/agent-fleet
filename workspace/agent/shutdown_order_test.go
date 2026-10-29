package main

// Ordering test for gracefulShutdown's managed lifecycle calls: carried interactions are
// promoted before any abort, every kind is aborted before any kind is shut down, kinds run in
// internal/kinds order, and the owned tmux sessions are killed only after the shutdowns.

import (
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// recordingAgent is a registered agent whose ManagedLifecycle only records its calls.
type recordingAgent struct {
	agents.Agent
	lc agents.ManagedLifecycle
}

func (r recordingAgent) ManagedLifecycle() agents.ManagedLifecycle { return r.lc }

// recordShutdownCalls swaps every hand-wired managed kind and promoteCarried for recorders.
// extra, when set, is appended to each Shutdown's record.
func recordShutdownCalls(t *testing.T, extra func() string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	rec := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, s)
	}
	prev := promoteCarried
	promoteCarried = func() { rec("promote") }
	t.Cleanup(func() { promoteCarried = prev })
	for _, h := range handWiredLifecycle {
		k := h.kind
		restore := agentreg.SwapForTest(k, recordingAgent{Agent: agentreg.Of(k), lc: agents.ManagedLifecycle{
			Reconcile: func(reason string) { rec("reconcile " + k + " " + reason) },
			Abort:     func() { rec("abort " + k) },
			Shutdown: func() {
				s := "shutdown " + k
				if extra != nil {
					s += extra()
				}
				rec(s)
			},
		}})
		t.Cleanup(restore)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func wantShutdownCalls(suffix string) []string {
	want := []string{"promote"}
	for _, h := range handWiredLifecycle {
		want = append(want, "abort "+h.kind)
	}
	for _, h := range handWiredLifecycle {
		want = append(want, "shutdown "+h.kind+suffix)
	}
	return want
}

// TestGracefulShutdownOrderNoOwnedSessions is the path with no owned tmux session: promote,
// abort every kind, shut every kind down, return.
func TestGracefulShutdownOrderNoOwnedSessions(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("AF_TMUX_SOCKET", isolatedTmuxSocket())
	calls := recordShutdownCalls(t, nil)
	gracefulShutdown(5 * time.Second)
	if got, want := calls(), wantShutdownCalls(""); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n got  %v\n want %v", got, want)
	}
}

// TestGracefulShutdownOrderWithOwnedSession is the path with an owned tmux session on an
// isolated socket: the same calls, with the session still alive at every Shutdown and gone
// once gracefulShutdown returns.
func TestGracefulShutdownOrderWithOwnedSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("AF_TMUX_SOCKET", isolatedTmuxSocket())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	// kill-server is allowed only against a dedicated socket (dev/04 §4.11).
	t.Cleanup(func() { _ = tmuxx.Cmd("kill-server").Run() })
	tn := session.TmuxName("owned1")
	// SIGINT is ignored so the shutdown's C-c leaves the pane running; only kill-session ends it.
	if out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "sh", "-c", `trap "" INT; sleep 60`).CombinedOutput(); err != nil {
		t.Fatalf("new-session %s: %v\n%s", tn, err, out)
	}
	session.WriteMeta(session.Meta{Name: "owned1", Dir: t.TempDir(), Kind: session.KindShell})
	alive := func() bool { return tmuxx.LiveSessionNames()["owned1"] }
	if !alive() {
		t.Fatal("the owned session is not live before the shutdown")
	}
	calls := recordShutdownCalls(t, func() string {
		if alive() {
			return " (session alive)"
		}
		return " (session gone)"
	})
	gracefulShutdown(5 * time.Second)
	if got, want := calls(), wantShutdownCalls(" (session alive)"); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n got  %v\n want %v", got, want)
	}
	if alive() {
		t.Error("the owned session survived gracefulShutdown")
	}
}

// TestBootReconcilesEveryManagedKind runs the boot loop of serve() over the recorders: every
// managed kind is reconciled once with the boot reason. The start order is managedLifecycles'
// order, which TestManagedLifecycleMatchesHandWiring pins; the goroutines' run order is not.
func TestBootReconcilesEveryManagedKind(t *testing.T) {
	calls := recordShutdownCalls(t, nil)
	var wg sync.WaitGroup
	for _, lc := range managedLifecycles() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lc.Reconcile("agent boot")
		}()
	}
	wg.Wait()
	got := map[string]bool{}
	for _, c := range calls() {
		got[c] = true
	}
	want := map[string]bool{}
	for _, h := range handWiredLifecycle {
		want["reconcile "+h.kind+" agent boot"] = true
	}
	if !reflect.DeepEqual(got, want) || len(calls()) != len(handWiredLifecycle) {
		t.Fatalf("reconciles = %v, want each of %v once", calls(), want)
	}
}
