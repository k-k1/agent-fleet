package sessionx

import (
	"sync"
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
//     and elapsed time for as long as a tool runs), agy (measured, docs/log/32) and the
//     pane-only kinds codex, cursor, copilot, kiro and opencode, whose pane was measured to
//     repaint about once a second through a 120 s silent `sleep` (ADR 0055 addendum, #1830).
//     Every other kind has no measured signal (muse, lcpp, shell, ssm) or has no pane at all
//     (managed: the runtime's in-flight tool is not visible from here), so a long silent
//     build there would look frozen;
//   - the pane-only kinds do not keep the idle-settle clock themselves, so this file feeds it
//     (one capture-pane per busy row per poll), and they skip the tool-process probe: codex
//     keeps setsid() helper daemons under its pane that read as a tool even when idle, which
//     would hold such a row forever;
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
	return progressPaneOnly(m)
}

// progressPaneOnly: the kind's only measured live signal is the pane repaint, and nothing else
// may stand in for it. Never true for a Managed row (no pane).
func progressPaneOnly(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return false
	}
	switch NormalizeKind(m.Kind) {
	case session.KindCodex, session.KindCursor, session.KindCopilot, session.KindKiro, session.KindOpencode:
		return true
	}
	return false
}

// progressProbes are the world inputs of progressOf, injectable for tests.
type progressProbes struct {
	now     func() time.Time
	trusted func(m session.Meta) bool
	// paneOnly: see progressPaneOnly. observePane records the pane's current frame on the
	// idle-settle clock; the kinds that do not read their own pane need it before paneAt speaks.
	paneOnly    func(m session.Meta) bool
	observePane func(name string)
	statusAt    func(sid string) (time.Time, bool)
	paneAt      func(name string) (time.Time, bool)
	sourceAt    func(m session.Meta) (time.Time, bool)
	// toolAlive: a tool process runs under the pane; observed=false when the pane root or the
	// process table could not be read.
	toolAlive func(m session.Meta) (alive, observed bool)
}

var realProgressProbes = progressProbes{
	now:         time.Now,
	trusted:     progressTrusted,
	paneOnly:    progressPaneOnly,
	observePane: func(name string) { tmuxx.ObservePane(name) },
	statusAt:    status.StateAt,
	paneAt:      tmuxx.PaneChangedAt,
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
	paneOnly := p.paneOnly(m)
	if paneOnly {
		p.observePane(m.Name)
	}
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
	if !paneOnly && (newest.IsZero() || now.Sub(newest) >= progressFresh) {
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

// Session.StateSince (#1819): when a working / compacting row's current state began, so the
// admin forecast can say "working for 21h" next to the session holding a Workspace awake.
//
// It is APPROXIMATE, and the two sources are both upper bounds on the true start:
//   - the first poll that observed the state (kept in memory per session, so it resets when the
//     Agent restarts, and a state that flipped away and back between two polls is not seen);
//   - for kinds whose hooks or driver write the state boundary (claude, codex, opencode, managed),
//     the status file's mtime while the stored state equals this one. A hook can rewrite the
//     same state, which moves the mtime later than the real start. Terminal agy / copilot /
//     cursor / kiro are excluded: their status is written by /input and never cleared by the
//     poll that sees the turn end, so a 21 h old "working" can sit there under a new turn.
//
// The OLDER of the two is reported, since each can only be late, and the start once adopted is
// kept for as long as the same busy state continues. Only busy rows carry it.

type stateSeen struct {
	state string
	since time.Time
}

var (
	stateSeenMu  sync.Mutex
	stateSeenMap = map[string]stateSeen{}
)

// sinceProbes are the world inputs of stateSinceOf, injectable for tests.
type sinceProbes struct {
	now         func() time.Time
	statusState func(sid string) (string, bool)
	statusAt    func(sid string) (time.Time, bool)
}

var realSinceProbes = sinceProbes{
	now: time.Now,
	statusState: func(sid string) (string, bool) {
		st, ok := status.Read(sid)
		return st.State, ok
	},
	statusAt: status.StateAt,
}

// stateSinceOf records this poll's observation and returns when the row's current busy state
// began (ok=false for a row that is not busy, which also forgets it).
func stateSinceOf(m session.Meta, state string, alive bool, p sinceProbes) (time.Time, bool) {
	stateSeenMu.Lock()
	defer stateSeenMu.Unlock()
	if !alive || (state != "working" && state != "compacting") {
		delete(stateSeenMap, m.Name)
		return time.Time{}, false
	}
	now := p.now()
	e, ok := stateSeenMap[m.Name]
	if !ok || e.state != state {
		e = stateSeen{state: state, since: now}
		stateSeenMap[m.Name] = e
	}
	if statusMarksBoundary(m) {
		sid := session.UUID(m.Dir, m.Name)
		if st, ok := p.statusState(sid); ok && st == state {
			if at, ok := p.statusAt(sid); ok && at.Before(e.since) && !at.After(now) {
				e.since = at
				stateSeenMap[m.Name] = e
			}
		}
	}
	return e.since, true
}

// statusMarksBoundary: the status file's mtime can stand for the start of the current state. Not
// for Terminal rows of the hook-less kinds (see above).
func statusMarksBoundary(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return true
	}
	switch NormalizeKind(m.Kind) {
	case session.KindAgy, session.KindCopilot, session.KindCursor, session.KindKiro:
		return false
	}
	return true
}

// fillStateSince sets Session.StateSince from stateSinceOf.
func fillStateSince(s *session.Session, m session.Meta, p sinceProbes) {
	if at, ok := stateSinceOf(m, s.State, s.Alive, p); ok {
		s.StateSince = at.Format(time.RFC3339)
	}
}
