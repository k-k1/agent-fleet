package main

// The lending side of ADR 0079 (decision 3, phase P1): the far deployment's super_admin mints
// the borrowing credential and reads it off a screen, because R9 found there is no other way to
// read it at all — it exists only inside that membership's own container.
//
// What is pinned here is what makes the route safe rather than what makes it work: the gate is
// deployment-wide and not tenant-wide, an inactive membership is refused before a useless string
// is handed out, the answer says how to revoke it, and the ledger says who asked.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func issueTokenFixture(t *testing.T) (*store.SQL, *manager, *engineRegistry, engineAdminAPI) {
	t.Helper()
	st := p3Store(t)
	mgr := p3Manager(t, st)
	// The same wiring engines.go does at boot (`reg.signKey = engineSignKey(mgr.tokenSignMaster())`),
	// so "the token verifies" below means "the gateway on this deployment accepts it".
	reg := &engineRegistry{byKey: map[string]*engineRuntimeState{}, signKey: engineSignKey(mgr.tokenSignMaster())}
	return st, mgr, reg, engineAdminAPI{memberAuth{mgr}, reg, st}
}

// issueTokenMember creates the purpose-made membership decision 3 describes: an invite naming a
// user_key and no address at all.
func issueTokenMember(t *testing.T, st *store.SQL, slug, key, role string) store.Membership {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, slug, slug)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "", key, "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, role)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	return mem
}

func callIssueToken(a engineAdminAPI, body string) (*httptest.ResponseRecorder, map[string]any) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/admin/engines/issue-token", strings.NewReader(body))
	a.postIssueToken(rec, r, store.Identity{ID: "boss"})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func issueTokenErrCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// The whole point of the route: the string on the screen is one the gateway here will accept,
// and it names the membership it was minted for.
func TestEngineIssueTokenMintsWhatTheGatewayAccepts(t *testing.T) {
	st, _, reg, a := issueTokenFixture(t)
	mem := issueTokenMember(t, st, "acme", "borrow-bot", "member")

	rec, out := callIssueToken(a, `{"tenant_slug":"acme","user_key":"borrow-bot"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue-token = %d %s, want 200", rec.Code, rec.Body.String())
	}
	tok, _ := out["token"].(string)
	if !strings.HasPrefix(tok, "afei_") {
		t.Fatalf("token = %q, want an afei_ issuing token", tok)
	}
	got, ok := verifyEngineIssueToken(reg.signKey, tok)
	if !ok {
		t.Fatal("the minted token does not verify against the registry's signing key — the gateway would 401 it")
	}
	if got != mem.ID {
		t.Fatalf("token carries membership %q, want %q", got, mem.ID)
	}
	if out["membership_id"] != mem.ID || out["tenant_slug"] != "acme" || out["user_key"] != "borrow-bot" {
		t.Errorf("the answer does not name whose token this is: %v", out)
	}
	// It must say where the borrower puts it and what it reaches — the list is short because the
	// credential is narrow, and a screen that does not say so invites reuse.
	if out["env_var"] != "AF_REMOTE_ENGINE_TOKEN" {
		t.Errorf("env_var = %v, want AF_REMOTE_ENGINE_TOKEN", out["env_var"])
	}
	opens, _ := out["opens"].([]any)
	if len(opens) != 2 {
		t.Errorf("opens = %v, want the two /internal/engine routes", out["opens"])
	}
}

// Decision 3's refusal, carried in the answer because no column can carry it: the value is
// deterministic, so revoking it means removing the membership, and that has to be on the screen
// next to the credential rather than in a document nobody opened.
func TestEngineIssueTokenAnswerSaysHowToRevokeIt(t *testing.T) {
	st, _, _, a := issueTokenFixture(t)
	issueTokenMember(t, st, "acme", "borrow-bot", "member")

	rec, out := callIssueToken(a, `{"tenant_slug":"acme","user_key":"borrow-bot"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue-token = %d %s", rec.Code, rec.Body.String())
	}
	if out["deterministic"] != true {
		t.Error("deterministic is not set — the machine-readable half of \"there is no revoking one copy\"")
	}
	revoke, _ := out["revoke"].(string)
	if !strings.Contains(revoke, "acme/borrow-bot") || !strings.Contains(strings.ToLower(revoke), "remove the membership") {
		t.Errorf("revoke = %q, want it to name removing THIS membership", revoke)
	}
	warning, _ := out["warning"].(string)
	if !strings.Contains(warning, "used for nothing else") {
		t.Errorf("warning = %q, want it to say the membership must be used for nothing else", warning)
	}
	if out["has_workspace"] != false {
		t.Errorf("has_workspace = %v, want false for a membership that never started one", out["has_workspace"])
	}
}

// Deterministic by construction (engine_token.go:47). Pinned because it is a property somebody
// will one day read as a bug: asking twice does NOT rotate the credential, and a route that
// looked like it minted a fresh one each time would be lying about revocation.
func TestEngineIssueTokenIsDeterministic(t *testing.T) {
	st, _, _, a := issueTokenFixture(t)
	issueTokenMember(t, st, "acme", "borrow-bot", "member")

	_, first := callIssueToken(a, `{"tenant_slug":"acme","user_key":"borrow-bot"}`)
	_, second := callIssueToken(a, `{"tenant_slug":"acme","user_key":"borrow-bot"}`)
	if first["token"] == "" || first["token"] != second["token"] {
		t.Fatalf("two mints gave %v and %v, want the same value", first["token"], second["token"])
	}
}

// An inactive membership's token is refused by liveMembership on every request
// (engine_gateway.go:365). Handing one out would be handing out a string that answers 401 with
// nothing to distinguish it from a typo.
func TestEngineIssueTokenRefusesAnInactiveMembership(t *testing.T) {
	st, _, _, a := issueTokenFixture(t)
	mem := issueTokenMember(t, st, "acme", "borrow-bot", "member")
	if err := st.SetMembershipStatus(context.Background(), mem.ID, "removed"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	rec, out := callIssueToken(a, `{"tenant_slug":"acme","user_key":"borrow-bot"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("issue-token for a removed membership = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if got := issueTokenErrCode(out); got != "membership_inactive" {
		t.Errorf("code = %q, want membership_inactive", got)
	}
	if strings.Contains(rec.Body.String(), "afei_") {
		t.Error("the refusal carries a token anyway")
	}
	rows, err := st.ListAuditByTenant(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	for _, row := range rows {
		if row.Action == "engine.issue_token" {
			t.Error("a refused mint was written to the ledger as if it had happened")
		}
	}
}

func TestEngineIssueTokenUnknownTargets(t *testing.T) {
	st, _, _, a := issueTokenFixture(t)
	issueTokenMember(t, st, "acme", "borrow-bot", "member")
	if _, err := st.CreateTenant(context.Background(), "other", "other"); err != nil {
		t.Fatalf("tenant: %v", err)
	}

	for _, tc := range []struct{ name, body, code string }{
		{"unknown tenant", `{"tenant_slug":"nope","user_key":"borrow-bot"}`, "no_tenant"},
		{"unknown user_key", `{"tenant_slug":"acme","user_key":"nobody"}`, "no_membership"},
		{"a real key in the wrong tenant", `{"tenant_slug":"other","user_key":"borrow-bot"}`, "no_membership"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, out := callIssueToken(a, tc.body)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("= %d %s, want 404", rec.Code, rec.Body.String())
			}
			if got := issueTokenErrCode(out); got != tc.code {
				t.Errorf("code = %q, want %s", got, tc.code)
			}
		})
	}
	// A typo must not CREATE the identity it failed to find: the admin lifecycle routes upsert,
	// and doing that here would mint a credential for a person who does not exist.
	if _, ok, _ := st.GetIdentityByUserKey(context.Background(), "nobody"); ok {
		t.Error("the unknown user_key was created by looking it up")
	}
}

// Showing a credential is an act somebody has to answer for later — but the ledger records
// whose, not what.
func TestEngineIssueTokenIsAudited(t *testing.T) {
	st, _, _, a := issueTokenFixture(t)
	mem := issueTokenMember(t, st, "acme", "borrow-bot", "member")

	rec, out := callIssueToken(a, `{"tenant_slug":"acme","user_key":"borrow-bot"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue-token = %d %s", rec.Code, rec.Body.String())
	}
	rows, err := st.ListAuditByTenant(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var found bool
	for _, row := range rows {
		if row.Action != "engine.issue_token" {
			continue
		}
		found = true
		if row.ActorID != "boss" {
			t.Errorf("audit actor = %q, want the super_admin who asked", row.ActorID)
		}
		if !strings.Contains(row.Target, mem.ID) || !strings.Contains(row.Target, "acme/borrow-bot") {
			t.Errorf("audit target = %q, want it to name the membership", row.Target)
		}
		if strings.Contains(row.Target+row.Detail, out["token"].(string)) {
			t.Error("the token itself is in the ledger")
		}
	}
	if !found {
		t.Fatal("no engine.issue_token audit row — nobody can answer who was shown the credential")
	}
}

// The gate. This token opens every engine on the DEPLOYMENT, so a tenant-scoped role is not in
// proportion — a tenant_admin must not reach it even for a member of their own tenant. And it is
// never a GET: a credential in a URL is a credential in the browser history and the proxy log.
func TestEngineIssueTokenGateIsSuperAdminAndPostOnly(t *testing.T) {
	st, mgr, reg, _ := issueTokenFixture(t)
	ctx := context.Background()
	mem := issueTokenMember(t, st, "acme", "borrow-bot", "member")
	// A tenant_admin of that same tenant, with a plain deployment identity.
	boss, err := st.UpsertIdentity(ctx, "yamada@acme.co.jp", "yamada-acme-co-jp", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, boss.ID, mem.TenantID, "tenant_admin"); err != nil {
		t.Fatalf("membership: %v", err)
	}
	mux := http.NewServeMux()
	registerEngineAdminRoutes(mux, config{mgr: mgr}, reg)

	post := httptest.NewRequest(http.MethodPost, "/api/admin/engines/issue-token",
		strings.NewReader(`{"tenant_slug":"acme","user_key":"borrow-bot"}`))
	post.Header.Set("X-Forwarded-Email", "yamada@acme.co.jp")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, post)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tenant_admin got %d %s, want 403 — this credential is deployment-wide", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "afei_") {
		t.Fatal("the refusal handed over the token anyway")
	}
	rows, _ := st.ListAuditByTenant(ctx, "", 10)
	for _, row := range rows {
		if row.Action == "engine.issue_token" {
			t.Error("a refused caller reached the handler far enough to be audited")
		}
	}

	get := httptest.NewRequest(http.MethodGet, "/api/admin/engines/issue-token", nil)
	get.Header.Set("X-Forwarded-Email", "yamada@acme.co.jp")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, get)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405 — a credential must not be reachable by a URL alone", rec.Code)
	}
}

// And the same route with a super_admin behind it really does get through the gate, so the 403
// above is the gate talking and not a fixture that cannot reach the handler at all.
func TestEngineIssueTokenGateAdmitsASuperAdmin(t *testing.T) {
	st, mgr, reg, _ := issueTokenFixture(t)
	ctx := context.Background()
	issueTokenMember(t, st, "acme", "borrow-bot", "member")
	if _, err := st.UpsertIdentity(ctx, "boss@acme.co.jp", "boss-acme-co-jp", "super_admin"); err != nil {
		t.Fatalf("identity: %v", err)
	}
	mux := http.NewServeMux()
	registerEngineAdminRoutes(mux, config{mgr: mgr}, reg)

	r := httptest.NewRequest(http.MethodPost, "/api/admin/engines/issue-token",
		strings.NewReader(`{"tenant_slug":"acme","user_key":"borrow-bot"}`))
	r.Header.Set("X-Forwarded-Email", "boss@acme.co.jp")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("super_admin got %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "afei_") {
		t.Fatal("the answer carries no token")
	}
}
