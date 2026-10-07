package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Session.ProgressAt: how recently a working / compacting row showed any sign of life, so the
// Control Plane can stop a frozen row from holding its Workspace awake forever (#1818).
//
// It is the NEWEST of four independent signs, and a live row needs only one:
//   - the status file's mtime (hook kinds write it on every hook, managed drivers on every
//     turn boundary);
//   - the mtime of the kind's own state source (agents.ProgressReporter);
//   - the pane's last repaint, read from the idle-settle clock the pane-reading kinds already
//     keep (no extra capture-pane);
//   - "now" while a tool process lives under the pane (procx.ToolProcessIn).
//
// Invariant: a legitimate long run keeps at least one of them fresh, so only a row that is
// frozen on every axis lapses. When no sign exists at all the answer is "" (unknown), which the
// CP reads as "hold", the behaviour before this field existed.
//
// Known gaps (they under-report progress, never over-report): a managed session has no tmux
// pane and no per-session process root reachable from here, so it gets no repaint or tool
// signal; and kinds without a ProgressReporter (opencode: one DB shared by every session;
// kiro, muse, lcpp, shell, ssm) rely on the status file and the pane.

// progressFresh is how recent the cheap signs must already be for the tool-process probe to
// be skipped: "now" and "a minute ago" are the same answer to an hour-scale threshold, and
// the probe costs a tmux call per busy row per poll.
const progressFresh = time.Minute

// progressProbes are the world inputs of progressAt, injectable for tests.
type progressProbes struct {
	now       func() time.Time
	statusAt  func(sid string) (time.Time, bool)
	paneAt    func(name string) (time.Time, bool)
	sourceAt  func(m session.Meta) (time.Time, bool)
	toolAlive func(m session.Meta) bool
}

var realProgressProbes = progressProbes{
	now:      time.Now,
	statusAt: status.StateAt,
	paneAt:   tmuxx.PaneChangedAt,
	sourceAt: func(m session.Meta) (time.Time, bool) {
		if r, ok := AgentOf(m.Kind).(agents.ProgressReporter); ok {
			return r.StateSourceModTime(m)
		}
		return time.Time{}, false
	},
	toolAlive: toolProcessUnderPane,
}

// toolProcessUnderPane: a tool process runs under the session's tmux pane. A managed session
// has no pane, so PaneRoot answers 0 and this is false.
func toolProcessUnderPane(m session.Meta) bool {
	root := procx.PaneRoot(m.Name)
	return root != 0 && procx.ToolProcessIn(root, procx.Snapshot())
}

// progressAt is the RFC3339 ProgressAt for a live row in state, "" when the row is not busy
// or nothing says when it last moved.
func progressAt(m session.Meta, state string, p progressProbes) string {
	if state != "working" && state != "compacting" {
		return ""
	}
	now := p.now()
	var newest time.Time
	take := func(t time.Time, ok bool) {
		if ok && t.After(newest) {
			newest = t
		}
	}
	take(p.statusAt(session.UUID(m.Dir, m.Name)))
	take(p.sourceAt(m))
	take(p.paneAt(m.Name))
	if newest.IsZero() || now.Sub(newest) >= progressFresh {
		if p.toolAlive(m) {
			newest = now
		}
	}
	if newest.IsZero() {
		return ""
	}
	if newest.After(now) {
		newest = now // a clock-skewed mtime must not push the lapse into the future
	}
	return newest.Format(time.RFC3339)
}
