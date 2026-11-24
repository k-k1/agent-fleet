package notice

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// Origin roots: who is at the top of the chain that started a session. The Control Plane's push
// preferences switch on these exact strings.
const (
	OriginRootOwn      = "own"
	OriginRootAutorun  = "autorun"
	OriginRootSchedule = "schedule"
)

// OriginOf answers, for the session name, the root it descends from and whether the session was
// itself started by another session (origin "session"). It follows origin=session edges only,
// like the Console's sessionAutoKind: a handoff the member launched keeps origin "user" and ends
// the walk as the member's own. A missing parent or a cycle ends the walk as own with child
// true, never a guess at an unattended root.
func OriginOf(name string) (root string, child bool) {
	return originWalk(name, func(n string) (session.Meta, bool) { return session.ReadMeta(n) })
}

func originWalk(name string, read func(string) (session.Meta, bool)) (string, bool) {
	m, ok := read(name)
	if !ok {
		return OriginRootOwn, false
	}
	child := m.Origin == session.OriginSession
	seen := map[string]bool{name: true}
	for {
		switch m.Origin {
		case session.OriginQueue:
			return OriginRootAutorun, child
		case session.OriginSchedule:
			return OriginRootSchedule, child
		case session.OriginSession:
		default:
			return OriginRootOwn, child
		}
		parent := m.OriginSession
		if parent == "" || seen[parent] {
			return OriginRootOwn, child
		}
		seen[parent] = true
		pm, ok := read(parent)
		if !ok {
			return OriginRootOwn, child
		}
		m = pm
	}
}
