package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/routepolicysrv"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// noSubscriptionsRule restricts a tenant to member keys: anthropic-api is allowed, every
// subscription is not.
const noSubscriptionsRule = `{"classes":["member-key"]}`

func restrictTenant(t *testing.T, st store.Store, tenantID string) {
	t.Helper()
	if _, _, err := st.PutRoutePolicy(context.Background(), store.RoutePolicyTenant, tenantID, noSubscriptionsRule, "admin-x", nil); err != nil {
		t.Fatal(err)
	}
}

// useAgentRoutePolicy answers every Agent's /healthz "route_policy" with reported (answered as
// given) and counts the questions.
func useAgentRoutePolicy(t *testing.T, reported string, answered bool) *int {
	t.Helper()
	asked := 0
	old := agentRoutePolicy
	agentRoutePolicy = func(context.Context, runtime.Runtime) (string, bool) { asked++; return reported, answered }
	t.Cleanup(func() { agentRoutePolicy = old })
	return &asked
}

// POST /api/sessions under a restricted tenant: an explicit forbidden route is refused before the
// Agent is asked, an allowed one and an empty selection reach it, and with no restriction nothing
// changes. Not in parallel: it swaps the Agent capability seam.
func TestSessionCreateUnderTheCredentialPolicy(t *testing.T) {
	f := newHandoffLife(t)
	rc := f.recipientWorkspace()
	asked := useAgentRoutePolicy(t, routepolicysrv.AgentEnforceCapability, true)

	// No restriction: a subscription route is forwarded and the Agent is not even asked.
	if rec := rc.create(t, map[string]any{"kind": "claude", "dir": "/repos/app", "route": map[string]any{"route": "anthropic-subscription"}}); rec.Code != http.StatusCreated {
		t.Fatalf("unrestricted: %d %s", rec.Code, rec.Body.String())
	}
	if *asked != 0 {
		t.Errorf("an unrestricted create asked the Agent for its capability %d times", *asked)
	}

	restrictTenant(t, f.st, f.recipient.mv.TenantID)
	before := len(rc.bodies)
	rec := rc.create(t, map[string]any{"kind": "claude", "dir": "/repos/app", "route": map[string]any{"route": "anthropic-subscription"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"route_forbidden"`) || len(rc.bodies) != before {
		t.Fatalf("a forbidden route: %d %s (forwarded %d)", rec.Code, rec.Body.String(), len(rc.bodies)-before)
	}
	for _, body := range []map[string]any{
		{"kind": "claude", "dir": "/repos/app", "route": map[string]any{"route": "anthropic-api"}},
		{"kind": "claude", "dir": "/repos/app"}, // empty: the Agent judges it (D11)
	} {
		if rec := rc.create(t, body); rec.Code != http.StatusCreated {
			t.Errorf("%v: %d %s, want forwarded", body, rec.Code, rec.Body.String())
		}
	}
	if len(rc.bodies) != before+2 {
		t.Errorf("forwarded %d creates, want 2", len(rc.bodies)-before)
	}
}

// D12's Upgrade rule on the REST path: a restricted member's create is not handed to an Agent
// that does not report enforcement, nor to one that does not answer; a kind that runs no model is
// not judged.
func TestSessionCreateRefusesAnAgentThatDoesNotEnforce(t *testing.T) {
	f := newHandoffLife(t)
	rc := f.recipientWorkspace()
	restrictTenant(t, f.st, f.recipient.mv.TenantID)
	for name, tc := range map[string]struct {
		reported string
		answered bool
		code     string
	}{
		"an Agent from before enforcement": {"", true, routepolicysrv.CodeAgentUnenforcing},
		"an Agent that does not answer":    {"", false, routepolicysrv.CodePolicyUnconfirmed},
	} {
		useAgentRoutePolicy(t, tc.reported, tc.answered)
		before := len(rc.bodies)
		rec := rc.create(t, map[string]any{"kind": "claude", "dir": "/repos/app", "route": map[string]any{"route": "anthropic-api"}})
		if !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) || len(rc.bodies) != before {
			t.Errorf("%s: %d %s (forwarded %d)", name, rec.Code, rec.Body.String(), len(rc.bodies)-before)
		}
	}
	useAgentRoutePolicy(t, "", true)
	if rec := rc.create(t, map[string]any{"kind": "shell", "dir": "/repos/app"}); rec.Code != http.StatusCreated {
		t.Errorf("a shell on an old Agent: %d %s", rec.Code, rec.Body.String())
	}
}

// A schedule's save under a restricted tenant: a forbidden route is not saved, an allowed one, an
// empty selection and an assistant schedule are.
func TestScheduleSaveUnderTheCredentialPolicy(t *testing.T) {
	api, ctx, mv := newSchedAPITest(t)
	tn, err := api.mgr.store.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mv.TenantID = tn.ID
	restrictTenant(t, api.mgr.store, tn.ID)
	const base = `"spec_kind":"cron","spec":"0 9 * * *","prompt":"x"`
	rec := doJSON(api.create, mv, "POST", `{`+base+`,"route":"anthropic-subscription"}`, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "route_forbidden") {
		t.Fatalf("a forbidden route saved: %d %s", rec.Code, rec.Body.String())
	}
	for _, b := range []string{`{` + base + `,"route":"anthropic-api"}`, `{` + base + `}`} {
		if rec := doJSON(api.create, mv, "POST", b, ""); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", b, rec.Code, rec.Body.String())
		}
	}
	// An edit onto a forbidden route is refused too.
	rec = doJSON(api.create, mv, "POST", `{`+base+`}`, "")
	id := extractScheduleID(t, rec.Body.String())
	if rec := doJSON(api.update, mv, "PATCH", `{"route":"anthropic-subscription"}`, id); rec.Code != http.StatusForbidden {
		t.Errorf("an edit onto a forbidden route: %d %s", rec.Code, rec.Body.String())
	}
}

func extractScheduleID(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `"id":"`)
	if i < 0 {
		t.Fatalf("no id in %s", body)
	}
	rest := body[i+len(`"id":"`):]
	return rest[:strings.Index(rest, `"`)]
}

// A fire after a tightening refuses the run before anything is woken; the failed run is what
// notifies the member (scheduler notifyOutcome).
func TestFireRefusesARouteThePolicyForbids(t *testing.T) {
	api, ctx, _ := newSchedAPITest(t)
	tn, err := api.mgr.store.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restrictTenant(t, api.mgr.store, tn.ID)
	_, _, err = (&wakeFirer{mgr: api.mgr}).fire(ctx, store.Schedule{ID: "s", SessionMode: "new", AgentKind: "claude",
		TenantID: tn.ID, MembershipID: "m-none", Route: "anthropic-subscription"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "route_forbidden") {
		t.Errorf("fire = %v, want a route_forbidden refusal", err)
	}
	var retry scheduleRetryable
	if errors.As(err, &retry) {
		t.Error("a policy refusal must not be retried")
	}
}

// A fire that meets a ceiling recorded by a newer Control Plane (a rollover) is retried on the
// same slot, not recorded as a failed run that advances the ledger.
func TestFireRetriesWhileTheCeilingBelongsToANewerControlPlane(t *testing.T) {
	api, ctx, _ := newSchedAPITest(t)
	tn, err := api.mgr.store.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := api.mgr.store.PutCeiling(ctx, `{"classes":["provided"]}`, "operator", store.CeilingWriterKey(1<<62, "z"), nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_MODEL_ROUTE_CEILING", "")
	if err := routepolicysrv.LoadCeiling(); err != nil {
		t.Fatal(err)
	}
	_, _, err = (&wakeFirer{mgr: api.mgr}).fire(ctx, store.Schedule{ID: "s", SessionMode: "new", AgentKind: "claude",
		TenantID: tn.ID, MembershipID: "m-none", Route: "anthropic-subscription"}, time.Now())
	var retry scheduleRetryable
	if !errors.As(err, &retry) {
		t.Errorf("fire = %v, want a retryable error", err)
	}
}
