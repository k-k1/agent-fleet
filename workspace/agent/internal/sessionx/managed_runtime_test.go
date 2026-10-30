package sessionx

import (
	"maps"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/lcpp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// wantRuntime is the dispatch the per-kind switches in session_handlers.go made before
// agents.ManagedRuntime replaced them: kind -> the package functions ManagedAlive / managedBusy /
// managedInFlight / dropManagedRuntime / removeManagedLedger called. A nil row is "no runtime":
// false, false, false, no-op, no-op. Only the kinds that had a runtime then, and spellings that
// are not kinds (which the switches matched exactly), have a row. Which registered kind has a
// runtime at all is TestManagedRuntimeFollowsCaps, derived from the registry: a new kind needs
// no row here.
var wantRuntime = map[string]*agents.ManagedRuntimeOps{
	session.KindOpencode: {Alive: opencode.ManagedAlive, Busy: opencode.ManagedBusy, InFlight: opencode.ManagedInFlight, Drop: opencode.DropHandle, RemoveLedger: opencode.RemoveLedger},
	session.KindCodex:    {Alive: codex.ManagedAlive, Busy: codex.ManagedBusy, InFlight: codex.ManagedInFlight, Drop: codex.DropHandle, RemoveLedger: codex.RemoveLedger},
	session.KindCopilot:  {Alive: copilot.ManagedAlive, Busy: copilot.ManagedBusy, InFlight: copilot.ManagedInFlight, Drop: copilot.DropHandle, RemoveLedger: copilot.RemoveLedger},
	session.KindCursor:   {Alive: cursor.ManagedAlive, Busy: cursor.ManagedBusy, InFlight: cursor.ManagedInFlight, Drop: cursor.DropHandle, RemoveLedger: cursor.RemoveLedger},
	session.KindKiro:     {Alive: kiro.ManagedAlive, Busy: kiro.ManagedBusy, InFlight: kiro.ManagedInFlight, Drop: kiro.DropHandle, RemoveLedger: kiro.RemoveLedger},
	session.KindLcpp:     {Alive: lcpp.ManagedAlive, Busy: lcpp.ManagedBusy, InFlight: lcpp.ManagedInFlight, Drop: lcpp.DropHandle, RemoveLedger: lcpp.RemoveLedger},
	session.KindMuse:     {Alive: muse.ManagedAlive, Busy: muse.ManagedBusy, InFlight: muse.ManagedInFlight, Drop: muse.DropHandle, RemoveLedger: muse.RemoveLedger},
	// Not kinds: no NormalizeKind, no AgentOf claude fallback.
	"":         nil,
	"Codex":    nil,
	"kiro-cli": nil,
	"OPENCODE": nil,
	"unknown":  nil,
}

func funcName(f any) string {
	if reflect.ValueOf(f).IsNil() {
		return "<nil>"
	}
	return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
}

func sameFunc(a, b any) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.IsNil() || vb.IsNil() {
		return va.IsNil() && vb.IsNil()
	}
	return va.Pointer() == vb.Pointer()
}

// Each kind dispatches to exactly the package functions the switches called, so a live handle
// gets the same answer it did; the table also has to name every registered kind.
func TestManagedRuntimeDispatchPinned(t *testing.T) {
	for kind, want := range wantRuntime {
		got, ok := managedRuntimeOf(kind)
		if ok != (want != nil) {
			t.Errorf("%q: has runtime = %v, want %v", kind, ok, want != nil)
			continue
		}
		if !ok {
			continue
		}
		for _, f := range []struct {
			op        string
			got, want any
		}{
			{"Alive", got.Alive, want.Alive},
			{"Busy", got.Busy, want.Busy},
			{"InFlight", got.InFlight, want.InFlight},
			{"Drop", got.Drop, want.Drop},
			{"RemoveLedger", got.RemoveLedger, want.RemoveLedger},
		} {
			if !sameFunc(f.got, f.want) {
				t.Errorf("%q: %s dispatches to %s, want %s", kind, f.op, funcName(f.got), funcName(f.want))
			}
		}
	}
}

// With no handle for the name — every row, runtime or not — the questions answer false and
// drop / ledger removal do nothing (and do not panic).
func TestManagedRuntimeWithoutHandle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, kind := range append(RegisteredKinds(), slices.Collect(maps.Keys(wantRuntime))...) {
		m := session.Meta{Name: "no-such-managed-runtime", Kind: kind, Driver: session.DriverManaged}
		if ManagedAlive(m) || managedBusy(m) || managedInFlight(m) {
			t.Errorf("%q: alive/busy/inFlight = %v/%v/%v without a handle, want all false",
				kind, ManagedAlive(m), managedBusy(m), managedInFlight(m))
		}
		dropManagedRuntime(m)
		removeManagedLedger(m)
	}
}

// fakeRuntimeAgent answers each question independently, and only for its own session name, so a
// wrapper that asks the wrong op or passes the wrong name gets a different answer.
type fakeRuntimeAgent struct {
	agents.Agent
	name                  string
	alive, busy, inFlight bool
	calls                 []string // "op name"
}

func (f *fakeRuntimeAgent) ManagedRuntime() agents.ManagedRuntimeOps {
	ask := func(op string, answer *bool) func(string) bool {
		return func(n string) bool {
			f.calls = append(f.calls, op+" "+n)
			return n == f.name && *answer
		}
	}
	record := func(op string) func(string) {
		return func(n string) { f.calls = append(f.calls, op+" "+n) }
	}
	return agents.ManagedRuntimeOps{
		Alive:        ask("alive", &f.alive),
		Busy:         ask("busy", &f.busy),
		InFlight:     ask("inFlight", &f.inFlight),
		Drop:         record("drop"),
		RemoveLedger: record("removeLedger"),
	}
}

// Each sessionx wrapper asks its own op, with the session's name, and hands the answer through.
func TestManagedRuntimeWrappersPassThrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const kind = "rt-fake"
	f := &fakeRuntimeAgent{Agent: AgentOf(session.KindClaude), name: "rt-probe"}
	t.Cleanup(agentreg.SwapForTest(kind, f))
	m := session.Meta{Name: f.name, Kind: kind, Driver: session.DriverManaged}
	for _, tc := range []struct{ alive, busy, inFlight bool }{
		{true, false, false}, {false, true, false}, {false, false, true}, {true, true, true}, {},
	} {
		f.alive, f.busy, f.inFlight = tc.alive, tc.busy, tc.inFlight
		f.calls = nil
		got := []bool{ManagedAlive(m), managedBusy(m), managedInFlight(m)}
		if !slices.Equal(got, []bool{tc.alive, tc.busy, tc.inFlight}) {
			t.Errorf("runtime says alive/busy/inFlight = %v/%v/%v; wrappers answered %v", tc.alive, tc.busy, tc.inFlight, got)
		}
		if want := []string{"alive rt-probe", "busy rt-probe", "inFlight rt-probe"}; !slices.Equal(f.calls, want) {
			t.Errorf("calls %q, want %q", f.calls, want)
		}
	}
	f.calls = nil
	dropManagedRuntime(m)
	removeManagedLedger(m)
	if want := []string{"drop rt-probe", "removeLedger rt-probe"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls %q, want %q", f.calls, want)
	}
}

// A kind that can run Managed — ManagedOnly, ManagedDefault, or a managed driver — must
// implement agents.ManagedRuntime with every op set; a kind with no Managed route must not.
// Without it a new kind's Managed sessions read as stopped and idle, and stop / halt / archive
// leave the runtime running.
func TestManagedRuntimeFollowsCaps(t *testing.T) {
	for kind := range managedDrivers {
		if !slices.Contains(RegisteredKinds(), kind) {
			t.Errorf("managedDrivers has %q, which is not a registered kind", kind)
		}
	}
	for _, kind := range RegisteredKinds() {
		c := AgentOf(kind).Caps()
		_, driver := managedDrivers[kind]
		canManaged := c.ManagedOnly || c.ManagedDefault || driver
		rt, ok := AgentOf(kind).(agents.ManagedRuntime)
		if ok != canManaged {
			t.Errorf("%s: implements agents.ManagedRuntime = %v, but ManagedOnly=%v ManagedDefault=%v managed driver=%v",
				kind, ok, c.ManagedOnly, c.ManagedDefault, driver)
			continue
		}
		if !ok {
			continue
		}
		ops := rt.ManagedRuntime()
		if ops.Alive == nil || ops.Busy == nil || ops.InFlight == nil || ops.Drop == nil || ops.RemoveLedger == nil {
			t.Errorf("%s: ManagedRuntimeOps has a nil op: %+v", kind, ops)
		}
	}
}
