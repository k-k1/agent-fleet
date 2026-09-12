package main

// engine_issue_token.go — the LENDING side of ADR 0079: a super_admin of the deployment that
// owns the engines mints the `afei_…` issuing token a borrowing deployment puts in
// `AF_REMOTE_ENGINE_TOKEN`, and reads it off the screen.
//
// Why this route exists at all. Decision 3 makes the credential a purpose-made membership's
// issuing token, and the review (R9) then found that the procedure does not close: the token
// is derived deterministically (engine_token.go:47) and injected into that membership's own
// workspace container and nowhere else (workspace_lifecycle.go:401). No admin route starts
// another member's workspace and none opens a session inside one — the admin surface is stop,
// clean-home, destroy and read-only lists — so the only way to READ it was to sign in AS that
// membership, which a `user_key`-only invite cannot do because no IdP will authenticate it.
// That is open question 6. This route is the way out of it.
//
// 🔴 What it must never become is a way to hand out a PERSON's issuing token. The value is
// deterministic, so there is no such thing as revoking one copy of it: the only lever is
// rotating the signing master, which is shared with the git, memo and schedule tokens
// (git_http.go:88), so revoking one borrower would log out the whole fleet. The answer is not
// a check the CP can make — "is this membership a person?" has no truthful column — so it is
// carried in the ANSWER: what the token is, what it opens, and that revoking it means removing
// the membership. `has_workspace` is the one honest signal available, and it is reported
// rather than enforced.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineIssueTokenView is the answer. It carries the credential once — there is no GET that
// returns it again, and re-POSTing is how it is read a second time (the mint is deterministic,
// so that is the same value, not a new one).
type engineIssueTokenView struct {
	Token        string `json:"token"`
	MembershipID string `json:"membership_id"`
	TenantSlug   string `json:"tenant_slug"`
	UserKey      string `json:"user_key"`
	Role         string `json:"role"`
	// EnvVar is where the borrowing deployment puts it (ADR 0079 decision 2), so that the
	// screen does not have to know the borrower's variable names.
	EnvVar string `json:"env_var"`
	// Opens is everything this token can reach on this deployment. It is short on purpose:
	// no git, no MCP, no memos, no API.
	Opens []string `json:"opens"`
	// Deterministic is the machine-readable half of the warning below: the reason there is no
	// "revoke this token" button anywhere is that there is no such operation.
	Deterministic bool `json:"deterministic"`
	// HasWorkspace is true when this membership has ever started a workspace — the closest
	// thing to evidence that it belongs to a person rather than to a borrowing deployment.
	HasWorkspace bool   `json:"has_workspace"`
	Revoke       string `json:"revoke"`
	Warning      string `json:"warning"`
}

// engineIssueTokenOpens is what the credential reaches, and the list is the whole point of
// decision 3: an issuing token buys a session token for one engine and reads the catalogue.
var engineIssueTokenOpens = []string{"POST /internal/engine/token", "GET /internal/engine/catalog"}

const engineIssueTokenWarning = "This is the membership's own engine credential, not a per-borrower secret. " +
	"It is derived from this deployment's signing master, so every mint returns the same value and no single copy can be invalidated. " +
	"Issue it only for a membership that is used for nothing else, and treat it as the borrowing deployment's password."

// engineIssueTokenPersonWarning is appended when the membership already has a workspace.
const engineIssueTokenPersonWarning = " This membership already has a workspace, which is what a person's membership looks like — " +
	"lending a person's issuing token is what ADR 0079 decision 3 forbids, because revoking it would mean rotating the signing master."

// postIssueToken (POST /api/admin/engines/issue-token) mints and shows the issuing token of the
// membership named by {tenant_slug, user_key}.
//
// POST rather than GET although it reads: a credential in a URL is a credential in the browser
// history, in the proxy log and in the CP's own access log.
func (a engineAdminAPI) postIssueToken(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	var body struct {
		TenantSlug string `json:"tenant_slug"`
		UserKey    string `json:"user_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	slug, key := strings.TrimSpace(body.TenantSlug), strings.TrimSpace(body.UserKey)
	if slug == "" || key == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "tenant_slug and user_key are required"})
		return
	}
	mem, aerr := a.membershipFor(r.Context(), slug, key)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// The same predicate the gateway applies on every single request (liveMembership,
	// engine_gateway.go:365). Minting for a membership it would refuse hands the operator a
	// string that answers 401 and no way to tell that from a typo in the URL.
	mv, ok, err := a.mgr.store.GetMembershipByID(r.Context(), mem.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusConflict, errCodeMembershipInactive,
			"this membership is not active — the engine gateway refuses its token, so there is nothing to hand out. Restore the membership first."})
		return
	}
	// Reported, never enforced: see the prohibition at the top of the file.
	_, hasWS, err := a.mgr.store.GetWorkspaceByMembership(r.Context(), mem.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	warning := engineIssueTokenWarning
	if hasWS {
		warning += engineIssueTokenPersonWarning
	}
	view := engineIssueTokenView{
		Token:         mintEngineIssueToken(a.issuingSignKey(), mem.ID),
		MembershipID:  mem.ID,
		TenantSlug:    mv.TenantSlug,
		UserKey:       key,
		Role:          mv.Role,
		EnvVar:        "AF_REMOTE_ENGINE_TOKEN",
		Opens:         engineIssueTokenOpens,
		Deterministic: true,
		HasWorkspace:  hasWS,
		Revoke: "Remove the membership " + mv.TenantSlug + "/" + key + ": it is refused on the next request. " +
			"There is no way to invalidate this one token on its own — the only alternative is rotating the signing master, " +
			"which also invalidates every git, memo and schedule token in this deployment.",
		Warning: warning,
	}
	// Showing a credential is an act somebody has to be able to answer for later. The token
	// itself is in neither the ledger nor the log — only whose it was.
	a.audit(r.Context(), ident, "engine.issue_token", mv.TenantSlug+"/"+key+" "+mem.ID)
	log.Printf("engines: issuing token shown to a super_admin for membership %s (%s/%s)", mem.ID, mv.TenantSlug, key)
	// The BODY of this response is a long-lived credential, which is not true of any other engine
	// route. A POST answer is not normally cached, but "not normally" is the wrong standard for a
	// value that opens another deployment's engines until somebody deletes a membership — the same
	// reason oauth_link.go sets it on the page that carries a link code.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, view)
}

// membershipFor resolves {tenant_slug, user_key} the way adminAPI.resolveMember does — tenant
// by slug, identity by user key, membership by the pair. It is written out here rather than
// borrowed because engineAdminAPI embeds memberAuth, not adminAPI.
//
// GetIdentityByUserKey, NOT UpsertIdentity: the admin lifecycle routes create the identity as a
// side effect of being asked about it, which is harmless for "stop this workspace" and wrong
// here — a typo in a user key must be a 404, not a new identity with a credential minted for it.
func (a engineAdminAPI) membershipFor(ctx context.Context, slug, key string) (store.Membership, *apiError) {
	t, ok, err := a.mgr.store.GetTenantBySlug(ctx, slug)
	if err != nil {
		return store.Membership{}, internalErr(err)
	}
	if !ok {
		return store.Membership{}, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"}
	}
	ident, ok, err := a.mgr.store.GetIdentityByUserKey(ctx, key)
	if err != nil {
		return store.Membership{}, internalErr(err)
	}
	if !ok {
		return store.Membership{}, &apiError{http.StatusNotFound, "no_membership", "not a member"}
	}
	mem, ok, err := a.mgr.store.GetMembership(ctx, ident.ID, t.ID)
	if err != nil {
		return store.Membership{}, internalErr(err)
	}
	if !ok {
		return store.Membership{}, &apiError{http.StatusNotFound, "no_membership", "not a member"}
	}
	return mem, nil
}

// issuingSignKey returns the key the gateway verifies with. The registry holds it (engines.go),
// and it is derived there from the same master — so the fallback is the identical value and not
// a guess. It matters only for a CP whose registry was built without a manager, which is a test.
func (a engineAdminAPI) issuingSignKey() []byte {
	if a.reg != nil && len(a.reg.signKey) > 0 {
		return a.reg.signKey
	}
	return engineSignKey(a.mgr.tokenSignMaster())
}
