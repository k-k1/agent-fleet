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
	if n := len(frames["provided_routes_admin"]); n != 0 {
		t.Fatalf("a member was sent %d provided_routes_admin frames", n)
	}
	if len(frames["sessions"]) == 0 {
		t.Fatal("the stream sent nothing at all: the test proves nothing")
	}
}

// The source PUT moves the tenant's own frame (its view says registered / on) and the super_admin's;
// a write on another tenant moves only the super_admin's; refused and no-op writes move neither.
// A tenant_admin and a member are sent no super_admin frame (#4371).
func TestProvidedRoutesFramesFollowSourceAndOtherTenants(t *testing.T) {
	ctx := context.Background()
	st, mux, tn, _ := providedRouteTenantFixture(t)
	other, err := st.CreateTenant(ctx, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	mgr := &manager{store: st}
	admin, err := st.UpsertIdentity(ctx, "root@x", "root-x", "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	boss, err := st.UpsertIdentity(ctx, "boss@x", "boss-x", "")
	if err != nil {
		t.Fatal(err)
	}
	tenantRev := func() string {
		t.Helper()
		p, ok := providedRoutesPayload(ctx, mgr, admin, tn.ID)
		if !ok {
			t.Fatal("no tenant frame")
		}
		return p["rev"].(string)
	}
	adminRev := func() string {
		t.Helper()
		p, ok := providedRoutesAdminPayload(ctx, mgr, admin)
		if !ok || len(p) != 1 {
			t.Fatalf("super_admin frame = %v, %v", p, ok)
		}
		return p["rev"].(string)
	}
	if _, ok := providedRoutesAdminPayload(ctx, mgr, boss); ok {
		t.Fatal("a non-super_admin got the super_admin frame")
	}
	if _, ok := providedRoutesAdminPayload(ctx, mgr, store.Identity{}); ok {
		t.Fatal("an anonymous subscriber got the super_admin frame")
	}
	putSource := func(body string) {
		t.Helper()
		if w := routeAdminDo(mux, "PUT", "/api/admin/provided-routes/deployment", "root@x", body); w.Code != 200 {
			t.Fatalf("source PUT: %d %s", w.Code, w.Body)
		}
	}
	const src = `{"role_arn":"arn:aws:iam::123456789012:role/af-model-route/bedrock","region":"us-east-1","models":["us.anthropic.claude-opus-5-5"]`
	t0, a0 := tenantRev(), adminRev()

	// A no-op PUT of the registered source changes nothing.
	putSource(src + `}`)
	if tenantRev() != t0 || adminRev() != a0 {
		t.Fatalf("a no-op source PUT moved a frame: tenant %v admin %v", tenantRev() != t0, adminRev() != a0)
	}
	// Switching it off moves both, at once.
	putSource(src + `,"active":false}`)
	t1, a1 := tenantRev(), adminRev()
	if t1 == t0 || a1 == a0 {
		t.Fatalf("switching the source off: tenant moved=%v admin moved=%v", t1 != t0, a1 != a0)
	}
	// A change only the operator sees (the role's name) moves the super_admin's frame alone.
	putSource(`{"role_arn":"arn:aws:iam::123456789012:role/af-model-route/bedrock2","region":"us-east-1","models":["us.anthropic.claude-opus-5-5"],"active":false}`)
	if tenantRev() != t1 || adminRev() == a1 {
		t.Fatal("an operator-side-only change must move the super_admin frame and not the tenant's")
	}
	a2 := adminRev()
	// Another tenant's grant moves the super_admin's frame and not this tenant's.
	if w := routeAdminDo(mux, "PUT", "/api/admin/tenants/"+other.Slug+"/provided-routes/deployment/grant", "root@x", `{"granted":true,"ceiling_micro":50000000}`); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	if tenantRev() != t1 || adminRev() == a2 {
		t.Fatal("another tenant's grant: the tenant's frame must hold and the super_admin's move")
	}
	a3 := adminRev()
	// A refused write moves nothing.
	if w := routeAdminDo(mux, "PUT", "/api/admin/tenants/"+other.Slug+"/provided-routes/deployment/grant", "root@x", `{"granted":true}`); w.Code != 400 {
		t.Fatalf("refused grant: %d", w.Code)
	}
	invalidateAllProvidedRevisions()
	if adminRev() != a3 {
		t.Fatal("a refused write moved the super_admin frame")
	}
}

// The super_admin's stream gets the digest once, stays silent while it holds and frames again
// after any tenant's change.
func TestEventsStreamProvidedRoutesAdminFrame(t *testing.T) {
	// The digest caches are process-wide and the default tenant's id is the same in every test.
	invalidateAllProvidedRevisions()
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
		if !waitFramesCtx(ctx, rec, "provided_routes_admin", 1) {
			return errors.New("no provided_routes_admin snapshot frame")
		}
		if err := stub.waitSubscriberTicks(ctx, rec, 1, 2); err != nil {
			return err
		}
		if n := len(rec.frames()["provided_routes_admin"]); n != 1 {
			return fmt.Errorf("the digest framed again with nothing changed: %d frames", n)
		}
		if err := a.mgr.store.SetProvidedRouteGrant(ctx, other.ID, providedroutes.SourceDeployment, true, &ceiling); err != nil {
			return err
		}
		invalidateProvidedRevision(other.ID)
		if !waitFramesCtx(ctx, rec, "provided_routes_admin", 2) {
			return errors.New("another tenant's change never reached the super_admin")
		}
		return stub.waitSubscriberTicks(ctx, rec, 3, 2)
	})
	if got := len(frames["provided_routes_admin"]); got != 2 {
		t.Fatalf("provided_routes_admin frames = %d, want 2", got)
	}
	if got := len(frames["provided_routes"]); got != 1 {
		t.Fatalf("another tenant's change moved this tenant's frame: %d frames", got)
	}
}

// A demoted super_admin's open stream stops getting the super_admin frame once the role re-check's
// cache lapses: the role the stream opened with is not enough.
func TestProvidedRoutesAdminPayloadStopsAfterDemotion(t *testing.T) {
	ctx := context.Background()
	st, _, _, _ := providedRouteTenantFixture(t)
	mgr := &manager{store: st}
	admin, err := st.UpsertIdentity(ctx, "root@x", "root-x", "super_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := providedRoutesAdminPayload(ctx, mgr, admin); !ok {
		t.Fatal("no frame for a super_admin")
	}
	if _, err := st.DemoteSuperAdmins(ctx, nil); err != nil {
		t.Fatal(err)
	}
	providedSuperAdminCache.Delete(admin.ID) // the TTL, lapsed
	if _, ok := providedRoutesAdminPayload(ctx, mgr, admin); ok {
		t.Fatal("a demoted identity still got the super_admin frame on its old stream")
	}
	if _, ok := providedRoutesAdminPayload(ctx, mgr, store.Identity{ID: "nobody", Role: "super_admin"}); ok {
		t.Fatal("an identity the store does not know got the frame")
	}
}

// On the stream, a tenant_admin follows its own tenant's frame, which a source switch-off moves,
// and is never sent the super_admin frame, whatever another tenant or the operator's side does.
func TestEventsStreamProvidedRoutesTenantAdmin(t *testing.T) {
	// The digest caches are process-wide and the default tenant's id is the same in every test.
	invalidateAllProvidedRevisions()
	stub := newEventsStub(`{"sessions":[]}`)
	a, res := eventsTestEnv(t, stub)
	ctx0 := context.Background()
	res.mv.TenantID = res.ws.TenantID
	boss, err := a.mgr.store.UpsertIdentity(ctx0, "boss-events@example.com", "boss-events", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.mgr.store.EnsureMembership(ctx0, boss.ID, res.ws.TenantID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	res.ident = boss
	other, err := a.mgr.store.CreateTenant(ctx0, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	const src = providedroutes.SourceDeployment
	dep, err := a.mgr.store.PutProvidedRouteSource(ctx0, store.ProvidedRouteSourceInput{Source: src, RoleARN: "arn:aws:iam::123456789012:role/af-model-route/bedrock",
		Regions: []string{"us-east-1"}, AllowList: []string{"us.anthropic.claude-opus-5-5"}})
	if err != nil {
		t.Fatal(err)
	}
	ceiling := int64(10_000_000)
	rec := &syncRecorder{}
	frames, _ := runStreamUntil(t, a, res, rec, func(ctx context.Context) error {
		if !waitFramesCtx(ctx, rec, "provided_routes", 1) {
			return errors.New("no provided_routes snapshot frame")
		}
		if err := stub.waitSubscriberTicks(ctx, rec, 1, 2); err != nil {
			return err
		}
		// Another tenant's grant and an operator-side-only edit: nothing for a tenant_admin.
		if err := a.mgr.store.SetProvidedRouteGrant(ctx, other.ID, src, true, &ceiling); err != nil {
			return err
		}
		if _, err := a.mgr.store.PutProvidedRouteSource(ctx, store.ProvidedRouteSourceInput{ID: dep.ID, Source: src,
			RoleARN: "arn:aws:iam::123456789012:role/af-model-route/other", Regions: []string{"us-east-1"}, AllowList: dep.AllowList}); err != nil {
			return err
		}
		invalidateAllProvidedRevisions()
		if err := stub.waitSubscriberTicks(ctx, rec, 3, 2); err != nil {
			return err
		}
		if n := len(rec.frames()["provided_routes"]); n != 1 {
			return fmt.Errorf("another tenant or operator-side change produced %d own-tenant frames: %s", n-1, rec.frames()["provided_routes"])
		}
		// The source switched on moves this tenant's frame.
		cur, err := a.mgr.store.ListProvidedRouteSources(ctx, "")
		if err != nil || len(cur) == 0 {
			return fmt.Errorf("no source: %v", err)
		}
		if err := a.mgr.store.SetProvidedRouteSourceActive(ctx, cur[0].ID, cur[0].Fingerprint, true); err != nil {
			return err
		}
		invalidateAllProvidedRevisions()
		if !waitFramesCtx(ctx, rec, "provided_routes", 2) {
			return errors.New("the source switch never reached the tenant_admin")
		}
		return stub.waitSubscriberTicks(ctx, rec, 5, 2)
	})
	if n := len(frames["provided_routes_admin"]); n != 0 {
		t.Fatalf("a tenant_admin was sent %d provided_routes_admin frames", n)
	}
	if n := len(frames["provided_routes"]); n != 2 {
		t.Fatalf("provided_routes frames = %d, want 2", n)
	}
}
