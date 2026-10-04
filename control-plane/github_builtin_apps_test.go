package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Issue #1667: a tenant's GitHub button talks to a built-in app or its own, and the kind
// of its own app (OAuth App / GitHub App) is detected rather than asked for.

// stubGitHubProbe replaces the save-time probe so no test reaches github.com.
func stubGitHubProbe(t *testing.T, f func(string) (string, string, *ghProbeError)) {
	t.Helper()
	prev := ghProbeAppType
	ghProbeAppType = f
	t.Cleanup(func() { ghProbeAppType = prev })
}

// withBuiltinGitHubApps compiles in both built-in apps for the duration of a test.
func withBuiltinGitHubApps(t *testing.T) {
	t.Helper()
	o, a, s := builtinGitHubOAuthClientID, builtinGitHubAppClientID, builtinGitHubAppSlug
	builtinGitHubOAuthClientID, builtinGitHubAppClientID, builtinGitHubAppSlug = "Ov23builtin", "Iv23builtin", "af-builtin"
	t.Cleanup(func() { builtinGitHubOAuthClientID, builtinGitHubAppClientID, builtinGitHubAppSlug = o, a, s })
}

// fakeDeviceCodeEndpoint answers the way github.com was measured to answer a device code
// request carrying a scope that does not exist.
func fakeDeviceCodeEndpoint(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("scope") != ghProbeScope {
			t.Errorf("probe sent scope %q, want the nonexistent %q", r.Form.Get("scope"), ghProbeScope)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("client_id") {
		case "oauth-app":
			_, _ = w.Write([]byte(`{"error":"invalid_scope","error_description":"The scopes requested are invalid"}`))
		case "github-app":
			_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_uri":"https://github.com/login/device","expires_in":899,"interval":5}`))
		case "no-device-flow":
			_, _ = w.Write([]byte(`{"error":"device_flow_disabled"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	prev := ghDeviceCodeURL
	ghDeviceCodeURL = srv.URL
	t.Cleanup(func() { ghDeviceCodeURL = prev })
}

func TestGitHubProbeTellsTheAppKindApart(t *testing.T) {
	fakeDeviceCodeEndpoint(t)
	for _, tc := range []struct {
		id, wantType, wantBy, wantErr string
	}{
		{"oauth-app", ghTypeOAuthApp, ghTypeByProbe, ""},
		{"github-app", ghTypeGitHubApp, ghTypeByProbe, ""},
		{"no-device-flow", "", "", "device_flow_disabled"},
		{"nobody", "", "", "github_client_not_found"},
	} {
		typ, by, perr := ghProbeAppType(tc.id)
		gotErr := ""
		if perr != nil {
			gotErr = perr.code
		}
		if typ != tc.wantType || by != tc.wantBy || gotErr != tc.wantErr {
			t.Errorf("%s: got (%q,%q,%q), want (%q,%q,%q)", tc.id, typ, by, gotErr, tc.wantType, tc.wantBy, tc.wantErr)
		}
	}
}

// A CP whose outbound traffic is restricted must still be able to save; it falls back to
// the client_id's shape and says the answer is a guess.
func TestGitHubProbeFallsBackToTheClientIDShapeWhenGitHubIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens any more
	prev := ghDeviceCodeURL
	ghDeviceCodeURL = srv.URL
	t.Cleanup(func() { ghDeviceCodeURL = prev })
	for id, want := range map[string]string{
		"Iv23liAbc":            ghTypeGitHubApp,
		"Iv1.0123456789abcdef": ghTypeGitHubApp,
		"Ov23liAbc":            ghTypeOAuthApp,
		"178c6fc778ccc68e1d6a": ghTypeOAuthApp,
		"something-else":       "",
	} {
		typ, by, perr := ghProbeAppType(id)
		if perr != nil || typ != want || (want != "" && by != ghTypeByPrefix) {
			t.Errorf("%s: got (%q,%q,%v), want %q by prefix", id, typ, by, perr, want)
		}
	}
}

func TestGitHubSaveRecordsTheDetectedKindAndRefusesWhatCannotWork(t *testing.T) {
	ctx := context.Background()
	st, _, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	fakeDeviceCodeEndpoint(t)
	stubGitHubProbe(t, ghProbeAppTypeReal)

	put := func(body string) *httptest.ResponseRecorder {
		return gitOAuthCall(api, http.MethodPut, "sub", "github", "admin@sub.co.jp", body)
	}
	if w := put(`{"client_id":"github-app","install_url":"https://github.com/apps/acme-af/installations/new"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	row, _, _ := st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if row.Source != ghSourceCustom || row.AppType != ghTypeGitHubApp || row.AppTypeBy != ghTypeByProbe ||
		row.InstallURL != "https://github.com/apps/acme-af" {
		t.Fatalf("row = %+v", row)
	}
	// An OAuth App has nothing to install; the URL left over from the GitHub App goes.
	if w := put(`{"client_id":"oauth-app","install_url":"https://github.com/apps/acme-af"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	row, _, _ = st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if row.AppType != ghTypeOAuthApp || row.InstallURL != "" {
		t.Fatalf("row = %+v", row)
	}
	for body, code := range map[string]string{
		`{"client_id":"nobody"}`:         "github_client_not_found",
		`{"client_id":"no-device-flow"}`: "device_flow_disabled",
		`{"client_id":"github-app","install_url":"https://evil.example/apps/x"}`: "bad_install_url",
		`{"source":"whatever"}`:      "bad_source",
		`{"source":"builtin_oauth"}`: "builtin_unavailable",
	} {
		if w := put(body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), code) {
			t.Errorf("%s: want %s, got %d %s", body, code, w.Code, w.Body.String())
		}
	}
	// A refused save leaves the previous app in place.
	row, _, _ = st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if row.ClientID != "oauth-app" {
		t.Fatalf("a refused save changed the row: %+v", row)
	}
}

// ghProbeAppTypeReal is the production probe, captured before any test stubs it.
var ghProbeAppTypeReal = ghProbeAppType

func TestGitHubBuiltinSourcesResolveFromTheBinary(t *testing.T) {
	ctx := context.Background()
	st, mgr, api := gitOAuthEnv(t)
	withBuiltinGitHubApps(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")

	if w := gitOAuthCall(api, http.MethodPut, "sub", "github", "admin@sub.co.jp",
		`{"source":"builtin_app","client_id":"ignored"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	row, _, _ := st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if row.ClientID != "" || row.Source != ghSourceBuiltinApp {
		t.Fatalf("a built-in row must not copy the client_id: %+v", row)
	}
	app, ok, err := mgr.githubOAuthApp(ctx, tn.ID)
	if err != nil || !ok || app.ClientID != "Iv23builtin" || app.Type != ghTypeGitHubApp ||
		app.InstallURL != "https://github.com/apps/af-builtin/installations/new" {
		t.Fatalf("resolve = %+v ok=%v err=%v", app, ok, err)
	}

	// The operator's switch takes the built-in apps away even from a tenant that chose one.
	mgr.githubBuiltinOff = true
	if _, ok, _ := mgr.githubOAuthApp(ctx, tn.ID); ok {
		t.Fatal("a built-in app resolved with AF_GITHUB_BUILTIN_APPS=off")
	}
	w := gitOAuthCall(api, http.MethodGet, "sub", "", "admin@sub.co.jp", "")
	var listed struct {
		Providers []gitOAuthBody `json:"providers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	for _, p := range listed.Providers {
		if p.Provider == gitOAuthGitHub &&
			(p.Source != ghSourceBuiltinApp || p.Builtin == nil || !p.Builtin.OffByOperator || p.Builtin.GitHubApp) {
			t.Fatalf("github card must show the choice and why it does not work: %+v %+v", p, p.Builtin)
		}
	}
	if w := gitOAuthCall(api, http.MethodPut, "sub", "github", "admin@sub.co.jp",
		`{"source":"builtin_oauth"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("saving a switched-off built-in app: want 400, got %d %s", w.Code, w.Body.String())
	}
}

// The deployment's default tenant starts on the built-in OAuth App; every other tenant,
// and a default tenant that chose "none", has no GitHub button until somebody picks one.
func TestGitHubDefaultIsTheBuiltinOAuthAppForTheDefaultTenantOnly(t *testing.T) {
	ctx := context.Background()
	st, mgr, _ := gitOAuthEnv(t)
	dt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mgr.defaultTenantID = dt.ID
	other := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")

	if _, ok, _ := mgr.githubOAuthApp(ctx, dt.ID); ok {
		t.Fatal("a build without built-in apps must not offer a default")
	}
	withBuiltinGitHubApps(t)
	app, ok, _ := mgr.githubOAuthApp(ctx, dt.ID)
	if !ok || !app.Default || app.Source != ghSourceBuiltinOAuth || app.ClientID != "Ov23builtin" {
		t.Fatalf("default tenant = %+v ok=%v", app, ok)
	}
	if app, ok, _ := mgr.githubOAuthApp(ctx, other.ID); ok || app.Source != ghSourceNone {
		t.Fatalf("another tenant = %+v ok=%v", app, ok)
	}

	if err := st.PutTenantGitOAuth(ctx, store.TenantGitOAuth{
		ID: store.NewID(), TenantID: dt.ID, Provider: gitOAuthGitHub, Source: ghSourceNone,
		CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := mgr.githubOAuthApp(ctx, dt.ID); ok {
		t.Fatal(`"none" must override the default`)
	}
}

func TestAfterGithubGrantCorrectsTheTypeAndReportsWhatTheMemberMustDo(t *testing.T) {
	ctx := context.Background()
	st, mgr, _ := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	// Saved while GitHub was unreachable: guessed GitHub App from the prefix.
	if err := st.PutTenantGitOAuth(ctx, store.TenantGitOAuth{
		ID: store.NewID(), TenantID: tn.ID, Provider: gitOAuthGitHub, ClientID: "Iv23guess",
		Source: ghSourceCustom, AppType: ghTypeGitHubApp, AppTypeBy: ghTypeByPrefix,
		CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}); err != nil {
		t.Fatal(err)
	}
	app, _, _ := mgr.githubOAuthApp(ctx, tn.ID)
	flow := &ghDeviceFlow{tenantID: tn.ID, clientID: "Iv23guess", app: app}

	out := mgr.afterGithubGrant(ctx, flow, "gho_abc", false)
	row, _, _ := st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if row.AppType != ghTypeOAuthApp || row.AppTypeBy != ghTypeByToken {
		t.Fatalf("the token's prefix must correct the guess: %+v", row)
	}
	if out["not_installed"] != nil || out["token_expires"] != nil {
		t.Fatalf("an OAuth App token has nothing to warn about: %v", out)
	}

	// A GitHub App installed nowhere, with token expiration on.
	prev := ghHasInstallation
	ghHasInstallation = func(string) (bool, bool) { return false, true }
	t.Cleanup(func() { ghHasInstallation = prev })
	flow.app = githubApp{Source: ghSourceCustom, ClientID: "Iv23guess", Type: ghTypeGitHubApp,
		TypeBy: ghTypeByToken, InstallURL: "https://github.com/apps/acme/installations/new"}
	out = mgr.afterGithubGrant(ctx, flow, "ghu_abc", true)
	if out["not_installed"] != true || out["token_expires"] != true ||
		out["install_url"] != "https://github.com/apps/acme/installations/new" {
		t.Fatalf("got %v", out)
	}
	// The row is compared as it is now, not as the flow saw it at start.
	row, _, _ = st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if row.AppType != ghTypeGitHubApp {
		t.Fatalf("a ghu_ token must record github_app: %+v", row)
	}

	audits, _ := st.ListAuditByTenant(ctx, tn.ID, 10)
	found := false
	for _, a := range audits {
		if a.Action == "git_oauth.github_connect" && strings.Contains(a.Detail, "client_id=Iv23guess") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no connect audit row: %+v", audits)
	}
}

func TestSetTenantGitOAuthAppTypeOnlyTouchesTheSameClientID(t *testing.T) {
	ctx := context.Background()
	st, _, _ := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	if err := st.PutTenantGitOAuth(ctx, store.TenantGitOAuth{
		ID: store.NewID(), TenantID: tn.ID, Provider: gitOAuthGitHub, ClientID: "new-app",
		Source: ghSourceCustom, AppType: ghTypeOAuthApp, AppTypeBy: ghTypeByProbe,
		CreatedAt: store.NowTS(), UpdatedAt: store.NowTS(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTenantGitOAuthAppType(ctx, tn.ID, gitOAuthGitHub, "old-app", ghTypeGitHubApp, ghTypeByToken); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.GetTenantGitOAuth(ctx, tn.ID, gitOAuthGitHub)
	if row.AppType != ghTypeOAuthApp {
		t.Fatalf("a token from the previous app rewrote the new app's type: %+v", row)
	}
}

func TestGitHubAvailabilityCarriesTheInstallLinkForAGitHubApp(t *testing.T) {
	ctx := context.Background()
	st, _, api := gitOAuthEnv(t)
	tn := seedGitOAuthTenant(t, st, "sub", "admin@sub.co.jp")
	member, _ := st.UpsertIdentity(ctx, "user@sub.co.jp", "user-sub-co-jp", "")
	if _, err := st.EnsureMembership(ctx, member.ID, tn.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if w := gitOAuthCall(api, http.MethodPut, "sub", "github", "admin@sub.co.jp",
		`{"client_id":"Iv23app","install_url":"https://github.com/apps/acme"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/api/git-oauth", nil)
	r.Header.Set("X-Forwarded-Email", "user@sub.co.jp")
	r.Header.Set("X-AF-Tenant", "sub")
	w := httptest.NewRecorder()
	api.withMembership(api.availability)(w, r)
	var out map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	gh := out[gitOAuthGitHub]
	if gh["configured"] != true || gh["app_type"] != ghTypeGitHubApp ||
		gh["install_url"] != "https://github.com/apps/acme/installations/new" {
		t.Fatalf("availability = %v", out)
	}
}

func TestGitHubHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/apps/acme-af":                    "https://github.com/apps/acme-af",
		"https://github.com/apps/acme-af/":                   "https://github.com/apps/acme-af",
		" https://github.com/apps/acme-af/installations/new": "https://github.com/apps/acme-af",
		"http://github.com/apps/acme":                        "",
		"https://github.com/apps/acme/settings":              "",
		"https://github.com.evil.example/apps/acme":          "",
		"javascript:alert(1)":                                "",
	} {
		got, _ := normalizeGitHubAppURL(in)
		if got != want {
			t.Errorf("normalizeGitHubAppURL(%q) = %q, want %q", in, got, want)
		}
	}
	for tok, want := range map[string]string{"gho_x": ghTypeOAuthApp, "ghu_x": ghTypeGitHubApp, "ghp_x": "", "": ""} {
		if got := ghAppTypeFromToken(tok); got != want {
			t.Errorf("ghAppTypeFromToken(%q) = %q, want %q", tok, got, want)
		}
	}
	for v, want := range map[string]bool{"off": true, "OFF": true, "false": true, "0": true, "": false, "on": false, "offf": false} {
		if got := githubBuiltinOffFromEnv(v); got != want {
			t.Errorf("githubBuiltinOffFromEnv(%q) = %v, want %v", v, got, want)
		}
	}
}
