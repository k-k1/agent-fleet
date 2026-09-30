package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

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

// failingSSMWriter refuses every publish of the active set.
type failingSSMWriter struct{}

func (failingSSMWriter) PutParameter(context.Context, *ssm.PutParameterInput, ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	return nil, errors.New("ssm: throttled")
}

// catalogUnreadableStore fails the catalogue read the purge takes its S3 keys from.
type catalogUnreadableStore struct{ store.Store }

func (catalogUnreadableStore) ListEngineModels(context.Context, string) ([]store.EngineModel, error) {
	return nil, errors.New("catalogue: connection reset")
}

// One purge request is one request row and one outcome row, and the outcome says what the
// client was answered: 200 with nothing else, 502 when the active set could not be
// published, and "unknown" — not "no file" — when the catalogue could not be read.
func TestEngineModelPurgeWritesOnePairWithTheAnsweredOutcome(t *testing.T) {
	for _, c := range []struct {
		name       string
		setup      func(a *engineAdminAPI, e *engineRuntimeState)
		wantStatus int
		wantDetail string
	}{
		{"published", func(*engineAdminAPI, *engineRuntimeState) {}, http.StatusOK, "no ingest task"},
		{"publish fails", func(_ *engineAdminAPI, e *engineRuntimeState) {
			e.ssm, e.activeParam = failingSSMWriter{}, "/af/engines/image/active"
		}, http.StatusBadGateway, "the active set was not published: "},
		{"catalogue unreadable", func(a *engineAdminAPI, _ *engineRuntimeState) {
			a.mgr.store = catalogUnreadableStore{a.mgr.store}
		}, http.StatusOK, "whether it had files is unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, e, st := engineModelAdminAPI(t)
			if err := st.PutEngineModel(t.Context(), store.EngineModel{Role: "image", ID: "tmp-model", Kind: "checkpoint",
				BaseModel: "flux1", Files: []store.EngineModelFile{{Flag: "--diffusion-model", S3Key: "image/diffusion_models/tmp.safetensors"}}}); err != nil {
				t.Fatal(err)
			}
			e.catalog.invalidate()
			c.setup(&a, e)

			rec := httptest.NewRecorder()
			r := httptest.NewRequest("DELETE", "/api/admin/engines/image/models/tmp-model?purge=1", nil)
			r.SetPathValue("key", "image")
			r.SetPathValue("id", "tmp-model")
			a.deleteModel(rec, r, store.Identity{ID: "u1"})
			if rec.Code != c.wantStatus {
				t.Fatalf("purge = %d %s, want %d", rec.Code, rec.Body.String(), c.wantStatus)
			}
			logs, err := st.ListAuditByTenant(t.Context(), "", 20)
			if err != nil {
				t.Fatal(err)
			}
			var req, out []store.AuditLog
			for _, l := range logs {
				switch l.Action {
				case "engine.image.model.requested":
					req = append(req, l)
				case "engine.image.model":
					out = append(out, l)
				}
			}
			if len(req) != 1 || len(out) != 1 {
				t.Fatalf("audit rows = %+v, want one request and one outcome", logs)
			}
			if out[0].HTTPStatus != c.wantStatus || !strings.Contains(out[0].Detail, c.wantDetail) {
				t.Errorf("outcome = %d %q, want %d containing %q", out[0].HTTPStatus, out[0].Detail, c.wantStatus, c.wantDetail)
			}
		})
	}
}

// Issue #1334: a runtime with no slot pool has nothing to terminate, and says so with a 404
// even when the audit log is down. The intent write used to come first and answer 503.
func TestTerminatePoolSlotWithNoPoolIs404EvenWithTheAuditLogDown(t *testing.T) {
	st, adm := poolSlotFixture(t, &destroyingFactory{})
	adm.mgr.store = auditFailingStore{st, failEveryAudit}
	if w := callTerminateSlot(adm, "i-x"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"no_pool"`) {
		t.Fatalf("terminate on a poolless runtime with the audit log down = %d %s, want 404 no_pool", w.Code, w.Body.String())
	}
}

// auditPair returns the request and outcome rows of action, failing unless there is exactly
// one of each.
func auditPair(t *testing.T, st store.Store, tenantID, action string) (req, out store.AuditLog) {
	t.Helper()
	logs, err := st.ListAuditByTenant(context.Background(), tenantID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var reqs, outs []store.AuditLog
	for _, l := range logs {
		switch l.Action {
		case action + store.AuditRequestedSuffix:
			reqs = append(reqs, l)
		case action:
			outs = append(outs, l)
		}
	}
	if len(reqs) != 1 || len(outs) != 1 {
		t.Fatalf("audit rows for %s = %+v, want one request and one outcome", action, logs)
	}
	return reqs[0], outs[0]
}

// Issue #1334: deleting a tenant's sign-in method loses its client secret and its approval.
func TestTenantIdPDeleteRefusesWithoutAnAuditRecord(t *testing.T) {
	ctx := context.Background()
	st := p3Store(t)
	mgr := p4Manager(t, st)
	tn, _ := st.CreateTenant(ctx, "sub", "Sub")
	admin, _ := st.UpsertIdentity(ctx, "admin@sub.co.jp", "admin-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, admin.ID, tn.ID, "tenant_admin"); err != nil {
		t.Fatal(err)
	}
	row := seedTenantIdP(t, st, tn.ID, "entra", "sub.co.jp", "active")
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodDelete, "/api/admin/tenants/sub/idp/"+row.ID, nil)
		r.SetPathValue("slug", "sub")
		r.SetPathValue("id", row.ID)
		r.Header.Set("X-Forwarded-Email", "admin@sub.co.jp")
		w := httptest.NewRecorder()
		newTenantIdPAPI(mgr, nil).remove(w, r)
		return w
	}

	mgr.store = auditFailingStore{st, failEveryAudit}
	wantAuditUnavailable(t, "delete sign-in method", call())
	if _, found, err := st.GetTenantIdP(ctx, tn.ID, row.ID); err != nil || !found {
		t.Fatalf("sign-in method after the refusal: found=%v err=%v, want it still there", found, err)
	}

	mgr.store = st
	if w := call(); w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s, want 200", w.Code, w.Body.String())
	}
	req, out := auditPair(t, st, tn.ID, "tenant_idp.delete")
	if req.ActorID != admin.ID || out.ActorID != admin.ID || out.HTTPStatus != http.StatusOK || !strings.Contains(out.Detail, "issuer=") {
		t.Errorf("request %+v / outcome %+v, want both by the admin and a 200 naming the issuer", req, out)
	}
}

// Issue #1334: deleting an internal repository removes the bare and its LFS objects, and a
// rename breaks every clone's origin; neither happens without a record of who asked.
func TestInternalGitDeleteAndRenameRefuseWithoutAnAuditRecord(t *testing.T) {
	ctx := context.Background()
	e := newP2Env(t)
	e.seedRepo(t, "keep")
	dir := filepath.Join(e.g.dataRoot, "git", "default", "keep.git")
	call := func(method, path, name string, body any, h func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView)) *httptest.ResponseRecorder {
		w, r := e.req(method, path, body)
		r.SetPathValue("name", name)
		e.g.withMembership(h)(w, r)
		return w
	}

	e.g.store = auditFailingStore{e.st, failEveryAudit}
	wantAuditUnavailable(t, "delete repo", call(http.MethodDelete, "/api/internal-git/repos/keep", "keep", nil, e.g.repoDelete))
	wantAuditUnavailable(t, "rename repo", call(http.MethodPost, "/api/internal-git/repos/keep/rename", "keep",
		map[string]string{"new_name": "moved"}, e.g.repoRename))
	if _, ok, err := e.st.GetGitRepo(ctx, e.tenantID, "keep"); err != nil || !ok {
		t.Fatalf("repo row after the refusals: ok=%v err=%v, want it still there under its name", ok, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("bare after the refusals: %v, want it untouched", err)
	}

	e.g.store = e.st
	if w := call(http.MethodPost, "/api/internal-git/repos/keep/rename", "keep", map[string]string{"new_name": "moved"}, e.g.repoRename); w.Code != http.StatusOK {
		t.Fatalf("rename = %d %s, want 200", w.Code, w.Body.String())
	}
	if _, out := auditPair(t, e.st, e.tenantID, "internal_git.repo.rename"); out.HTTPStatus != http.StatusOK || out.Detail != "to=moved" {
		t.Errorf("rename outcome = %+v, want 200 to=moved", out)
	}
	if w := call(http.MethodDelete, "/api/internal-git/repos/moved", "moved", nil, e.g.repoDelete); w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s, want 200", w.Code, w.Body.String())
	}
	if req, out := auditPair(t, e.st, e.tenantID, "internal_git.repo.delete"); req.Target != "moved" || out.HTTPStatus != http.StatusOK {
		t.Errorf("delete request %+v / outcome %+v, want target moved and a 200", req, out)
	}
}

// A rename that fails on disk after the request was recorded closes it with the failure,
// so the lone request row does not read as an outcome that was lost.
func TestInternalGitRenameFailureClosesTheRequest(t *testing.T) {
	e := newP2Env(t)
	e.seedRepo(t, "gone")
	if err := os.RemoveAll(filepath.Join(e.g.dataRoot, "git", "default", "gone.git")); err != nil {
		t.Fatal(err)
	}
	w, r := e.req(http.MethodPost, "/api/internal-git/repos/gone/rename", map[string]string{"new_name": "other"})
	r.SetPathValue("name", "gone")
	e.g.withMembership(e.g.repoRename)(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("rename of a missing bare = %d %s, want 500", w.Code, w.Body.String())
	}
	if _, out := auditPair(t, e.st, e.tenantID, "internal_git.repo.rename"); out.HTTPStatus != http.StatusInternalServerError ||
		!strings.HasPrefix(out.Detail, "error rename_failed: ") {
		t.Errorf("rename outcome = %+v, want the 500 and rename_failed", out)
	}
}
