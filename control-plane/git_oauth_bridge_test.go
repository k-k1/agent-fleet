package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// docs/71 §71.8 + ADR0052 決定 7. The bridge exists so the tenant's client secret stops
// being copied into every member's workspace, and these pin the three things that would
// quietly undo that:
//
//   - the app is resolved from the TOKEN's tenant, never from the request
//   - the response carries the tokens and NOT the app's key/secret
//   - a revoked refresh token is reported as permanent, so the Agent stops asking

// bridgeEnv builds a manager with one tenant, one member, and that tenant's Bitbucket
// app registered.
func bridgeEnv(t *testing.T) (*sqlStore, *manager, MembershipView) {
	t.Helper()
	ctx := context.Background()
	st, mgr, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	if w := gitOAuthCall(api, http.MethodPut, "sub", "bitbucket", "admin@sub.co.jp",
		`{"client_id":"tenant-key","client_secret":"tenant-secret"}`); w.Code != http.StatusOK {
		t.Fatalf("save app: %d %s", w.Code, w.Body.String())
	}
	member, _ := st.UpsertIdentity(ctx, "user@sub.co.jp", "user-sub-co-jp", "")
	mem, err := st.EnsureMembership(ctx, member.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	mv, ok, err := st.GetMembershipByID(ctx, mem.ID)
	if err != nil || !ok {
		t.Fatalf("membership view: (%v,%v)", ok, err)
	}
	return st, mgr, mv
}

// bridgeCall posts a refresh through the real handler + token gate.
func bridgeCall(mgr *manager, token, body string) *httptest.ResponseRecorder {
	api := newGitOAuthBridgeAPI(mgr)
	r := httptest.NewRequest(http.MethodPost, "/internal/git-oauth/bitbucket/refresh", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	api.withGitOAuthToken(api.refreshBitbucket)(w, r)
	return w
}

// ★ The whole point in one test: the Agent sends only a refresh token, the CP adds the
// tenant's secret, and what comes back is a token — never the app credential the bridge
// exists to keep out of the container.
func TestGitOAuthBridgeRefreshesWithTheTenantsSecretAndReturnsNoSecret(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	_ = st

	var gotUser, gotPass, gotGrant, gotRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		_ = r.ParseForm()
		gotGrant, gotRefresh = r.PostForm.Get("grant_type"), r.PostForm.Get("refresh_token")
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"rot","expires_in":7200}`))
	}))
	defer srv.Close()
	old := bbTokenURL
	bbTokenURL = srv.URL
	defer func() { bbTokenURL = old }()

	tok := mintGitOAuthToken(gitOAuthSignKey(mgr.tokenSignMaster()), mv.MembershipID)
	w := bridgeCall(mgr, tok, `{"refresh_token":"member-refresh"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", w.Code, w.Body.String())
	}
	if gotUser != "tenant-key" || gotPass != "tenant-secret" {
		t.Fatalf("the grant must be authenticated with the TENANT's app: user=%q pass set=%v", gotUser, gotPass != "")
	}
	if gotGrant != "refresh_token" || gotRefresh != "member-refresh" {
		t.Fatalf("grant=%q refresh=%q", gotGrant, gotRefresh)
	}
	body := w.Body.String()
	for _, leaked := range []string{"tenant-secret", "tenant-key"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("the response leaked the app credential the bridge exists to withhold: %s", body)
		}
	}
	var out bbRefreshToken
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.AccessToken != "fresh" || out.RefreshToken != "rot" || out.ExpiresIn != 7200 {
		t.Fatalf("payload: %+v", out)
	}
}

// ★ The tenant comes from the token, never the request — otherwise one member could
// refresh against another tenant's OAuth app.
func TestGitOAuthBridgeRejectsAForeignOrAbsentToken(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	good := mintGitOAuthToken(gitOAuthSignKey(mgr.tokenSignMaster()), mv.MembershipID)

	for _, tc := range []struct{ name, token string }{
		{"absent", ""},
		{"garbage", "afo_zzz.zzz"},
		// A token minted with a DIFFERENT sign key: this is what a memo/schedule/MCP
		// token would look like, and it must not open the refresh bridge.
		{"other bridge's key", mintMCPToken(mcpSignKey(mgr.tokenSignMaster()), mv.MembershipID)},
		{"tampered membership", strings.Replace(good, "afo_", "afo_x", 1)},
	} {
		if w := bridgeCall(mgr, tc.token, `{"refresh_token":"x"}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401 got %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

// The tenant removed the app after this member connected: their token cannot be
// refreshed, and saying so is the difference between a diagnosable failure and git
// suddenly asking for a password.
func TestGitOAuthBridgeSaysWhenTheTenantHasNoAppAnyMore(t *testing.T) {
	st, mgr, mv := bridgeEnv(t)
	if err := st.DeleteTenantGitOAuth(context.Background(), mv.TenantID, gitOAuthBitbucket); err != nil {
		t.Fatalf("delete: %v", err)
	}
	tok := mintGitOAuthToken(gitOAuthSignKey(mgr.tokenSignMaster()), mv.MembershipID)
	w := bridgeCall(mgr, tok, `{"refresh_token":"member-refresh"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not_configured") {
		t.Fatalf("want not_configured, got %d %s", w.Code, w.Body.String())
	}
}

// ★ Permanent vs transient has to survive the move into the CP. A revoked refresh token
// (4xx) must come back as invalid_grant and NOT be retried — the Agent uses that to stop
// asking and prompt for a reconnect, instead of hammering the grant on every git command.
func TestGitOAuthBridgeSeparatesARevokedGrantFromATransientFailure(t *testing.T) {
	_, mgr, mv := bridgeEnv(t)
	tok := mintGitOAuthToken(gitOAuthSignKey(mgr.tokenSignMaster()), mv.MembershipID)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	old := bbTokenURL
	bbTokenURL = srv.URL
	defer func() { bbTokenURL = old }()

	w := bridgeCall(mgr, tok, `{"refresh_token":"revoked"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("want invalid_grant, got %d %s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("a permanent refusal must not be retried, got %d calls", calls)
	}

	// A 5xx is the opposite: transient, so it IS retried before giving up.
	calls = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv2.Close()
	bbTokenURL = srv2.URL
	w = bridgeCall(mgr, tok, `{"refresh_token":"x"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d %s", w.Code, w.Body.String())
	}
	if calls < 2 {
		t.Fatalf("a transient failure must be retried, got %d calls", calls)
	}
}

// ★ The bridge is useless if the token never reaches the container, and it would fail
// SILENTLY: the Agent would just keep using the legacy client secret (or, on a fresh
// connect, stop refreshing after ~2h). Pin the injection next to the other bridges.
func TestWorkspaceEnvCarriesTheGitOAuthBridgeToken(t *testing.T) {
	ctx := context.Background()
	st, mgr, mv := bridgeEnv(t)
	mgr.dataRoot = t.TempDir()
	mgr.publicBaseURL = "https://af.example"

	ws := Workspace{ID: "ws1", TenantID: mv.TenantID, MembershipID: mv.MembershipID}
	env := mgr.workspaceExtraEnv(ctx, ws)

	var token string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "AF_GIT_OAUTH_TOKEN="); ok {
			token = v
		}
		// The tenant's client secret must never be an env var — that would put it back
		// in the container through a different door.
		if strings.Contains(kv, "tenant-secret") {
			t.Fatalf("the tenant's client secret leaked into the container env: %q", kv)
		}
	}
	if token == "" {
		t.Fatalf("AF_GIT_OAUTH_TOKEN was not injected: %v", env)
	}
	mid, ok := verifyGitOAuthToken(gitOAuthSignKey(mgr.tokenSignMaster()), token)
	if !ok || mid != mv.MembershipID {
		t.Fatalf("injected token does not verify to this membership: (%q,%v)", mid, ok)
	}
	_ = st
}
