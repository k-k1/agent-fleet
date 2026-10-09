package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type progFixture struct {
	status, pane, source *time.Time
	tool, toolObserved   bool
	trusted              bool
	paneOnly             bool
	observed             *[]string // names recorded on the pane clock
}

func (f progFixture) probes(now time.Time, toolCalls *int) progressProbes {
	at := func(t *time.Time) (time.Time, bool) {
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	}
	return progressProbes{
		now:      func() time.Time { return now },
		trusted:  func(session.Meta) bool { return f.trusted },
		paneOnly: func(session.Meta) bool { return f.paneOnly },
		observePane: func(name string) {
			if f.observed != nil {
				*f.observed = append(*f.observed, name)
			}
		},
		statusAt: func(string) (time.Time, bool) { return at(f.status) },
		paneAt:   func(string) (time.Time, bool) { return at(f.pane) },
		sourceAt: func(session.Meta) (time.Time, bool) { return at(f.source) },
		toolAlive: func(session.Meta) (bool, bool) {
			*toolCalls++
			return f.tool, f.toolObserved
		},
	}
}

func TestProgressOf(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	h2, h5, m10, far := ago(2*time.Hour), ago(5*time.Hour), ago(10*time.Minute), ago(-12*time.Hour)
	for _, tc := range []struct {
		name  string
		state string
		f     progFixture
		want  *time.Time // nil = no answer (the CP holds)
		tool  int
	}{
		{"idle row has none", "idle", progFixture{trusted: true, pane: &now}, nil, 0},
		{"untrusted row (managed / unverified kind) never answers, however stale its files", "working", progFixture{status: h5, source: h5, pane: h5}, nil, 0},
		{"pane never observed: no answer, not 'frozen'", "working", progFixture{trusted: true, status: h5, source: h5}, nil, 0},
		{"newest of status/source/pane wins", "compacting", progFixture{trusted: true, status: h5, source: h2, pane: m10, toolObserved: true}, m10, 1},
		{"stale everywhere, tool alive means now", "working", progFixture{trusted: true, status: h5, pane: h2, tool: true, toolObserved: true}, &now, 1},
		{"stale everywhere, tool probe failed: no answer", "working", progFixture{trusted: true, status: h5, pane: h2}, nil, 1},
		{"stale everywhere, tool probe saw nothing: genuinely frozen", "working", progFixture{trusted: true, status: h5, pane: h2, toolObserved: true}, h2, 1},
		{"fresh sign skips the process probe", "working", progFixture{trusted: true, pane: &now, tool: true}, &now, 0},
		{"future source mtime is ignored, not clamped", "working", progFixture{trusted: true, source: far, pane: h2, toolObserved: true}, h2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, ok := progressOf(session.Meta{Dir: "/d", Name: "n"}, tc.state, tc.f.probes(now, &calls))
			if (tc.want == nil) == ok || (ok && !got.Equal(*tc.want)) {
				t.Errorf("progressOf = %v, %v; want %v", got, ok, tc.want)
			}
			if calls != tc.tool {
				t.Errorf("toolAlive called %d times, want %d", calls, tc.tool)
			}
		})
	}
}

// A frozen source whose mtime sits in the future must not look fresh poll after poll.
func TestProgressOfFutureMtimeDoesNotRefreshEveryPoll(t *testing.T) {
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	future := start.Add(12 * time.Hour)
	pane := start.Add(-2 * time.Hour)
	f := progFixture{trusted: true, source: &future, pane: &pane, toolObserved: true}
	for i := 0; i < 5; i++ {
		now := start.Add(time.Duration(i) * time.Hour)
		calls := 0
		got, ok := progressOf(session.Meta{Name: "n"}, "working", f.probes(now, &calls))
		if !ok || !got.Equal(pane) {
			t.Fatalf("poll %d: progressOf = %v, %v; want the pane's %v", i, got, ok, pane)
		}
	}
}

func TestFillProgressAge(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	p := now.Add(-90 * time.Minute)
	calls := 0
	f := progFixture{trusted: true, pane: &p, toolObserved: true}
	s := session.Session{State: "working"}
	fillProgress(&s, session.Meta{Name: "n"}, f.probes(now, &calls))
	if s.ProgressAt != p.Format(time.RFC3339) || s.ProgressAgeSec != 5400 {
		t.Errorf("got %q / %d, want %q / 5400", s.ProgressAt, s.ProgressAgeSec, p.Format(time.RFC3339))
	}
}

func TestProgressTrusted(t *testing.T) {
	for _, tc := range []struct {
		m    session.Meta
		want bool
	}{
		{session.Meta{Kind: session.KindClaude}, true},
		{session.Meta{Kind: ""}, true}, // an old row without a kind is claude
		{session.Meta{Kind: session.KindAgy}, true},
		{session.Meta{Kind: session.KindClaude, Driver: session.DriverManaged}, false},
		// Pane repaint measured through a 120 s silent tool (#1830).
		{session.Meta{Kind: session.KindCodex}, true},
		{session.Meta{Kind: session.KindCursor}, true},
		{session.Meta{Kind: session.KindCopilot}, true},
		{session.Meta{Kind: session.KindKiro}, true},
		{session.Meta{Kind: session.KindOpencode}, true},
		{session.Meta{Kind: session.KindCodex, Driver: session.DriverManaged}, false},
		{session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged}, false},
		// No measured signal: keep holding.
		{session.Meta{Kind: session.KindMuse}, false},
		{session.Meta{Kind: session.KindLcpp}, false},
		{session.Meta{Kind: session.KindShell}, false},
		{session.Meta{Kind: session.KindSSM}, false},
	} {
		if got := progressTrusted(tc.m); got != tc.want {
			t.Errorf("%+v: trusted = %v, want %v", tc.m, got, tc.want)
		}
	}
}

func TestStateSinceOf(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	now := t0
	var stState string
	var stAt time.Time
	stOK := false
	p := sinceProbes{
		now:         func() time.Time { return now },
		statusState: func(string) (string, bool) { return stState, stOK },
		statusAt:    func(string) (time.Time, bool) { return stAt, stOK },
	}
	m := session.Meta{Dir: "/d", Name: "since-test"}
	t.Cleanup(func() { stateSinceOf(m, "idle", true, p) })

	got, ok := stateSinceOf(m, "working", true, p)
	if !ok || !got.Equal(t0) {
		t.Fatalf("first poll: %v %v, want %v", got, ok, t0)
	}
	now = t0.Add(3 * time.Hour)
	if got, _ := stateSinceOf(m, "working", true, p); !got.Equal(t0) {
		t.Errorf("a later poll moved since to %v; it is the FIRST observation", got)
	}
	// A hook kind: the status file says the state began earlier than this Agent has watched.
	stState, stAt, stOK = "working", t0.Add(-5*time.Hour), true
	if got, _ := stateSinceOf(m, "working", true, p); !got.Equal(t0.Add(-5 * time.Hour)) {
		t.Errorf("status mtime older than the observation was not used: %v", got)
	}
	// A status file holding another state, or a future mtime, says nothing (fresh stretches: an
	// adopted start is kept while the state continues).
	stateSinceOf(m, "idle", true, p)
	stState = "idle"
	base := now
	if got, _ := stateSinceOf(m, "working", true, p); !got.Equal(base) {
		t.Errorf("a status file in another state was used: %v", got)
	}
	stateSinceOf(m, "idle", true, p)
	stState, stAt = "working", now.Add(time.Hour)
	if got, _ := stateSinceOf(m, "working", true, p); !got.Equal(base) {
		t.Errorf("a future status mtime was used: %v", got)
	}
	// Going idle forgets; the next working starts a new clock.
	if _, ok := stateSinceOf(m, "idle", true, p); ok {
		t.Error("an idle row has a since")
	}
	stOK = false
	if got, _ := stateSinceOf(m, "working", true, p); !got.Equal(now) {
		t.Errorf("a new working stretch kept the old start: %v", got)
	}
	// compacting after working is a different state: the clock restarts.
	now = now.Add(time.Hour)
	if got, _ := stateSinceOf(m, "compacting", true, p); !got.Equal(now) {
		t.Errorf("compacting did not restart the clock: %v", got)
	}
}

// The start adopted from an old status file survives a later rewrite of the same state (F3), and
// a Terminal hook-less kind never adopts a status mtime, which can belong to a previous turn (F2).
func TestStateSinceKeepsAdoptedStartAndSkipsHooklessStatus(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	now := t0
	stState, stAt := "working", t0.Add(-21*time.Hour)
	p := sinceProbes{
		now:         func() time.Time { return now },
		statusState: func(string) (string, bool) { return stState, true },
		statusAt:    func(string) (time.Time, bool) { return stAt, true },
	}
	hook := session.Meta{Dir: "/d", Name: "since-hook", Kind: session.KindClaude}
	t.Cleanup(func() { stateSinceOf(hook, "idle", true, p) })
	if got, _ := stateSinceOf(hook, "working", true, p); !got.Equal(stAt) {
		t.Fatalf("first poll after a restart: %v, want the status mtime %v", got, stAt)
	}
	// A hook rewrites the same state 10 minutes later: the 21 h must not shrink to 10 min.
	now = t0.Add(10 * time.Minute)
	stAt = now
	if got, _ := stateSinceOf(hook, "working", true, p); !got.Equal(t0.Add(-21 * time.Hour)) {
		t.Errorf("a status rewrite rejuvenated the start: %v", got)
	}

	// Terminal cursor: a stuck "working" status from 21 h ago, an idle poll, then a new turn.
	now = t0
	stAt = t0.Add(-21 * time.Hour)
	cur := session.Meta{Dir: "/d", Name: "since-cursor", Kind: session.KindCursor}
	t.Cleanup(func() { stateSinceOf(cur, "idle", true, p) })
	stateSinceOf(cur, "idle", true, p)
	now = t0.Add(time.Hour)
	if got, _ := stateSinceOf(cur, "working", true, p); !got.Equal(now) {
		t.Errorf("a new Terminal turn took the previous turn's status mtime: %v, want %v", got, now)
	}
}

// Per pane-only kind: status and state source stale for hours, a long silent tool running (so
// the pane keeps repainting). The row must read as live, and the tool-process probe must stay
// out of it (codex's setsid() helpers would read as a tool forever).
func TestProgressOfPaneOnlyKinds(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	h5, h2, fresh := now.Add(-5*time.Hour), now.Add(-2*time.Hour), now.Add(-3*time.Second)
	for _, kind := range []string{session.KindCodex, session.KindCursor, session.KindCopilot, session.KindKiro, session.KindOpencode} {
		t.Run(kind, func(t *testing.T) {
			m := session.Meta{Dir: "/d", Name: "n", Kind: kind}
			if !progressTrusted(m) || !progressPaneOnly(m) {
				t.Fatalf("%s must be a trusted pane-only kind", kind)
			}
			var observed []string
			calls := 0
			f := progFixture{trusted: true, paneOnly: true, status: &h5, source: &h5, pane: &fresh, tool: true, toolObserved: true, observed: &observed}
			got, ok := progressOf(m, "working", f.probes(now, &calls))
			if !ok || !got.Equal(fresh) {
				t.Errorf("repainting pane under stale status/source: progressOf = %v, %v; want %v", got, ok, fresh)
			}
			if calls != 0 || len(observed) != 1 {
				t.Errorf("toolAlive called %d times (want 0), pane observed %d times (want 1)", calls, len(observed))
			}
			// Frozen: nothing repaints. A live-looking tool process must not rescue it.
			f.pane = &h2
			got, ok = progressOf(m, "working", f.probes(now, &calls))
			if !ok || !got.Equal(h2) || calls != 0 {
				t.Errorf("frozen pane: progressOf = %v, %v, toolAlive calls %d; want %v, true, 0", got, ok, calls, h2)
			}
			// Pane never read: unknown, never "frozen".
			f.pane = nil
			if _, ok := progressOf(m, "working", f.probes(now, &calls)); ok {
				t.Error("unreadable pane must give no answer")
			}
		})
	}
}
