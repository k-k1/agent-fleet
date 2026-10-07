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
		{session.Meta{Kind: session.KindCodex}, false},
		{session.Meta{Kind: session.KindCursor}, false},
		{session.Meta{Kind: session.KindCopilot}, false},
		{session.Meta{Kind: session.KindKiro}, false},
		{session.Meta{Kind: session.KindOpencode}, false},
	} {
		if got := progressTrusted(tc.m); got != tc.want {
			t.Errorf("%+v: trusted = %v, want %v", tc.m, got, tc.want)
		}
	}
}
