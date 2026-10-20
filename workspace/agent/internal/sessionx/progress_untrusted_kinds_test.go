package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// shell and ssm never report a busy state: their WireLive carries no State, and the optimistic
// "working" mark every /input writes (markSessionWorking is not kind-gated) is never read back
// for them. A row that cannot be busy cannot hold a Workspace on the busy path, so there is no
// signal to bound (#1943).
func TestShellAndSSMRowsNeverShowBusy(t *testing.T) {
	isolateAgentState(t)
	for _, kind := range []string{session.KindShell, session.KindSSM} {
		m := session.Meta{Name: "nobusy-" + kind, Kind: kind, Dir: t.TempDir()}
		status.Persist(session.UUID(m.Dir, m.Name), "working")
		if got := AgentOf(kind).WireLive(m, true).State; got != "" {
			t.Errorf("%s: WireLive state = %q, want empty (no busy state exists for this kind)", kind, got)
		}
		if got := wireSession(m, true).State; got == "working" || got == "compacting" {
			t.Errorf("%s: listed state = %q after a stored working mark, want not busy", kind, got)
		}
	}
}

// muse and lcpp have no Terminal route at all (ADR 0093 decision 2 / ADR 0095), so they are only
// ever Managed rows - #1942's concern. Pinned here so that a Terminal route appearing later has
// to come with a measured signal rather than inheriting a silent unbounded hold.
func TestMuseAndLcppHaveNoTerminalRow(t *testing.T) {
	for _, kind := range []string{session.KindMuse, session.KindLcpp} {
		a := AgentOf(kind)
		if !a.Caps().ManagedOnly {
			t.Errorf("%s: ManagedOnly = false; a Terminal route needs a measured progress signal first", kind)
		}
		if _, err := a.BuildLaunch(session.Meta{Name: "x", Kind: kind, Dir: t.TempDir()}, agents.LaunchOpts{}); err == nil {
			t.Errorf("%s: BuildLaunch succeeded, want a refusal (no pane program)", kind)
		}
	}
}

// Fail safe for all four kinds: whatever the files and tool processes say, an untrusted
// Terminal row gets no progress answer, so the Control Plane keeps holding on it.
func TestProgressOfUntrustedTerminalKindsKeepHolding(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-5 * time.Hour)
	for _, kind := range []string{session.KindMuse, session.KindLcpp, session.KindShell, session.KindSSM} {
		for _, tool := range []bool{false, true} {
			calls := 0
			p := progFixture{status: &stale, source: &stale, pane: &stale, tool: tool, toolObserved: true}.probes(now, &calls)
			p.trusted = progressTrusted
			p.paneOnly = progressPaneOnly
			m := session.Meta{Name: "hold-" + kind, Kind: kind, Dir: t.TempDir()}
			if at, ok := progressOf(m, "working", p); ok {
				t.Errorf("%s (tool alive=%v): progress answered %v for a stale row; want no answer (hold)", kind, tool, at)
			}
		}
	}
}
