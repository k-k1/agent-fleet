package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Each committed PUT moves the tenant's frame at once, a refused or no-op PUT does not, and
// another tenant's frame never moves (ADR 0129 D3 gate 4).
func TestProvidedRoutesEventsFrameFollowsPUTs(t *testing.T) {
	ctx := context.Background()
	st, mux, tn, member := providedRouteTenantFixture(t)
	other, err := st.CreateTenant(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	mgr := &manager{store: st}
	admin, err := st.UpsertIdentity(ctx, "root@x", "root-x", "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	rev := func(tenantID string) string {
		t.Helper()
		p, ok := providedRoutesPayload(ctx, mgr, admin, tenantID)
		if !ok {
			t.Fatal("no frame")
		}
		if len(p) != 1 {
			t.Fatalf("the frame carries more than the revision: %v", p)
		}
		return p["rev"].(string)
	}
	base := "/api/admin/tenants/" + tn.Slug + "/provided-routes/deployment"
	put := func(path, who, body string, want int) {
		t.Helper()
		if w := routeAdminDo(mux, "PUT", base+path, who, body); w.Code != want {
			t.Fatalf("PUT %s: %d %s, want %d", path, w.Code, w.Body, want)
		}
	}
	r0, o0 := rev(tn.ID), rev(other.ID)

	// Refused: no ceiling, a member who may not write, a negative budget, a seat of no tenant.
	put("/grant", "root@x", `{"granted":true}`, 400)
	put("/policy", "m@x", `{"members_only":true,"member_default_micro":20000000}`, 403)
	put("/members", "boss@x", `{"membership_id":"`+member+`","granted":true,"budget_micro":-5}`, 400)
	put("/members", "boss@x", `{"membership_id":"elsewhere","granted":true}`, 404)
	// rev() answers from the cache; drop it so the comparison reads the stored values.
	invalidateProvidedRevision(tn.ID)
	if got := rev(tn.ID); got != r0 {
		t.Fatalf("a refused write moved the frame: %s -> %s", r0, got)
	}

	steps := []struct{ path, who, body string }{
		{"/grant", "root@x", `{"granted":true,"ceiling_micro":50000000}`},
		{"/policy", "boss@x", `{"members_only":true,"member_default_micro":20000000}`},
		{"/members", "boss@x", `{"membership_id":"` + member + `","granted":true,"budget_micro":9000000}`},
		{"/members", "boss@x", `{"membership_id":"` + member + `","granted":true,"budget_micro":9500000}`},
	}
	prev := r0
	for _, s := range steps {
		put(s.path, s.who, s.body, 200)
		cur := rev(tn.ID)
		if cur == prev {
			t.Fatalf("PUT %s %s did not move the frame (still %s)", s.path, s.body, cur)
		}
		prev = cur
	}
	// The same values again change nothing, so nothing is announced.
	put("/members", "boss@x", steps[3].body, 200)
	invalidateProvidedRevision(tn.ID)
	if got := rev(tn.ID); got != prev {
		t.Fatalf("a no-op write moved the frame: %s -> %s", prev, got)
	}
	if got := rev(other.ID); got != o0 {
		t.Fatalf("another tenant's frame moved: %s -> %s", o0, got)
	}
}

// A subscriber with no tenant gets no frame, and a store error drops the tick without one.
func TestProvidedRoutesPayloadNoFrame(t *testing.T) {
	st, _, tn, _ := providedRouteTenantFixture(t)
	admin, err := st.UpsertIdentity(context.Background(), "root@x", "root-x", "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := providedRoutesPayload(context.Background(), &manager{store: st}, store.Identity{}, ""); ok {
		t.Fatal("a frame for no tenant")
	}
	st.Close()
	invalidateProvidedRevision(tn.ID)
	if _, ok := providedRoutesPayload(context.Background(), &manager{store: st}, admin, tn.ID); ok {
		t.Fatal("a frame after a store error")
	}
}

// The stream frames the subscriber's own tenant's revision once, stays silent while it holds and
// when another tenant changes, and frames again after a committed change of its own tenant.
func TestEventsStreamProvidedRoutesFrame(t *testing.T) {
	stub := newEventsStub(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, stub)
	res.mv.TenantID = res.ws.TenantID
	admin, err := a.mgr.store.UpsertIdentity(context.Background(), "root@x", "root-x", "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	res.ident = admin
	other, err := a.mgr.store.CreateTenant(context.Background(), "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	ceiling := int64(10_000_000)
	rec := &syncRecorder{}
	frames, _ := runStreamUntil(t, a, res, rec, func(ctx context.Context) error {
		if !waitFramesCtx(ctx, rec, "provided_routes", 1) {
			return errors.New("no provided_routes snapshot frame")
		}
		// Another tenant's change: no frame for this subscriber.
		if err := a.mgr.store.SetProvidedRouteGrant(ctx, other.ID, providedroutes.SourceDeployment, true, &ceiling); err != nil {
			return err
		}
		invalidateProvidedRevision(other.ID)
		if err := stub.waitSubscriberTicks(ctx, rec, 1, 2); err != nil {
			return err
		}
		if n := len(rec.frames()["provided_routes"]); n != 1 {
			return fmt.Errorf("another tenant's change produced %d frames", n-1)
		}
		if err := a.mgr.store.SetProvidedRouteGrant(ctx, res.ws.TenantID, providedroutes.SourceDeployment, true, &ceiling); err != nil {
			return err
		}
		invalidateProvidedRevision(res.ws.TenantID)
		if !waitFramesCtx(ctx, rec, "provided_routes", 2) {
			return errors.New("the change frame never arrived")
		}
		return stub.waitSubscriberTicks(ctx, rec, 3, 2)
	})
	if got := len(frames["provided_routes"]); got != 2 {
		t.Fatalf("provided_routes frames = %d, want 2 (snapshot + change, silent between)", got)
	}
	if string(frames["provided_routes"][0]) == string(frames["provided_routes"][1]) {
		t.Fatalf("the change frame repeats the snapshot: %s", frames["provided_routes"][1])
	}
}

// A plain member reads nothing of the tenant's provided routes, so it is sent no frame either:
// the frame's timing would tell it when an admin changed a grant it may not see.
func TestEventsStreamProvidedRoutesNoFrameForMember(t *testing.T) {
	stub := newEventsStub(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, stub)
	res.mv.TenantID = res.ws.TenantID
	mem, err := a.mgr.store.UpsertIdentity(context.Background(), "events@example.com", "events", "")
	if err != nil {
		t.Fatal(err)
	}
	res.ident = mem
	frames, _ := runStream(t, a, res, stub, 4)
	if n := len(frames["provided_routes"]); n != 0 {
		t.Fatalf("a member was sent %d provided_routes frames", n)
	}
	if len(frames["sessions"]) == 0 {
		t.Fatal("the stream sent nothing at all: the test proves nothing")
	}
}
