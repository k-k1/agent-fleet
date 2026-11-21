package sessionx

// The schedule id is a fact about how a session was created: it rides the meta and the wire of
// the session a scheduled fire created, and no later session inherits it.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestForkDoesNotInheritTheScheduleID(t *testing.T) {
	src := metaFor("schsrc1")
	src.Origin, src.ScheduleID = session.OriginSchedule, "sch-1"
	if m := forkMeta(src, "schfork1", "title", "", ""); m.ScheduleID != "" {
		t.Fatalf("fork ScheduleID = %q, want empty: a fork is not a fire of the schedule", m.ScheduleID)
	}
}

func TestRecreateDoesNotInheritTheScheduleID(t *testing.T) {
	home := writebackEnv(t)
	const name = "schrec1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude, Origin: session.OriginSchedule, ScheduleID: "sch-1"})
	orig := launchTmuxFn
	var launched session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = m; return http.ErrAbortHandler } // the launch is not under test
	t.Cleanup(func() { launchTmuxFn = orig })
	if w := callHandler(HandleRecreateSession, http.MethodPost, name, ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("recreate = %d %s, want the launch failure (500)", w.Code, w.Body)
	}
	if launched.Name == "" || launched.Name == name {
		t.Fatalf("launched = %+v, want the new identity", launched)
	}
	if launched.ScheduleID != "" {
		t.Fatalf("recreate ScheduleID = %q, want empty", launched.ScheduleID)
	}
}

func TestArchivedRowKeepsTheScheduleID(t *testing.T) {
	m := metaFor("scharc1")
	m.Origin, m.ScheduleID = session.OriginSchedule, "sch-1"
	if got := wireArchivedRow(m).ScheduleID; got != "sch-1" {
		t.Fatalf("archived row ScheduleID = %q, want sch-1", got)
	}
}

func TestCleanScheduleIDDropsUnusableValuesWhole(t *testing.T) {
	long := strings.Repeat("あ", maxScheduleIDRunes+1)
	for _, c := range []struct{ in, want string }{
		{"  sch-1 ", "sch-1"},
		{strings.Repeat("a", maxScheduleIDRunes), strings.Repeat("a", maxScheduleIDRunes)},
		{long, ""},
		{"sch\x00-1", ""},
		{"", ""},
	} {
		if got := cleanScheduleID(c.in); got != c.want {
			t.Errorf("cleanScheduleID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// armMeta stores the id through cleanScheduleID, and only for a schedule-origin create.
func TestArmMetaStoresOnlyAUsableScheduleID(t *testing.T) {
	isolateAgentState(t)
	for _, c := range []struct {
		name, origin, id, want string
	}{
		{"ok", session.OriginSchedule, "sch-1", "sch-1"},
		{"too long", session.OriginSchedule, strings.Repeat("a", maxScheduleIDRunes+1), ""},
		{"nul", session.OriginSchedule, "sch\x00-1", ""},
		{"not a schedule", session.OriginUser, "sch-1", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := &createRun{w: httptest.NewRecorder(), r: httptest.NewRequest(http.MethodPost, "/sessions", nil),
				name: "schmeta", origin: c.origin, req: &CreateReq{Dir: t.TempDir(), ScheduleID: c.id}}
			meta, ok := run.armMeta()
			if !ok {
				t.Fatal("armMeta refused")
			}
			if meta.ScheduleID != c.want {
				t.Fatalf("meta.ScheduleID = %q, want %q", meta.ScheduleID, c.want)
			}
		})
	}
}
