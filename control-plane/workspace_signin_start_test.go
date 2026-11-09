package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// signinRT is a runtime whose state the test sets and whose Start calls are counted. A
// Start moves it to running, like a real one that has come up.
type signinRT struct {
	stubRuntime
	mu     sync.Mutex
	state  string
	starts int
}

func (r *signinRT) State(context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

func (r *signinRT) Start(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	r.state = "running"
	return nil
}

func (r *signinRT) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

type signinCase struct {
	api workspaceAPI
	res *resolved
	rt  *signinRT
	st  *store.SQL
	ws  store.Workspace
	tn  store.Tenant
}

func newSigninCase(t *testing.T, state string, autostart bool) *signinCase {
	t.Helper()
	rt := &signinRT{state: state}
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	return &signinCase{
		api: newWorkspaceAPI(mgr, autostart),
		res: &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}},
		rt:  rt, st: st, ws: ws, tn: tn,
	}
}

func (c *signinCase) tenantLimits(t *testing.T, json string) {
	t.Helper()
	if err := c.st.SetTenantLimits(context.Background(), c.tn.ID, json); err != nil {
		t.Fatal(err)
	}
}

func (c *signinCase) wsSettings(t *testing.T, json string) {
	t.Helper()
	if err := c.st.SetWorkspaceSettings(context.Background(), c.ws.ID, json); err != nil {
		t.Fatal(err)
	}
}

func (c *signinCase) call(t *testing.T) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	c.api.signinStart(w, httptest.NewRequest(http.MethodPost, "/api/workspace/signin-start", nil), c.res)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestSigninStartStartsAStoppedWorkspaceWhenTheTenantIsOn(t *testing.T) {
	c := newSigninCase(t, "stopped", true)
	c.tenantLimits(t, `{"autostart_on_signin":true}`)
	code, out := c.call(t)
	if code != http.StatusOK || out["started"] != true {
		t.Fatalf("answer = %d %v, want started", code, out)
	}
	if c.rt.startCount() != 1 {
		t.Fatalf("Start called %d times, want 1", c.rt.startCount())
	}
	// A second sign-in while it is up does not start it again.
	_, out = c.call(t)
	if out["started"] != false || out["reason"] != signinStartNotStopped || c.rt.startCount() != 1 {
		t.Fatalf("second call = %v, starts=%d", out, c.rt.startCount())
	}
}

// The policy is the CP's: a client that calls the endpoint regardless cannot start anything
// the tenant has not switched on or the member has opted out of.
func TestSigninStartRefusesWhenTheTenantIsOffOrTheMemberOptedOut(t *testing.T) {
	for _, c := range []struct {
		name, limits, settings, reason string
		autostart                      bool
	}{
		{"tenant never set it", `{}`, ``, signinStartTenantOff, true},
		{"tenant explicitly off", `{"autostart_on_signin":false}`, ``, signinStartTenantOff, true},
		{"member opted out", `{"autostart_on_signin":true}`, `{"signinStartOptOut":true}`, signinStartOptedOut, true},
		{"operator turned autostart off", `{"autostart_on_signin":true}`, ``, signinStartOperatorOff, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tc := newSigninCase(t, "stopped", c.autostart)
			tc.tenantLimits(t, c.limits)
			if c.settings != "" {
				tc.wsSettings(t, c.settings)
			}
			code, out := tc.call(t)
			if code != http.StatusOK || out["started"] != false || out["reason"] != c.reason {
				t.Fatalf("answer = %d %v, want not started (%s)", code, out, c.reason)
			}
			if tc.rt.startCount() != 0 {
				t.Fatal("the workspace was started")
			}
		})
	}
}

// A workspace the runtime cannot describe (a CP that lost its runtime, a state outside the
// contract) must never be started; neither is one that is not positively `stopped`.
func TestSigninStartNeverStartsOnAnUncertainState(t *testing.T) {
	for state, reason := range map[string]string{
		"":         signinStartStateUnsure,
		"unknown":  signinStartStateUnsure,
		"starting": signinStartNotStopped,
		"running":  signinStartNotStopped,
		"none":     signinStartNotStopped,
	} {
		tc := newSigninCase(t, state, true)
		tc.tenantLimits(t, `{"autostart_on_signin":true}`)
		_, out := tc.call(t)
		if out["started"] != false || out["reason"] != reason || tc.rt.startCount() != 0 {
			t.Errorf("state %q: answer %v, starts=%d, want not started (%s)", state, out, tc.rt.startCount(), reason)
		}
	}
}

// The limit check is ensureWorkspaceStarted's own; here it is reported as a reason Home can
// show, not as an error and not as a start.
func TestSigninStartReportsAReachedWorkspaceLimit(t *testing.T) {
	c := newSigninCase(t, "stopped", true)
	c.tenantLimits(t, `{"autostart_on_signin":true,"max_workspaces":1}`)
	// Another member's workspace in the same tenant is running and fills the limit.
	other, err := c.st.UpsertIdentity(context.Background(), "other@acme.co.jp", "other-acme-co-jp", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := c.st.EnsureMembership(context.Background(), other.ID, c.tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.st.CreateWorkspace(context.Background(), store.Workspace{
		ID: "W-2", TenantID: c.tn.ID, MembershipID: mem.ID, ContainerName: "af-ws-sales-other",
		Network: "n", DataDir: "/srv/data/sales/other", AgentPort: "7732", AgentToken: "t",
		State: "running", CreatedAt: store.NowTS(),
	}); err != nil {
		t.Fatal(err)
	}
	// Both workspaces share the one fake runtime, so make the other count as running and
	// this one stopped by flipping the state only for the member under test afterwards.
	c.rt.state = "running"
	c.res.rt = stateOnlyRuntime{state: "stopped"}
	code, out := c.call(t)
	if code != http.StatusOK || out["started"] != false || out["reason"] != signinStartQuota {
		t.Fatalf("answer = %d %v, want the quota reason", code, out)
	}
	if out["message"] == "" || out["message"] == nil {
		t.Error("no message to show on Home")
	}
}

// The member's choice is stored as an opt-out, so every existing member follows the tenant,
// and the tenant's gate rides on the same body for the Console to decide whether to show it.
func TestWSSettingsCarriesTheSigninStartChoiceAndTheTenantGate(t *testing.T) {
	c := newSigninCase(t, "stopped", true)
	api := newWSSettingsAPI(c.api.mgr)
	get := func() map[string]any {
		w := httptest.NewRecorder()
		api.get(w, httptest.NewRequest(http.MethodGet, "/api/env/ws-settings", nil), c.res)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	put := func(body string) {
		w := httptest.NewRecorder()
		api.put(w, httptest.NewRequest(http.MethodPut, "/api/env/ws-settings", strings.NewReader(body)), c.res)
		if w.Code != http.StatusOK {
			t.Fatalf("put %s = %d %s", body, w.Code, w.Body.String())
		}
	}
	if out := get(); out["signinStart"] != true || out["allowSigninStart"] != false {
		t.Fatalf("default = %v, want signinStart true (follows the tenant) and the gate closed", out)
	}
	c.tenantLimits(t, `{"autostart_on_signin":true}`)
	put(`{"signinStart":false}`)
	if out := get(); out["signinStart"] != false || out["allowSigninStart"] != true {
		t.Fatalf("after opting out = %v", out)
	}
	if _, out := c.call(t); out["reason"] != signinStartOptedOut {
		t.Fatalf("the saved opt-out did not reach the start: %v", out)
	}
	put(`{"signinStart":true}`)
	if out := get(); out["signinStart"] != true {
		t.Fatalf("after opting back in = %v", out)
	}
	if _, out := c.call(t); out["started"] != true {
		t.Fatalf("opted back in but not started: %v", out)
	}
}
