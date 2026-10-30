package mcpreg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestParseBearerChallenges(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   []bearerChallenge
	}{
		{"quoted", []string{`Bearer resource_metadata="https://h/prm", scope="a b"`},
			[]bearerChallenge{{"https://h/prm", "a b", ""}}},
		{"bare values and mixed case", []string{`BEARER Resource_Metadata=https://h/prm,SCOPE=read`},
			[]bearerChallenge{{"https://h/prm", "read", ""}}},
		{"realm and error first", []string{`Bearer realm="mcp", error="invalid_token", resource_metadata="https://h/prm"`},
			[]bearerChallenge{{"https://h/prm", "", "invalid_token"}}},
		{"several challenges in one header", []string{`Basic realm="x", Bearer resource_metadata="https://h/prm", scope="s1"`},
			[]bearerChallenge{{"https://h/prm", "s1", ""}}},
		{"several headers", []string{`Basic realm="x"`, `Bearer scope="s1"`, `Bearer resource_metadata="https://h/prm"`},
			[]bearerChallenge{{"", "s1", ""}, {"https://h/prm", "", ""}}},
		{"escaped quote", []string{`Bearer scope="a \"b\" c"`}, []bearerChallenge{{"", `a "b" c`, ""}}},
		{"other scheme parameters are not ours", []string{`Basic scope="no"`}, nil},
		{"token68 tail", []string{`Negotiate abc==, Bearer scope=x`}, []bearerChallenge{{"", "x", ""}}},
		{"empty", []string{``}, nil},
	}
	for _, c := range cases {
		got := parseBearerChallenges(c.values)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestCanonicalResource(t *testing.T) {
	for in, want := range map[string]string{
		"https://Example.COM/mcp":      "https://example.com/mcp",
		"https://example.com:443/mcp":  "https://example.com/mcp",
		"http://example.com:80/mcp":    "http://example.com/mcp",
		"https://example.com:8443/mcp": "https://example.com:8443/mcp",
		"https://example.com/":         "https://example.com",
		"https://example.com/mcp#frag": "https://example.com/mcp",
		"https://example.com/mcp?x=1":  "https://example.com/mcp?x=1",
		"http://[::1]:9/mcp":           "http://[::1]:9/mcp",
	} {
		got, ok := canonicalResource(in)
		if !ok || got != want {
			t.Errorf("%s: got %q,%v want %q", in, got, ok, want)
		}
	}
	if !resourceMatches("https://h/", "https://h") || resourceMatches("https://h/x/", "https://h/x") {
		t.Error("the root allowance must cover only the bare root")
	}
}

func TestASCandidatesOrder(t *testing.T) {
	cases := map[string][]string{
		"https://as.example/tenant1": {
			"https://as.example/.well-known/oauth-authorization-server/tenant1",
			"https://as.example/.well-known/openid-configuration/tenant1",
			"https://as.example/tenant1/.well-known/openid-configuration",
		},
		"https://as.example": {
			"https://as.example/.well-known/oauth-authorization-server",
			"https://as.example/.well-known/openid-configuration",
		},
		"https://as.example/": {
			"https://as.example/.well-known/oauth-authorization-server",
			"https://as.example/.well-known/openid-configuration",
		},
	}
	for issuer, want := range cases {
		iu, ok := issuerUsable(issuer)
		if !ok {
			t.Fatalf("%s unusable", issuer)
		}
		if got := asCandidates(iu); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: got %v want %v", issuer, got, want)
		}
	}
	for _, bad := range []string{"http://as.example", "https://as.example/?q=1", "https://as.example#f", "ftp://x", ""} {
		if _, ok := issuerUsable(bad); ok {
			t.Errorf("issuer %q must be unusable", bad)
		}
	}
}

// oauthFake is a resource server plus an authorization server on one httptest server, with
// every document and the paths it is served at configurable per test.
type oauthFake struct {
	t   *testing.T
	srv *httptest.Server

	challenge string            // WWW-Authenticate header; %PRM% is replaced by the PRM URL
	docs      map[string]any    // path -> JSON document (nil value = not present)
	raw       map[string]string // path -> raw 200 body
	redirects map[string]string // path -> Location
	mu        sync.Mutex
	hits      []string
	headers   map[string][]string // path -> request headers seen on the last hit
}

func newOAuthFake(t *testing.T) *oauthFake {
	f := &oauthFake{t: t, docs: map[string]any{}, raw: map[string]string{}, redirects: map[string]string{}, headers: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *oauthFake) url(p string) string { return f.srv.URL + p }

func (f *oauthFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.Method+" "+r.URL.Path)
	f.headers[r.URL.Path] = r.Header.Values("X-Static")
	f.mu.Unlock()
	if r.URL.Path == "/mcp" {
		if f.challenge != "" {
			w.Header().Add("WWW-Authenticate", strings.ReplaceAll(f.challenge, "%PRM%", f.url("/prm")))
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if loc, ok := f.redirects[r.URL.Path]; ok {
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}
	if s, ok := f.raw[r.URL.Path]; ok {
		_, _ = w.Write([]byte(s))
		return
	}
	if d, ok := f.docs[r.URL.Path]; ok {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d)
		return
	}
	http.NotFound(w, r)
}

func (f *oauthFake) probe() ProbeResult {
	return Probe(context.Background(), ServerDef{
		Transport: TransportHTTP, URL: f.url("/mcp"), Enabled: true,
		Headers: map[string]string{"X-Static": "secret"},
	})
}

func (f *oauthFake) prm(authServers ...string) map[string]any {
	return map[string]any{"resource": f.url("/mcp"), "authorization_servers": authServers, "scopes_supported": []string{"mcp:read", "mcp:write"}}
}

func codes(r *OAuthReport) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.Code)
	}
	return out
}

func asDoc(issuer string, extra map[string]any) map[string]any {
	d := map[string]any{"issuer": issuer, "code_challenge_methods_supported": []string{"plain", "S256"}}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func TestProbeOAuthHappyPathViaChallenge(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer realm="mcp", resource_metadata="%PRM%", scope="mcp:read"`
	issuer := f.url("")
	f.docs["/prm"] = f.prm(issuer)
	f.docs["/.well-known/oauth-authorization-server"] = asDoc(issuer, map[string]any{
		"client_id_metadata_document_supported":          true,
		"registration_endpoint":                          issuer + "/register",
		"authorization_response_iss_parameter_supported": true,
		"scopes_supported":                               []string{"mcp:read", "offline_access"},
	})
	res := f.probe()
	if res.OK || res.OAuth == nil || !res.OAuth.Required {
		t.Fatalf("want a recognised OAuth server, got %+v", res)
	}
	if res.Code != codeOAuthRequired || res.Error != errOAuthRequired {
		t.Errorf("code/error = %q / %q, want the oauth_required code with its English fallback", res.Code, res.Error)
	}
	o := res.OAuth
	if len(o.Problems) != 0 || o.Selected != issuer || len(o.Servers) != 1 {
		t.Fatalf("unexpected report: %+v", o)
	}
	s := o.Servers[0]
	if !s.Valid || !s.S256 || !s.IssParam || strings.Join(s.Registration, ",") != "cimd,dcr" {
		t.Errorf("server offer wrong: %+v", s)
	}
	if o.ChallengedScope != "mcp:read" || strings.Join(o.ScopesSupported, " ") != "mcp:read mcp:write" {
		t.Errorf("scopes wrong: %+v", o)
	}
	if o.Resource != f.url("/mcp") || o.MetadataURL != f.url("/prm") {
		t.Errorf("resource/metadata url wrong: %+v", o)
	}
	// Wire shape the Console reads.
	b, _ := json.Marshal(res)
	for _, k := range []string{`"oauth":{"required":true`, `"registration":["cimd","dcr"]`, `"issParam":true`, `"challengedScope":"mcp:read"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("wire JSON lacks %s: %s", k, b)
		}
	}
}

func TestProbeOAuthStaticHeadersOnlyToTheServer(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.docs["/prm"] = f.prm(f.url(""))
	f.docs["/.well-known/oauth-authorization-server"] = asDoc(f.url(""), nil)
	f.probe()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.headers["/mcp"]) == 0 {
		t.Fatal("the MCP server itself must still get the static headers")
	}
	for _, p := range []string{"/prm", "/.well-known/oauth-authorization-server"} {
		if len(f.headers[p]) != 0 {
			t.Errorf("static header leaked to %s", p)
		}
	}
}

func TestProbeOAuthWellKnownFallbackOrder(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer realm="mcp"` // no resource_metadata
	issuer := f.url("")
	// Only the root document exists: the path-inserted URL must be tried first and miss.
	f.docs["/.well-known/oauth-protected-resource"] = f.prm(issuer)
	f.docs["/.well-known/oauth-authorization-server"] = asDoc(issuer, nil)
	res := f.probe()
	if res.OAuth == nil || len(res.OAuth.Problems) != 0 || res.OAuth.Selected != issuer {
		t.Fatalf("fallback to the root document failed: %+v %+v", res, res.OAuth)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var withPath, root = -1, -1
	for i, h := range f.hits {
		switch h {
		case "GET /.well-known/oauth-protected-resource/mcp":
			withPath = i
		case "GET /.well-known/oauth-protected-resource":
			root = i
		}
	}
	if withPath < 0 || root < 0 || withPath > root {
		t.Errorf("want path form before root form, hits: %v", f.hits)
	}
}

func TestProbeOAuthPathInsertedWellKnownPreferred(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer realm="mcp"`
	issuer := f.url("")
	f.docs["/.well-known/oauth-protected-resource/mcp"] = f.prm(issuer)
	f.docs["/.well-known/oauth-authorization-server"] = asDoc(issuer, nil)
	res := f.probe()
	if res.OAuth == nil || res.OAuth.MetadataURL != f.url("/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("got %+v", res.OAuth)
	}
}

func TestProbePlain401IsStillAFailure(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Basic realm="x"`
	res := f.probe()
	if res.OAuth != nil || res.OK || res.Error != "HTTP 401" {
		t.Fatalf("a 401 with no OAuth metadata is a plain failure: %+v", res)
	}
}

func TestProbeOAuthPRMResourceMismatch(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.docs["/prm"] = map[string]any{"resource": "https://other.example/mcp", "authorization_servers": []string{f.url("")}}
	f.docs["/.well-known/oauth-authorization-server"] = asDoc(f.url(""), nil)
	res := f.probe()
	if res.OAuth == nil || !res.OAuth.Required || strings.Join(codes(res.OAuth), ",") != OAuthProblemPRMResource {
		t.Fatalf("got %+v", res.OAuth)
	}
	if len(res.OAuth.Servers) != 0 {
		t.Error("nothing from a PRM about another resource may be used")
	}
}

func TestProbeOAuthPRMUnreachableAndInvalid(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	res := f.probe() // /prm is a 404
	if res.OAuth == nil || !res.OAuth.Required || strings.Join(codes(res.OAuth), ",") != OAuthProblemPRMUnreachable {
		t.Fatalf("unreachable: %+v", res.OAuth)
	}
	f.raw["/prm"] = "<html>not json"
	res = f.probe()
	if strings.Join(codes(res.OAuth), ",") != OAuthProblemPRMInvalid {
		t.Fatalf("invalid: %+v", res.OAuth)
	}
	f.raw["/prm"] = "{}"
	res = f.probe()
	if strings.Join(codes(res.OAuth), ",") != OAuthProblemPRMResource {
		t.Fatalf("empty document has no matching resource: %+v", res.OAuth)
	}
	delete(f.raw, "/prm")
	f.docs["/prm"] = map[string]any{"resource": f.url("/mcp")}
	res = f.probe()
	if strings.Join(codes(res.OAuth), ",") != OAuthProblemNoAuthServers {
		t.Fatalf("no servers: %+v", res.OAuth)
	}
}

func TestProbeOAuthIssuerMismatchRejected(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.docs["/prm"] = f.prm(f.url(""))
	f.docs["/.well-known/oauth-authorization-server"] = asDoc(f.url("")+"/", nil) // trailing slash: not byte-equal
	res := f.probe()
	o := res.OAuth
	if o == nil || strings.Join(codes(o), ",") != OAuthProblemASIssuerMismatch || o.Selected != "" || o.Servers[0].Valid {
		t.Fatalf("a differing issuer must be refused: %+v", o)
	}
}

func TestProbeOAuthNoS256(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.docs["/prm"] = f.prm(f.url(""))
	f.docs["/.well-known/oauth-authorization-server"] = map[string]any{
		"issuer": f.url(""), "code_challenge_methods_supported": []string{"plain"},
	}
	o := f.probe().OAuth
	if o == nil || strings.Join(codes(o), ",") != OAuthProblemASNoS256 || o.Selected != "" || o.Servers[0].S256 {
		t.Fatalf("got %+v", o)
	}
	// An AS that omits the field altogether does not advertise S256 either.
	f.docs["/.well-known/oauth-authorization-server"] = map[string]any{"issuer": f.url("")}
	if o = f.probe().OAuth; strings.Join(codes(o), ",") != OAuthProblemASNoS256 {
		t.Fatalf("missing field: %+v", o)
	}
}

func TestProbeOAuthASDiscoveryOrderWithPath(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	issuer := f.url("/tenant1")
	f.docs["/prm"] = f.prm(issuer)
	// Only the third form (OIDC appending) exists.
	f.docs["/tenant1/.well-known/openid-configuration"] = asDoc(issuer, map[string]any{"registration_endpoint": issuer + "/reg"})
	o := f.probe().OAuth
	if o == nil || len(o.Problems) != 0 || o.Selected != issuer || strings.Join(o.Servers[0].Registration, ",") != "dcr" {
		t.Fatalf("got %+v", o)
	}
	f.mu.Lock()
	var order []string
	for _, h := range f.hits {
		if strings.Contains(h, "well-known") && !strings.Contains(h, "protected") {
			order = append(order, h)
		}
	}
	f.mu.Unlock()
	want := "GET /.well-known/oauth-authorization-server/tenant1,GET /.well-known/openid-configuration/tenant1,GET /tenant1/.well-known/openid-configuration"
	if strings.Join(order, ",") != want {
		t.Errorf("order %v", order)
	}
}

func TestProbeOAuthOIDCPathInsertionBeforeAppending(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	issuer := f.url("/tenant1")
	f.docs["/prm"] = f.prm(issuer)
	f.docs["/.well-known/openid-configuration/tenant1"] = asDoc(issuer, nil)
	f.docs["/tenant1/.well-known/openid-configuration"] = asDoc(issuer+"x", nil) // must never be reached
	o := f.probe().OAuth
	if o == nil || len(o.Problems) != 0 || o.Servers[0].MetadataURL != f.url("/.well-known/openid-configuration/tenant1") {
		t.Fatalf("got %+v", o)
	}
}

func TestProbeOAuthASUnreachable(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.docs["/prm"] = f.prm(f.url(""))
	o := f.probe().OAuth
	if o == nil || strings.Join(codes(o), ",") != OAuthProblemASUnreachable || o.Servers[0].Valid {
		t.Fatalf("got %+v", o)
	}
}

func TestProbeOAuthSelectsFirstValidServer(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	bad, good := f.url("/bad"), f.url("/good")
	f.docs["/prm"] = f.prm(bad, good)
	f.docs["/.well-known/oauth-authorization-server/bad"] = map[string]any{"issuer": bad, "code_challenge_methods_supported": []string{"plain"}}
	f.docs["/.well-known/oauth-authorization-server/good"] = asDoc(good, nil)
	o := f.probe().OAuth
	if o == nil || o.Selected != good || len(o.Servers) != 2 || strings.Join(codes(o), ",") != OAuthProblemASNoS256 {
		t.Fatalf("got %+v", o)
	}
}

func TestProbeOAuthUnusableIssuer(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.docs["/prm"] = f.prm("http://as.example.com") // plain http, not loopback
	o := f.probe().OAuth
	if o == nil || strings.Join(codes(o), ",") != OAuthProblemASIssuerUnusable {
		t.Fatalf("got %+v", o)
	}
	for _, h := range f.hits {
		if strings.Contains(h, "as.example.com") {
			t.Error("nothing may be fetched for an unusable issuer")
		}
	}
}

func TestProbeOAuthDoesNotFollowRedirectsOffOrigin(t *testing.T) {
	off := newOAuthFake(t) // a second origin that would happily serve the PRM
	off.docs["/prm"] = map[string]any{"resource": "x"}
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.redirects["/prm"] = off.url("/prm")
	o := f.probe().OAuth
	if o == nil || strings.Join(codes(o), ",") != OAuthProblemPRMUnreachable {
		t.Fatalf("got %+v", o)
	}
	off.mu.Lock()
	defer off.mu.Unlock()
	if len(off.hits) != 0 {
		t.Errorf("a cross-origin redirect was followed: %v", off.hits)
	}
}

func TestProbeOAuthFollowsSameOriginRedirect(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.redirects["/prm"] = "/prm2"
	f.docs["/prm2"] = f.prm(f.url(""))
	f.docs["/.well-known/oauth-authorization-server"] = asDoc(f.url(""), nil)
	if o := f.probe().OAuth; o == nil || len(o.Problems) != 0 {
		t.Fatalf("got %+v", o)
	}
}

func TestProbeOAuthBodyIsBounded(t *testing.T) {
	f := newOAuthFake(t)
	f.challenge = `Bearer resource_metadata="%PRM%"`
	f.raw["/prm"] = `{"resource":"` + strings.Repeat("a", maxMetadataBody) + `"}`
	o := f.probe().OAuth
	if o == nil || strings.Join(codes(o), ",") != OAuthProblemPRMUnreachable {
		t.Fatalf("an oversized document must be refused: %+v", o)
	}
}
