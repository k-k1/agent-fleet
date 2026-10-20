package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// Sign-in to a loopback redirect is a supported use of the dev IdP, also while the member's
// workspace is stopped: an app on the member's own machine has nothing else to reach. These
// tests pin that no step of the flow asks whether the workspace is running, so a later
// "refuse clients of stopped workspaces" check turns them red (ADR 0116, 2026-10-18 note).

// stoppableFactory hands out runtimes whose State follows one flag, so a stop moves the
// runtime the way rt.Stop does in workspace_lifecycle.go and not only the database row.
type stoppableFactory struct {
	endpoint string
	stopped  *atomic.Bool
}

type stoppableRuntime struct {
	previewTestRuntime
	stopped *atomic.Bool
}

func (f stoppableFactory) New(ws runtime.Workspace, _ runtime.SecretKeys, _ []string) runtime.Runtime {
	return stoppableRuntime{previewTestRuntime{endpoint: f.endpoint, token: "tok-" + ws.ID}, f.stopped}
}

func (r stoppableRuntime) Stop(context.Context) error { r.stopped.Store(true); return nil }
func (r stoppableRuntime) State(context.Context) string {
	if r.stopped.Load() {
		return "stopped"
	}
	return "running"
}

// stopWorkspace does what a stop does: rt.Stop, then the state row (which also clears the
// preview slug). It asserts the runtime, the row and the slug all say stopped, so a check on
// any of the three would turn these tests red. d.slug keeps the old value to build the stale
// preview host.
func (d *devIdPTestEnv) stopWorkspace(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	stopped := &atomic.Bool{}
	d.mgr.rtFactory = stoppableFactory{endpoint: "http://127.0.0.1:1", stopped: stopped}
	rt := d.mgr.runtimeFor(d.ws, noSecretKeys)
	if got := rt.State(ctx); got != "running" {
		t.Fatalf("runtime before the stop: %q", got)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.mgr.store.SetWorkspaceState(ctx, d.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	ws, ok, err := d.mgr.store.GetWorkspaceByMembership(ctx, d.ws.MembershipID)
	if err != nil || !ok || ws.State != "stopped" || ws.PreviewSlug != "" {
		t.Fatalf("workspace is not stopped: %+v ok=%v err=%v", ws, ok, err)
	}
	if got := d.mgr.runtimeFor(ws, noSecretKeys).State(ctx); got != "stopped" {
		t.Fatalf("runtime state = %q, want stopped", got)
	}
}

func loopbackRedirects() []string {
	return []string{"http://localhost:5173/cb", "http://127.0.0.1:8080/cb"}
}

// completeFlow takes a successful authorize/consent redirect through token and userinfo and
// returns the verified ID token claims. It checks where the browser is sent, the ID token's
// signature, issuer, audience and nonce, and that userinfo names the same subject.
func (d *devIdPTestEnv) completeFlow(t *testing.T, rec *httptest.ResponseRecorder, redirect, verifier string, basic bool) map[string]any {
	t.Helper()
	code := codeFrom(t, rec)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	want, _ := url.Parse(redirect)
	if loc.Scheme != want.Scheme || loc.Host != want.Host || loc.Path != want.Path {
		t.Fatalf("redirected to %q, want %q", rec.Header().Get("Location"), redirect)
	}
	if loc.Query().Get("state") != "st-1" || loc.Query().Get("iss") != devIdPTestIssuer {
		t.Errorf("state / iss: %v", loc.Query())
	}
	var tok *httptest.ResponseRecorder
	if basic {
		tok = d.exchange(code, redirect, verifier)
	} else {
		f := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"client_id": {d.clientID}, "client_secret": {d.secret}}
		if verifier != "" {
			f.Set("code_verifier", verifier)
		}
		tok = d.tokenReq(f, "", "")
	}
	if tok.Code != http.StatusOK {
		t.Fatalf("token: %d %s", tok.Code, tok.Body)
	}
	body := idpJSON(t, tok)
	idToken, _ := body["id_token"].(string)
	claims, err := verifyIDToken(idToken, d.jwks(t), devIdPTestIssuer, d.clientID, d.now())
	if err != nil {
		t.Fatalf("ID token: %v", err)
	}
	sub, _ := claims["sub"].(string)
	if sub == "" || claims["nonce"] != "nonce-1" || claims["af_dev"] != true {
		t.Fatalf("claims: %v", claims)
	}
	req := httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPUserinfoPath, nil)
	req.Header.Set("Authorization", "Bearer "+body["access_token"].(string))
	ui := httptest.NewRecorder()
	d.mux.ServeHTTP(ui, req)
	if ui.Code != http.StatusOK {
		t.Fatalf("userinfo: %d %s", ui.Code, ui.Body)
	}
	if got, _ := idpJSON(t, ui)["sub"].(string); got != sub {
		t.Fatalf("userinfo sub = %q, ID token sub = %q", got, sub)
	}
	return claims
}

const devIdPTestIssuer = "https://af.example.com/dev-idp"

func TestDevIdPStoppedWorkspaceOwnerSignsInToLoopback(t *testing.T) {
	for _, redirect := range loopbackRedirects() {
		for _, pkce := range []bool{true, false} {
			for _, basic := range []bool{true, false} {
				d := newDevIdPTestEnv(t)
				d.stopWorkspace(t)
				verifier, challenge := "", ""
				if pkce {
					verifier, challenge = pkcePair()
				}
				rec := d.authorizeAs(t, "preview-user", d.authQuery(redirect, challenge))
				claims := d.completeFlow(t, rec, redirect, verifier, basic)
				if sub, _ := claims["sub"].(string); !strings.HasPrefix(sub, "af-") {
					t.Errorf("owner subject is not a member subject: %v", claims)
				}
			}
		}
	}
}

func TestDevIdPStoppedWorkspaceOwnerSignsInToLoopbackThroughConsent(t *testing.T) {
	for _, redirect := range loopbackRedirects() {
		d := newDevIdPTestEnv(t)
		d.addPersona(t, "admin", "Admin User")
		d.stopWorkspace(t)
		verifier, challenge := pkcePair()
		_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, challenge))
		rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {d.personaID(t, "admin")}}, nil)
		c := d.completeFlow(t, rec, redirect, verifier, true)
		if c["sub"] != "persona:admin" || c["email"] != "admin@example.test" || c["name"] != "Admin User" {
			t.Errorf("persona claims: %v", c)
		}
		// Choosing myself is the owner's own subject, not the persona's.
		_, ticket = d.askAs(t, "preview-user", d.authQuery(redirect, ""))
		rec = d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {"self"}}, nil)
		self := d.completeFlow(t, rec, redirect, "", true)
		if self["sub"] == "persona:admin" || self["sub"] == "" {
			t.Errorf("self claims: %v", self)
		}
	}
}

// The preview host needs the current slug, which a stopped workspace does not have.
func TestDevIdPStoppedWorkspaceRefusesThePreviewHost(t *testing.T) {
	d := newDevIdPTestEnv(t)
	preview := d.ownHost(3000) + "/cb"
	if rec := d.authorizeAs(t, "preview-user", d.authQuery(preview, "")); rec.Code != http.StatusFound {
		t.Fatalf("preview host while running: %d", rec.Code)
	}
	d.stopWorkspace(t)
	if rec := d.authorizeAs(t, "preview-user", d.authQuery(preview, "")); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Errorf("preview host while stopped: %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

// A tenant colleague follows the preview rules, stopped or not: only with tenant share on.
func TestDevIdPStoppedWorkspaceColleagueFollowsTheShareRules(t *testing.T) {
	for _, redirect := range loopbackRedirects() {
		d := newDevIdPTestEnv(t)
		d.addViewer(t, "colleague")
		d.stopWorkspace(t)
		if rec := d.authorizeAs(t, "colleague", d.authQuery(redirect, "")); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("colleague, sharing off, %s: %d loc=%q", redirect, rec.Code, rec.Header().Get("Location"))
		}
		d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
		colleague := d.completeFlow(t, d.authorizeAs(t, "colleague", d.authQuery(redirect, "")), redirect, "", true)
		owner := d.completeFlow(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")), redirect, "", true)
		if colleague["sub"] == owner["sub"] {
			t.Errorf("colleague and owner share a subject: %v", owner["sub"])
		}
		d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = false })
		if rec := d.authorizeAs(t, "colleague", d.authQuery(redirect, "")); rec.Code != http.StatusBadRequest {
			t.Errorf("colleague after sharing was turned off, %s: %d", redirect, rec.Code)
		}
	}
}

// Someone outside the workspace's tenant is refused whatever the workspace's state.
func TestDevIdPStoppedWorkspaceRefusesANonMember(t *testing.T) {
	d := newDevIdPTestEnv(t)
	d.stopWorkspace(t)
	if _, err := d.mgr.store.UpsertIdentity(context.Background(), "", "stranger", ""); err != nil {
		t.Fatal(err)
	}
	for _, redirect := range loopbackRedirects() {
		if rec := d.authorizeAs(t, "stranger", d.authQuery(redirect, "")); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("non-member, %s: %d loc=%q", redirect, rec.Code, rec.Header().Get("Location"))
		}
	}
}
