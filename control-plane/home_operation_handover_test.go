package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Every path that runs the home task, cut off after RunTask and before its answer was
// recorded, is finished by another CP: the starter left a record bound to the task's
// clientToken, and a fresh CP's reconciler resumes under that token, sees the task stopped
// with exit 0 and applies the step after it exactly once (#1603). The golden pipeline's
// Destroy is the path that opened no record: its seed then stayed refused until an
// operator deleted the marker.
func TestEveryHomeTaskPathIsFinishedByTheNextCP(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		// run starts the operation on the first CP.
		run func(t *testing.T, mgr *manager, st *store.SQL, ws store.Workspace, rt runtime.Runtime)
		// after checks the step after the task, applied by the second CP.
		after func(t *testing.T, st *store.SQL, tn store.Tenant, ws store.Workspace, rt2 *recordedHomeRuntime)
		op    string
	}{
		{name: "member recreate", op: "wipe:repos",
			run: func(t *testing.T, mgr *manager, _ *store.SQL, ws store.Workspace, rt runtime.Runtime) {
				memberWipeOn(t, mgr, ws, rt, "recreate")
			},
			after: startedOnce},
		{name: "member clean home", op: "wipe:clean",
			run: func(t *testing.T, mgr *manager, _ *store.SQL, ws store.Workspace, rt runtime.Runtime) {
				memberWipeOn(t, mgr, ws, rt, "clean-home")
			},
			after: startedOnce},
		{name: "admin clean home", op: "erase",
			run: func(t *testing.T, mgr *manager, _ *store.SQL, _ store.Workspace, _ runtime.Runtime) {
				if w := httpCleanHome(mgr); w.Code != http.StatusAccepted {
					t.Fatalf("clean home = %d %s", w.Code, w.Body.String())
				}
			},
			after: func(t *testing.T, st *store.SQL, tn store.Tenant, ws store.Workspace, _ *recordedHomeRuntime) {
				if n := countAudit(t, st, tn, "workspace.clean_home:home erased"); n != 1 {
					t.Errorf("%d outcome entries, want one: %v", n, auditActions(t, st, tn))
				}
				if got, _, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID); got.State != "stopped" {
					t.Errorf("state = %q, want stopped", got.State)
				}
			}},
		{name: "admin destroy", op: "destroy",
			run: func(t *testing.T, mgr *manager, st *store.SQL, ws store.Workspace, _ runtime.Runtime) {
				if err := st.SetMembershipStatus(ctx, ws.MembershipID, "inactive"); err != nil {
					t.Fatal(err)
				}
				if w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusAccepted {
					t.Fatalf("destroy = %d %s", w.Code, w.Body.String())
				}
			},
			after: destroyedOnce("workspace destroyed (home and runtime resources deleted)")},
		{name: "purge", op: "destroy",
			run: func(t *testing.T, mgr *manager, _ *store.SQL, _ store.Workspace, _ runtime.Runtime) {
				r := httptest.NewRequest(http.MethodDelete, "/api/admin/memberships",
					strings.NewReader(`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","purge":true}`))
				r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
				w := httptest.NewRecorder()
				newAdminAPI(mgr).removeMembership(w, r)
				if w.Code != http.StatusAccepted {
					t.Fatalf("purge = %d %s", w.Code, w.Body.String())
				}
			},
			after: destroyedOnce("status=inactive; workspace destroyed (purge)")},
		{name: "golden seed destroy", op: "destroy",
			run: func(t *testing.T, mgr *manager, _ *store.SQL, ws store.Workspace, _ runtime.Runtime) {
				// The golden pipeline's call: in the tick, with no administrator's audit.
				if _, err := mgr.destroyWorkspaceByMembership(ctx, ws.MembershipID); err == nil {
					t.Fatal("a Destroy whose task outcome is unknown reported success")
				}
			},
			after: func(t *testing.T, st *store.SQL, _ store.Tenant, ws store.Workspace, _ *recordedHomeRuntime) {
				if _, found, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID); found {
					t.Error("the row survived the finished Destroy")
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := newRecordedHomeRuntime("running", errStillRunning)
			close(rt.gate)
			st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
			ws := victimWorkspace(t, st, victim, tn)
			c.run(t, mgr, st, ws, rt)
			waitFor(t, "the starter to let go", func() bool {
				return strings.Contains(rt.rec.log(), c.op) && leaseFree(t, mgr, ws.MembershipID)
			})
			first := rt.lastBinding()
			if first.Token == "" || first.Resume {
				t.Fatalf("the starter's task was bound to %+v, want its record's token", first)
			}
			if !recordOpen(t, st, ws.ID) {
				t.Fatal("the starter left no record: a CP that replaces it has nothing to finish")
			}

			// The next CP: another process on the same database, whose ECS now shows the
			// task STOPPED with exit 0.
			rt2 := newRecordedHomeRuntime("stopped")
			close(rt2.gate)
			mgr2 := p3Manager(t, st)
			mgr2.rtFactory = fixedRuntimeFactory{rt2}
			reconcileNow(mgr2)
			reconcileNow(mgr2)
			if b := rt2.lastBinding(); b.Token != first.Token || !b.Resume {
				t.Errorf("the next CP resumed with %+v, want the starter's token %s, resumed", b, first.Token)
			}
			if n := strings.Count(rt2.rec.log(), c.op); n != 1 {
				t.Errorf("the next CP ran %s %d times (%q), want once", c.op, n, rt2.rec.log())
			}
			if recordOpen(t, st, ws.ID) {
				t.Error("the record is still open after the next CP finished it")
			}
			c.after(t, st, tn, ws, rt2)
		})
	}
}

func memberWipeOn(t *testing.T, mgr *manager, ws store.Workspace, rt runtime.Runtime, op string) {
	t.Helper()
	res := &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	if w := callMemberWipe(newWorkspaceAPI(mgr, false), op, res); w.Code != http.StatusAccepted {
		t.Fatalf("%s = %d %s", op, w.Code, w.Body.String())
	}
}

func startedOnce(t *testing.T, _ *store.SQL, _ store.Tenant, _ store.Workspace, rt2 *recordedHomeRuntime) {
	t.Helper()
	if n := strings.Count(rt2.rec.log(), "start"); n != 1 {
		t.Errorf("the next CP started the workspace %d times (%q), want once", n, rt2.rec.log())
	}
}

func destroyedOnce(ok string) func(*testing.T, *store.SQL, store.Tenant, store.Workspace, *recordedHomeRuntime) {
	return func(t *testing.T, st *store.SQL, tn store.Tenant, ws store.Workspace, _ *recordedHomeRuntime) {
		t.Helper()
		if _, found, _ := st.GetWorkspaceByMembership(context.Background(), ws.MembershipID); found {
			t.Error("the row survived the finished Destroy")
		}
		n := 0
		for _, a := range auditActions(t, st, tn) {
			if strings.Contains(a, ok) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d outcome entries %q, want one: %v", n, ok, auditActions(t, st, tn))
		}
	}
}

// The golden pipeline's own call: its seed's Destroy opens the record before the task, in
// the tick, so the seed is not refused for good when the CP is replaced mid-task.
func TestGoldenSeedDestroyOpensAHomeOperationRecord(t *testing.T) {
	ctx := context.Background()
	rt := newRecordedHomeRuntime("stopped", errStillRunning)
	close(rt.gate)
	st, mgr, _, _ := destroyFixture(t, fixedRuntimeFactory{rt})
	b := newGoldenBaker(mgr, newFakeGoldenPool())
	key := b.seedKey(runtime.EC2ArchX86)
	mem, _, err := b.membership(ctx, key, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(ctx, store.Workspace{ID: "W-seed", TenantID: mem.TenantID, MembershipID: mem.MembershipID,
		ContainerName: "af-ws-golden-seed", Network: "af-net-golden-seed", DataDir: "/srv/data/golden/seed",
		AgentPort: "7731", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	b.destroy(ctx, key)
	op, open, err := st.GetHomeOperationByWorkspace(ctx, "W-seed")
	if err != nil || !open {
		t.Fatalf("the seed's Destroy left no record (%v): nothing resolves its marker after a CP replacement", err)
	}
	if op.Kind != store.HomeOpDestroy || rt.lastBinding().Token != op.ID {
		t.Errorf("record %+v, binding %+v: want a destroy record whose id is the task's token", op, rt.lastBinding())
	}
}
