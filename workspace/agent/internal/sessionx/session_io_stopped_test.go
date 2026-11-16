package sessionx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stoppedTmux installs a fake tmux whose session exists only while the file $TMUX_TEST_UP does,
// and whose new-session blocks on a fifo until the test releases it (the launch in flight).
// It returns the command log and the up/release paths.
func stoppedTmux(t *testing.T) (logPath, up, release string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(bin, "tmux.log")
	up = filepath.Join(bin, "up")
	release = filepath.Join(bin, "release")
	if err := syscall.Mkfifo(release, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
# A launch can lead with "set-environment ... ;" (tmuxx.withLaunchEnv), so new-session is
# matched anywhere in the arguments, not as $1.
case "$*" in *new-session*) read _ < "$TMUX_TEST_RELEASE"; : > "$TMUX_TEST_UP"; exit 0 ;; esac
case "$1" in
  has-session) [ -e "$TMUX_TEST_UP" ] && exit 0; exit 1 ;;
  list-panes) printf '1 %%9\n' ;;
  capture-pane) printf 'ready\nmedium · ~/repos\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_TMUX_SOCKET", "") // the fake reads $1 as the subcommand, so no -L prefix
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_UP", up)
	t.Setenv("TMUX_TEST_RELEASE", release)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Cleanup(WaitInitialDeliveries)
	return logPath, up, release
}

func releaseFifo(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0) // read-write: never blocks for want of a reader
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("go\n")
	f.Close()
}

func tmuxLog(path string) string { b, _ := os.ReadFile(path); return string(b) }

func TestWhenReadyToStoppedSessionIsRefused(t *testing.T) {
	logPath, _, _ := stoppedTmux(t)
	const name = "stopped_when_ready"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
	rec := postInput(t, name, `{"prompt":"x","when_ready":true}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not_running") {
		t.Fatalf("status = %d, body = %s, want 409 not_running", rec.Code, rec.Body.String())
	}
	WaitInitialDeliveries()
	if strings.Contains(tmuxLog(logPath), "load-buffer") || strings.Contains(tmuxLog(logPath), "send-keys") {
		t.Fatalf("a refused prompt was delivered anyway: %s", tmuxLog(logPath))
	}
	if m, _ := session.ReadMeta(name); m.InitialPromptState != "" {
		t.Fatalf("a refused prompt left state %q", m.InitialPromptState)
	}
}

func TestInputToStoppedRetiredSessionIsSessionRetired(t *testing.T) {
	stoppedTmux(t)
	const name = "stopped_retired"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, RetiredAt: time.Now().Format(time.RFC3339), RetireID: "r1"})
	for what, body := range map[string]string{
		"when_ready": `{"prompt":"x","when_ready":true}`,
		"prompt":     `{"prompt":"x"}`,
		"keys":       `{"keys":["Enter"]}`,
	} {
		rec := postInput(t, name, body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session_retired") {
			t.Fatalf("%s: status = %d, body = %s, want 409 session_retired", what, rec.Code, rec.Body.String())
		}
	}
}

// startInFlight runs startSessionTmux for m on a goroutine and returns once the fake tmux is in
// new-session, i.e. the launch is in flight and the pane does not exist yet. It returns the wait
// for the launch's result. A cleanup releases the fake tmux and joins the launch, so a test that
// fails halfway leaves neither behind.
func startInFlight(t *testing.T, logPath, release string, m session.Meta) (wait func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- startSessionTmux(m, false) }()
	var once sync.Once
	var res error
	wait = func() error {
		once.Do(func() {
			select {
			case res = <-done:
			case <-time.After(15 * time.Second):
				res = errors.New("the launch never returned; tmux log = " + tmuxLog(logPath))
			}
		})
		return res
	}
	t.Cleanup(func() { releaseFifo(t, release); _ = wait() })
	for start := time.Now(); !strings.Contains(tmuxLog(logPath), "new-session"); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatal("the launch never reached tmux new-session")
		}
	}
	if !launchInFlight(m.Name) {
		p, _ := exec.LookPath("tmux")
		t.Fatalf("launch not marked in flight; tmux = %s, socket = %q, log = %s", p, os.Getenv("AF_TMUX_SOCKET"), tmuxLog(logPath))
	}
	return wait
}

func waitDelivered(t *testing.T, logPath string) {
	t.Helper()
	for start := time.Now(); strings.Count(tmuxLog(logPath), "send-keys -t %9 Enter") < 2; time.Sleep(20 * time.Millisecond) {
		if time.Since(start) > 15*time.Second {
			t.Fatalf("prompt was never delivered; tmux commands = %s", tmuxLog(logPath))
		}
	}
}

// A start in flight: the meta exists, the launch is inside tmux new-session, no pane yet.
func TestWhenReadyDuringStartInFlightIsAcceptedAndDelivered(t *testing.T) {
	logPath, _, release := stoppedTmux(t)
	const name = "starting_when_ready"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, RetiredAt: time.Now().Format(time.RFC3339)}
	session.WriteMeta(m)
	wait := startInFlight(t, logPath, release, m)
	rec := postInput(t, name, `{"prompt":"first","when_ready":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s, want 202", rec.Code, rec.Body.String())
	}
	releaseFifo(t, release)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	waitDelivered(t, logPath)
}

// A create in flight: nothing has published the meta yet, so a name with no meta must wait for
// the launch rather than answer not_found.
func TestWhenReadyDuringCreateInFlightIsAcceptedAndDelivered(t *testing.T) {
	logPath, _, release := stoppedTmux(t)
	const name = "creating_when_ready"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex}
	wait := startInFlight(t, logPath, release, m)
	rec := postInput(t, name, `{"prompt":"first","when_ready":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s, want 202", rec.Code, rec.Body.String())
	}
	session.WriteMeta(m) // what the create publishes after the launch
	releaseFifo(t, release)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	waitDelivered(t, logPath)
	// A create in flight has no meta when the input is accepted, so no state was marked pending
	// and none is settled: the guarantee for this path is the delivery and the log, not the meta.
	WaitInitialDeliveries()
	if m, _ := session.ReadMeta(name); m.InitialPromptState != "" {
		t.Fatalf("state = %q, want none on the no-meta path", m.InitialPromptState)
	}
}

// holdHandlerAtGate runs h for name while a test-held exclusive lifecycle gate stops it, and
// reports whether a launch of name was marked in flight meanwhile. After the caller goes away the
// handler returns and the mark has to be gone.
func holdHandlerAtGate(t *testing.T, name string, h http.HandlerFunc, target, body string) (marked bool) {
	t.Helper()
	x, err := lifegate.Lock(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)).WithContext(ctx)
	req.SetPathValue("name", name)
	finished := make(chan struct{})
	go func() { defer close(finished); h(httptest.NewRecorder(), req) }()
	for start := time.Now(); !launchInFlight(name) && time.Since(start) < 5*time.Second; time.Sleep(5 * time.Millisecond) {
	}
	marked = launchInFlight(name)
	cancel()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler did not return after its caller went away")
	}
	if launchInFlight(name) {
		t.Fatal("the mark outlived the handler")
	}
	return marked
}

// A route switch and a driver switch stop the pane before they relaunch it, so the mark is held
// from the handler's start, not only inside the launch.
func TestSwitchHandlersMarkALaunchInFlight(t *testing.T) {
	stoppedTmux(t)
	for _, c := range []struct {
		what, target, body string
		h                  http.HandlerFunc
	}{
		{"route switch", "/sessions/sw_route/route-switch", `{}`, HandleSessionRouteSwitch},
		{"driver switch", "/sessions/sw_driver/driver", `{"driver":"managed"}`, HandleSessionDriver},
	} {
		name := strings.Split(c.target, "/")[2]
		session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
		if !holdHandlerAtGate(t, name, c.h, c.target, c.body) {
			t.Fatalf("%s: no launch was marked while the handler ran", c.what)
		}
	}
}

// A follow-up when_ready (the route switch's carry-on) must not turn the session's delivered
// first instruction back into a failure.
func TestWhenReadyKeepsDeliveredFirstInstruction(t *testing.T) {
	old := initialPaneTries
	initialPaneTries = 1
	t.Cleanup(func() { initialPaneTries = old })
	stoppedTmux(t)
	const name = "keeps_delivered"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, InitialPromptState: session.InitialPromptDelivered})
	defer markLaunching(name)()
	if rec := postInput(t, name, `{"prompt":"carry on","when_ready":true}`); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	WaitInitialDeliveries() // gives up: no pane
	if m, _ := session.ReadMeta(name); m.InitialPromptState != session.InitialPromptDelivered {
		t.Fatalf("state = %q, want delivered kept", m.InitialPromptState)
	}
}

// 202 is never followed by a silent drop: a delivery that finds no pane settles the meta failed.
func TestWhenReadyGiveUpLeavesTrace(t *testing.T) {
	// Before stoppedTmux, so the value is restored after its WaitInitialDeliveries cleanup.
	old := initialPaneTries
	initialPaneTries = 1
	t.Cleanup(func() { initialPaneTries = old })
	stoppedTmux(t)
	const name = "gives_up"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
	end := markLaunching(name) // a launch that never produces a pane
	defer end()
	if rec := postInput(t, name, `{"prompt":"x","when_ready":true}`); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if m, _ := session.ReadMeta(name); m.InitialPromptState != session.InitialPromptPending && m.InitialPromptState != session.InitialPromptFailed {
		t.Fatalf("state after 202 = %q, want pending (or already failed)", m.InitialPromptState)
	}
	WaitInitialDeliveries()
	if m, _ := session.ReadMeta(name); m.InitialPromptState != session.InitialPromptFailed {
		t.Fatalf("state after give-up = %q, want failed", m.InitialPromptState)
	}
}
