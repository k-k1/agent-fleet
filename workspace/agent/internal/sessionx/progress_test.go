package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestProgressAt(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	type probe struct {
		status, pane, source *time.Time
		tool                 bool
	}
	at := func(t *time.Time) func() (time.Time, bool) {
		return func() (time.Time, bool) {
			if t == nil {
				return time.Time{}, false
			}
			return *t, true
		}
	}
	h2, h5, m10, future := ago(2*time.Hour), ago(5*time.Hour), ago(10*time.Minute), now.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		state string
		pr    probe
		want  string // "" = unknown
		tool  int    // expected toolAlive calls
	}{
		{"idle row has none", "idle", probe{status: &h2}, "", 0},
		{"status only", "working", probe{status: &h5}, h5.Format(time.RFC3339), 1},
		{"newest of status/source/pane wins", "compacting", probe{status: &h5, source: &h2, pane: &m10}, m10.Format(time.RFC3339), 1},
		{"stale everywhere, tool alive means now", "working", probe{status: &h5, source: &h2, tool: true}, now.Format(time.RFC3339), 1},
		{"fresh sign skips the process probe", "working", probe{pane: &now, tool: true}, now.Format(time.RFC3339), 0},
		{"no sign and no tool is unknown", "working", probe{}, "", 1},
		{"no sign but a tool is now", "working", probe{tool: true}, now.Format(time.RFC3339), 1},
		{"future mtime is clamped to now", "working", probe{source: &future}, now.Format(time.RFC3339), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := 0
			p := progressProbes{
				now:      func() time.Time { return now },
				statusAt: func(string) (time.Time, bool) { return at(tc.pr.status)() },
				paneAt:   func(string) (time.Time, bool) { return at(tc.pr.pane)() },
				sourceAt: func(session.Meta) (time.Time, bool) { return at(tc.pr.source)() },
				toolAlive: func(session.Meta) bool {
					tool++
					return tc.pr.tool
				},
			}
			if got := progressAt(session.Meta{Dir: "/d", Name: "n"}, tc.state, p); got != tc.want {
				t.Errorf("progressAt = %q, want %q", got, tc.want)
			}
			if tool != tc.tool {
				t.Errorf("toolAlive called %d times, want %d", tool, tc.tool)
			}
		})
	}
}
