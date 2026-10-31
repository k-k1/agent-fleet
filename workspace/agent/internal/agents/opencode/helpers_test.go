package opencode

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// isolatedHome gives the test its own HOME and hides the image's pin list, so neither the
// machine's home nor its versions.json can decide the test.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldPins := buildPinsPath
	buildPinsPath = filepath.Join(home, "no-versions.json")
	t.Cleanup(func() { buildPinsPath = oldPins })
	return home
}

// stubTerminalSlots replaces terminalSlots for the test. The restore waits for the reconciles the
// code under test started in the background (goReconcile): they read terminalSlots, and restoring
// it under them is a data race. The same goes for the idle watchers (idleWatch) an earlier test's
// Supervisor armed: they poll terminalSlots on a timer, so they are ended before every write.
func stubTerminalSlots(t *testing.T, fn func() []session.Meta) {
	t.Helper()
	idleWatch.stop()
	old := terminalSlots
	terminalSlots = fn
	t.Cleanup(func() {
		reconcileInflight.Wait()
		idleWatch.stop()
		terminalSlots = old
	})
}

// A 1.x launch drops a shared slot mode an earlier opencode 2 launch of the slot recorded.
func TestV1LaunchClearsAStaleSharedSlotMode(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 1)
	stubUsagePref(t, UsageOwn)
	m := session.Meta{Name: "oc-1", Dir: t.TempDir(), Kind: session.KindOpencode}
	sid := session.UUID(m.Dir, m.Name)
	markSlotMode(sid, modeShared)
	if _, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := slotModes.Read(sid); got != "" {
		t.Fatalf("slot mode after a 1.x launch = %q, want none", got)
	}
}

// A Supervisor an earlier test armed leaves its zero-demand watcher polling terminalSlots; the next
// stubTerminalSlots must end it before writing the var, or -race reports the write against the
// watcher's read.
func TestStubbingTerminalSlotsEndsLeftoverIdleWatchers(t *testing.T) {
	prev := agents.IdleTickForTest(time.Millisecond)
	t.Cleanup(func() { idleWatch.stop(); agents.IdleTickForTest(prev) })
	t.Setenv(idleGraceEnv, "600")
	s := &Supervisor{}
	s.mu.Lock()
	s.armIdleWatchLocked()
	s.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // several ticks: the watcher is reading terminalSlots
	t.Run("stub", func(t *testing.T) {
		stubTerminalSlots(t, func() []session.Meta { return nil })
		time.Sleep(20 * time.Millisecond)
	})
}

func freePortForTest(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}
