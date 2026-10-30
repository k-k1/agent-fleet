package opencode

import (
	"os/exec"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// daemonExit runs a fake daemon through waitDaemon with one live handle and returns the exit
// record written for it. Nobody asked the daemon to stop.
func daemonExit(t *testing.T, script string) status.ExitInfo {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	const name = "exit-143-oc-test"
	h := &threadHandle{name: name, alive: true}
	handlesMu.Lock()
	handles[name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, name)
		handlesMu.Unlock()
	})
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s := &Supervisor{}
	s.mu.Lock()
	s.up, s.cmd = true, cmd
	s.mu.Unlock()
	s.waitDaemon(cmd, nil, 1)
	ex, ok := status.ReadExit(name)
	if !ok {
		t.Fatal("no exit record")
	}
	return ex
}

func TestWaitDaemonRecordsCrashedWhenDaemonExits143OnItsOwn(t *testing.T) {
	ex := daemonExit(t, "exit 143")
	if ex.Reason != "crashed" || ex.Code != 143 || ex.Signal != 0 {
		t.Fatalf("unrequested exit 143 = %+v, want crashed/143/no signal", ex)
	}
}

func TestWaitDaemonKeepsStoppedWhenDaemonDiesOfSIGTERM(t *testing.T) {
	ex := daemonExit(t, "kill -TERM $$")
	if ex.Reason != "stopped" || ex.Signal != 15 {
		t.Fatalf("SIGTERM death = %+v, want stopped/sig 15", ex)
	}
}
