package main

import (
	"log"
	"net/http"
)

// Reasons POST /api/workspace/signin-start answers with when it did not start anything.
// The Console draws the quota one as an explanation on Home; the rest are silent no-ops.
const (
	signinStartTenantOff   = "tenant_off"       // the tenant's switch is off (the default)
	signinStartOptedOut    = "opted_out"        // the member turned it off for themselves
	signinStartOperatorOff = "autostart_off"    // AF_AUTOSTART=0: no on-demand start on this deployment
	signinStartPrefUnknown = "pref_unreadable"  // the member's preference could not be read
	signinStartNotStopped  = "not_stopped"      // running or starting already
	signinStartStateUnsure = "state_unknown"    // the runtime could not say what state it is in
	signinStartQuota       = "quota_workspaces" // the tenant's running-workspace limit is reached
)

// signinStart starts the caller's own stopped workspace when they sign in to the Console.
//
// It is the same on-demand start as session create / resume (ensureWorkspaceStarted: the
// per-workspace lock, the lifecycle lease and the max_workspaces check), with the policy in
// front of it enforced HERE and not in the Console, because the Console is only a client:
//   - the tenant's autostart_on_signin switch must be on;
//   - the member must not have opted out (workspace settings, signinStart=false);
//   - only a workspace the runtime reports `stopped` or `none` is started. `none` is the
//     NORMAL stopped state on the docker and native adapters (Stop removes the container /
//     the pid file) and the Console shows it as "stopped"; refusing it made the feature do
//     nothing there. It is also what an unreadable cluster reads as, which is no worse than
//     session create, whose Start reads the cluster itself and fails rather than launching
//     blind. `starting` and `running` are already converging; anything else ("" from a
//     runtime that could not be asked) is "do not know", and not knowing never costs money.
//
// There is no workspace id in the request: res is the one the session cookie resolves to, so
// a member can only ever start their own workspace.
//
// The answer is 200 with started=false and a reason for every policy no-op, so a refusal is
// not an error in the browser console. It does not wait for the Agent: the Console follows the
// boot on the state it already polls.
func (a workspaceAPI) signinStart(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	no := func(reason string, extra ...string) {
		// One line per refusal so a live "it did not start" names its branch.
		log.Printf("signin-start: ws %s: not started: %s", res.ws.ID, reason)
		out := map[string]any{"started": false, "reason": reason}
		if len(extra) > 0 {
			out["message"] = extra[0]
		}
		writeJSON(w, http.StatusOK, out)
	}
	if !a.autostart {
		no(signinStartOperatorOff)
		return
	}
	t, err := a.mgr.store.GetTenant(ctx, res.ws.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !parseLimits(t.Limits).AutostartOnSignin {
		no(signinStartTenantOff)
		return
	}
	// Read the preference directly instead of through mustSettings: that helper falls back
	// to defaults on a read error, and for an opt-out the default is the wrong way to fail.
	raw, err := a.mgr.store.GetWorkspaceSettings(ctx, res.ws.ID)
	if err != nil {
		no(signinStartPrefUnknown)
		return
	}
	if parseWSSettings(raw).SigninStartOptOut {
		no(signinStartOptedOut)
		return
	}
	switch state := res.rt.State(ctx); state {
	case "stopped", "none":
	case "running", "starting":
		no(signinStartNotStopped)
		return
	default:
		log.Printf("signin-start: ws %s: runtime state %q is not one it knows", res.ws.ID, state)
		no(signinStartStateUnsure)
		return
	}
	if aerr := a.ensureWorkspaceStarted(ctx, res); aerr != nil {
		if aerr.code == "quota_workspaces" {
			no(signinStartQuota, aerr.message)
			return
		}
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true, "state": res.rt.State(ctx)})
}
