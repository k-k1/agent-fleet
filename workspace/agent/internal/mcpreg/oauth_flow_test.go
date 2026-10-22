package mcpreg

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// asFake is a resource server and an authorization server on one httptest server, recording
// every request the sign-in flow makes to the token, registration and revocation endpoints.
type asFake struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	asDoc    map[string]any // overrides merged into the AS metadata
	tokens   []url.Values   // token-endpoint form bodies, in order
	tokenHdr []http.Header
	regs     []map[string]any
	revokes  []url.Values
	// tokenReply answers the n-th token request (0-based); default is a success.
	tokenReply func(n int, form url.Values) (status int, body string)
	tokenDelay time.Duration
	revokeCode int
	regBody    string
}

func newASFake(t *testing.T, doc map[string]any) *asFake {
	f := &asFake{t: t, asDoc: doc, revokeCode: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *asFake) url(p string) string { return f.srv.URL + p }

func (f *asFake) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/mcp":
		w.Header().Add("WWW-Authenticate", `Bearer resource_metadata="`+f.url("/prm")+`", scope="mcp:read"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case "/prm":
		writeTestJSON(w, map[string]any{
			"resource": f.url("/mcp"), "authorization_servers": []string{f.url("")},
			"scopes_supported": []string{"mcp:read", "mcp:write"},
		})
	case "/.well-known/oauth-authorization-server":
		d := map[string]any{
			"issuer":                           f.url(""),
			"authorization_endpoint":           f.url("/authorize"),
			"token_endpoint":                   f.url("/token"),
			"revocation_endpoint":              f.url("/revoke"),
			"registration_endpoint":            f.url("/register"),
			"code_challenge_methods_supported": []string{"S256"},
			"scopes_supported":                 []string{"mcp:read", "offline_access"},
		}
		f.mu.Lock()
		for k, v := range f.asDoc {
			if v == nil {
				delete(d, k)
			} else {
				d[k] = v
			}
		}
		f.mu.Unlock()
		writeTestJSON(w, d)
	case "/register":
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.mu.Lock()
		f.regs = append(f.regs, m)
		n := len(f.regs)
		body := f.regBody
		f.mu.Unlock()
		if body != "" {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(w, map[string]any{"client_id": fmt.Sprintf("dcr-client-%d", n)})
	case "/token":
		_ = r.ParseForm()
		f.mu.Lock()
		n := len(f.tokens)
		f.tokens = append(f.tokens, r.PostForm)
		f.tokenHdr = append(f.tokenHdr, r.Header.Clone())
		reply, delay := f.tokenReply, f.tokenDelay
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		status, body := 200, fmt.Sprintf(`{"access_token":"at-%d","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-%d"}`, n, n)
		if reply != nil {
			status, body = reply(n, r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	case "/revoke":
		_ = r.ParseForm()
		f.mu.Lock()
		f.revokes = append(f.revokes, r.PostForm)
		code := f.revokeCode
		f.mu.Unlock()
		w.WriteHeader(code)
	default:
		http.NotFound(w, r)
	}
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *asFake) tokenCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokens)
}

const testRedirect = "https://af.example.com/oauth2/mcp/callback"
const testCIMD = "https://af.example.com/oauth/mcp-client.json"

// registerServer stores a user server pointing at the fake and returns its id.
func registerServer(t *testing.T, f *asFake) string {
	t.Helper()
	d, err := Create(ServerDef{Name: "hosted", Transport: TransportHTTP, URL: f.url("/mcp"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Delete(d.ID) })
	return d.ID
}

var stateSeq int

func newState() string {
	stateSeq++
	return fmt.Sprintf("state-%016d", stateSeq)
}

func startReq(extra func(*StartRequest)) StartRequest {
	r := StartRequest{State: newState(), RedirectURI: testRedirect, CIMDURL: testCIMD, ClientName: "Agent Fleet"}
	if extra != nil {
		extra(&r)
	}
	return r
}

func mustStart(t *testing.T, id string, req StartRequest) (StartResult, url.Values) {
	t.Helper()
	res, err := StartOAuth(context.Background(), id, req)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	u, err := url.Parse(res.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return res, u.Query()
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var oe *OAuthError
	if !errors.As(err, &oe) || oe.Code != code {
		t.Fatalf("want error code %q, got %v", code, err)
	}
}

func resetOAuthState(t *testing.T) {
	t.Helper()
	pendingMu.Lock()
	pendingAuths = map[string]*pendingAuth{}
	pendingMu.Unlock()
	rotatedMu.Lock()
	rotated = map[string]rotation{}
	rotatedMu.Unlock()
	oldNow, oldUpd := oauthNow, updateSecrets
	t.Cleanup(func() { oauthNow, updateSecrets = oldNow, oldUpd })
	_ = secrets.Update(func(s *secrets.Data) error {
		s.MCPOAuth, s.MCPOAuthClients = nil, nil
		return nil
	})
}

func TestStartBuildsTheAuthorizationRequest(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, map[string]any{"client_id_metadata_document_supported": true})
	id := registerServer(t, f)
	req := startReq(nil)
	res, q := mustStart(t, id, req)

	if !strings.HasPrefix(res.AuthorizeURL, f.url("/authorize")+"?") {
		t.Fatalf("authorize URL = %s", res.AuthorizeURL)
	}
	want := map[string]string{
		"response_type": "code", "client_id": testCIMD, "redirect_uri": testRedirect,
		"code_challenge_method": "S256", "state": req.State,
		"resource": f.url("/mcp"), "scope": "mcp:read offline_access",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if len(q.Get("code_challenge")) != 43 {
		t.Errorf("code_challenge = %q", q.Get("code_challenge"))
	}
	if res.Registration != OAuthRegCIMD || res.Issuer != f.url("") || res.ServerName != "hosted" {
		t.Errorf("result = %+v", res)
	}
	if strings.Contains(res.AuthorizeURL, "code_verifier") {
		t.Error("the verifier must not be in the authorization URL")
	}
}

func TestSelectScope(t *testing.T) {
	cases := []struct {
		name      string
		challenge string
		prm, as   []string
		want      string
	}{
		{"challenge wins over prm", "a b", []string{"x", "y"}, nil, "a b"},
		{"prm when no challenge", "", []string{"x", "y"}, nil, "x y"},
		{"offline_access added when the AS lists it", "a", nil, []string{"a", "offline_access"}, "a offline_access"},
		{"not added when the AS does not", "a", nil, []string{"a"}, "a"},
		{"not duplicated", "a offline_access", nil, []string{"offline_access"}, "a offline_access"},
		{"found beyond the report's cap", "a", nil, append(make([]string, 100), "offline_access"), "a offline_access"},
		{"nothing at all", "", nil, nil, ""},
	}
	for _, c := range cases {
		if got := selectScope(c.challenge, c.prm, c.as); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestRegistrationOrder(t *testing.T) {
	resetOAuthState(t)
	cimd := map[string]any{"client_id_metadata_document_supported": true}

	t.Run("pre-registered beats CIMD and DCR", func(t *testing.T) {
		f := newASFake(t, cimd)
		id := registerServer(t, f)
		if err := SetPreregisteredClient(id, f.url(""), "member-client", "member-secret"); err != nil {
			t.Fatal(err)
		}
		res, q := mustStart(t, id, startReq(nil))
		if res.Registration != OAuthRegPreregistered || q.Get("client_id") != "member-client" {
			t.Errorf("got %+v %s", res, q.Get("client_id"))
		}
		if len(f.regs) != 0 {
			t.Error("DCR must not run when a client id was entered")
		}
	})
	t.Run("CIMD beats DCR", func(t *testing.T) {
		f := newASFake(t, cimd)
		id := registerServer(t, f)
		res, _ := mustStart(t, id, startReq(nil))
		if res.Registration != OAuthRegCIMD || len(f.regs) != 0 {
			t.Errorf("got %+v, %d registrations", res, len(f.regs))
		}
	})
	t.Run("DCR when CIMD is not offered, and it is reused", func(t *testing.T) {
		f := newASFake(t, nil)
		id := registerServer(t, f)
		r1, q1 := mustStart(t, id, startReq(nil))
		r2, q2 := mustStart(t, id, startReq(nil))
		if r1.Registration != OAuthRegDCR || r2.Registration != OAuthRegDCR {
			t.Fatalf("paths %s %s", r1.Registration, r2.Registration)
		}
		if len(f.regs) != 1 || q1.Get("client_id") != q2.Get("client_id") || q1.Get("client_id") != "dcr-client-1" {
			t.Errorf("registrations=%d ids %s %s", len(f.regs), q1.Get("client_id"), q2.Get("client_id"))
		}
		reg := f.regs[0]
		if reg["application_type"] != "web" || reg["token_endpoint_auth_method"] != "none" ||
			fmt.Sprint(reg["redirect_uris"]) != "["+testRedirect+"]" {
			t.Errorf("registration body = %v", reg)
		}
	})
	t.Run("DCR when CIMD is offered but the deployment has no https URL", func(t *testing.T) {
		f := newASFake(t, cimd)
		id := registerServer(t, f)
		res, _ := mustStart(t, id, startReq(func(r *StartRequest) { r.CIMDURL = "" }))
		if res.Registration != OAuthRegDCR {
			t.Errorf("path = %s", res.Registration)
		}
	})
	t.Run("forced DCR skips CIMD", func(t *testing.T) {
		f := newASFake(t, cimd)
		id := registerServer(t, f)
		res, _ := mustStart(t, id, startReq(func(r *StartRequest) { r.Registration = OAuthRegDCR }))
		if res.Registration != OAuthRegDCR || len(f.regs) != 1 {
			t.Errorf("got %+v, %d registrations", res, len(f.regs))
		}
	})
	t.Run("a changed redirect registers again", func(t *testing.T) {
		f := newASFake(t, nil)
		id := registerServer(t, f)
		mustStart(t, id, startReq(nil))
		mustStart(t, id, startReq(func(r *StartRequest) { r.RedirectURI = "https://other.example.com/oauth2/mcp/callback" }))
		if len(f.regs) != 2 {
			t.Errorf("registrations = %d, want 2", len(f.regs))
		}
	})
	t.Run("a client id is needed when nothing else is on offer", func(t *testing.T) {
		f := newASFake(t, map[string]any{"registration_endpoint": nil})
		id := registerServer(t, f)
		_, err := StartOAuth(context.Background(), id, startReq(nil))
		wantCode(t, err, OAuthErrClientIDNeeded)
		var oe *OAuthError
		errors.As(err, &oe)
		if oe.Issuer != f.url("") {
			t.Errorf("the error must name the issuer to enter a client id for: %+v", oe)
		}
	})
	t.Run("a failing registration endpoint is reported", func(t *testing.T) {
		f := newASFake(t, nil)
		f.regBody = `{"error":"invalid_redirect_uri"}`
		id := registerServer(t, f)
		_, err := StartOAuth(context.Background(), id, startReq(nil))
		wantCode(t, err, OAuthErrRegistration)
	})
}

func TestStartRefusesWhatP1Refuses(t *testing.T) {
	resetOAuthState(t)
	t.Run("AS metadata issuer mismatch", func(t *testing.T) {
		f := newASFake(t, map[string]any{"issuer": "https://evil.example.com"})
		id := registerServer(t, f)
		_, err := StartOAuth(context.Background(), id, startReq(nil))
		wantCode(t, err, OAuthErrDiscovery)
		if !strings.Contains(err.Error(), OAuthProblemASIssuerMismatch) {
			t.Errorf("error should name the problem: %v", err)
		}
	})
	t.Run("no S256", func(t *testing.T) {
		f := newASFake(t, map[string]any{"code_challenge_methods_supported": []string{"plain"}})
		id := registerServer(t, f)
		_, err := StartOAuth(context.Background(), id, startReq(nil))
		wantCode(t, err, OAuthErrDiscovery)
	})
	t.Run("missing token endpoint", func(t *testing.T) {
		f := newASFake(t, map[string]any{"token_endpoint": nil})
		id := registerServer(t, f)
		_, err := StartOAuth(context.Background(), id, startReq(nil))
		wantCode(t, err, OAuthErrEndpoints)
	})
	t.Run("bad requests", func(t *testing.T) {
		f := newASFake(t, nil)
		id := registerServer(t, f)
		for name, mod := range map[string]func(*StartRequest){
			"short state":  func(r *StartRequest) { r.State = "x" },
			"http cimd":    func(r *StartRequest) { r.CIMDURL = "http://af.example.com/c.json" },
			"bad redirect": func(r *StartRequest) { r.RedirectURI = "javascript:alert(1)" },
			"bad scope":    func(r *StartRequest) { r.Scope = "a\"b" },
			"bad path":     func(r *StartRequest) { r.Registration = "magic" },
		} {
			_, err := StartOAuth(context.Background(), id, startReq(mod))
			if err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
		_, err := StartOAuth(context.Background(), "mcp-nope", startReq(nil))
		wantCode(t, err, OAuthErrNotFound)
	})
}

func TestCompleteExchangesTheCodeWithTheVerifier(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, map[string]any{"client_id_metadata_document_supported": true})
	id := registerServer(t, f)
	req := startReq(nil)
	_, q := mustStart(t, id, req)
	res, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "the-code", Iss: f.url("")})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	form := f.tokens[0]
	for k, v := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "redirect_uri": testRedirect,
		"resource": f.url("/mcp"), "client_id": testCIMD,
	} {
		if form.Get(k) != v {
			t.Errorf("token %s = %q, want %q", k, form.Get(k), v)
		}
	}
	sum := sha256.Sum256([]byte(form.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Error("the verifier sent to the token endpoint is not the one the challenge was made from")
	}
	if res.Issuer != f.url("") || res.Registration != OAuthRegCIMD || res.ServerName != "hosted" {
		t.Errorf("result = %+v", res)
	}
	st, _ := OAuthStatusOf(id)
	if st.State != OAuthStateConnected || strings.Join(st.Scopes, " ") != "mcp:read offline_access" || st.ExpiresAt == 0 {
		t.Errorf("status = %+v", st)
	}
	// Nothing that leaves this package carries a token, a code or the verifier.
	for _, v := range []any{res, st} {
		b, _ := json.Marshal(v)
		for _, secret := range []string{"at-0", "rt-0", "the-code", form.Get("code_verifier")} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s leaks %q", b, secret)
			}
		}
	}
	tok, err := AccessToken(context.Background(), id)
	if err != nil || tok != "at-0" {
		t.Errorf("AccessToken = %q, %v", tok, err)
	}
}

func TestCompleteClientAuthentication(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := registerServer(t, f)
	if err := SetPreregisteredClient(id, f.url(""), "cid", "sec ret"); err != nil {
		t.Fatal(err)
	}
	req := startReq(nil)
	mustStart(t, id, req)
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	u, p, ok := parseBasic(f.tokenHdr[0].Get("Authorization"))
	if !ok || u != "cid" || p != "sec+ret" {
		t.Errorf("basic auth = %q %q %v (RFC 6749 §2.3.1 form-encodes both)", u, p, ok)
	}
	if f.tokens[0].Get("client_secret") != "" {
		t.Error("the secret must not also travel in the body")
	}
}

func parseBasic(h string) (string, string, bool) {
	r := &http.Request{Header: http.Header{"Authorization": {h}}}
	return r.BasicAuth()
}

// The MCP specification's table for RFC 9207.
func TestIssValidation(t *testing.T) {
	cases := []struct {
		name      string
		advertise bool
		iss       string // "" absent, "same", or a literal
		wantCode  string
	}{
		{"present and equal, advertised", true, "same", ""},
		{"present and equal, not advertised", false, "same", ""},
		{"absent, not advertised", false, "", ""},
		{"absent while advertised", true, "", OAuthErrIssMissing},
		{"mismatch, advertised", true, "https://evil.example.com", OAuthErrIssMismatch},
		{"mismatch, not advertised", false, "https://evil.example.com", OAuthErrIssMismatch},
		{"a trailing slash is a different issuer", false, "SAME/", OAuthErrIssMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetOAuthState(t)
			doc := map[string]any{"authorization_response_iss_parameter_supported": c.advertise}
			f := newASFake(t, doc)
			id := registerServer(t, f)
			req := startReq(nil)
			mustStart(t, id, req)
			iss := c.iss
			switch {
			case iss == "same":
				iss = f.url("")
			case iss == "SAME/":
				iss = f.url("") + "/"
			}
			_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: iss})
			if c.wantCode == "" {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			wantCode(t, err, c.wantCode)
			if f.tokenCount() != 0 {
				t.Error("a rejected response must not reach the token endpoint")
			}
		})
	}
}

func TestStateIsConsumedExactlyOnce(t *testing.T) {
	t.Run("replay after success", func(t *testing.T) {
		resetOAuthState(t)
		f := newASFake(t, nil)
		id := registerServer(t, f)
		req := startReq(nil)
		mustStart(t, id, req)
		cb := CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}
		if _, err := CompleteOAuth(context.Background(), id, cb); err != nil {
			t.Fatal(err)
		}
		_, err := CompleteOAuth(context.Background(), id, cb)
		wantCode(t, err, OAuthErrStateUnknown)
		if f.tokenCount() != 1 {
			t.Errorf("token requests = %d", f.tokenCount())
		}
	})
	t.Run("a failed callback burns the state", func(t *testing.T) {
		resetOAuthState(t)
		f := newASFake(t, nil)
		id := registerServer(t, f)
		req := startReq(nil)
		mustStart(t, id, req)
		_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: "https://evil.example.com"})
		wantCode(t, err, OAuthErrIssMismatch)
		_, err = CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
		wantCode(t, err, OAuthErrStateUnknown)
	})
	t.Run("an AS error burns the state and says nothing of its text", func(t *testing.T) {
		resetOAuthState(t)
		f := newASFake(t, nil)
		id := registerServer(t, f)
		req := startReq(nil)
		mustStart(t, id, req)
		_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Error: "access_denied"})
		wantCode(t, err, OAuthErrDenied)
		_, err = CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
		wantCode(t, err, OAuthErrStateUnknown)
	})
	t.Run("expired", func(t *testing.T) {
		resetOAuthState(t)
		f := newASFake(t, nil)
		id := registerServer(t, f)
		req := startReq(nil)
		mustStart(t, id, req)
		oauthNow = func() time.Time { return time.Now().Add(oauthStateTTL + time.Second) }
		_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
		wantCode(t, err, OAuthErrStateUnknown)
		if f.tokenCount() != 0 {
			t.Error("an expired state must not reach the token endpoint")
		}
	})
	t.Run("another server's state", func(t *testing.T) {
		resetOAuthState(t)
		f := newASFake(t, nil)
		id := registerServer(t, f)
		req := startReq(nil)
		mustStart(t, id, req)
		_, err := CompleteOAuth(context.Background(), "mcp-other", CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
		wantCode(t, err, OAuthErrStateMismatch)
	})
	t.Run("unknown state", func(t *testing.T) {
		resetOAuthState(t)
		_, err := CompleteOAuth(context.Background(), "x", CompleteRequest{State: "never-issued-state-value", Code: "c"})
		wantCode(t, err, OAuthErrStateUnknown)
	})
}

func TestTokenExchangeFailureLeaksNothing(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	f.tokenReply = func(int, url.Values) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"code SECRET-CODE-VALUE and verifier were bad"}`
	}
	id := registerServer(t, f)
	req := startReq(nil)
	mustStart(t, id, req)
	_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "SECRET-CODE-VALUE", Iss: f.url("")})
	wantCode(t, err, OAuthErrTokenExchange)
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "verifier") {
		t.Errorf("error leaks the AS's text: %v", err)
	}
	if st, _ := OAuthStatusOf(id); st.State != OAuthStateNotSignedIn {
		t.Errorf("state = %s", st.State)
	}
}

func TestTokenEndpointRedirectIsNotFollowed(t *testing.T) {
	resetOAuthState(t)
	var leaked bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer evil.Close()
	f := newASFake(t, nil)
	f.tokenReply = nil
	id := registerServer(t, f)
	// Point the token endpoint at a redirecting handler.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	f.asDoc = map[string]any{"token_endpoint": redir.URL + "/token"}
	req := startReq(nil)
	mustStart(t, id, req)
	_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
	wantCode(t, err, OAuthErrTokenExchange)
	if leaked {
		t.Error("the code and verifier were replayed to a redirect target")
	}
}

// signedIn leaves a grant for a fresh server whose refresh requests the fake answers.
func signedIn(t *testing.T, f *asFake) string {
	t.Helper()
	id := registerServer(t, f)
	req := startReq(nil)
	mustStart(t, id, req)
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	return id
}

func storedGrant(t *testing.T, id string) secrets.MCPOAuthGrant {
	t.Helper()
	data, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	g := findGrant(data, id)
	if g == nil {
		t.Fatal("no grant")
	}
	return *g
}

func TestRefreshRotatesAndPersists(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f) // token request 0: at-0 / rt-0
	// Age the access token.
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	tok, err := AccessToken(context.Background(), id)
	if err != nil || tok != "at-1" {
		t.Fatalf("AccessToken = %q, %v", tok, err)
	}
	form := f.tokens[1]
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt-0" || form.Get("resource") != f.url("/mcp") {
		t.Errorf("refresh form = %v", form)
	}
	if g := storedGrant(t, id); g.RefreshToken != "rt-1" || g.AccessToken != "at-1" {
		t.Errorf("stored grant = %+v", g)
	}
	// Still fresh at the same instant: no second request.
	if tok, _ := AccessToken(context.Background(), id); tok != "at-1" || f.tokenCount() != 2 {
		t.Errorf("a fresh token must be reused: %q after %d requests", tok, f.tokenCount())
	}
}

func TestRefreshKeepsTheOldRefreshTokenWhenNoneIsReturned(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	f.tokenReply = func(n int, _ url.Values) (int, string) {
		return 200, `{"access_token":"at-new","token_type":"bearer","expires_in":"3600"}`
	}
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if tok, err := AccessToken(context.Background(), id); err != nil || tok != "at-new" {
		t.Fatalf("%q %v", tok, err)
	}
	if g := storedGrant(t, id); g.RefreshToken != "rt-0" {
		t.Errorf("refresh token = %q, want the old one kept", g.RefreshToken)
	}
}

func TestConcurrentRefreshSendsOneRequest(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	f.tokenDelay = 150 * time.Millisecond
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	before := f.tokenCount()
	var wg sync.WaitGroup
	got := make([]string, 12)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = AccessToken(context.Background(), id)
		}()
	}
	wg.Wait()
	if n := f.tokenCount() - before; n != 1 {
		t.Fatalf("refresh requests = %d, want 1", n)
	}
	for i, g := range got {
		if g != "at-1" {
			t.Errorf("caller %d got %q", i, g)
		}
	}
}

func TestRefreshAfterRejectedTokenDoesNotRepeatAnothersRefresh(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	// Two callers both saw a 401 for at-0. The first renews; the second must take its result.
	if tok, err := RefreshAccessToken(context.Background(), id, "at-0"); err != nil || tok != "at-1" {
		t.Fatalf("%q %v", tok, err)
	}
	if tok, err := RefreshAccessToken(context.Background(), id, "at-0"); err != nil || tok != "at-1" {
		t.Fatalf("%q %v", tok, err)
	}
	if f.tokenCount() != 2 {
		t.Errorf("token requests = %d, want 2 (exchange + one refresh)", f.tokenCount())
	}
	// A 401 for the token that is current does renew again.
	if tok, _ := RefreshAccessToken(context.Background(), id, "at-1"); tok != "at-2" {
		t.Errorf("got %q", tok)
	}
}

func TestInvalidGrantMeansSignInAgainWithoutARetryLoop(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	f.tokenReply = func(int, url.Values) (int, string) { return 400, `{"error":"invalid_grant"}` }
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	before := f.tokenCount()
	for i := 0; i < 4; i++ {
		if _, err := AccessToken(context.Background(), id); !errors.Is(err, ErrSignInAgain) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := f.tokenCount() - before; n != 1 {
		t.Errorf("token requests = %d, want exactly 1", n)
	}
	st, _ := OAuthStatusOf(id)
	if st.State != OAuthStateSignInAgain {
		t.Errorf("state = %s", st.State)
	}
	if g := storedGrant(t, id); g.RefreshToken != "" || g.AccessToken != "" {
		t.Error("a dead grant keeps no tokens")
	}
}

func TestTransientRefreshFailureKeepsTheGrant(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	f.tokenReply = func(int, url.Values) (int, string) { return 503, `busy` }
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := AccessToken(context.Background(), id); err == nil || errors.Is(err, ErrSignInAgain) {
		t.Fatalf("err = %v", err)
	}
	if g := storedGrant(t, id); g.RefreshToken != "rt-0" || g.SignInAgain {
		t.Errorf("grant = %+v", g)
	}
}

// The response carries the rotated refresh token and the old one is spent at the AS. If the
// write to the store fails, the rotated token must survive for the next call, and the access
// token must not be handed out meanwhile.
func TestRotatedRefreshTokenSurvivesAFailedWrite(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	real := updateSecrets
	fail := true
	updateSecrets = func(fn func(*secrets.Data) error) error {
		if fail {
			return errors.New("disk full")
		}
		return real(fn)
	}
	tok, err := AccessToken(context.Background(), id)
	if err == nil || tok != "" {
		t.Fatalf("an unsaved token must not be used: %q %v", tok, err)
	}
	fail = false
	before := f.tokenCount()
	tok, err = AccessToken(context.Background(), id)
	if err != nil || tok != "at-1" {
		t.Fatalf("second call: %q %v", tok, err)
	}
	if f.tokenCount() != before {
		t.Error("the saved response must be reused, not requested again with a spent refresh token")
	}
	if g := storedGrant(t, id); g.RefreshToken != "rt-1" {
		t.Errorf("stored refresh token = %q", g.RefreshToken)
	}
}

// A crash at the write: nothing was returned to a caller before it, and a restart finds the
// store either entirely old or entirely new (the file is replaced atomically).
func TestNoTokenIsReturnedBeforeItIsPersisted(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	real := updateSecrets
	var returned string
	updateSecrets = func(fn func(*secrets.Data) error) error {
		if returned != "" {
			t.Error("a token was returned before the write")
		}
		panic("crash")
	}
	func() {
		defer func() { _ = recover() }()
		returned, _ = AccessToken(context.Background(), id)
	}()
	updateSecrets = real
	if returned != "" {
		t.Fatalf("returned %q", returned)
	}
	if g := storedGrant(t, id); g.RefreshToken != "rt-0" {
		t.Errorf("an interrupted write must leave the store as it was: %+v", g)
	}
}

func TestDisconnectRevokesThenDeletes(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	res, err := DisconnectOAuth(context.Background(), id)
	if err != nil || !res.HadGrant || !res.Revoked {
		t.Fatalf("%+v %v", res, err)
	}
	if len(f.revokes) != 1 || f.revokes[0].Get("token") != "rt-0" || f.revokes[0].Get("token_type_hint") != "refresh_token" {
		t.Errorf("revocation = %v", f.revokes)
	}
	if st, _ := OAuthStatusOf(id); st.State != OAuthStateNotSignedIn {
		t.Errorf("state = %s", st.State)
	}
	if _, err := AccessToken(context.Background(), id); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("err = %v", err)
	}
	if res, _ := DisconnectOAuth(context.Background(), id); res.HadGrant {
		t.Error("nothing was left to disconnect")
	}
}

func TestDisconnectDeletesLocallyWhateverTheASAnswers(t *testing.T) {
	t.Run("revocation refused", func(t *testing.T) {
		resetOAuthState(t)
		f := newASFake(t, nil)
		id := signedIn(t, f)
		f.revokeCode = 500
		res, _ := DisconnectOAuth(context.Background(), id)
		if !res.HadGrant || res.Revoked {
			t.Errorf("%+v", res)
		}
		if st, _ := OAuthStatusOf(id); st.State != OAuthStateNotSignedIn {
			t.Errorf("state = %s", st.State)
		}
	})
	t.Run("no revocation endpoint", func(t *testing.T) {
		resetOAuthState(t)
		f := newASFake(t, map[string]any{"revocation_endpoint": nil})
		id := signedIn(t, f)
		res, _ := DisconnectOAuth(context.Background(), id)
		if !res.HadGrant || res.Revoked || len(f.revokes) != 0 {
			t.Errorf("%+v %v", res, f.revokes)
		}
		if st, _ := OAuthStatusOf(id); st.State != OAuthStateNotSignedIn {
			t.Errorf("state = %s", st.State)
		}
	})
}

func TestDeletingAServerPurgesItsGrant(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	if err := SetPreregisteredClient(id, f.url(""), "cid", ""); err != nil {
		t.Fatal(err)
	}
	if err := Delete(id); err != nil {
		t.Fatal(err)
	}
	data, _ := secrets.Load()
	if len(data.MCPOAuth) != 0 {
		t.Errorf("%d grants left behind", len(data.MCPOAuth))
	}
	for _, c := range data.MCPOAuthClients {
		// A DCR client belongs to the issuer, not the server, and is reused by the next one.
		if c.Kind == OAuthRegPreregistered {
			t.Errorf("a pre-registered client was left behind: %+v", c)
		}
	}
}

func TestStepUpStatusAndScope(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := signedIn(t, f)
	if err := NoteInsufficientScope(id, "mcp:write"); err != nil {
		t.Fatal(err)
	}
	if st, _ := OAuthStatusOf(id); st.State != OAuthStateNeedsMoreOAu || st.StepUpScope != "mcp:write" {
		t.Errorf("status = %+v", st)
	}
	_, q := mustStart(t, id, startReq(nil))
	if q.Get("scope") != "mcp:read offline_access mcp:write" {
		t.Errorf("a step-up sign-in asks for the union: %q", q.Get("scope"))
	}
	if err := NoteInsufficientScope(id, `a"b`); err == nil {
		t.Error("an invalid scope must be refused")
	}
}

func TestSecretsNeverReachTheSourceOfLogs(t *testing.T) {
	// A guard on the source: these files must not print or format a token, code or verifier.
	for _, f := range []string{"oauth_flow.go", "oauth_token.go"} {
		b := readFile(t, f)
		if strings.Contains(b, "log.") || strings.Contains(b, "fmt.Print") {
			t.Errorf("%s logs; secrets must never be logged", f)
		}
	}
}

// A sign-in still open in a browser tab must not bring a disconnected grant back.
func TestDisconnectInvalidatesAnOpenSignIn(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	id := registerServer(t, f)
	req := startReq(nil)
	mustStart(t, id, req)
	if _, err := DisconnectOAuth(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	_, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c", Iss: f.url("")})
	wantCode(t, err, OAuthErrStateUnknown)
	if f.tokenCount() != 0 {
		t.Error("the code reached the token endpoint after the disconnect")
	}
}

// The static headers of a definition go to the server only: a redirect to another origin is
// not followed with them.
func TestSignInDiscoveryDoesNotFollowACrossOriginRedirectWithHeaders(t *testing.T) {
	var leaked bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Static") != "" {
			leaked = true
		}
	}))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	discoverForSignIn(context.Background(), ServerDef{Transport: TransportHTTP, URL: srv.URL + "/mcp", Headers: map[string]string{"X-Static": "secret"}})
	if leaked {
		t.Error("a static header followed a redirect to another origin")
	}
}
