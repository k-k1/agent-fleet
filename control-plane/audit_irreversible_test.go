package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Issue #1266: every irreversible admin action records who asked for it BEFORE it acts, and
// refuses when that record cannot be written. The acceptance is literal: with the store's
// audit insert failing, none of them completes.

// auditFailingStore fails InsertAudit for the rows failIf selects and passes everything else
// through, so the action itself still has a working store.
type auditFailingStore struct {
	store.Store
	failIf func(store.AuditLog) bool
}

func (s auditFailingStore) InsertAudit(ctx context.Context, a store.AuditLog) error {
	if s.failIf(a) {
		return errors.New("audit_log: database is locked")
	}
	return s.Store.InsertAudit(ctx, a)
}

func failEveryAudit(store.AuditLog) bool { return true }

// failOutcomeOnly lets the request row through and fails the outcome row: the action has
// happened, and the answer must still be the action's.
func failOutcomeOnly(a store.AuditLog) bool {
	return !strings.HasSuffix(a.Action, store.AuditRequestedSuffix)
}

func wantAuditUnavailable(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"audit_unavailable"`) {
		t.Errorf("%s with the audit log down = %d %s, want 503 audit_unavailable", what, w.Code, w.Body.String())
	}
}

func TestIrreversibleAdminActionsRefuseWithoutAnAuditRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("clean home", func(t *testing.T) {
		rec := &wipeRecorder{}
		st, mgr, _, _ := destroyFixture(t, fixedRuntimeFactory{&reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: rec, state: "stopped"}}})
		mgr.store = auditFailingStore{st, failEveryAudit}
		w := httptest.NewRecorder()
		newAdminAPI(mgr).cleanHome(w, adminRequest(http.MethodPost, "/api/admin/clean-home",
			`{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`))
		wantAuditUnavailable(t, "clean home", w)
		if got := rec.log(); got != "" {
			t.Errorf("the runtime was driven (%q) although nothing could record who asked", got)
		}
	})

	t.Run("delete home backups", func(t *testing.T) {
		rt := &reachableHomeRuntime{unreachableHomeRuntime: unreachableHomeRuntime{rec: &wipeRecorder{}, state: "stopped"}, backups: 3}
		st, mgr, _, _ := destroyFixture(t, fixedRuntimeFactory{rt})
		mgr.store = auditFailingStore{st, failEveryAudit}
		wantAuditUnavailable(t, "delete backups", callHomeBackups(newAdminAPI(mgr), http.MethodDelete))
		mgr.store = st
		if w := callHomeBackups(newAdminAPI(mgr), http.MethodGet); !strings.Contains(w.Body.String(), `"count":3`) {
			t.Errorf("backups after the refusal: %s, want all 3 still there", w.Body.String())
		}
	})

	t.Run("destroy workspace", func(t *testing.T) {
		f := &destroyingFactory{}
		st, mgr, victim, tn := destroyFixture(t, f)
		if err := st.SetMembershipStatus(ctx, membershipIDOf(t, st, victim, tn), "inactive"); err != nil {
			t.Fatal(err)
		}
		mgr.store = auditFailingStore{st, failEveryAudit}
		wantAuditUnavailable(t, "destroy", callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`))
		if f.destroyed != 0 {
			t.Error("the workspace was destroyed although nothing could record who asked")
		}
	})

	t.Run("remove membership with purge", func(t *testing.T) {
		f := &destroyingFactory{}
		st, mgr, victim, tn := destroyFixture(t, f)
		mgr.store = auditFailingStore{st, failEveryAudit}
		w := callRemoveMembership(mgr, `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp","purge":true}`)
		wantAuditUnavailable(t, "remove+purge", w)
		mem, _, _ := st.GetMembership(ctx, victim.ID, tn.ID)
		if f.destroyed != 0 || mem.Status != "active" {
			t.Errorf("destroyed=%d status=%s after the refusal, want 0 and active", f.destroyed, mem.Status)
		}
	})

	t.Run("delete membership", func(t *testing.T) {
		st, mgr, tn, memID := cleanupFixture(t)
		if err := st.SetMembershipStatus(ctx, memID, "inactive"); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteWorkspace(ctx, "W-1"); err != nil {
			t.Fatal(err)
		}
		mgr.store = auditFailingStore{st, failEveryAudit}
		wantAuditUnavailable(t, "delete membership", callDeleteMembership(mgr, "sales", "leaver-acme-co-jp"))
		if left, err := st.ListRemovedMembersByTenant(ctx, tn.ID); err != nil || len(left) != 1 {
			t.Errorf("removed memberships after the refusal = %+v %v, want the row still there", left, err)
		}
	})

	t.Run("delete tenant", func(t *testing.T) {
		st, mgr, tn, _ := cleanupFixture(t)
		for _, m := range mustMembers(t, st, tn.ID) {
			if err := st.SetMembershipStatus(ctx, m.MembershipID, "inactive"); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.DeleteWorkspace(ctx, "W-1"); err != nil {
			t.Fatal(err)
		}
		mgr.store = auditFailingStore{st, failEveryAudit}
		wantAuditUnavailable(t, "delete tenant", callDeleteTenant(mgr, "sales"))
		tenantStillThere(t, st, "sales")
	})

	t.Run("terminate pool slot", func(t *testing.T) {
		f := &slotPoolFactory{reason: "mount home on i-bad: no device"}
		st := p3Store(t)
		mgr := p3Manager(t, st)
		mgr.rtFactory = f
		if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
			t.Fatal(err)
		}
		mgr.store = auditFailingStore{st, failEveryAudit}
		wantAuditUnavailable(t, "terminate slot", callTerminateSlot(newAdminAPI(mgr), "i-bad"))
		if len(f.asked) != 0 {
			t.Errorf("the adapter was asked to terminate %v although nothing could record who asked", f.asked)
		}
	})
}

// The positive control, and the other half of the pair: with the request row written and the
// outcome row failing, the action goes ahead, answers as it would, and leaves the request on
// record. A handler that refused on every audit error would pass the test above.
func TestIrreversibleAdminActionKeepsTheRequestWhenTheOutcomeWriteFails(t *testing.T) {
	ctx := context.Background()
	f := &destroyingFactory{leftovers: []string{"efs:/home/leaver"}}
	st, mgr, victim, tn := destroyFixture(t, f)
	if err := st.SetMembershipStatus(ctx, membershipIDOf(t, st, victim, tn), "inactive"); err != nil {
		t.Fatal(err)
	}
	mgr.store = auditFailingStore{st, failOutcomeOnly}
	w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`)
	if w.Code != http.StatusOK || f.destroyed != 1 {
		t.Fatalf("destroy = %d %s (destroyed %d), want 200 and the workspace gone", w.Code, w.Body.String(), f.destroyed)
	}
	got := auditActions(t, st, tn)
	if len(got) != 1 || !strings.HasPrefix(got[0], "workspace.destroy.requested:") {
		t.Errorf("audit rows = %v, want the request alone", got)
	}
}

// With the store healthy, a request row precedes the outcome row, and a refusal after the
// request still closes it with the reason.
func TestIrreversibleAdminActionWritesRequestThenOutcome(t *testing.T) {
	ctx := context.Background()
	f := &destroyingFactory{err: errors.New("ec2: volume busy")}
	st, mgr, victim, tn := destroyFixture(t, f)
	if err := st.SetMembershipStatus(ctx, membershipIDOf(t, st, victim, tn), "inactive"); err != nil {
		t.Fatal(err)
	}
	if w := callDestroy(newAdminAPI(mgr), `{"tenant_slug":"sales","user_key":"leaver-acme-co-jp"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("destroy with a failing runtime = %d %s, want 500", w.Code, w.Body.String())
	}
	logs, err := st.ListAuditByTenant(ctx, tn.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var req, out *store.AuditLog
	for i := range logs {
		switch logs[i].Action {
		case "workspace.destroy.requested":
			req = &logs[i]
		case "workspace.destroy":
			out = &logs[i]
		}
	}
	if req == nil || out == nil {
		t.Fatalf("audit rows = %+v, want a request and an outcome", logs)
	}
	if req.ActorID == "" || req.ActorID != out.ActorID || req.Target != "leaver-acme-co-jp" {
		t.Errorf("request %+v and outcome %+v do not name the same actor and target", *req, *out)
	}
	if out.HTTPStatus != http.StatusInternalServerError || !strings.Contains(out.Detail, "volume busy") {
		t.Errorf("outcome = %+v, want the 500 and the runtime's reason", *out)
	}
}

func callRemoveMembership(mgr *manager, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	newAdminAPI(mgr).removeMembership(w, adminRequest(http.MethodPost, "/api/admin/memberships/remove", body))
	return w
}

// Purging a model's files cannot be undone either; forgetting the row alone can, and is not
// held to this.
func TestEngineModelPurgeRefusesWithoutAnAuditRecord(t *testing.T) {
	a, e, st := engineModelAdminAPI(t)
	if err := st.PutEngineModel(t.Context(), store.EngineModel{Role: "image", ID: "tmp-model", Kind: "checkpoint",
		BaseModel: "flux1", Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/tmp.safetensors"}}}); err != nil {
		t.Fatal(err)
	}
	e.catalog.invalidate()
	a.mgr.store = auditFailingStore{st, failEveryAudit}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/api/admin/engines/image/models/tmp-model?purge=1", nil)
	r.SetPathValue("key", "image")
	r.SetPathValue("id", "tmp-model")
	a.deleteModel(rec, r, store.Identity{ID: "u1"})
	wantAuditUnavailable(t, "purge", rec)
	rows, err := st.ListEngineModels(t.Context(), "image")
	if err != nil || len(rows) != 1 {
		t.Errorf("model rows after the refusal = %+v %v, want the row still there", rows, err)
	}

	// Forgetting without ?purge=1 is not gated on the audit log.
	if code, out := adminModel(t, a, "DELETE", "image", "tmp-model", ""); code != http.StatusOK {
		t.Errorf("forget without purge and the audit log down = %d %v, want 200", code, out)
	}
}
