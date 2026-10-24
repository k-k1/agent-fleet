package mcpreg

// A tenant's pre-registered client (ADR 0119 decision 9). What is pinned here:
//   - the tenant's client comes first in the registration order, but only at the AS it was
//     registered at;
//   - a confidential client's token and revocation requests go to the CP, carrying no client
//     authentication, and never to the AS from here;
//   - a user row cannot carry one, and a malformed one is refused.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// cpFake stands in for the CP's /internal/mcp-servers/{id}/oauth/{token,revoke}.
type cpFake struct {
	mu      sync.Mutex
	paths   []string
	forms   []url.Values
	auth    []string
	reply   func(n int, form url.Values) (int, string)
	revoked int
}

func newCPFake(t *testing.T) *cpFake {
	c := &cpFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c.mu.Lock()
		n := len(c.forms)
		c.paths = append(c.paths, r.URL.Path)
		c.forms = append(c.forms, r.PostForm)
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		reply := c.reply
		c.mu.Unlock()
		if r.URL.Path == "/internal/mcp-servers/t-hosted/oauth/revoke" {
			c.mu.Lock()
			c.revoked++
			c.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		status, body := 200, `{"access_token":"cp-at-`+string(rune('0'+n))+`","token_type":"Bearer","expires_in":3600,"refresh_token":"cp-rt-`+string(rune('0'+n))+`"}`
		if reply != nil {
			status, body = reply(n, r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AF_CP_BASE_URL", srv.URL)
	t.Setenv("AF_MCP_TOKEN", "afm_test")
	return c
}

// distributeOAuth puts one tenant OAuth definition for the fake into the tenant cache.
func distributeOAuth(t *testing.T, f *asFake, client *secrets.MCPTenantClient) string {
	t.Helper()
	d := ServerDef{
		ID: "t-hosted", Name: "hosted", Origin: OriginTenant, Transport: TransportHTTP, URL: f.url("/mcp"),
		Auth: AuthOAuth, Enabled: true, Targets: Targets{Session: true}, OAuthClient: client,
	}
	kept, dropped := acceptTenant([]ServerDef{d})
	if dropped != 0 {
		t.Fatalf("the tenant definition was dropped: %+v", d)
	}
	if err := writeTenantCache(kept, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tenantCachePath()) })
	return d.ID
}

func TestTenantPublicClientComesFirst(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, map[string]any{"client_id_metadata_document_supported": true})
	id := distributeOAuth(t, f, &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp-public"})
	req := startReq(nil)
	res, q := mustStart(t, id, req)
	if q.Get("client_id") != "corp-public" || res.Registration != OAuthRegTenant {
		t.Fatalf("client_id=%q registration=%q, want the tenant's client", q.Get("client_id"), res.Registration)
	}
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	if f.tokenCount() != 1 || f.tokens[0].Get("client_id") != "corp-public" || f.tokens[0].Get("client_secret") != "" {
		t.Fatalf("token request: %v", f.tokens)
	}
	if len(f.regs) != 0 {
		t.Error("a dynamic registration was made although the tenant registered a client")
	}
	if st, _ := OAuthStatusOf(id); st.Registration != OAuthRegTenant {
		t.Errorf("status registration = %q", st.Registration)
	}
}

func TestTenantClientForAnotherIssuerIsNotUsed(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := distributeOAuth(t, f, &secrets.MCPTenantClient{Issuer: "https://other-as.example.com", ClientID: "corp"})
	res, q := mustStart(t, id, startReq(nil))
	if q.Get("client_id") == "corp" || res.Registration != OAuthRegDCR {
		t.Fatalf("client_id=%q registration=%q, want dynamic registration", q.Get("client_id"), res.Registration)
	}
}

func TestTenantConfidentialClientGoesThroughTheCP(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	cp := newCPFake(t)
	id := distributeOAuth(t, f, &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp", Confidential: true})
	req := startReq(nil)
	_, q := mustStart(t, id, req)
	if q.Get("client_id") != "corp" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "the-code", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	if f.tokenCount() != 0 {
		t.Fatal("the Agent sent a token request to the AS itself")
	}
	if len(cp.forms) != 1 || cp.paths[0] != "/internal/mcp-servers/t-hosted/oauth/token" || cp.auth[0] != "Bearer afm_test" {
		t.Fatalf("CP saw %v %v", cp.paths, cp.auth)
	}
	form := cp.forms[0]
	for k, v := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "redirect_uri": testRedirect, "resource": f.url("/mcp"),
	} {
		if form.Get(k) != v {
			t.Errorf("CP form %s = %q, want %q", k, form.Get(k), v)
		}
	}
	if form.Get("code_verifier") == "" || form.Has("client_id") || form.Has("client_secret") {
		t.Errorf("CP form: %v", form)
	}
	g := storedGrant(t, id)
	if g.TokenAuth != tokenAuthCP || g.ClientSecret != "" || g.AccessToken != "cp-at-0" || g.Registration != OAuthRegTenant {
		t.Fatalf("grant = %+v", g)
	}

	// Refresh goes the same way.
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	tok, err := AccessToken(context.Background(), id)
	if err != nil || tok != "cp-at-1" {
		t.Fatalf("AccessToken = %q, %v", tok, err)
	}
	if cp.forms[1].Get("grant_type") != "refresh_token" || cp.forms[1].Get("refresh_token") != "cp-rt-0" || f.tokenCount() != 0 {
		t.Fatalf("refresh: %v, AS token requests %d", cp.forms[1], f.tokenCount())
	}

	// invalid_grant relayed by the CP ends the grant like the AS's own.
	cp.reply = func(int, url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	oauthNow = func() time.Time { return time.Now().Add(4 * time.Hour) }
	if _, err := AccessToken(context.Background(), id); !errors.Is(err, ErrSignInAgain) {
		t.Fatalf("after invalid_grant: %v", err)
	}
}

func TestDisconnectRevokesAConfidentialGrantThroughTheCP(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	cp := newCPFake(t)
	id := distributeOAuth(t, f, &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp", Confidential: true})
	req := startReq(nil)
	mustStart(t, id, req)
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	res, err := DisconnectOAuth(context.Background(), id)
	if err != nil || !res.Revoked || cp.revoked != 1 || len(f.revokes) != 0 {
		t.Fatalf("disconnect %+v %v: CP revokes %d, AS revokes %d", res, err, cp.revoked, len(f.revokes))
	}
	if last := cp.forms[len(cp.forms)-1]; last.Get("token") != "cp-rt-0" || last.Get("token_type_hint") != "refresh_token" {
		t.Errorf("revoke form: %v", last)
	}
}

func TestValidateTenantClient(t *testing.T) {
	base := ServerDef{Name: "hosted", Origin: OriginTenant, Transport: TransportHTTP, URL: "https://m.example.com/mcp", Auth: AuthOAuth,
		OAuthClient: &secrets.MCPTenantClient{Issuer: "https://as.example.com", ClientID: "corp", Confidential: true}}
	if err := Validate(base); err != nil {
		t.Fatalf("valid: %v", err)
	}
	cases := map[string]struct {
		mod  func(*ServerDef)
		code string
	}{
		"user row":  {func(d *ServerDef) { d.Origin = OriginUser }, CodeOAuthClientUser},
		"not oauth": {func(d *ServerDef) { d.Auth = "" }, CodeOAuthClient},
		"http issuer": {func(d *ServerDef) {
			d.OAuthClient = &secrets.MCPTenantClient{Issuer: "http://as.example.com", ClientID: "c"}
		}, CodeOAuthClient},
		"issuer query": {func(d *ServerDef) {
			d.OAuthClient = &secrets.MCPTenantClient{Issuer: "https://as.example.com/?x=1", ClientID: "c"}
		}, CodeOAuthClient},
		"no client id": {func(d *ServerDef) { d.OAuthClient = &secrets.MCPTenantClient{Issuer: "https://as.example.com"} }, CodeOAuthClient},
		"control chars": {func(d *ServerDef) {
			d.OAuthClient = &secrets.MCPTenantClient{Issuer: "https://as.example.com", ClientID: "a\nb"}
		}, CodeOAuthClient},
	}
	for name, c := range cases {
		d := base
		c.mod(&d)
		var ve *ValidationError
		if err := Validate(d); !errors.As(err, &ve) || ve.Code != c.code {
			t.Errorf("%s: %v, want %s", name, err, c.code)
		}
	}
}

func TestChangingTheTenantClientEndsTheSignIn(t *testing.T) {
	d := ServerDef{ID: "t1", Auth: AuthOAuth, URL: "https://m.example.com/mcp",
		OAuthClient: &secrets.MCPTenantClient{Issuer: "https://as.example.com", ClientID: "corp"}}
	same := d
	same.OAuthClient = &secrets.MCPTenantClient{Issuer: "https://as.example.com", ClientID: "corp"}
	if got := endedOAuth([]ServerDef{d}, []ServerDef{same}); len(got) != 0 {
		t.Errorf("an unchanged client ended %v", got)
	}
	confidential := d
	confidential.OAuthClient = &secrets.MCPTenantClient{Issuer: "https://as.example.com", ClientID: "corp", Confidential: true}
	none := d
	none.OAuthClient = nil
	for name, next := range map[string]ServerDef{"made confidential": confidential, "removed": none} {
		if got := endedOAuth([]ServerDef{d}, []ServerDef{next}); len(got) != 1 {
			t.Errorf("%s: ended %v", name, got)
		}
	}
}

// An admin's issuer that differs only by a trailing "/" still names the AS the sign-in selected.
func TestTenantClientIssuerIgnoresATrailingSlash(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := distributeOAuth(t, f, &secrets.MCPTenantClient{Issuer: f.url("") + "/", ClientID: "corp-public"})
	if res, q := mustStart(t, id, startReq(nil)); q.Get("client_id") != "corp-public" || res.Registration != OAuthRegTenant {
		t.Fatalf("client_id=%q registration=%q, want the tenant's client", q.Get("client_id"), res.Registration)
	}
}

// A sign-in that finishes after the tenant changed the server is not stored, and the tokens it
// got are revoked: a disconnect that ran in between found nothing to end.
func TestCompleteRefusesWhenTheDefinitionChangedMeanwhile(t *testing.T) {
	for name, change := range map[string]func(f *asFake) *secrets.MCPTenantClient{
		"client changed": func(f *asFake) *secrets.MCPTenantClient {
			return &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "other"}
		},
		"client removed": func(*asFake) *secrets.MCPTenantClient { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			resetOAuthState(t)
			f := newASFake(t, nil)
			id := distributeOAuth(t, f, &secrets.MCPTenantClient{Issuer: f.url(""), ClientID: "corp-public"})
			req := startReq(nil)
			mustStart(t, id, req)
			distributeOAuth(t, f, change(f))
			_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
			wantCode(t, err, OAuthErrStateMismatch)
			if g := currentGrant(id); g != nil {
				t.Fatalf("a grant was stored: %+v", g)
			}
			if len(f.revokes) != 1 || f.revokes[0].Get("token") != "rt-0" {
				t.Errorf("the unstored tokens were not revoked: %v", f.revokes)
			}
		})
	}
}
