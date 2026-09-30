package sessionx

import (
	"errors"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A codex Managed-to-Terminal switch checks that the session is stopped and that the shared
// app-server has unloaded its thread, then opens the pane and writes Terminal onto the meta. A
// Managed Resume anywhere in between (a /turn, an /input, an answer, all of which read the meta
// still saying managed) would load the thread again and leave a Managed writer behind Terminal
// metadata, with the pane on codex's "open in another app" screen. So while a switch runs, no
// codex Resume goes through driverOf, and a switch does not start while one is in flight.
// In memory only: a switch never outlives the request that runs it.
var (
	codexSwitchMu  sync.Mutex
	codexSwitching = map[string]bool{}
	codexResuming  = map[string]int{}
)

// errDriverSwitching refuses a Managed Resume that races a codex switch to Terminal.
var errDriverSwitching = errors.New("実行方式を切り替えています。切り替えが終わってからもう一度お試しください")

// errCodeDriverSwitching is its HTTP code (writeRuntimeErr, and the switch refused while a
// Resume is in flight).
const errCodeDriverSwitching = "driver_switching"

// beginCodexTerminalSwitch claims session name for a switch to Terminal. It fails while a
// Managed Resume of it is in flight; end releases the claim.
func beginCodexTerminalSwitch(name string) (end func(), ok bool) {
	codexSwitchMu.Lock()
	defer codexSwitchMu.Unlock()
	if codexSwitching[name] || codexResuming[name] > 0 {
		return nil, false
	}
	codexSwitching[name] = true
	return func() {
		codexSwitchMu.Lock()
		delete(codexSwitching, name)
		codexSwitchMu.Unlock()
	}, true
}

// switchGuardedDriver is what driverOf hands out for codex.
type switchGuardedDriver struct{ agents.Driver }

// Resume refuses while a switch to Terminal runs, and after one: a caller that read the meta
// before the switch wrote Terminal would otherwise bring the thread back under Terminal
// metadata. A meta not on disk yet (a session being created) is not Terminal.
func (d switchGuardedDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	codexSwitchMu.Lock()
	if codexSwitching[m.Name] {
		codexSwitchMu.Unlock()
		return nil, errDriverSwitching
	}
	codexResuming[m.Name]++
	codexSwitchMu.Unlock()
	defer func() {
		codexSwitchMu.Lock()
		if codexResuming[m.Name]--; codexResuming[m.Name] <= 0 {
			delete(codexResuming, m.Name)
		}
		codexSwitchMu.Unlock()
	}()
	if cur, ok := session.ReadMeta(m.Name); ok && cur.DriverKind() != session.DriverManaged {
		return nil, errDriverSwitching
	}
	return d.Driver.Resume(m)
}

// LiveHandle passes the codex driver's registry lookup through (the /turn queue edits), which
// starts nothing and so needs no guard.
func (d switchGuardedDriver) LiveHandle(m session.Meta) (agents.ThreadHandle, bool) {
	if lh, ok := d.Driver.(agents.LiveHandles); ok {
		return lh.LiveHandle(m)
	}
	return nil, false
}
