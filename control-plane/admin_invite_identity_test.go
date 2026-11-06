package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// An invite must not hand an address to an identity another tenant already holds, and
// re-activating a removed tenant_admin must not give the role back.

func TestInviteCannotSetEmailOfIdentityInAnotherTenant(t *testing.T) {
	ctx := context.Background()
	st, mgr, _ := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	other, err := st.CreateTenant(ctx, "legal", "Legal")
	if err != nil {
		t.Fatal(err)
	}
	// The other tenant invited this person by key only; they have not signed in yet.
	pending, err := st.InviteIdentity(ctx, "", "pending-acme-co-jp", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, pending.ID, other.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}

	w := callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships",
		`{"tenant_slug":"sales","user_key":"pending-acme-co-jp","email":"attacker@evil.example"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("invite = %d %s, want 409", w.Code, w.Body.String())
	}
	got, _, _ := st.GetIdentityByUserKey(ctx, "pending-acme-co-jp")
	if got.Email != "" {
		t.Errorf("email = %q, want it left empty", got.Email)
	}
	if _, ok, _ := st.GetMembership(ctx, pending.ID, mustTenantID(t, st, "sales")); ok {
		t.Error("refused invite still created a membership")
	}
}

func TestInviteSetsEmailOnlyWhereNoOtherTenantHoldsTheIdentity(t *testing.T) {
	ctx := context.Background()
	st, mgr, _ := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	w := callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships",
		`{"tenant_slug":"sales","user_key":"fresh-key","email":"fresh@acme.co.jp"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("new identity = %d %s", w.Code, w.Body.String())
	}
	// A key-only placeholder inside this tenant only may be filled.
	if _, err := st.InviteIdentity(ctx, "", "solo-key", mustTenantID(t, st, "sales")); err != nil {
		t.Fatal(err)
	}
	w = callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships",
		`{"tenant_slug":"sales","user_key":"solo-key","email":"solo@acme.co.jp"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("own-tenant fill = %d %s", w.Code, w.Body.String())
	}
	got, _, _ := st.GetIdentityByUserKey(ctx, "solo-key")
	if got.Email != "solo@acme.co.jp" {
		t.Errorf("email = %q", got.Email)
	}
	logs, _ := st.ListAuditByTenant(ctx, mustTenantID(t, st, "sales"), 50)
	var found bool
	for _, a := range logs {
		if a.Action == "identity.email_set" && a.Target == "solo-key" && strings.Contains(a.Detail, "previous=;") {
			found = true
		}
	}
	if !found {
		t.Errorf("no identity.email_set audit entry in %+v", logs)
	}
}

func TestInviteNeverOverwritesAnExistingEmail(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	victim, _ := st.UpsertIdentity(ctx, "victim@acme.co.jp", "victim-acme-co-jp", "")
	if _, err := st.EnsureMembership(ctx, victim.ID, tn.ID, "member"); err != nil {
		t.Fatal(err)
	}
	callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships",
		`{"tenant_slug":"sales","user_key":"victim-acme-co-jp","email":"attacker@evil.example"}`)
	got, _, _ := st.GetIdentityByUserKey(ctx, "victim-acme-co-jp")
	if got.Email != "victim@acme.co.jp" {
		t.Errorf("email = %q, want unchanged", got.Email)
	}
}

func TestReinviteOfRemovedTenantAdminRestoresMemberOnly(t *testing.T) {
	ctx := context.Background()
	st, mgr, tn := userKeyFixture(t)
	adm := newAdminAPI(mgr)
	gone, _ := st.UpsertIdentity(ctx, "gone@acme.co.jp", "gone-acme-co-jp", "")
	mem, err := st.EnsureMembership(ctx, gone.ID, tn.ID, "tenant_admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"tenant_slug":"sales","user_key":"gone-acme-co-jp"}`,
		`{"tenant_slug":"sales","user_key":"gone-acme-co-jp","role":"tenant_admin"}`,
	} {
		if err := st.SetMembershipStatus(ctx, mem.ID, "inactive"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMembershipRole(ctx, mem.ID, "tenant_admin"); err != nil {
			t.Fatal(err)
		}
		if w := callAdmin(adm.addMembership, http.MethodPost, "/api/admin/memberships", body); w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", body, w.Code, w.Body.String())
		}
		m, _, _ := st.GetMembership(ctx, gone.ID, tn.ID)
		if m.Status != "active" || m.Role != "member" {
			t.Errorf("%s: status=%s role=%s, want active member", body, m.Status, m.Role)
		}
	}
}

func TestEmailSetSuspectsListsMultiTenantIdentitiesWithoutALogin(t *testing.T) {
	ctx := context.Background()
	st, _, tn := userKeyFixture(t)
	other, _ := st.CreateTenant(ctx, "legal", "Legal")
	// Address written through an invite, never signed in, held by two tenants.
	bad, err := st.InviteIdentity(ctx, "", "bad-key", tn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{tn.ID, other.ID} {
		if _, err := st.EnsureMembership(ctx, bad.ID, id, "member"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertIdentity(ctx, "x@evil.example", "bad-key", ""); err != nil {
		t.Fatal(err)
	}
	// A single-tenant identity with an address is not listed.
	solo, _ := st.UpsertIdentity(ctx, "solo@acme.co.jp", "solo-key", "")
	_, _ = st.EnsureMembership(ctx, solo.ID, tn.ID, "member")

	list, err := st.ListEmailSetSuspects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].UserKey != "bad-key" || len(list[0].Tenants) != 2 || !list[0].Audited || !list[0].NoLogin {
		t.Errorf("suspects = %+v", list)
	}
	var out strings.Builder
	if rc := printEmailSuspects(ctx, st, &out); rc != 1 || !strings.Contains(out.String(), "bad-key") {
		t.Errorf("rc=%d out=%q", rc, out.String())
	}
}

func mustTenantID(t *testing.T, st *store.SQL, slug string) string {
	t.Helper()
	tn, ok, err := st.GetTenantBySlug(context.Background(), slug)
	if err != nil || !ok {
		t.Fatalf("tenant %s: %v %v", slug, ok, err)
	}
	return tn.ID
}
