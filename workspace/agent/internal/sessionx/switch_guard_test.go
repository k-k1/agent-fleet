package sessionx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// probeHookAgent runs afterProbe once codex's BuildLaunch has found the thread unloaded: the
// moment a racing Managed Resume would load it again.
type probeHookAgent struct {
	agents.Agent
	afterProbe func()
}

func (a probeHookAgent) BuildLaunch(m session.Meta, o agents.LaunchOpts) (agents.LaunchPlan, error) {
	p, err := a.Agent.BuildLaunch(m, o)
	if err == nil {
		a.afterProbe()
	}
	return p, err
}

// countingDriver records Resume calls; block, when set, holds Resume until it is closed.
type countingDriver struct {
	agents.Driver
	resumed atomic.Int32
	entered chan struct{}
	block   chan struct{}
}

func (d *countingDriver) Resume(session.Meta) (agents.ThreadHandle, error) {
	d.resumed.Add(1)
	if d.entered != nil {
		close(d.entered)
	}
	if d.block != nil {
		<-d.block
	}
	return stopOnlyHandle{}, nil
}

type stopOnlyHandle struct{ agents.ThreadHandle }

func (stopOnlyHandle) Interrupt(agents.InterruptOpts) (agents.InterruptResult, error) {
	return agents.InterruptResult{}, nil
}

func swapCodexDriver(t *testing.T, d agents.Driver) {
	t.Helper()
	prev := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = d
	t.Cleanup(func() { managedDrivers[session.KindCodex] = prev })
}

// A /turn that arrives while a switch to Terminal is between its checks and the meta write
// still reads managed. Its Resume must be refused, or it loads the thread again and leaves a
// Managed writer behind Terminal metadata (found in review of #1339).
func TestCodexSwitchRefusesAResumeMidSwitch(t *testing.T) {
	fakeTmux(t)
	const name = "guard_mid_switch"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindCodex, Driver: session.DriverManaged})
	codex.RememberSid(session.UUID(dir, name), "thr-unloaded")
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", fakeLoadedListServer(t))
	d := &countingDriver{Driver: managedDrivers[session.KindCodex]}
	swapCodexDriver(t, d)
	prevAgent := agentRegistry[session.KindCodex]
	t.Cleanup(func() { agentRegistry[session.KindCodex] = prevAgent })
	var mid *httptest.ResponseRecorder
	agentRegistry[session.KindCodex] = probeHookAgent{Agent: prevAgent, afterProbe: func() {
		mid = postTurn(t, name, `{"op":"interrupt"}`)
	}}

	if rec := postDriver(t, name, `{"driver":"tui"}`); rec.Code != http.StatusOK {
		t.Fatalf("switch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := d.resumed.Load(); n != 0 {
		t.Fatalf("a Managed Resume ran %d time(s) mid-switch", n)
	}
	if mid == nil || mid.Code != http.StatusConflict || !strings.Contains(mid.Body.String(), errCodeDriverSwitching) {
		t.Fatalf("/turn mid-switch: %+v, want 409 %s", mid, errCodeDriverSwitching)
	}
	// After the switch the meta says Terminal; a caller that read managed before must not
	// bring the thread back either.
	m, _ := session.ReadMeta(name)
	if m.DriverKind() != session.DriverTUI {
		t.Fatalf("driver = %s, want tui", m.DriverKind())
	}
	stale := m
	stale.Driver = session.DriverManaged
	gd, _ := driverOf(stale)
	if _, err := gd.Resume(stale); !errors.Is(err, errDriverSwitching) || d.resumed.Load() != 0 {
		t.Fatalf("stale Resume after the switch: err=%v resumed=%d", err, d.resumed.Load())
	}
}

// The other order: a Resume already in flight when the switch arrives. The switch is refused
// rather than checking a runtime that is about to come up.
func TestCodexSwitchRefusedWhileAResumeIsInFlight(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "guard_resume_in_flight"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	d := &countingDriver{Driver: managedDrivers[session.KindCodex], entered: make(chan struct{}), block: make(chan struct{})}
	swapCodexDriver(t, d)

	done := make(chan struct{})
	go func() {
		defer close(done)
		gd, _ := driverOf(session.Meta{Name: name, Kind: session.KindCodex, Driver: session.DriverManaged})
		_, _ = gd.Resume(session.Meta{Name: name, Kind: session.KindCodex, Driver: session.DriverManaged})
	}()
	<-d.entered
	rec := postDriver(t, name, `{"driver":"tui"}`)
	close(d.block)
	<-done
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeDriverSwitching) {
		t.Fatalf("status=%d body=%s, want 409 %s", rec.Code, rec.Body.String(), errCodeDriverSwitching)
	}
	if log, _ := os.ReadFile(logPath); strings.Contains(string(log), "new-session") {
		t.Fatalf("the refused switch opened a pane: %q", log)
	}
	// Once the Resume is over, the claim can be taken again.
	end, ok := beginCodexTerminalSwitch(name)
	if !ok {
		t.Fatal("the claim stayed blocked after the Resume returned")
	}
	end()
}
