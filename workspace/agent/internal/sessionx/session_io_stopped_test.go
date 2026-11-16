package sessionx

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
// new-session, i.e. the launch is in flight and the pane does not exist yet.
func startInFlight(t *testing.T, logPath string, m session.Meta) (done chan error) {
	t.Helper()
	done = make(chan error, 1)
	go func() { done <- startSessionTmux(m, false) }()
	for start := time.Now(); !strings.Contains(tmuxLog(logPath), "new-session"); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatal("the launch never reached tmux new-session")
		}
	}
	if !launchInFlight(m.Name) {
		p, _ := exec.LookPath("tmux")
		t.Fatalf("launch not marked in flight; tmux = %s, socket = %q, log = %s", p, os.Getenv("AF_TMUX_SOCKET"), tmuxLog(logPath))
	}
	return done
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
	done := startInFlight(t, logPath, m)
	rec := postInput(t, name, `{"prompt":"first","when_ready":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s, want 202", rec.Code, rec.Body.String())
	}
	releaseFifo(t, release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the launch never returned; tmux log = %s", tmuxLog(logPath))
	}
	waitDelivered(t, logPath)
}

// A create in flight: nothing has published the meta yet, so a name with no meta must wait for
// the launch rather than answer not_found.
func TestWhenReadyDuringCreateInFlightIsAcceptedAndDelivered(t *testing.T) {
	logPath, _, release := stoppedTmux(t)
	const name = "creating_when_ready"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex}
	done := startInFlight(t, logPath, m)
	rec := postInput(t, name, `{"prompt":"first","when_ready":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s, want 202", rec.Code, rec.Body.String())
	}
	session.WriteMeta(m) // what the create publishes after the launch
	releaseFifo(t, release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the launch never returned; tmux log = %s", tmuxLog(logPath))
	}
	waitDelivered(t, logPath)
}

// A route switch stops the pane before it relaunches: the handler's own window has no tmux.
func TestWhenReadyDuringRouteSwitchWindowIsAccepted(t *testing.T) {
	logPath, up, _ := stoppedTmux(t)
	const name = "switching_when_ready"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
	end := markLaunching(name) // what HandleSessionRouteSwitch and HandleSessionDriver hold
	defer end()
	rec := postInput(t, name, `{"prompt":"first","when_ready":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s, want 202", rec.Code, rec.Body.String())
	}
	if err := os.WriteFile(up, nil, 0o644); err != nil { // the relaunch's pane appears
		t.Fatal(err)
	}
	waitDelivered(t, logPath)
}

// 202 is never followed by a silent drop: a delivery that finds no pane settles the meta failed.
func TestWhenReadyGiveUpLeavesTrace(t *testing.T) {
	stoppedTmux(t)
	old := initialPaneTries
	initialPaneTries = 1
	t.Cleanup(func() { initialPaneTries = old })
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
