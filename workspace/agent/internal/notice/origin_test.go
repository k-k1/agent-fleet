package notice

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestOriginWalk(t *testing.T) {
	metas := map[string]session.Meta{
		"own":        {Origin: session.OriginUser},
		"own-kid":    {Origin: session.OriginSession, OriginSession: "own"},
		"own-grand":  {Origin: session.OriginSession, OriginSession: "own-kid"},
		"run":        {Origin: session.OriginQueue},
		"run-kid":    {Origin: session.OriginSession, OriginSession: "run"},
		"sched":      {Origin: session.OriginSchedule},
		"sched-kid":  {Origin: session.OriginSession, OriginSession: "sched"},
		"handoff":    {Origin: session.OriginUser, OriginSession: "run"},
		"handoffkid": {Origin: session.OriginSession, OriginSession: "handoff"},
		"orphan":     {Origin: session.OriginSession, OriginSession: "gone"},
		"loopA":      {Origin: session.OriginSession, OriginSession: "loopB"},
		"loopB":      {Origin: session.OriginSession, OriginSession: "loopA"},
	}
	read := func(n string) (session.Meta, bool) { m, ok := metas[n]; return m, ok }
	for name, want := range map[string][2]any{
		"own": {"own", false}, "own-kid": {"own", true}, "own-grand": {"own", true},
		"run": {"autorun", false}, "run-kid": {"autorun", true},
		"sched": {"schedule", false}, "sched-kid": {"schedule", true},
		"handoff": {"own", false}, "handoffkid": {"own", true},
		"orphan": {"own", true}, "loopA": {"own", true}, "nometa": {"own", false},
	} {
		r, c := originWalk(name, read)
		if r != want[0] || c != want[1] {
			t.Errorf("%s: got (%s,%v) want %v", name, r, c, want)
		}
	}
}

func TestPutStampsOriginFromSessionMeta(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	session.WriteMeta(session.Meta{Name: "run", Origin: session.OriginQueue})
	session.WriteMeta(session.Meta{Name: "helper", Origin: session.OriginSession, OriginSession: "run"})
	e := New("question", "helper", "claude", "x")
	if err := Put(e); err != nil {
		t.Fatal(err)
	}
	got := List()
	if len(got) != 1 || got[0].OriginRoot != "autorun" || !got[0].OriginChild {
		t.Fatalf("events=%+v", got)
	}
	// A workspace-level event has no session to walk from.
	ws := New("usage", "", "", "")
	ws.TargetType = "workspace"
	if err := Put(ws); err != nil {
		t.Fatal(err)
	}
	for _, g := range List() {
		if g.ID == ws.ID && g.OriginRoot != "" {
			t.Fatalf("workspace event stamped: %+v", g)
		}
	}
}
