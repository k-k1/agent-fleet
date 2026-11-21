package sessionx

// The queue category id is a fact about how a session was created: it rides the meta and the
// wire of the session a queue delivery created, and no other origin or later session has it.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestArmMetaStoresTheQueueCategoryOnlyForAQueueCreate(t *testing.T) {
	isolateAgentState(t)
	for _, c := range []struct {
		name, origin string
		delivery     *QueueDelivery
		want         string
	}{
		{"queue", session.OriginQueue, &QueueDelivery{ID: "d1", Queue: "cat-1"}, "cat-1"},
		{"too long", session.OriginQueue, &QueueDelivery{ID: "d1", Queue: strings.Repeat("a", maxScheduleIDRunes+1)}, ""},
		{"nul", session.OriginQueue, &QueueDelivery{ID: "d1", Queue: "cat\x00-1"}, ""},
		{"no delivery", session.OriginUser, nil, ""},
		{"other origin", session.OriginUser, &QueueDelivery{ID: "d1", Queue: "cat-1"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := &createRun{w: httptest.NewRecorder(), r: httptest.NewRequest(http.MethodPost, "/sessions", nil),
				name: "qcatmeta", origin: c.origin, req: &CreateReq{Dir: t.TempDir(), QueueDelivery: c.delivery}}
			meta, ok := run.armMeta()
			if !ok {
				t.Fatal("armMeta refused")
			}
			if meta.QueueCategoryID != c.want {
				t.Fatalf("meta.QueueCategoryID = %q, want %q", meta.QueueCategoryID, c.want)
			}
		})
	}
}

func TestQueueCategoryRidesTheListedAndArchivedWire(t *testing.T) {
	m := metaFor("qcatarc1")
	m.Origin, m.QueueCategoryID = session.OriginQueue, "cat-1"
	if got := wireArchivedRow(m).QueueCategoryID; got != "cat-1" {
		t.Fatalf("archived row QueueCategoryID = %q, want cat-1", got)
	}
	m.Dir = t.TempDir() // wireSession resolves a working-copy id; never point it at a real checkout
	for _, alive := range []bool{true, false} {
		if got := wireSession(m, alive).QueueCategoryID; got != "cat-1" {
			t.Fatalf("wireSession(alive=%v) QueueCategoryID = %q, want cat-1", alive, got)
		}
	}
}

func TestRecreateDoesNotInheritTheQueueCategory(t *testing.T) {
	home := writebackEnv(t)
	const name = "qcatrec1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude, Origin: session.OriginQueue, QueueCategoryID: "cat-1"})
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
	if launched.QueueCategoryID != "" {
		t.Fatalf("recreate QueueCategoryID = %q, want empty", launched.QueueCategoryID)
	}
}

func TestForkDoesNotInheritTheQueueCategory(t *testing.T) {
	src := metaFor("qcatsrc1")
	src.Origin, src.QueueCategoryID = session.OriginQueue, "cat-1"
	if m := forkMeta(src, "qcatfork1", "title", "", ""); m.QueueCategoryID != "" {
		t.Fatalf("fork QueueCategoryID = %q, want empty", m.QueueCategoryID)
	}
}
