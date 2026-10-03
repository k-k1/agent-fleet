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

// Reserving a slot for replacement (#1473). What is pinned here is the HTTP half: every
// reservation is in the audit log with the workspace it moves, the adapter's refusals reach
// the operator as answers rather than 500s, and the bulk path reserves only what the adapter
// confirms is below $Latest — one audit pair per slot.

type reservingPoolFactory struct {
	slotPoolFactory
	refuse map[string]error
	asked  []string
}

func (f *reservingPoolFactory) ReserveSlotReplacement(_ context.Context, id string, reserve, onlyOutdated bool) (runtime.SlotReservation, error) {
	mode := "reserve"
	if !reserve {
		mode = "cancel"
	}
	if onlyOutdated {
		mode += "-outdated"
	}
	f.asked = append(f.asked, id+":"+mode)
	if err := f.refuse[id]; err != nil {
		return runtime.SlotReservation{InstanceID: id}, err
	}
	return runtime.SlotReservation{InstanceID: id, Workspace: "af-ws-" + id, TemplateVersion: "3", TemplateLatest: "5", Reserved: reserve}, nil
}

func callReserveSlot(adm adminAPI, method, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/ec2-pool/slots/"+id+"/replace", nil)
	r.SetPathValue("id", id)
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	adm.withSuperAdmin(adm.reservePoolSlot)(w, r)
	return w
}

func callReserveOutdated(adm adminAPI, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/ec2-pool/reserve-outdated", strings.NewReader(body))
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	w := httptest.NewRecorder()
	adm.withSuperAdmin(adm.reserveOutdatedPoolSlots)(w, r)
	return w
}

func auditRows(t *testing.T, st store.Store, action string) []store.AuditLog {
	t.Helper()
	rows, err := st.ListAuditByTenant(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var out []store.AuditLog
	for _, a := range rows {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

func TestReservePoolSlotIsAuditedWithTheWorkspaceItMoves(t *testing.T) {
	f := &reservingPoolFactory{}
	st, adm := poolSlotFixture(t, f)

	if w := callReserveSlot(adm, http.MethodPut, "i-old"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"reserved":true`) {
		t.Fatalf("reserve = %d %s, want 200 reserved", w.Code, w.Body.String())
	}
	if w := callReserveSlot(adm, http.MethodDelete, "i-old"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"reserved":false`) {
		t.Fatalf("cancel = %d %s, want 200 not reserved", w.Code, w.Body.String())
	}
	if strings.Join(f.asked, ",") != "i-old:reserve,i-old:cancel" {
		t.Fatalf("adapter asked %v", f.asked)
	}
	res := auditRows(t, st, "pool.slot_replace_reserve")
	if len(res) != 1 || res[0].Target != "i-old" || res[0].Detail != "workspace=af-ws-i-old template=3 latest=5" {
		t.Fatalf("reserve audit = %+v", res)
	}
	if len(auditRows(t, st, "pool.slot_replace_reserve"+store.AuditRequestedSuffix)) != 1 {
		t.Fatal("no intent row: the reservation was not recorded before it ran")
	}
	if c := auditRows(t, st, "pool.slot_replace_cancel"); len(c) != 1 || c[0].Target != "i-old" {
		t.Fatalf("cancel audit = %+v", c)
	}
}

func TestReservePoolSlotPassesTheAdaptersRefusalsThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"unknown id", runtime.ErrSlotNotFound, http.StatusNotFound},
		{"quarantined", runtime.ErrSlotQuarantined, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, adm := poolSlotFixture(t, &reservingPoolFactory{refuse: map[string]error{"i-x": tc.err}})
			if w := callReserveSlot(adm, http.MethodPut, "i-x"); w.Code != tc.want {
				t.Fatalf("reserve = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			for _, a := range auditRows(t, st, "pool.slot_replace_reserve") {
				if a.HTTPStatus != tc.want || !strings.HasPrefix(a.Detail, "error ") {
					t.Fatalf("a refused reservation was audited as one: %+v", a)
				}
			}
		})
	}
}

func TestReserveOutdatedPoolSlotsReservesOnlyWhatTheAdapterConfirms(t *testing.T) {
	f := &reservingPoolFactory{refuse: map[string]error{"i-cur": runtime.ErrSlotNotOutdated}}
	st, adm := poolSlotFixture(t, f)

	w := callReserveOutdated(adm, `{"instance_ids":["i-a","i-cur","i-b","i-a"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("bulk = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"instance_id":"i-cur","code":"slot_not_outdated"`) {
		t.Fatalf("bulk reply %s does not report i-cur as skipped", body)
	}
	// Every id goes through the outdated re-check, once.
	if got := strings.Join(f.asked, ","); got != "i-a:reserve-outdated,i-cur:reserve-outdated,i-b:reserve-outdated" {
		t.Fatalf("adapter asked %s", got)
	}
	ok := 0
	for _, a := range auditRows(t, st, "pool.slot_replace_reserve") {
		if a.HTTPStatus == http.StatusOK {
			ok++
		}
	}
	if ok != 2 || len(auditRows(t, st, "pool.slot_replace_reserve")) != 3 {
		t.Fatalf("audit rows: want one per slot, two of them reservations; got %+v", auditRows(t, st, "pool.slot_replace_reserve"))
	}
}

func TestReservePoolSlotOnARuntimeWithNoPool(t *testing.T) {
	_, adm := poolSlotFixture(t, &destroyingFactory{})
	if w := callReserveSlot(adm, http.MethodPut, "i-x"); w.Code != http.StatusNotFound {
		t.Fatalf("reserve on a poolless runtime = %d %s, want 404", w.Code, w.Body.String())
	}
	if w := callReserveOutdated(adm, `{"instance_ids":["i-x"]}`); w.Code != http.StatusNotFound {
		t.Fatalf("bulk on a poolless runtime = %d %s, want 404", w.Code, w.Body.String())
	}
}

type slotReplaceStubRuntime struct {
	stubRuntime
	pending bool
}

func (s slotReplaceStubRuntime) SlotReplacePending(context.Context) bool { return s.pending }

// The member's WS bar learns that the next start moves to a new slot. Not during a start: that
// start is the move.
func TestWorkspacePayloadSlotReplace(t *testing.T) {
	a := workspaceAPI{}
	ctx := context.Background()
	for _, state := range []string{"running", "stopped"} {
		m := a.workspacePayload(ctx, &resolved{rt: slotReplaceStubRuntime{stubRuntime{state: state}, true}}, state)
		if m["slotReplace"] != true {
			t.Fatalf("%s + reserved: slotReplace = %v, want true", state, m["slotReplace"])
		}
	}
	m := a.workspacePayload(ctx, &resolved{rt: slotReplaceStubRuntime{stubRuntime{state: "starting"}, true}}, "starting")
	if _, ok := m["slotReplace"]; ok {
		t.Fatal("slotReplace shown while the start that moves it runs")
	}
	m = a.workspacePayload(ctx, &resolved{rt: slotReplaceStubRuntime{stubRuntime{}, false}}, "running")
	if _, ok := m["slotReplace"]; ok {
		t.Fatal("slotReplace present with no reservation; the key must stay absent")
	}
}
