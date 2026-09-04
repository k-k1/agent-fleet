package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// destroyingRuntime records the teardown and reports a leftover, standing in for the
// Fargate adapter's "the EFS directory survives its access point" case.
type destroyingRuntime struct {
	stubRuntime
	destroyed *int
	leftovers []string
	err       error
}

func (d destroyingRuntime) Destroy(context.Context) ([]string, error) {
	if d.err != nil {
		return nil, d.err
	}
	*d.destroyed++
	return d.leftovers, nil
}

type destroyingFactory struct {
	destroyed int
	leftovers []string
	err       error
}

func (f *destroyingFactory) New(runtime.Workspace, string, []string) runtime.Runtime {
	return destroyingRuntime{destroyed: &f.destroyed, leftovers: f.leftovers, err: f.err}
}

// setup for the destroy tests: a tenant with an admin and a member who already has a
// workspace row.
func destroyFixture(t *testing.T, f runtime.RuntimeFactory) (*store.SQL, *manager, store.Identity, store.Tenant) {
	t.Helper()
	ctx := context.Background()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.rtFactory = f
	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	admin, _ := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin")
	victim, _ := st.UpsertIdentity(ctx, "leaver@acme.co.jp", "leaver-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatalf("admin membership: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, victim.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if err := st.CreateWorkspace(ctx, store.Workspace{
		ID: "W-1", TenantID: tn.ID, MembershipID: mem.ID,
		ContainerName: "af-ws-sales-leaver", Network: "af-net-sales-leaver",
		DataDir: "/srv/data/sales/leaver", AgentPort: "7731", AgentToken: "tok",
		State: "stopped", CreatedAt: store.NowTS(),
	}); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return st, mgr, victim, tn
}

func callDestroy(adm adminAPI, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/workspaces", strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	adm.destroyWorkspace(w, r)
	return w
}

// The destroy operation is one misclick away from a member who is at their desk, and
// there is no undo — so it only accepts a membership that was already removed
// (ADR 0045 decision 13-2).
func TestDestroyWorkspaceRefusesAnActiveMember(t *testing.T) {
	f := &destroyingFactory{}
	st, mgr, victim, tn := destroyFixture(t, f)
	w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("destroy of an active member = %d %s, want 409", w.Code, w.Body.String())
	}
	if f.destroyed != 0 {
		t.Error("the runtime was torn down for an active member")
	}
	if _, ok, _ := st.GetWorkspaceByMembership(context.Background(), membershipIDOf(t, st, victim, tn)); !ok {
		t.Error("workspace row deleted despite the refusal")
	}
}

func membershipIDOf(t *testing.T, st *store.SQL, ident store.Identity, tn store.Tenant) string {
	t.Helper()
	mem, ok, err := st.GetMembership(context.Background(), ident.ID, tn.ID)
	if err != nil || !ok {
		t.Fatalf("membership lookup: %v", err)
	}
	return mem.ID
}

// The whole point of the operation: after it, nothing bills. The runtime resources are
// gone, the DB row is gone, and whatever the adapter COULD NOT delete is in the audit
// log rather than only in an HTTP response nobody kept (docs/log/64 §64.18.4).
func TestDestroyWorkspaceRemovesTheRowAndAuditsTheLeftovers(t *testing.T) {
	ctx := context.Background()
	f := &destroyingFactory{leftovers: []string{"efs:fs-1/home/M-1"}}
	st, mgr, victim, tn := destroyFixture(t, f)
	memID := membershipIDOf(t, st, victim, tn)
	if err := st.SetMembershipStatus(ctx, memID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("destroy: %d %s", w.Code, w.Body.String())
	}
	if f.destroyed != 1 {
		t.Errorf("runtime Destroy calls = %d, want 1", f.destroyed)
	}
	if _, ok, _ := st.GetWorkspaceByMembership(ctx, memID); ok {
		t.Error("workspace row survived the destroy")
	}
	if !strings.Contains(w.Body.String(), "efs:fs-1/home/M-1") {
		t.Errorf("the caller is not told what survived: %s", w.Body.String())
	}
	rows, err := st.ListAuditByTenant(ctx, tn.ID, 10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("audit: %+v %v", rows, err)
	}
	found := false
	for _, row := range rows {
		if row.Action == "workspace.destroy" && strings.Contains(row.Detail, "efs:fs-1/home/M-1") {
			found = true
		}
	}
	if !found {
		t.Errorf("the un-deletable resource must be in the audit log, got %+v", rows)
	}
}

// A runtime failure must not delete the DB row: that would leave cloud resources with
// nothing in the database pointing at them — the exact leak this operation closes.
func TestDestroyWorkspaceKeepsTheRowWhenTheRuntimeFails(t *testing.T) {
	ctx := context.Background()
	f := &destroyingFactory{err: errors.New("VolumeInUse")}
	st, mgr, victim, tn := destroyFixture(t, f)
	memID := membershipIDOf(t, st, victim, tn)
	if err := st.SetMembershipStatus(ctx, memID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code == http.StatusOK {
		t.Fatal("destroy reported success although the runtime failed")
	}
	if _, ok, _ := st.GetWorkspaceByMembership(ctx, memID); !ok {
		t.Error("the row was deleted even though the cloud resources are still there")
	}
}

// Taking yourself off a roster is allowed — except for the last roster you are on.
// The guard exists against a lockout with no undo from inside the product, and that is
// the last membership, not any of them. Refusing all of them made the golden bake's
// throwaway tenant impossible to clean up on a deployment with one administrator
// (docs/log/64 §64.28: the seed has to be an account that can sign in, i.e. your own).
func TestRemoveMembershipSelfKeepsTheLastOne(t *testing.T) {
	ctx := context.Background()
	call := func(mgr *manager, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/api/admin/memberships", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		newAdminAPI(mgr).removeMembership(w, r)
		return w
	}
	// The admin's own throwaway tenant, with a workspace of its own — the shape the
	// bake runbook leaves behind.
	seedTenant := func(t *testing.T, st *store.SQL, admin store.Identity) store.Tenant {
		t.Helper()
		tn, err := st.CreateTenant(ctx, "golden-seed", "焼き用")
		if err != nil {
			t.Fatalf("tenant: %v", err)
		}
		mem, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin")
		if err != nil {
			t.Fatalf("membership: %v", err)
		}
		if err := st.CreateWorkspace(ctx, store.Workspace{
			ID: "W-SEED", TenantID: tn.ID, MembershipID: mem.ID,
			ContainerName: "af-ws-golden-seed-boss", Network: "af-net-golden-seed-boss",
			DataDir: "/srv/data/golden-seed/boss", AgentPort: "7732", AgentToken: "tok",
			State: "stopped", CreatedAt: store.NowTS(),
		}); err != nil {
			t.Fatalf("workspace: %v", err)
		}
		return tn
	}
	adminOf := func(t *testing.T, st *store.SQL) store.Identity {
		t.Helper()
		ident, ok, err := st.GetIdentityByUserKey(ctx, "boss-acme-co-jp")
		if err != nil || !ok {
			t.Fatalf("admin identity: %v", err)
		}
		return ident
	}

	t.Run("your own throwaway tenant can be cleaned up", func(t *testing.T) {
		f := &destroyingFactory{}
		st, mgr, _, sales := destroyFixture(t, f)
		admin := adminOf(t, st)
		seed := seedTenant(t, st, admin)
		seedMem := membershipIDOf(t, st, admin, seed)

		if w := call(mgr, `{"tenant_slug":"golden-seed","user_key":"boss-acme-co-jp","purge":true}`); w.Code != http.StatusOK {
			t.Fatalf("self removal from a second tenant: %d %s", w.Code, w.Body.String())
		}
		if f.destroyed != 1 {
			t.Errorf("Destroy calls = %d, want 1 (the home has to go, or it keeps billing)", f.destroyed)
		}
		if _, ok, _ := st.GetWorkspaceByMembership(ctx, seedMem); ok {
			t.Error("the throwaway workspace row survived")
		}
		if got, ok, _ := st.GetMembership(ctx, admin.ID, sales.ID); !ok || got.Status != "active" {
			t.Errorf("the admin's other membership = %+v, want it untouched and active", got)
		}
	})

	t.Run("the last one is refused", func(t *testing.T) {
		f := &destroyingFactory{}
		st, mgr, _, sales := destroyFixture(t, f)
		admin := adminOf(t, st)

		w := call(mgr, `{"tenant_slug":"sales","user_key":"boss-acme-co-jp"}`)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "self_removal") {
			t.Fatalf("removing your only membership = %d %s, want 400 self_removal", w.Code, w.Body.String())
		}
		if got, ok, _ := st.GetMembership(ctx, admin.ID, sales.ID); !ok || got.Status != "active" {
			t.Errorf("membership = %+v, want it still active", got)
		}
	})

	t.Run("an inactive membership elsewhere is not a way back in", func(t *testing.T) {
		f := &destroyingFactory{}
		st, mgr, _, sales := destroyFixture(t, f)
		admin := adminOf(t, st)
		seed := seedTenant(t, st, admin)
		if err := st.SetMembershipStatus(ctx, membershipIDOf(t, st, admin, seed), "inactive"); err != nil {
			t.Fatalf("deactivate: %v", err)
		}

		if w := call(mgr, `{"tenant_slug":"sales","user_key":"boss-acme-co-jp"}`); w.Code != http.StatusBadRequest {
			t.Fatalf("= %d %s, want 400: the only remaining active membership is this one", w.Code, w.Body.String())
		}
		if got, ok, _ := st.GetMembership(ctx, admin.ID, sales.ID); !ok || got.Status != "active" {
			t.Errorf("membership = %+v, want it still active", got)
		}
	})
}

// Offboarding keeps the home by default (docs/log/61 §61.10.6) and destroys it only when the
// caller asks — the two are separate decisions that happen to be convenient in one click.
func TestRemoveMembershipPurgeIsOptIn(t *testing.T) {
	ctx := context.Background()
	call := func(mgr *manager, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/api/admin/memberships", strings.NewReader(body))
		r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
		w := httptest.NewRecorder()
		newAdminAPI(mgr).removeMembership(w, r)
		return w
	}

	t.Run("default keeps the workspace", func(t *testing.T) {
		f := &destroyingFactory{}
		st, mgr, victim, tn := destroyFixture(t, f)
		if w := call(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusOK {
			t.Fatalf("remove: %d %s", w.Code, w.Body.String())
		}
		if f.destroyed != 0 {
			t.Error("plain offboarding destroyed the workspace")
		}
		if _, ok, _ := st.GetWorkspaceByMembership(ctx, membershipIDOf(t, st, victim, tn)); !ok {
			t.Error("workspace row removed by plain offboarding")
		}
	})

	t.Run("purge=true destroys it", func(t *testing.T) {
		f := &destroyingFactory{}
		st, mgr, victim, tn := destroyFixture(t, f)
		memID := membershipIDOf(t, st, victim, tn)
		if w := call(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","purge":true}`); w.Code != http.StatusOK {
			t.Fatalf("remove+purge: %d %s", w.Code, w.Body.String())
		}
		if f.destroyed != 1 {
			t.Errorf("Destroy calls = %d, want 1", f.destroyed)
		}
		if _, ok, _ := st.GetWorkspaceByMembership(ctx, memID); ok {
			t.Error("workspace row survived purge")
		}
		if got, ok, _ := st.GetMembership(ctx, victim.ID, tn.ID); !ok || got.Status != "inactive" {
			t.Errorf("membership = %+v, want the inactive row to remain", got)
		}
	})

	t.Run("a failed purge still locks the person out", func(t *testing.T) {
		f := &destroyingFactory{err: errors.New("VolumeInUse")}
		st, mgr, victim, tn := destroyFixture(t, f)
		if w := call(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","purge":true}`); w.Code == http.StatusOK {
			t.Fatal("a failed purge must not report success")
		}
		if got, ok, _ := st.GetMembership(ctx, victim.ID, tn.ID); !ok || got.Status != "inactive" {
			t.Errorf("membership = %+v; the deactivation happens first and must stand", got)
		}
	})
}
