package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// devIdPTestEnv is the preview test environment with the dev IdP mounted on its Console mux,
// a controllable clock, and a custodian so the signing key is stored sealed.
type devIdPTestEnv struct {
	*previewHostEnv
	api      devIdPAPI
	slug     string
	clientID string
	secret   string
	mu       sync.Mutex
	clock    time.Time
}

func (d *devIdPTestEnv) now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clock
}

func (d *devIdPTestEnv) advance(by time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clock = d.clock.Add(by)
}

func newDevIdPTestEnv(t *testing.T) *devIdPTestEnv {
	t.Helper()
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	e.mgr.master32 = bytes.Repeat([]byte{7}, 32)
	e.mgr.custodian = newLocalCustodian(e.mgr.master32)
	d := &devIdPTestEnv{previewHostEnv: e, clock: time.Now()}
	e.cfg.brand = newBrandResolver(brandConfig{}, nil) // tests set .env to brand the consent page
	d.api = newDevIdPAPI(e.cfg)
	d.api.now = d.now
	d.api.keys = newDevIdPKeyring(e.mgr.store, e.mgr, d.now)
	d.api.mount(e.mux)
	d.slug = e.mintSlug(t)
	d.clientID, d.secret = d.registration(t)
	return d
}

func (d *devIdPTestEnv) registration(t *testing.T) (id, secret string) {
	t.Helper()
	id, secret, err := d.mgr.ensureDevIdPClient(context.Background(), d.ws)
	if err != nil {
		t.Fatal(err)
	}
	return id, secret
}

func (d *devIdPTestEnv) ownHost(port int) string {
	return "https://" + previewHostname(d.slug, port, d.domain)
}

func pkcePair() (verifier, challenge string) {
	verifier = devIdPRandom(32) // 43 chars of base64url
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// differentVerifier returns a verifier of the same length that differs from v in its first
// character. A fixed replacement character is not enough: v is random base64url, so it
// already starts with that character one time in 64 and the "wrong" verifier is the right one.
func differentVerifier(v string) string {
	if v[0] == 'x' {
		return "y" + v[1:]
	}
	return "x" + v[1:]
}

// authorizeAs calls /dev-idp/authorize on the Console origin as userKey.
func (d *devIdPTestEnv) authorizeAs(t *testing.T, userKey string, q url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var rec *httptest.ResponseRecorder
	d.asUser(userKey, func() {
		rec = d.console(t, "https://af.example.com"+devIdPAuthorizePath+"?"+q.Encode())
	})
	return rec
}

func (d *devIdPTestEnv) authQuery(redirect, challenge string) url.Values {
	q := url.Values{
		"client_id": {d.clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {"openid email profile"}, "state": {"st-1"}, "nonce": {"nonce-1"},
	}
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return q
}

// codeFrom pulls the code out of a successful authorize redirect.
func codeFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize: %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || u.Query().Get("code") == "" {
		t.Fatalf("authorize Location %q carries no code", rec.Header().Get("Location"))
	}
	return u.Query().Get("code")
}

func (d *devIdPTestEnv) tokenReq(form url.Values, basicID, basicSecret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "https://af.example.com"+devIdPTokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicID != "" {
		req.SetBasicAuth(url.QueryEscape(basicID), url.QueryEscape(basicSecret))
	}
	rec := httptest.NewRecorder()
	d.mux.ServeHTTP(rec, req)
	return rec
}

func (d *devIdPTestEnv) exchange(code, redirect, verifier string) *httptest.ResponseRecorder {
	f := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}}
	if verifier != "" {
		f.Set("code_verifier", verifier)
	}
	return d.tokenReq(f, d.clientID, d.secret)
}

func idpJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body)
	}
	return m
}

func (d *devIdPTestEnv) jwks(t *testing.T) map[string]any {
	t.Helper()
	rec := d.console(t, "https://af.example.com"+devIdPJWKSPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("jwks: %d %s", rec.Code, rec.Body)
	}
	return idpJSON(t, rec)
}

// verifyIDToken is what a relying party does: find the key by kid in the JWKS, check the
// RS256 signature, then issuer, audience and expiry. It returns the claims or the reason.
func verifyIDToken(token string, jwks map[string]any, wantIss, wantAud string, now time.Time) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT")
	}
	dec := base64.RawURLEncoding.DecodeString
	hb, err := dec(parts[0])
	if err != nil {
		return nil, err
	}
	var hdr struct{ Alg, Kid string }
	_ = json.Unmarshal(hb, &hdr)
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("alg %q", hdr.Alg)
	}
	var pub *rsa.PublicKey
	for _, k := range jwks["keys"].([]any) {
		km := k.(map[string]any)
		if km["kid"] != hdr.Kid {
			continue
		}
		nb, _ := dec(km["n"].(string))
		eb, _ := dec(km["e"].(string))
		pub = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	}
	if pub == nil {
		return nil, fmt.Errorf("no key %q in the JWKS", hdr.Kid)
	}
	sig, err := dec(parts[2])
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	cb, _ := dec(parts[1])
	var c map[string]any
	if err := json.Unmarshal(cb, &c); err != nil {
		return nil, err
	}
	if c["iss"] != wantIss {
		return nil, fmt.Errorf("iss %v, want %s", c["iss"], wantIss)
	}
	if c["aud"] != wantAud {
		return nil, fmt.Errorf("aud %v, want %s", c["aud"], wantAud)
	}
	if exp, _ := c["exp"].(float64); int64(exp) <= now.Unix() {
		return nil, fmt.Errorf("expired")
	}
	return c, nil
}

// --- the happy path, end to end ------------------------------------------------------

func TestDevIdPAuthorizationCodeFlow(t *testing.T) {
	d := newDevIdPTestEnv(t)
	issuer := "https://af.example.com/dev-idp"
	verifier, challenge := pkcePair()
	redirect := d.ownHost(3000) + "/api/auth/callback/google"

	rec := d.authorizeAs(t, "preview-user", d.authQuery(redirect, challenge))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Scheme+"://"+loc.Host+loc.Path != redirect {
		t.Fatalf("redirected to %q, want %q", rec.Header().Get("Location"), redirect)
	}
	if loc.Query().Get("state") != "st-1" || loc.Query().Get("iss") != issuer {
		t.Errorf("state / iss not passed through: %v", loc.Query())
	}
	code := loc.Query().Get("code")

	// client_secret_basic
	tok := d.exchange(code, redirect, verifier)
	if tok.Code != http.StatusOK {
		t.Fatalf("token: %d %s", tok.Code, tok.Body)
	}
	if cc := tok.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("token Cache-Control = %q", cc)
	}
	body := idpJSON(t, tok)
	claims, err := verifyIDToken(body["id_token"].(string), d.jwks(t), issuer, d.clientID, d.now())
	if err != nil {
		t.Fatalf("the ID token does not verify against the published JWKS: %v", err)
	}
	if claims["nonce"] != "nonce-1" || claims["af_dev"] != true || claims["azp"] != d.clientID {
		t.Errorf("claims: %v", claims)
	}
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat > 600 {
		t.Errorf("token lives %vs, want <= 600", exp-iat)
	}
	if _, has := claims["picture"]; has {
		t.Errorf("picture is not known and must be absent: %v", claims)
	}

	// userinfo with the access token
	req := httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPUserinfoPath, nil)
	req.Header.Set("Authorization", "Bearer "+body["access_token"].(string))
	ui := httptest.NewRecorder()
	d.mux.ServeHTTP(ui, req)
	if ui.Code != http.StatusOK {
		t.Fatalf("userinfo: %d %s", ui.Code, ui.Body)
	}
	if got := idpJSON(t, ui); got["sub"] != claims["sub"] || got["af_dev"] != true {
		t.Errorf("userinfo = %v, want the ID token's subject", got)
	}
	// No bearer, no claims.
	ui = httptest.NewRecorder()
	d.mux.ServeHTTP(ui, httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPUserinfoPath, nil))
	if ui.Code != http.StatusUnauthorized {
		t.Errorf("userinfo without a token: %d", ui.Code)
	}

	// client_secret_post works too
	code2 := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
	post := d.tokenReq(url.Values{"grant_type": {"authorization_code"}, "code": {code2}, "redirect_uri": {redirect},
		"client_id": {d.clientID}, "client_secret": {d.secret}}, "", "")
	if post.Code != http.StatusOK {
		t.Fatalf("client_secret_post (and no PKCE, a confidential client): %d %s", post.Code, post.Body)
	}

	// Every issuance is audited, and the audit never carries a token.
	rows, _ := d.mgr.store.ListAuditByTenant(context.Background(), d.ws.TenantID, 100)
	n := 0
	for _, r := range rows {
		if r.Action == "workspace.dev_idp_token" {
			n++
			if strings.Contains(r.Detail, body["access_token"].(string)) || strings.Contains(r.Detail, body["id_token"].(string)) {
				t.Errorf("audit detail carries a token: %q", r.Detail)
			}
		}
	}
	if n != 2 {
		t.Errorf("token audit rows = %d, want 2", n)
	}
}

func TestDevIdPDiscoveryDocument(t *testing.T) {
	d := newDevIdPTestEnv(t)
	rec := d.console(t, "https://af.example.com"+devIdPDiscoveryPath)
	doc := idpJSON(t, rec)
	iss := "https://af.example.com/dev-idp"
	for k, want := range map[string]string{
		"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
		"userinfo_endpoint": iss + "/userinfo", "jwks_uri": iss + "/jwks",
	} {
		if doc[k] != want {
			t.Errorf("%s = %v, want %s", k, doc[k], want)
		}
	}
	if algs := fmt.Sprint(doc["id_token_signing_alg_values_supported"]); algs != "[RS256]" {
		t.Errorf("signing algs = %s", algs)
	}
}

// --- enablement -------------------------------------------------------------------------

func TestDevIdPDisabledIsA404Everywhere(t *testing.T) {
	d := newDevIdPTestEnv(t)
	setDevIdPDeployment(t, d.mgr, false)
	for _, p := range []string{devIdPDiscoveryPath, devIdPJWKSPath, devIdPAuthorizePath, devIdPTokenPath, devIdPUserinfoPath} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
			rec := httptest.NewRecorder()
			d.mux.ServeHTTP(rec, httptest.NewRequest(m, "https://af.example.com"+p, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s with the deployment flag off: %d, want 404", m, p, rec.Code)
			}
		}
	}
	// And no env reaches the container.
	if env := d.mgr.devIdPEnv(context.Background(), d.ws); len(env) != 0 {
		t.Errorf("env injected while disabled: %v", env)
	}
}

// There is no per-workspace switch: a workspace whose stored settings still say devIdp:false
// signs in, and a PUT of the removed field is accepted, ignored and no longer reported.
func TestDevIdPServesAWorkspaceWithoutAnOptIn(t *testing.T) {
	d := newDevIdPTestEnv(t)
	ctx := context.Background()
	if err := d.mgr.store.SetWorkspaceSettings(ctx, d.ws.ID, `{"devIdp":false}`); err != nil {
		t.Fatal(err)
	}
	if got := d.signInWith(t, d.clientID, d.secret); got != http.StatusOK {
		t.Fatalf("sign-in for a workspace with devIdp:false = %d", got)
	}
	for _, body := range []string{`{"devIdp":true}`, `{"devIdp":false}`} {
		rec := httptest.NewRecorder()
		d.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "https://af.example.com/api/env/ws-settings", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rec.Code, rec.Body)
		}
		if b := idpJSON(t, rec); b["devIdp"] != nil || b["devIdpAvailable"] != true {
			t.Errorf("after PUT %s: %v", body, b)
		}
		if got := d.signInWith(t, d.clientID, d.secret); got != http.StatusOK {
			t.Errorf("sign-in after PUT %s = %d", body, got)
		}
	}
}

func TestDevIdPNothingWhereTheDeploymentDoesNotServeIt(t *testing.T) {
	for name, off := range map[string]func(*testing.T, *devIdPTestEnv){
		"switched off": func(t *testing.T, e *devIdPTestEnv) { setDevIdPDeployment(t, e.mgr, false) },
		"locked":       func(_ *testing.T, e *devIdPTestEnv) { e.mgr.devIdPEnvOff = true },
		"no base URL":  func(_ *testing.T, e *devIdPTestEnv) { e.mgr.publicBaseURL = "" },
	} {
		e := newDevIdPTestEnv(t)
		off(t, e)
		if _, _, ok := envCreds(e); ok {
			t.Errorf("%s: env injected", name)
		}
		if got := e.signInWith(t, e.clientID, e.secret); got == http.StatusOK {
			t.Errorf("%s: sign-in worked", name)
		}
	}
}

func TestDevIdPEnvInjection(t *testing.T) {
	d := newDevIdPTestEnv(t)
	env := d.mgr.devIdPEnv(context.Background(), d.ws)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["AF_DEV_IDP_ISSUER"] != "https://af.example.com/dev-idp" ||
		got["AF_DEV_IDP_WELL_KNOWN"] != "https://af.example.com/dev-idp/.well-known/openid-configuration" ||
		got["AF_DEV_IDP_CLIENT_ID"] != d.clientID || got["AF_DEV_IDP_CLIENT_SECRET"] != d.secret {
		t.Errorf("env = %v", got)
	}
	// The ws-settings answer names the client but never the secret.
	rec := httptest.NewRecorder()
	d.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://af.example.com/api/env/ws-settings", nil))
	if strings.Contains(rec.Body.String(), d.secret) {
		t.Error("ws-settings returned the client secret")
	}
	if b := idpJSON(t, rec); b["devIdpClientId"] != d.clientID {
		t.Errorf("ws-settings = %v", b)
	}
}

// --- redirect_uri ACL -------------------------------------------------------------------

func TestDevIdPRedirectURIAllowList(t *testing.T) {
	d := newDevIdPTestEnv(t)
	host3000 := previewHostname(d.slug, 3000, d.domain)
	other := d.addViewer(t, "mallory")
	otherSlug, err := d.mgr.rotatePreviewSlug(context.Background(), store.Workspace{ID: "ws-mallory",
		TenantID: d.ws.TenantID, MembershipID: other.membershipID})
	if err != nil {
		t.Fatal(err)
	}

	allowed := []string{
		"https://" + host3000 + "/api/auth/callback/google",
		"https://" + previewHostname(d.slug, 8080, d.domain) + "/login/oauth2/code/google", // 8080 is on the default list
		"http://localhost:3000/cb",
		"http://127.0.0.1:5173/",
		"http://localhost:8080/login/oauth2/code/google?x=1",
	}
	refused := map[string]string{
		"https://" + previewHostname(d.slug, 9999, d.domain) + "/cb":                 "port not on the preview list",
		"https://" + previewHostname(otherSlug, 3000, d.domain) + "/cb":              "another member's slug",
		"https://" + previewHostname("aaaaaaaaaaaaaaaaaaaa", 3000, d.domain) + "/cb": "a slug that is not current",
		"https://" + strings.ToUpper(host3000) + "/cb":                               "upper-case host",
		"https://" + host3000 + "./cb":                                               "trailing dot",
		"https://" + host3000 + ":443/cb":                                            "explicit port",
		"https://evil@" + host3000 + "/cb":                                           "userinfo before the host",
		"https://" + host3000 + "@evil.example/cb":                                   "preview host as userinfo",
		"https://" + host3000 + ".evil.example/cb":                                   "suffix trick",
		"https://evil.example/" + host3000:                                           "host in the path",
		"https://" + host3000 + "/cb#frag":                                           "fragment",
		"https://" + host3000 + `/\@evil.example/`:                                   "backslash",
		"http://" + host3000 + "/cb":                                                 "preview over http",
		"https://af.example.com/cb":                                                  "the Console origin itself",
		"//" + host3000 + "/cb":                                                      "scheme-relative",
		"javascript:alert(1)":                                                        "javascript:",
		"https://localhost:3000/cb":                                                  "localhost over https",
		"http://localhost/cb":                                                        "localhost without a port",
		"http://localhost:/cb":                                                       "empty port",
		"http://localhost:03000/cb":                                                  "non-canonical port",
		"http://localhost:99999/cb":                                                  "port out of range",
		"http://localhost:3000.evil.example/cb":                                      "localhost prefix",
		"http://localhost.evil.example:3000/cb":                                      "localhost prefix 2",
		"http://localhost:3000@evil.example/cb":                                      "localhost as userinfo",
		"http://LOCALHOST:3000/cb":                                                   "upper-case localhost",
		"http://localhost.:3000/cb":                                                  "localhost with trailing dot",
		"http://0.0.0.0:3000/cb":                                                     "0.0.0.0",
		"http://[::1]:3000/cb":                                                       "ipv6 loopback is not on the list",
		"http://127.0.0.2:3000/cb":                                                   "another loopback address",
		"":                                                                           "empty",
	}
	for _, uri := range allowed {
		rec := d.authorizeAs(t, "preview-user", d.authQuery(uri, ""))
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), strings.SplitN(uri, "?", 2)[0]) {
			t.Errorf("allowed %q: %d %s loc=%q", uri, rec.Code, rec.Body, rec.Header().Get("Location"))
		}
	}
	for uri, why := range refused {
		rec := d.authorizeAs(t, "preview-user", d.authQuery(uri, ""))
		// Refused means shown to the person, never redirected: no Location at all.
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s (%q): %d loc=%q, want a 400 with no redirect", why, uri, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestDevIdPRedirectAfterARestartUsesTheNewSlug(t *testing.T) {
	d := newDevIdPTestEnv(t)
	old := d.ownHost(3000) + "/cb"
	if rec := d.authorizeAs(t, "preview-user", d.authQuery(old, "")); rec.Code != http.StatusFound {
		t.Fatalf("current slug refused: %d", rec.Code)
	}
	fresh := d.mintSlug(t) // a restart mints a new slug
	if rec := d.authorizeAs(t, "preview-user", d.authQuery(old, "")); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Errorf("the old slug after a restart: %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	d.slug = fresh
	if rec := d.authorizeAs(t, "preview-user", d.authQuery(d.ownHost(3000)+"/cb", "")); rec.Code != http.StatusFound {
		t.Errorf("the new slug refused: %d", rec.Code)
	}
}

// A code minted for one slug cannot be redeemed after a restart moved the workspace: the
// permission is decided again at the token step.
func TestDevIdPCodeDiesWithTheSlugItWasIssuedFor(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	code := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
	d.mintSlug(t)
	if rec := d.exchange(code, redirect, ""); rec.Code != http.StatusBadRequest || idpJSON(t, rec)["error"] != "invalid_grant" {
		t.Errorf("redeeming after a restart: %d %s", rec.Code, rec.Body)
	}
}

func TestDevIdPTenantSharedPreview(t *testing.T) {
	d := newDevIdPTestEnv(t)
	d.addViewer(t, "colleague")
	redirect := d.ownHost(3000) + "/cb"

	// Not shared: the colleague may not use this client at all.
	if rec := d.authorizeAs(t, "colleague", d.authQuery(redirect, "")); rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Errorf("colleague, sharing off: %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	rec := d.authorizeAs(t, "colleague", d.authQuery(redirect, ""))
	code := codeFrom(t, rec)
	tok := d.exchange(code, redirect, "")
	if tok.Code != http.StatusOK {
		t.Fatalf("colleague token: %d %s", tok.Code, tok.Body)
	}
	// The subject is the colleague's, not the owner's.
	owner := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
	oc, _ := verifyIDToken(idpJSON(t, d.exchange(owner, redirect, ""))["id_token"].(string), d.jwks(t), "https://af.example.com/dev-idp", d.clientID, d.now())
	cc, _ := verifyIDToken(idpJSON(t, tok)["id_token"].(string), d.jwks(t), "https://af.example.com/dev-idp", d.clientID, d.now())
	if oc["sub"] == cc["sub"] {
		t.Errorf("owner and colleague share a subject: %v", oc["sub"])
	}
	// Sharing off again closes it.
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = false })
	if rec := d.authorizeAs(t, "colleague", d.authQuery(redirect, "")); rec.Code != http.StatusBadRequest {
		t.Errorf("colleague after sharing was turned off: %d", rec.Code)
	}
}

func TestDevIdPTokenRedirectURIMustMatchExactly(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	for _, bad := range []string{redirect + "/", redirect + "?a=1", d.ownHost(8080) + "/cb", "http://localhost:3000/cb", ""} {
		code := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
		rec := d.exchange(code, bad, "")
		if rec.Code != http.StatusBadRequest || idpJSON(t, rec)["error"] != "invalid_grant" {
			t.Errorf("token with redirect_uri %q: %d %s", bad, rec.Code, rec.Body)
		}
	}
}

// --- codes ----------------------------------------------------------------------------

func TestDevIdPCodeReplay(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	code := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
	first := d.exchange(code, redirect, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first use: %d %s", first.Code, first.Body)
	}
	access := idpJSON(t, first)["access_token"].(string)

	if rec := d.exchange(code, redirect, ""); rec.Code != http.StatusBadRequest || idpJSON(t, rec)["error"] != "invalid_grant" {
		t.Errorf("replay: %d %s, want invalid_grant", rec.Code, rec.Body)
	}
	// …and the replay took the first use's access token with it.
	req := httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPUserinfoPath, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	d.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the access token survived a replay of its code: %d", rec.Code)
	}
}

func TestDevIdPCodeExpiresInAMinute(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	code := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
	d.advance(61 * time.Second)
	if rec := d.exchange(code, redirect, ""); rec.Code != http.StatusBadRequest || idpJSON(t, rec)["error"] != "invalid_grant" {
		t.Errorf("a 61-second-old code: %d %s", rec.Code, rec.Body)
	}
	if devIdPCodeTTL > 60*time.Second {
		t.Errorf("code TTL %v exceeds a minute", devIdPCodeTTL)
	}
}

func TestDevIdPCodeIsBoundToItsClient(t *testing.T) {
	d := newDevIdPTestEnv(t)
	// A second workspace with its own registration (the colleague's).
	v := d.addViewer(t, "colleague")
	ctx := context.Background()
	vws, _, _ := d.mgr.store.GetWorkspaceByMembership(ctx, v.membershipID)
	if err := d.mgr.store.SetWorkspaceSettings(ctx, vws.ID, `{"previewTenantShare":true}`); err != nil {
		t.Fatal(err)
	}
	otherID, otherSecret, err := d.mgr.ensureDevIdPClient(ctx, vws)
	if err != nil {
		t.Fatal(err)
	}
	// Both workspaces share with the tenant and localhost is allowed for both, so the redirect
	// ACL alone cannot be what refuses the foreign client: the binding has to.
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	redirect := "http://localhost:3000/cb"
	code := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
	rec := d.tokenReq(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}}, otherID, otherSecret)
	if rec.Code != http.StatusBadRequest || idpJSON(t, rec)["error"] != "invalid_grant" {
		t.Errorf("another client redeeming the code: %d %s", rec.Code, rec.Body)
	}
	// It is spent for the rightful client too.
	if rec := d.exchange(code, redirect, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("rightful client after the foreign attempt: %d", rec.Code)
	}
}

// --- client authentication ------------------------------------------------------------

func TestDevIdPClientAuthentication(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	form := func(code string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}}
	}
	fresh := func() string { return codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, ""))) }

	if rec := d.tokenReq(form(fresh()), d.clientID, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret (basic): %d", rec.Code)
	}
	f := form(fresh())
	f.Set("client_id", d.clientID)
	f.Set("client_secret", "wrong")
	if rec := d.tokenReq(f, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret (post): %d", rec.Code)
	}
	if rec := d.tokenReq(form(fresh()), "afdev-nope", d.secret); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown client: %d", rec.Code)
	}
	if rec := d.tokenReq(form(fresh()), "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", rec.Code)
	}
	// Two methods at once is a malformed request, not a pick-the-better-one.
	f = form(fresh())
	f.Set("client_secret", d.secret)
	if rec := d.tokenReq(f, d.clientID, d.secret); rec.Code != http.StatusUnauthorized {
		t.Errorf("both methods: %d", rec.Code)
	}
	// The secret in the query string is not a credential.
	req := httptest.NewRequest(http.MethodPost, "https://af.example.com"+devIdPTokenPath+"?client_id="+d.clientID+"&client_secret="+d.secret,
		strings.NewReader(form(fresh()).Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	d.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("secret in the URL: %d", rec.Code)
	}
	if rec := d.tokenReq(url.Values{"grant_type": {"password"}}, d.clientID, d.secret); rec.Code != http.StatusBadRequest {
		t.Errorf("password grant: %d", rec.Code)
	}
}

func TestDifferentVerifier(t *testing.T) {
	for _, v := range []string{"xAAA", "yAAA", "AAAA", "x", "-_x"} {
		got := differentVerifier(v)
		if got == v || len(got) != len(v) || got[1:] != v[1:] {
			t.Errorf("differentVerifier(%q) = %q, want same length and tail, different value", v, got)
		}
	}
}

// --- PKCE -----------------------------------------------------------------------------

func TestDevIdPPKCE(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	verifier, challenge := pkcePair()
	otherVerifier, _ := pkcePair()
	issue := func() string { return codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, challenge))) }

	cases := map[string]string{
		"missing verifier": "",
		"wrong verifier":   otherVerifier,
		"short verifier":   "short",
	}
	for name, v := range cases {
		if rec := d.exchange(issue(), redirect, v); rec.Code != http.StatusBadRequest || idpJSON(t, rec)["error"] != "invalid_grant" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := d.exchange(issue(), redirect, verifier); rec.Code != http.StatusOK {
		t.Errorf("right verifier: %d %s", rec.Code, rec.Body)
	}
	// A verifier for a request that had no challenge is a downgrade probe.
	plain := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))
	if rec := d.exchange(plain, redirect, verifier); rec.Code != http.StatusBadRequest {
		t.Errorf("verifier without a challenge: %d", rec.Code)
	}

	// Only S256 is accepted, and the error goes back to the (vetted) redirect_uri.
	q := d.authQuery(redirect, challenge)
	q.Set("code_challenge_method", "plain")
	rec := d.authorizeAs(t, "preview-user", q)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Query().Get("error") != "invalid_request" || loc.Query().Get("code") != "" || loc.Query().Get("state") != "st-1" {
		t.Errorf("plain challenge: %d %v", rec.Code, rec.Header().Get("Location"))
	}
	q = d.authQuery(redirect, "")
	q.Set("code_challenge", challenge) // no method
	if rec := d.authorizeAs(t, "preview-user", q); !strings.Contains(rec.Header().Get("Location"), "error=invalid_request") {
		t.Errorf("challenge without method: %v", rec.Header().Get("Location"))
	}
}

func TestDevIdPAuthorizeProtocolErrors(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	for name, mut := range map[string]func(url.Values){
		"implicit flow":    func(q url.Values) { q.Set("response_type", "id_token") },
		"no openid scope":  func(q url.Values) { q.Set("scope", "email") },
		"form_post":        func(q url.Values) { q.Set("response_mode", "form_post") },
		"fragment mode":    func(q url.Values) { q.Set("response_mode", "fragment") },
		"scope lookalike":  func(q url.Values) { q.Set("scope", "openidx") },
		"missing response": func(q url.Values) { q.Del("response_type") },
	} {
		q := d.authQuery(redirect, "")
		mut(q)
		rec := d.authorizeAs(t, "preview-user", q)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusFound || loc.Query().Get("error") == "" || loc.Query().Get("code") != "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// --- claims -------------------------------------------------------------------------------

func TestDevIdPClaims(t *testing.T) {
	d := newDevIdPTestEnv(t)
	ctx := context.Background()
	redirect := d.ownHost(3000) + "/cb"
	claimsFor := func(scope string) map[string]any {
		q := d.authQuery(redirect, "")
		q.Set("scope", scope)
		tok := d.exchange(codeFrom(t, d.authorizeAs(t, "preview-user", q)), redirect, "")
		c, err := verifyIDToken(idpJSON(t, tok)["id_token"].(string), d.jwks(t), "https://af.example.com/dev-idp", d.clientID, d.now())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ident, err := d.mgr.store.UpsertIdentity(ctx, "alice@corp.example", "preview-user", "")
	if err != nil {
		t.Fatal(err)
	}

	// No Google link: a stable per-person subject that cannot be a Google one.
	c := claimsFor("openid email profile")
	sub, _ := c["sub"].(string)
	if !strings.HasPrefix(sub, "af-") || c["email"] != "alice@corp.example" || c["email_verified"] != true || c["name"] != "alice" {
		t.Errorf("claims without a Google link: %v", c)
	}
	if _, has := c["hd"]; has {
		t.Errorf("hd without a Google identity: %v", c)
	}
	if again := claimsFor("openid"); again["sub"] != sub {
		t.Errorf("the subject is not stable: %v vs %v", again["sub"], sub)
	}
	// Scopes bound the claims.
	if min := claimsFor("openid"); min["email"] != nil || min["name"] != nil || min["sub"] == nil {
		t.Errorf("openid alone leaked profile claims: %v", min)
	}
	if em := claimsFor("openid email"); em["email"] == nil || em["name"] != nil {
		t.Errorf("openid email: %v", em)
	}

	// A linked Google identity brings the real subject and address, and never an hd: a non-gmail address does not prove a Workspace account.
	if err := d.mgr.store.AttachProvider(ctx, ident.ID, store.IdentityLink{Provider: "google", Subject: "109876543210",
		Realm: googleRealm, Email: "alice@corp.example"}); err != nil {
		t.Fatal(err)
	}
	c = claimsFor("openid email profile")
	if c["sub"] != "109876543210" || c["email"] != "alice@corp.example" || c["hd"] != nil {
		t.Errorf("claims with a Google link: %v", c)
	}
	// A consumer Google account on a gmail.com address, likewise.
	bob, err := d.mgr.store.UpsertIdentity(ctx, "bob@gmail.com", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.mgr.store.AttachProvider(ctx, bob.ID, store.IdentityLink{Provider: "google", Subject: "5550001",
		Realm: googleRealm, Email: "bob@gmail.com"}); err != nil {
		t.Fatal(err)
	}
	cl := devIdPClaims(ctx, d.mgr.store, bob)
	if strings.Contains(devIdPProfileJSON(cl), `"hd"`) || cl.Sub != "5550001" {
		t.Errorf("a gmail.com account: %v", cl)
	}
}

// --- the tokens belong to this IdP, this workspace --------------------------------------

func TestDevIdPTokenVerifiesOnlyAgainstThisIssuerAndClient(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	tok := d.exchange(codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, ""))), redirect, "")
	idToken := idpJSON(t, tok)["id_token"].(string)
	keys := d.jwks(t)
	iss := "https://af.example.com/dev-idp"

	if _, err := verifyIDToken(idToken, keys, iss, d.clientID, d.now()); err != nil {
		t.Fatalf("control: %v", err)
	}
	if _, err := verifyIDToken(idToken, keys, "https://accounts.google.com", d.clientID, d.now()); err == nil {
		t.Error("the token verifies against Google's issuer")
	}
	if _, err := verifyIDToken(idToken, keys, iss, "afdev-another-workspace", d.now()); err == nil {
		t.Error("the token verifies for another workspace's client")
	}
	// A relying party that really fetched Google's keys has none with this kid.
	if _, err := verifyIDToken(idToken, map[string]any{"keys": []any{}}, "https://accounts.google.com", d.clientID, d.now()); err == nil {
		t.Error("the token verifies without our key")
	}
	if _, err := verifyIDToken(idToken, keys, iss, d.clientID, d.now().Add(11*time.Minute)); err == nil {
		t.Error("the token is still valid after 11 minutes")
	}
	// Tampering with the claims breaks the signature.
	parts := strings.Split(idToken, ".")
	forged, _ := json.Marshal(map[string]any{"iss": iss, "aud": d.clientID, "sub": "someone-else", "exp": d.now().Add(time.Hour).Unix()})
	parts[1] = base64.RawURLEncoding.EncodeToString(forged)
	if _, err := verifyIDToken(strings.Join(parts, "."), keys, iss, d.clientID, d.now()); err == nil {
		t.Error("a token with edited claims verifies")
	}
}

// --- the preview host serves none of it ---------------------------------------------------

func TestDevIdPIsNotServedOnAPreviewHost(t *testing.T) {
	d := newDevIdPTestEnv(t)
	// The preview dispatcher in front of the real Console handler, as main wires it.
	host := newPreviewHostAPI(d.cfg).dispatch(d.mux)
	for _, p := range []string{devIdPDiscoveryPath, devIdPJWKSPath, devIdPAuthorizePath, devIdPTokenPath, devIdPUserinfoPath} {
		for _, port := range []int{3000, 9999} {
			h := previewHostname(d.slug, port, d.domain)
			req := httptest.NewRequest(http.MethodGet, "https://"+h+p, nil)
			req.Host = h
			rec := httptest.NewRecorder()
			host.ServeHTTP(rec, req)
			// An unauthenticated visitor is sent to sign in (non-HTML: a 401 / 404), and never sees
			// an IdP document.
			if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "jwks_uri") || strings.Contains(rec.Body.String(), `"keys"`) {
				t.Errorf("%s on a preview host (port %d): %d %s", p, port, rec.Code, rec.Body)
			}
		}
	}
	// With the owner's cookie the request is RELAYED to the app (the fake Agent), still not
	// answered by the CP: the relay target here is closed, so the CP's own page is a 502.
	ck := d.viewerCookie(t, previewViewer{userKey: "preview-user"}, d.slug, 3000)
	h := previewHostname(d.slug, 3000, d.domain)
	req := httptest.NewRequest(http.MethodGet, "https://"+h+devIdPJWKSPath, nil)
	req.Host = h
	req.AddCookie(ck)
	rec := httptest.NewRecorder()
	host.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `"keys"`) {
		t.Errorf("the JWKS was served on a preview host: %s", rec.Body)
	}
}

// --- the signing key ---------------------------------------------------------------------

func TestDevIdPSigningKeyNeverLeaks(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	tok := d.exchange(codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, ""))), redirect, "")
	// Provoke the error paths too.
	d.exchange("afdevcode-nope", redirect, "")
	d.tokenReq(url.Values{"grant_type": {"authorization_code"}}, d.clientID, "wrong")

	keys, err := d.mgr.store.ListDevIdPKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys: %v %v", keys, err)
	}
	// The plaintext of the private key, which only the custodian can produce.
	plain, err := d.mgr.openTenantSecret(context.Background(), keys[0].PrivateEnc, keys[0].KeyRef)
	if err != nil || len(plain) < 100 {
		t.Fatalf("unseal: %v", err)
	}
	if keys[0].PrivateEnc == plain || keys[0].KeyRef != devIdPKeyRef {
		t.Errorf("the private key is not stored sealed (key_ref=%q)", keys[0].KeyRef)
	}
	audit, _ := d.mgr.store.ListAuditByTenant(context.Background(), d.ws.TenantID, 1000)
	var auditText strings.Builder
	for _, a := range audit {
		auditText.WriteString(a.Action + a.Target + a.Detail + a.ActorID)
	}
	secretPart := plain[len(plain)/2 : len(plain)/2+40] // a slice from the middle of the DER
	for name, text := range map[string]string{
		"token response": tok.Body.String(), "jwks": jsonString(d.jwks(t)), "log": logs.String(),
		"audit": auditText.String(), "discovery": d.console(t, "https://af.example.com"+devIdPDiscoveryPath).Body.String(),
	} {
		if strings.Contains(text, secretPart) || strings.Contains(text, "PRIVATE KEY") {
			t.Errorf("%s contains key material", name)
		}
	}
	// The JWKS carries the public parameters only.
	for _, k := range d.jwks(t)["keys"].([]any) {
		for _, private := range []string{"d", "p", "q", "dp", "dq", "qi"} {
			if _, has := k.(map[string]any)[private]; has {
				t.Errorf("the JWKS carries the private parameter %q", private)
			}
		}
	}
}

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestDevIdPKeyRotation(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	issue := func() string {
		return idpJSON(t, d.exchange(codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, ""))), redirect, ""))["id_token"].(string)
	}
	iss := "https://af.example.com/dev-idp"
	issued := d.now()
	before := issue()

	d.advance(devIdPKeyRotateAfter + time.Hour)
	after := issue()
	keys := d.jwks(t)["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("after rotation the JWKS has %d keys, want the new one and the retired one", len(keys))
	}
	if _, err := verifyIDToken(after, d.jwks(t), iss, d.clientID, d.now()); err != nil {
		t.Errorf("a token signed after rotation: %v", err)
	}
	// The token signed by the retired key was already expired by then, but its key is still
	// published: verifying it with a clock set back shows the key is still there.
	if _, err := verifyIDToken(before, d.jwks(t), iss, d.clientID, issued); err != nil {
		t.Errorf("a token from before the rotation no longer verifies against the JWKS: %v", err)
	}
	hdr := func(tok string) string {
		b, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
		return string(b)
	}
	if hdr(before) == hdr(after) {
		t.Error("the key did not change at rotation")
	}

	// A retired key is dropped once no token it signed can be alive.
	d.advance(devIdPKeyRetainFor + time.Hour)
	d.api.keys.mu.Lock()
	d.api.keys.cached = nil // the one-minute cache would otherwise hide the sweep
	d.api.keys.mu.Unlock()
	if keys := d.jwks(t)["keys"].([]any); len(keys) != 1 {
		t.Errorf("the retired key was not dropped: %d keys", len(keys))
	}
}

// --- review round 1 ------------------------------------------------------------------------

// auditHold blocks the token endpoint between "code spent" and "token stored" by parking the
// audit write, so a replay can land inside that window.
type auditHold struct {
	store.Store
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *auditHold) InsertAudit(ctx context.Context, a store.AuditLog) error {
	if a.Action == "workspace.dev_idp_token" {
		h.once.Do(func() {
			close(h.reached)
			<-h.release
		})
	}
	return h.Store.InsertAudit(ctx, a)
}

// A replay that lands while the first redemption is still between consuming the code and
// storing its token must still leave no live token.
func TestDevIdPReplayDuringTheFirstRedemptionLeavesNoLiveToken(t *testing.T) {
	d := newDevIdPTestEnv(t)
	hold := &auditHold{Store: d.mgr.store, reached: make(chan struct{}), release: make(chan struct{})}
	d.mgr.store = hold
	redirect := d.ownHost(3000) + "/cb"
	code := codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, "")))

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- d.exchange(code, redirect, "") }()
	<-hold.reached
	if rec := d.exchange(code, redirect, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("the replay: %d %s", rec.Code, rec.Body)
	}
	close(hold.release)
	rec := <-first
	if rec.Code == http.StatusOK {
		req := httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPUserinfoPath, nil)
		req.Header.Set("Authorization", "Bearer "+idpJSON(t, rec)["access_token"].(string))
		ui := httptest.NewRecorder()
		d.mux.ServeHTTP(ui, req)
		if ui.Code == http.StatusOK {
			t.Fatal("a token issued by the first redemption is live after the code was replayed")
		}
	}
}

func TestDevIdPDisabledIsA404BehindTheAuthGateToo(t *testing.T) {
	cfg := oauthTestConfig(t)
	cfg.mgr.publicBaseURL = "https://af.example.com"
	cfg.mgr.previewDomain = "pv.example.com" // the IdP is offered only with preview subdomains
	cfg.mgr.store = brandTestStore(t)
	setDevIdPDeployment(t, cfg.mgr, false)
	mux := http.NewServeMux()
	newDevIdPAPI(cfg).mount(mux)
	gate := cfg.authGate(mux)
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		for _, accept := range []string{"text/html", "application/json"} {
			req := httptest.NewRequest(m, "https://af.example.com"+devIdPAuthorizePath+"?client_id=x", nil)
			req.Header.Set("Accept", accept)
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("signed-out %s authorize (%s) with the IdP off: %d, want 404", m, accept, rec.Code)
			}
		}
	}
	// Switched on, a signed-out visitor meets the login as before.
	setDevIdPDeployment(t, cfg.mgr, true)
	req := httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPAuthorizePath, nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	cfg.authGate(mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("signed-out authorize with the IdP on: %d %q, want a redirect to /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAccessLogRedactsCredentialsInTheQuery(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	for _, target := range []string{
		devIdPTokenPath + "?client_secret=SENTINEL-SECRET&grant_type=authorization_code",
		devIdPAuthorizePath + "?code=SENTINEL-SECRET",
		"/api/auth/callback/google?code=SENTINEL-SECRET&state=SENTINEL-SECRET&scope=openid",
		"/__af/preview-auth?t=SENTINEL-SECRET&next=/",
		"/x?Access_Token=SENTINEL-SECRET&id_token=SENTINEL-SECRET",
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, target, nil))
	}
	if strings.Contains(logs.String(), "SENTINEL-SECRET") {
		t.Errorf("the access log kept a credential:\n%s", logs.String())
	}
	// An ordinary query is still logged.
	logs.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/audit?limit=5", nil))
	if !strings.Contains(logs.String(), "/api/audit?limit=5") {
		t.Errorf("an ordinary query was not logged: %s", logs.String())
	}
}

func TestDevIdPRefusesReauthenticationItCannotDo(t *testing.T) {
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	for name, kv := range map[string][2]string{
		"prompt=login": {"prompt", "login"}, "prompt=consent": {"prompt", "consent"},
		"max_age=0": {"max_age", "0"}, "max_age=300": {"max_age", "300"},
	} {
		q := d.authQuery(redirect, "")
		q.Set(kv[0], kv[1])
		rec := d.authorizeAs(t, "preview-user", q)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusFound || loc.Query().Get("error") != "invalid_request" || loc.Query().Get("code") != "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	q := d.authQuery(redirect, "")
	q.Set("prompt", "none")
	codeFrom(t, d.authorizeAs(t, "preview-user", q)) // already signed in: fine
	// The ID token does not claim a re-authentication that did not happen.
	tok := d.exchange(codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, ""))), redirect, "")
	c, err := verifyIDToken(idpJSON(t, tok)["id_token"].(string), d.jwks(t), "https://af.example.com/dev-idp", d.clientID, d.now())
	if err != nil {
		t.Fatal(err)
	}
	if _, has := c["auth_time"]; has {
		t.Errorf("auth_time is present: %v", c)
	}
}
