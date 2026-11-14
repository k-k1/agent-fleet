package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func (f *slotPoolFactory) New(runtime.Workspace, runtime.SecretKeys, []string) runtime.Runtime {
	return stubRuntime{}
}

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
				if a.Action == "pool.slot_terminate" && (a.HTTPStatus != tc.want || !strings.HasPrefix(a.Detail, "error ")) {
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

// POST /api/admin/ec2-pool/slots/{id}/force-stop (#2449) stops a machine because somebody
// asked. What is pinned: the override is the operator's explicit word and reaches the adapter
// only when the body says so, the adapter's refusals are answers (404/409) and not faults, and
// what the adapter did lands in the audit line even when the call failed half way.

type forceStopFactory struct {
	slotPoolFactory
	res      runtime.SlotForceStop
	fsErr    error
	override []bool
	// lateRes / lateErr, when lateErr or inProgress is set, make the adapter answer
	// ErrSlotForceStopInProgress and deliver this final answer to the late callback.
	inProgress bool
	lateRes    runtime.SlotForceStop
	lateErr    error
}

func (f *forceStopFactory) ForceStopSlot(ctx context.Context, id string, override bool) (runtime.SlotForceStop, error) {
	f.asked = append(f.asked, id)
	f.override = append(f.override, override)
	if f.inProgress {
		if fn := runtime.ForceStopLateFrom(ctx); fn != nil {
			fn(f.lateRes, f.lateErr)
		}
		return f.res, runtime.ErrSlotForceStopInProgress
	}
	return f.res, f.fsErr
}

func callForceStop(adm adminAPI, id, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/ec2-pool/slots/"+id+"/force-stop", strings.NewReader(body))
	r.SetPathValue("id", id)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	adm.withSuperAdmin(adm.forceStopPoolSlot)(w, r)
	return w
}

func TestForceStopPoolSlotAuditsWhatTheAdapterDid(t *testing.T) {
	f := &forceStopFactory{res: runtime.SlotForceStop{
		InstanceID: "i-wedge", Workspace: "af-ws-x", PreviousState: "running",
		CancelledCommands: 2,
	}}
	st, adm := poolSlotFixture(t, f)
	w := callForceStop(adm, "i-wedge", "boss@acme.co.jp", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"cancelled_commands":2`) {
		t.Fatalf("force-stop = %d %s", w.Code, w.Body.String())
	}
	if len(f.override) != 1 || f.override[0] {
		t.Fatalf("override without a body = %v, want false", f.override)
	}
	rows, _ := st.ListAuditByTenant(context.Background(), "", 10)
	var found bool
	for _, a := range rows {
		if a.Action == "pool.slot_force_stop" && a.Target == "i-wedge" && a.HTTPStatus == http.StatusOK &&
			strings.Contains(a.Detail, "workspace=af-ws-x") && strings.Contains(a.Detail, "cancelled=2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit row for the force-stop: %+v", rows)
	}
}

// The adapter ends the wait after taking its token (the request ended): the Console gets 202
// in_progress, the audit has the intent now and exactly one outcome row once the work is done.
func TestForceStopPoolSlotInProgressAnswers202AndAuditsTheLateOutcome(t *testing.T) {
	f := &forceStopFactory{
		inProgress: true,
		res:        runtime.SlotForceStop{InstanceID: "i-wedge", Workspace: "af-ws-x"},
		lateRes:    runtime.SlotForceStop{InstanceID: "i-wedge", Workspace: "af-ws-x", Deregistered: true, Forced: false},
	}
	st, adm := poolSlotFixture(t, f)
	w := callForceStop(adm, "i-wedge", "boss@acme.co.jp", "")
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"in_progress":true`) {
		t.Fatalf("force-stop = %d %s", w.Code, w.Body.String())
	}
	rows, _ := st.ListAuditByTenant(context.Background(), "", 10)
	outcomes := 0
	for _, a := range rows {
		if a.Action == "pool.slot_force_stop" && a.HTTPStatus == http.StatusOK && strings.Contains(a.Detail, "deregistered=true") {
			outcomes++
		}
	}
	if outcomes != 1 {
		t.Fatalf("want exactly one outcome row from the late answer, got %d: %+v", outcomes, rows)
	}
}

func TestForceStopPoolSlotOverrideComesOnlyFromTheBody(t *testing.T) {
	f := &forceStopFactory{}
	_, adm := poolSlotFixture(t, f)
	if w := callForceStop(adm, "i-a", "boss@acme.co.jp", `{"override_healthy":true}`); w.Code != http.StatusOK {
		t.Fatalf("override = %d %s", w.Code, w.Body.String())
	}
	if w := callForceStop(adm, "i-a", "boss@acme.co.jp", `{not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body = %d, want 400", w.Code)
	}
	if len(f.override) != 1 || !f.override[0] {
		t.Fatalf("adapter calls = %v, want one with override", f.override)
	}
}

func TestForceStopPoolSlotPassesTheAdaptersRefusalsThrough(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
		code string
	}{
		{runtime.ErrSlotNotFound, http.StatusNotFound, "no_such_slot"},
		{runtime.ErrSlotNotRunning, http.StatusConflict, "slot_not_running"},
		{runtime.ErrSlotQuarantined, http.StatusConflict, "slot_quarantined"},
		{runtime.ErrSlotHealthy, http.StatusConflict, "slot_healthy"},
		{fmt.Errorf("not stopping: %w", runtime.ErrSlotTaskENIHeld), http.StatusConflict, "slot_task_eni_held"},
		{runtime.ErrSlotStartedAgain, http.StatusConflict, "slot_started_again"},
	} {
		st, adm := poolSlotFixture(t, &forceStopFactory{fsErr: tc.err})
		w := callForceStop(adm, "i-x", "boss@acme.co.jp", "")
		if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("%v: = %d %s, want %d %s", tc.err, w.Code, w.Body.String(), tc.want, tc.code)
		}
		rows, _ := st.ListAuditByTenant(context.Background(), "", 10)
		for _, a := range rows {
			if a.Action == "pool.slot_force_stop" && (a.HTTPStatus != tc.want || !strings.HasPrefix(a.Detail, "error "+tc.code)) {
				t.Fatalf("a refused force-stop was audited as %+v", a)
			}
		}
	}
}

func TestForceStopPoolSlotIsSuperAdminOnly(t *testing.T) {
	f := &forceStopFactory{}
	st, adm := poolSlotFixture(t, f)
	if _, err := st.UpsertIdentity(context.Background(), "ta@acme.co.jp", "ta-acme-co-jp", "member"); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"ta@acme.co.jp", "stranger@example.com"} {
		if w := callForceStop(adm, "i-x", email, ""); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Fatalf("%s got %d, want 401/403", email, w.Code)
		}
	}
	if len(f.asked) != 0 {
		t.Fatalf("a non-super-admin reached the adapter: %v", f.asked)
	}
}

func TestForceStopPoolSlotOnARuntimeWithNoPool(t *testing.T) {
	_, adm := poolSlotFixture(t, &destroyingFactory{})
	if w := callForceStop(adm, "i-x", "boss@acme.co.jp", ""); w.Code != http.StatusNotFound {
		t.Fatalf("force-stop on a poolless runtime = %d, want 404", w.Code)
	}
}

// POST /api/admin/ec2-pool/slots/{id}/return (#4019) lifts a rung-L2 hold. Pinned: the token
// from the body reaches the adapter untouched (the delete is by that exact value), the
// adapter's refusals are answers, and the quarantine reason lands in the audit row because the
// record that held it is deleted.

type returnSlotFactory struct {
	slotPoolFactory
	res    runtime.SlotReturn
	retErr error
	tokens []string
}

func (f *returnSlotFactory) ReturnQuarantinedSlot(_ context.Context, id, token string) (runtime.SlotReturn, error) {
	f.asked = append(f.asked, id)
	f.tokens = append(f.tokens, token)
	return f.res, f.retErr
}

func callReturnSlot(adm adminAPI, id, email, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/ec2-pool/slots/"+id+"/return", strings.NewReader(body))
	r.SetPathValue("id", id)
	r.Header.Set("X-Forwarded-Email", email)
	w := httptest.NewRecorder()
	adm.withSuperAdmin(adm.returnPoolSlot)(w, r)
	return w
}

func TestReturnPoolSlotPassesTheTokenAndAuditsTheReason(t *testing.T) {
	f := &returnSlotFactory{res: runtime.SlotReturn{InstanceID: "i-held", Workspace: "ws-1", Reason: "repeat: dark again", CountBumped: true}}
	st, adm := poolSlotFixture(t, f)
	const token = "vol-1:7/repeat: dark again/2026-11-13T00:00:00Z"
	w := callReturnSlot(adm, "i-held", "boss@acme.co.jp", `{"token":"`+token+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("return = %d %s", w.Code, w.Body.String())
	}
	if len(f.tokens) != 1 || f.tokens[0] != token || f.asked[0] != "i-held" {
		t.Fatalf("adapter asked %v %v", f.asked, f.tokens)
	}
	rows, _ := st.ListAuditByTenant(context.Background(), "", 10)
	var found bool
	for _, a := range rows {
		if a.Action == "pool.slot_return" && a.Target == "i-held" && a.HTTPStatus == http.StatusOK &&
			strings.Contains(a.Detail, "reason=repeat: dark again") && strings.Contains(a.Detail, "count_bumped=true") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit row carrying the reason: %+v", rows)
	}
}

func TestReturnPoolSlotRefusalsAreAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"unknown id", runtime.ErrSlotNotFound, http.StatusNotFound},
		{"bad token", runtime.ErrSlotQuarantineToken, http.StatusBadRequest},
		{"role quarantined", runtime.ErrSlotRoleQuarantined, http.StatusConflict},
		{"not quarantined", runtime.ErrSlotNotQuarantined, http.StatusConflict},
		{"re-quarantined", runtime.ErrSlotQuarantineChanged, http.StatusConflict},
		{"a start is placing", runtime.ErrSlotPlacing, http.StatusConflict},
		{"lock busy", runtime.ErrSlotBusy, http.StatusConflict},
		{"AWS failed", errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, adm := poolSlotFixture(t, &returnSlotFactory{retErr: tc.err})
			if w := callReturnSlot(adm, "i-x", "boss@acme.co.jp", `{"token":"t"}`); w.Code != tc.want {
				t.Fatalf("return = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			rows, _ := st.ListAuditByTenant(context.Background(), "", 10)
			for _, a := range rows {
				if a.Action == "pool.slot_return" && (a.HTTPStatus != tc.want || !strings.HasPrefix(a.Detail, "error ")) {
					t.Fatalf("a refused return was audited as one: %+v", a)
				}
			}
		})
	}
}

func TestReturnPoolSlotNeedsATokenAndASuperAdmin(t *testing.T) {
	f := &returnSlotFactory{}
	st, adm := poolSlotFixture(t, f)
	for _, body := range []string{"", "{}", `{"token":"  "}`, "not json"} {
		if w := callReturnSlot(adm, "i-x", "boss@acme.co.jp", body); w.Code != http.StatusBadRequest {
			t.Fatalf("body %q = %d, want 400", body, w.Code)
		}
	}
	if _, err := st.UpsertIdentity(context.Background(), "ta@acme.co.jp", "ta-acme-co-jp", "member"); err != nil {
		t.Fatal(err)
	}
	if w := callReturnSlot(adm, "i-x", "ta@acme.co.jp", `{"token":"t"}`); w.Code != http.StatusForbidden {
		t.Fatalf("a non-super-admin = %d, want 403", w.Code)
	}
	if len(f.asked) != 0 {
		t.Fatalf("the adapter was reached: %v", f.asked)
	}
}

func TestReturnPoolSlotOnARuntimeWithNoPool(t *testing.T) {
	_, adm := poolSlotFixture(t, &destroyingFactory{})
	if w := callReturnSlot(adm, "i-x", "boss@acme.co.jp", `{"token":"t"}`); w.Code != http.StatusNotFound {
		t.Fatalf("return on a poolless runtime = %d, want 404", w.Code)
	}
}
