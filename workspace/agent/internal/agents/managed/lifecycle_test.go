package managed

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type fakeHandle struct {
	alive, busy, holds bool
	aborted, downed    int
	stopped            *int
}

func (h *fakeHandle) Alive() bool      { return h.alive }
func (h *fakeHandle) Busy() bool       { return h.busy }
func (h *fakeHandle) HoldsEntry() bool { return h.holds }
func (h *fakeHandle) Abort()           { h.aborted++ }

// downHandle is a handle with a child to stop.
type downHandle struct{ fakeHandle }

func (h *downHandle) Down() func() {
	h.downed++
	h.alive = false
	return func() { *h.stopped++ }
}

func newLife[H Handle](t *testing.T) (*Lifecycle[H], *map[string]H) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	m := map[string]H{}
	return &Lifecycle[H]{Kind: session.KindCodex, Name: "codex", Mu: &mu, Handles: &m,
		Ledger: agents.NewMsgLedger("managed-test-msgledger")}, &m
}

func TestRegistryAndQuestions(t *testing.T) {
	l, m := newLife[*fakeHandle](t)
	if l.Alive("a") || l.Busy("a") || l.InFlight("a") {
		t.Fatal("an unregistered session answered yes")
	}
	a := l.GetOrAdd("a", func() *fakeHandle { return &fakeHandle{alive: true, busy: true, holds: true} })
	if again := l.GetOrAdd("a", func() *fakeHandle { t.Fatal("mk ran for a registered name"); return nil }); again != a {
		t.Fatal("GetOrAdd replaced the registered handle")
	}
	(*m)["b"] = &fakeHandle{}
	if !l.Alive("a") || !l.Busy("a") || !l.InFlight("a") || l.Alive("b") || l.Busy("b") || l.InFlight("b") {
		t.Fatal("the questions do not follow the handles' answers")
	}
	if live := l.Live(); len(live) != 1 || live[0] != a {
		t.Fatalf("Live = %v, want only the alive handle", live)
	}
	l.Abort()
	if a.aborted != 1 || (*m)["b"].aborted != 0 {
		t.Fatalf("Abort reached a=%d b=%d, want only the live handle", a.aborted, (*m)["b"].aborted)
	}
	if h, ok := l.Take("a"); !ok || h != a {
		t.Fatal("Take did not return the handle")
	}
	if _, ok := l.Get("a"); ok {
		t.Fatal("Take left the handle registered")
	}
	// A test may swap the kind's map: the lifecycle reads the variable, not a copy.
	*m = map[string]*fakeHandle{"c": {alive: true}}
	if !l.Alive("c") {
		t.Fatal("the lifecycle kept reading the old map")
	}
}

func TestShutdownStopsOnlyHandlesWithAChild(t *testing.T) {
	l, m := newLife[Handle](t)
	stopped := 0
	d := &downHandle{fakeHandle{alive: true, stopped: &stopped}}
	(*m)["child"] = d
	(*m)["none"] = &fakeHandle{alive: true}
	l.Shutdown()
	if d.downed != 1 || stopped != 1 || d.alive {
		t.Fatalf("downed %d, stopped %d, alive %v; want the child marked dead and stopped once", d.downed, stopped, d.alive)
	}
}

func TestReconcileResumesWhatTheKindOwns(t *testing.T) {
	l, m := newLife[*fakeHandle](t)
	write := func(name string, mut func(*session.Meta)) {
		mm := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
		if mut != nil {
			mut(&mm)
		}
		session.WriteMeta(mm)
	}
	write("plain", nil)
	write("archived", func(m *session.Meta) { m.Archived = true })
	write("stopped", func(m *session.Meta) { m.StoppedAt = "2026-10-29T00:00:00Z" })
	write("stopped-held", func(m *session.Meta) { m.StoppedAt = "2026-10-29T00:00:00Z" })
	(*m)["stopped-held"] = &fakeHandle{}
	write("other-kind", func(m *session.Meta) { m.Kind = session.KindCursor })
	write("terminal", func(m *session.Meta) { m.Driver = "" })
	write("skipped", nil)
	write("unclaimed", nil)
	write("fails", nil)

	var order []string
	l.Resume = func(m session.Meta) error {
		order = append(order, "resume "+m.Name)
		if m.Name == "fails" {
			return errors.New("boom")
		}
		return nil
	}
	l.SkipReconcile = func(m session.Meta) bool { return m.Name == "skipped" }
	l.Claim = func(name string) (func(), bool) {
		if name == "unclaimed" {
			return nil, false
		}
		order = append(order, "claim "+name)
		return func() { order = append(order, "release "+name) }, true
	}
	var logged []string
	l.LogReconcile = func(m session.Meta, reason string, err error) {
		logged = append(logged, m.Name+" "+reason+" "+err.Error())
	}
	after := 0
	l.AfterReconcile = func(reason string) {
		if reason != "boot" {
			t.Errorf("AfterReconcile reason %q", reason)
		}
		after++
	}
	l.Reconcile("boot")

	var resumed []string
	for i, o := range order {
		if len(o) > 7 && o[:7] == "resume " {
			name := o[7:]
			resumed = append(resumed, name)
			if i == 0 || order[i-1] != "claim "+name || i+1 >= len(order) || order[i+1] != "release "+name {
				t.Errorf("resume of %s not inside its claim: %v", name, order)
			}
		}
	}
	slices.Sort(resumed)
	if want := []string{"fails", "plain", "stopped-held"}; !slices.Equal(resumed, want) {
		t.Errorf("resumed %v, want %v", resumed, want)
	}
	if len(logged) != 1 || logged[0] != "fails boot boom" {
		t.Errorf("logged %v, want the one failure", logged)
	}
	if after != 1 {
		t.Errorf("AfterReconcile ran %d times", after)
	}
}
