package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// DELETE /api/admin/ec2-pool/slots/{id} is the only route in the product that deletes a
// machine because somebody asked, so the two things pinned here are the two that make it
// safe to have: a refusal the adapter raises must not reach the operator as "500, try
// again" (the id was wrong, or the box is a working slot — both are answers, not faults),
// and the quarantine reason must land in the audit log, because it lived in the instance's
// tags and the instance is about to stop existing (ADR 0045 decision 20 / 23).

// slotPoolFactory is a runtime factory that owns a slot pool, i.e. implements the one method
// terminateQuarantinedSlot type-asserts for.
type slotPoolFactory struct {
	reason string
	err    error
	asked  []string
}

func (f *slotPoolFactory) New(runtime.Workspace, string, []string) runtime.Runtime { return stubRuntime{} }

func (f *slotPoolFactory) TerminateQuarantinedSlot(_ context.Context, instanceID string) (string, error) {
	f.asked = append(f.asked, instanceID)
	return f.reason, f.err
}

func poolSlotFixture(t *testing.T, f runtime.RuntimeFactory) (*store.SQL, adminAPI) {
	t.Helper()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	mgr.rtFactory = f
	if _, err := st.UpsertIdentity(context.Background(), "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return st, newAdminAPI(mgr)
}

func callTerminateSlot(adm adminAPI, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "/api/admin/ec2-pool/slots/"+id, nil)
	r.SetPathValue("id", id)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	adm.withSuperAdmin(adm.terminatePoolSlot)(w, r)
	return w
}

func TestTerminatePoolSlotAuditsTheQuarantineReason(t *testing.T) {
	f := &slotPoolFactory{reason: "mount home on i-bad: no device"}
	st, adm := poolSlotFixture(t, f)

	w := callTerminateSlot(adm, "i-bad")
	if w.Code != http.StatusOK {
		t.Fatalf("terminate = %d %s, want 200", w.Code, w.Body.String())
	}
	if len(f.asked) != 1 || f.asked[0] != "i-bad" {
		t.Fatalf("adapter asked for %v, want [i-bad]", f.asked)
	}
	rows, err := st.ListAuditByTenant(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var found bool
	for _, a := range rows {
		if a.Action == "pool.slot_terminate" && a.Target == "i-bad" && a.Detail == "quarantined: mount home on i-bad: no device" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit row carrying the quarantine reason: %+v — the tags holding it go with the box", rows)
	}
}

// The adapter refuses a box that is not quarantined (a live slot leaves through the
// dormancy series). That is the operator's mistake, and 409 with the reason is the only
// form of it they can act on.
func TestTerminatePoolSlotPassesTheAdaptersRefusalsThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"unknown id", runtime.ErrSlotNotFound, http.StatusNotFound},
		{"a working slot", runtime.ErrSlotNotQuarantined, http.StatusConflict},
		{"still holds a home", runtime.ErrSlotInUse, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, adm := poolSlotFixture(t, &slotPoolFactory{err: tc.err})
			if w := callTerminateSlot(adm, "i-x"); w.Code != tc.want {
				t.Fatalf("terminate = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			rows, _ := st.ListAuditByTenant(context.Background(), "", 10)
			for _, a := range rows {
				if a.Action == "pool.slot_terminate" {
					t.Fatalf("a refused terminate was audited as one: %+v", a)
				}
			}
		})
	}
}

// Every other runtime has no pool at all. Answering 200 there would tell an operator a box
// was removed on a deployment that has none.
func TestTerminatePoolSlotOnARuntimeWithNoPool(t *testing.T) {
	_, adm := poolSlotFixture(t, &destroyingFactory{})
	if w := callTerminateSlot(adm, "i-x"); w.Code != http.StatusNotFound {
		t.Fatalf("terminate on a poolless runtime = %d %s, want 404", w.Code, w.Body.String())
	}
}
