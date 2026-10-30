package sessionx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// managedFixture is a Managed row's world: the status file's mtime, and what the kind's driver
// says (nil = cannot tell).
type managedFixture struct {
	status, driver *time.Time
}

func (f managedFixture) probes(now time.Time, kind string) progressProbes {
	at := func(t *time.Time) (time.Time, bool) {
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	}
	return progressProbes{
		now:       func() time.Time { return now },
		trusted:   progressTrusted,
		statusAt:  func(string) (time.Time, bool) { return at(f.status) },
		managedAt: func(session.Meta) (time.Time, bool) { return at(f.driver) },
		// A Managed row has no pane and no tool process under one: any read of them is a bug.
		paneOnly:    func(session.Meta) bool { panic("managed row read paneOnly") },
		observePane: func(string) bool { panic("managed row observed a pane") },
		paneAt:      func(string) (time.Time, bool) { panic("managed row read a pane") },
		sourceAt:    func(session.Meta) (time.Time, bool) { panic("managed row read a source file") },
		toolAlive:   func(session.Meta) (bool, bool) { panic("managed row probed a pane's tools") },
	}
}

func managedMeta(kind string) session.Meta {
	return session.Meta{Dir: "/d", Name: "n", Kind: kind, Driver: session.DriverManaged}
}

// The kinds whose driver signal was measured through a silent `sleep 120` (ADR 0055, 2026-10-17
// addendum). Adding a kind here needs that measurement, not a guess.
var managedMeasuredKinds = []string{session.KindCursor, session.KindKiro, session.KindCodex, session.KindOpencode}

func TestManagedProgressPerKind(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	h5, h2, m10, future := ago(5*time.Hour), ago(2*time.Hour), ago(10*time.Minute), ago(-12*time.Hour)

	for _, kind := range managedMeasuredKinds {
		for _, tc := range []struct {
			name  string
			state string
			f     managedFixture
			want  *time.Time // nil = no answer (the CP holds)
		}{
			{"stale status and a silent tool still running: now, keeps holding", "working", managedFixture{status: h5, driver: &now}, &now},
			{"compacting counts as busy", "compacting", managedFixture{status: h5, driver: &now}, &now},
			{"stale everywhere, no tool: the bound can lapse", "working", managedFixture{status: h5, driver: h2}, h2},
			{"a status write newer than the driver's sign wins", "working", managedFixture{status: m10, driver: h2}, m10},
			{"driver cannot tell: no answer, keeps holding", "working", managedFixture{status: h5}, nil},
			{"driver cannot tell and no status: no answer", "working", managedFixture{}, nil},
			{"future stamp is ignored, not clamped", "working", managedFixture{status: h5, driver: future}, h5},
			{"idle row has none", "idle", managedFixture{status: &now, driver: &now}, nil},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				got, ok := progressOf(managedMeta(kind), tc.state, tc.f.probes(now, kind))
				if (tc.want == nil) == ok || (ok && !got.Equal(*tc.want)) {
					t.Errorf("progressOf = %v, %v; want %v", got, ok, tc.want)
				}
			})
		}
	}
}

// Kinds with no measured signal keep holding without bound, however stale their files.
func TestManagedProgressUnmeasuredKindsStayUnbounded(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	h5 := now.Add(-5 * time.Hour)
	for _, kind := range []string{session.KindMuse, session.KindLcpp, session.KindCopilot} {
		if progressTrusted(managedMeta(kind)) {
			t.Errorf("%s: trusted without a measured signal", kind)
		}
		f := managedFixture{status: &h5, driver: &h5}
		if _, ok := progressOf(managedMeta(kind), "working", f.probes(now, kind)); ok {
			t.Errorf("%s: answered without a measured signal", kind)
		}
	}
}

// The measured set and the drivers that implement the capability are one list.
func TestManagedMeasuredKindsMatchTheDrivers(t *testing.T) {
	want := map[string]bool{}
	for _, k := range managedMeasuredKinds {
		want[k] = true
	}
	for kind := range managedDrivers {
		if got := managedProgressMeasured(managedMeta(kind)); got != want[kind] {
			t.Errorf("%s: managedProgressMeasured = %v, measured list says %v", kind, got, want[kind])
		}
	}
}

// A Managed row of a measured kind runs through the real probes without a pane: no live handle
// means the driver cannot tell, and the row keeps holding.
func TestManagedProgressWithoutHandleKeepsHolding(t *testing.T) {
	for _, kind := range managedMeasuredKinds {
		m := managedMeta(kind)
		m.Name = "no-such-managed-session"
		if _, ok := progressOf(m, "working", realProgressProbes); ok {
			t.Errorf("%s: a row without a live handle reported progress", kind)
		}
	}
}

type fakeReporter struct {
	agents.Driver
	at time.Time
	ok bool
}

func (f fakeReporter) ManagedProgress(session.Meta) (time.Time, bool) { return f.at, f.ok }

// managedProgressAt must hand the driver's answer through untouched, and progressOf must carry
// it to the wire: stale stays stale, fresh stays fresh, "cannot tell" stays unknown.
func TestManagedProgressAtPassesTheDriverAnswerThrough(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	old := managedDrivers[session.KindCursor]
	t.Cleanup(func() { managedDrivers[session.KindCursor] = old })
	m := managedMeta(session.KindCursor)
	for _, tc := range []struct {
		name string
		f    fakeReporter
		want *time.Time
	}{
		{"fresh", fakeReporter{at: now, ok: true}, &now},
		{"stale", fakeReporter{at: now.Add(-3 * time.Hour), ok: true}, ptr(now.Add(-3 * time.Hour))},
		{"unknown", fakeReporter{}, nil},
	} {
		managedDrivers[session.KindCursor] = tc.f
		p := managedFixture{}.probes(now, "")
		p.managedAt = managedProgressAt
		got, ok := progressOf(m, "working", p)
		if (tc.want == nil) == ok || (ok && !got.Equal(*tc.want)) {
			t.Errorf("%s: progressOf = %v, %v; want %v", tc.name, got, ok, tc.want)
		}
	}
}

func ptr(t time.Time) *time.Time { return &t }
