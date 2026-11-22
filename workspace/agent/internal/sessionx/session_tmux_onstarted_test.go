package sessionx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// onStartedAgent wraps a real agent and attaches an OnStarted hook to its launch plan.
type onStartedAgent struct {
	agents.Agent
	hook func() error
}

func (a onStartedAgent) BuildLaunch(m session.Meta, o agents.LaunchOpts) (agents.LaunchPlan, error) {
	plan, err := a.Agent.BuildLaunch(m, o)
	plan.OnStarted = a.hook
	return plan, err
}

// failableTmux is a tmux whose new-session fails while $TMUX_TEST_FAIL exists; every call is logged.
func failableTmux(t *testing.T) (logPath, failFile string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(bin, "tmux.log")
	failFile = filepath.Join(bin, "fail")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_TEST_LOG\"\ncase \"$*\" in *new-session*) [ -e \"$TMUX_TEST_FAIL\" ] && exit 1 ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_TMUX_SOCKET", "")
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_FAIL", failFile)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	return logPath, failFile
}

func swapCodex(t *testing.T, hook func() error) {
	t.Helper()
	real, ok := agentreg.Lookup(session.KindCodex)
	if !ok {
		t.Fatal("no codex agent registered")
	}
	t.Cleanup(agentreg.SwapForTest(session.KindCodex, onStartedAgent{Agent: real, hook: hook}))
}

// OnStarted runs only after tmux new-session succeeded: a failed start leaves the first-launch
// state for the retry, and the retry's successful start consumes it.
func TestStartSessionTmuxRunsOnStartedOnlyAfterANewSession(t *testing.T) {
	_, failFile := failableTmux(t)
	calls := 0
	swapCodex(t, func() error { calls++; return nil })
	m := session.Meta{Name: "onstarted_wiring", Dir: t.TempDir(), Kind: session.KindCodex}
	session.WriteMeta(m)

	if err := os.WriteFile(failFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startSessionTmux(m, false); err == nil {
		t.Fatal("a failing new-session must fail the launch")
	}
	if calls != 0 {
		t.Fatalf("OnStarted ran %d times for a pane that never started", calls)
	}
	if err := os.Remove(failFile); err != nil {
		t.Fatal(err)
	}
	if err := startSessionTmux(m, false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("OnStarted ran %d times after a successful start, want 1", calls)
	}
}

// When the bookkeeping cannot be completed the pane is stopped and the launch fails, so the
// retry is still a first launch instead of a start with a stale marker.
func TestStartSessionTmuxStopsThePaneWhenOnStartedKeepsFailing(t *testing.T) {
	logPath, _ := failableTmux(t)
	calls := 0
	swapCodex(t, func() error { calls++; return errors.New("marker busy") })
	m := session.Meta{Name: "onstarted_fails", Dir: t.TempDir(), Kind: session.KindCodex}
	session.WriteMeta(m)
	err := startSessionTmux(m, false)
	if err == nil || !strings.Contains(err.Error(), "marker busy") {
		t.Fatalf("err = %v, want the bookkeeping failure", err)
	}
	if calls != 2 {
		t.Fatalf("OnStarted ran %d times, want 1 try + 1 retry", calls)
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "kill-session") {
		t.Fatalf("the pane was left running; tmux log = %s", b)
	}
}
