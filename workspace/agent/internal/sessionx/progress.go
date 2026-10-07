package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Session.ProgressAt / ProgressAgeSec: how recently a working / compacting row showed any sign
// of life, so the Control Plane can stop a frozen row from holding its Workspace awake forever
// (#1818). The age is computed here, on the clock the mtimes come from, because the CP compares
// it with a threshold and must not depend on its own clock agreeing with the Workspace's.
//
// It is the NEWEST of four independent signs, and a live row needs only one:
//   - the status file's mtime (hook kinds write it on every hook);
//   - the mtime of the kind's own state source (agents.ProgressReporter);
//   - the pane's last repaint, read from the idle-settle clock the pane-reading kinds keep
//     (no extra capture-pane);
//   - "now" while a tool process lives under the pane (procx.ToolProcessIn).
//
// "Could not observe" must never read as "not moving": the answer is "" (unknown, which the CP
// holds on, the behaviour before this field existed) unless the row is one whose live signals
// are known to be observable:
//   - only Terminal rows of kinds in progressTrusted: claude (the pane repaints its spinner
//     and elapsed time for as long as a tool runs) and agy (measured, docs/log/32). Every other
//     kind either never feeds the pane clock (codex, cursor, copilot, kiro) or has no pane at
//     all (managed: the runtime's in-flight tool is not visible from here), so a long silent
//     build there would look frozen;
//   - the pane must have been read recently (PaneChangedAt), and when the cheaper signs are
//     stale the process table must actually have been read (a tmux or /proc failure is not an
//     idle pane);
//   - an mtime in the future (clock step, restored backup) is ignored rather than clamped to
//     now: clamping would refresh it on every poll and hold the Workspace for as long as the
//     skew lasts.

// progressFresh is how recent the cheap signs must already be for the tool-process probe to
// be skipped: "now" and "a minute ago" are the same answer to an hour-scale threshold, and
// the probe costs a tmux call per busy row per poll.
const progressFresh = time.Minute

// progressSkew is how far into the future an mtime may be before it is ignored.
const progressSkew = time.Minute

// progressTrusted reports whether this row's live signals are known to be observable.
func progressTrusted(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return false
	}
	switch NormalizeKind(m.Kind) {
	case session.KindClaude, session.KindAgy:
		return true
	}
	return false
}

// progressProbes are the world inputs of progressOf, injectable for tests.
type progressProbes struct {
	now      func() time.Time
	trusted  func(m session.Meta) bool
	statusAt func(sid string) (time.Time, bool)
	paneAt   func(name string) (time.Time, bool)
	sourceAt func(m session.Meta) (time.Time, bool)
	// toolAlive: a tool process runs under the pane; observed=false when the pane root or the
	// process table could not be read.
	toolAlive func(m session.Meta) (alive, observed bool)
}

var realProgressProbes = progressProbes{
	now:      time.Now,
	trusted:  progressTrusted,
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

func toolProcessUnderPane(m session.Meta) (alive, observed bool) {
	root := procx.PaneRoot(m.Name)
	if root == 0 {
		return false, false
	}
	tab := procx.Snapshot()
	if _, ok := tab[root]; !ok {
		return false, false
	}
	return procx.ToolProcessIn(root, tab), true
}

// progressOf is when a live row last showed progress; ok=false when the row is not busy, is
// not trusted, or nothing reliable says.
func progressOf(m session.Meta, state string, p progressProbes) (time.Time, bool) {
	if (state != "working" && state != "compacting") || !p.trusted(m) {
		return time.Time{}, false
	}
	now := p.now()
	paneAt, paneOK := p.paneAt(m.Name)
	if !paneOK {
		return time.Time{}, false
	}
	var newest time.Time
	take := func(t time.Time, ok bool) {
		if !ok || t.After(now.Add(progressSkew)) {
			return
		}
		if t.After(now) {
			t = now
		}
		if t.After(newest) {
			newest = t
		}
	}
	take(p.statusAt(session.UUID(m.Dir, m.Name)))
	take(p.sourceAt(m))
	take(paneAt, true)
	if newest.IsZero() || now.Sub(newest) >= progressFresh {
		alive, observed := p.toolAlive(m)
		switch {
		case alive:
			newest = now
		case !observed:
			return time.Time{}, false
		}
	}
	return newest, !newest.IsZero()
}

// fillProgress sets the wire fields from progressOf.
func fillProgress(s *session.Session, m session.Meta, p progressProbes) {
	at, ok := progressOf(m, s.State, p)
	if !ok {
		return
	}
	s.ProgressAt = at.Format(time.RFC3339)
	if age := int(p.now().Sub(at) / time.Second); age > 0 {
		s.ProgressAgeSec = age
	}
}
