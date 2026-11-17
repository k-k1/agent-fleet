package main

import (
	"context"
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
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
	rev := func(tenantID string) string {
		t.Helper()
		p, ok := providedRoutesPayload(ctx, mgr, tenantID)
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
	if _, ok := providedRoutesPayload(context.Background(), &manager{store: st}, ""); ok {
		t.Fatal("a frame for no tenant")
	}
	st.Close()
	invalidateProvidedRevision(tn.ID)
	if _, ok := providedRoutesPayload(context.Background(), &manager{store: st}, tn.ID); ok {
		t.Fatal("a frame after a store error")
	}
}

// The stream frames the subscriber's own tenant's revision once, stays silent while it holds, and
// frames again after a committed change.
func TestEventsStreamProvidedRoutesFrame(t *testing.T) {
	stub := newEventsStub(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, stub)
	res.mv.TenantID = res.ws.TenantID
	rec := &syncRecorder{}
	frames, _ := runStreamUntil(t, a, res, rec, func(ctx context.Context) error {
		if !waitFramesCtx(ctx, rec, "provided_routes", 1) {
			return errors.New("no provided_routes snapshot frame")
		}
		ceiling := int64(10_000_000)
		if err := a.mgr.store.SetProvidedRouteGrant(ctx, res.ws.TenantID, providedroutes.SourceDeployment, true, &ceiling); err != nil {
			return err
		}
		invalidateProvidedRevision(res.ws.TenantID)
		if !waitFramesCtx(ctx, rec, "provided_routes", 2) {
			return errors.New("the change frame never arrived")
		}
		return stub.waitSubscriberTicks(ctx, rec, 1, 2)
	})
	if got := len(frames["provided_routes"]); got != 2 {
		t.Fatalf("provided_routes frames = %d, want 2 (snapshot + change, silent between)", got)
	}
	if string(frames["provided_routes"][0]) == string(frames["provided_routes"][1]) {
		t.Fatalf("the change frame repeats the snapshot: %s", frames["provided_routes"][1])
	}
}
