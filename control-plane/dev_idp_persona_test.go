package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const personaAPI = "https://af.example.com/api/env/ws-settings/dev-idp/personas"

func (d *devIdPTestEnv) personaReq(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	d.mux.ServeHTTP(rec, req)
	return rec
}

func (d *devIdPTestEnv) addPersona(t *testing.T, handle, name string) {
	t.Helper()
	body, _ := jsonMarshal(map[string]string{"handle": handle, "name": name})
	if rec := d.personaReq(t, http.MethodPost, personaAPI, body); rec.Code != http.StatusCreated {
		t.Fatalf("create persona %q: %d %s", handle, rec.Code, rec.Body)
	}
}

func jsonMarshal(v any) (string, error) { return jsonString(v), nil }

func (d *devIdPTestEnv) personaID(t *testing.T, handle string) string {
	t.Helper()
	list, err := d.mgr.store.ListDevIdPPersonas(context.Background(), d.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Handle == handle {
			return p.ID
		}
	}
	t.Fatalf("no persona %q", handle)
	return ""
}

var ticketRE = regexp.MustCompile(`name="ticket" value="([^"]+)"`)

// askAs runs authorize and expects the consent page; it returns the page and its ticket.
func (d *devIdPTestEnv) askAs(t *testing.T, userKey string, q url.Values) (string, string) {
	t.Helper()
	rec := d.authorizeAs(t, userKey, q)
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Fatalf("authorize with personas: %d loc=%q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	m := ticketRE.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no ticket in the consent page: %s", rec.Body)
	}
	return rec.Body.String(), m[1]
}

func (d *devIdPTestEnv) postConsent(userKey string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	d.asUser(userKey, func() {
		req := httptest.NewRequest(http.MethodPost, "https://af.example.com"+devIdPConsentPath, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		d.mux.ServeHTTP(rec, req)
	})
	return rec
}

func (d *devIdPTestEnv) idTokenClaims(t *testing.T, code, redirect, verifier string) map[string]any {
	t.Helper()
	tok := d.exchange(code, redirect, verifier)
	if tok.Code != http.StatusOK {
		t.Fatalf("token: %d %s", tok.Code, tok.Body)
	}
	c, err := verifyIDToken(idpJSON(t, tok)["id_token"].(string), d.jwks(t), "https://af.example.com/dev-idp", d.clientID, d.now())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDevIdPPersonaHandleIsStrictAndNeverFolded(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	for _, h := range []string{
		"Admin", "ADMIN", "ａdmin" /* fullwidth a */, "аdmin" /* Cyrillic a */, "admin ", " admin", "ad min",
		"a@b.com", "admin@example.test", "admin‮", "admin\x00", "x/../y", "", ".dot", "-dash",
		strings.Repeat("a", 33), "ǅ", "ı", "İ", "K" /* Kelvin sign */, "ſ", /* long s */
	} {
		body := jsonString(map[string]string{"handle": h, "name": "x"})
		if rec := d.personaReq(t, http.MethodPost, personaAPI, body); rec.Code != http.StatusBadRequest {
			t.Errorf("handle %q: %d, want 400 (never folded into something else)", h, rec.Code)
		}
	}
	for _, name := range []string{"a\nb", "evil‮txt", "x​y", strings.Repeat("n", 65), "\x00"} {
		body := jsonString(map[string]string{"handle": "ok", "name": name})
		if rec := d.personaReq(t, http.MethodPost, personaAPI, body); rec.Code != http.StatusBadRequest {
			t.Errorf("name %q: %d, want 400", name, rec.Code)
		}
	}
	if list, _ := d.mgr.store.ListDevIdPPersonas(context.Background(), d.ws.ID); len(list) != 0 {
		t.Errorf("refused personas were stored: %v", list)
	}
}

func TestDevIdPPersonaCRUDAndLimits(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "Admin User")
	if rec := d.personaReq(t, http.MethodPost, personaAPI, `{"handle":"admin","name":"again"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate handle: %d", rec.Code)
	}
	rec := d.personaReq(t, http.MethodGet, personaAPI, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"admin@example.test"`) || !strings.Contains(rec.Body.String(), `"sub":"persona:admin"`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < devIdPPersonaMax-1; i++ {
		d.addPersona(t, "p"+string(rune('a'+i)), "")
	}
	if rec := d.personaReq(t, http.MethodPost, personaAPI, `{"handle":"one-too-many"}`); rec.Code != http.StatusConflict {
		t.Errorf("over the limit: %d", rec.Code)
	}
	id := d.personaID(t, "admin")
	if rec := d.personaReq(t, http.MethodDelete, personaAPI+"/"+id, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := d.personaReq(t, http.MethodDelete, personaAPI+"/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete twice: %d", rec.Code)
	}
	// Definitions and removals are audited.
	rows, _ := d.mgr.store.ListAuditByTenant(context.Background(), d.ws.TenantID, 200)
	adds, removes := 0, 0
	for _, r := range rows {
		if r.Action == "workspace.dev_idp_persona" {
			if strings.HasPrefix(r.Detail, "add ") {
				adds++
			} else if strings.HasPrefix(r.Detail, "remove ") {
				removes++
			}
		}
	}
	if adds != devIdPPersonaMax || removes != 1 {
		t.Errorf("audit rows add=%d remove=%d", adds, removes)
	}
}

func TestDevIdPPersonaAPIIs409WhereTheIdPIsNotServed(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	setDevIdPDeployment(t, d.mgr, false)
	if rec := d.personaReq(t, http.MethodPost, personaAPI, `{"handle":"admin"}`); rec.Code != http.StatusConflict {
		t.Errorf("create with the IdP off: %d", rec.Code)
	}
	if rec := d.personaReq(t, http.MethodGet, personaAPI, ""); rec.Code != http.StatusConflict {
		t.Errorf("list with the IdP off: %d", rec.Code)
	}
}

func TestDevIdPPersonaCannotBeARealMembersAddress(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	ctx := context.Background()
	// A real member (a test tenant that really uses example.test) and one with the address on a
	// linked sign-in only.
	if _, err := d.mgr.store.UpsertIdentity(ctx, "Taken@Example.Test", "taken-user", ""); err != nil {
		t.Fatal(err)
	}
	if rec := d.personaReq(t, http.MethodPost, personaAPI, `{"handle":"taken"}`); rec.Code != http.StatusConflict {
		t.Errorf("persona on a real member's address: %d, want 409", rec.Code)
	}
	// Defence in depth: a row that got in anyway (written before the member existed) is refused
	// when it would be used.
	d.addPersona(t, "late", "Late")
	if _, err := d.mgr.store.UpsertIdentity(ctx, "late@example.test", "late-user", ""); err != nil {
		t.Fatal(err)
	}
	redirect := d.ownHost(3000) + "/cb"
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {d.personaID(t, "late")}}, nil)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Query().Get("code") != "" || loc.Query().Get("error") != "access_denied" {
		t.Errorf("persona colliding with a member at issue time: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDevIdPPersonaSubjectCannotEqualAMembersSubject(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	ctx := context.Background()
	ident, err := d.mgr.store.UpsertIdentity(ctx, "alice@corp.example", "preview-user", "")
	if err != nil {
		t.Fatal(err)
	}
	derived := devIdPClaims(ctx, d.mgr.store, ident).Sub
	for _, h := range []string{"admin", "af-" + strings.Repeat("0", 32), "109876543210987654321"} {
		p := devIdPPersonaProfile(store.DevIdPPersona{Handle: h})
		if !strings.HasPrefix(p.Sub, "persona:") || p.Sub == derived || strings.HasPrefix(derived, "persona:") {
			t.Errorf("persona %q: sub %q vs member %q", h, p.Sub, derived)
		}
		if !strings.HasSuffix(p.Email, "@example.test") || strings.Count(p.Email, "@") != 1 {
			t.Errorf("persona %q: email %q", h, p.Email)
		}
	}
}

func TestDevIdPConsentSignsInAsAPersonaAndAuditsIt(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "Admin User")
	d.addPersona(t, "viewer", "Viewer")
	redirect := d.ownHost(3000) + "/cb"
	verifier, challenge := pkcePair()
	page, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, challenge))
	for _, want := range []string{"admin@example.test", "viewer@example.test", "Admin User", `name="choice" value="self"`} {
		if !strings.Contains(page, want) {
			t.Errorf("consent page lacks %q", want)
		}
	}
	rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {d.personaID(t, "admin")}}, nil)
	code := codeFrom(t, rec)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("state") != "st-1" || loc.Query().Get("iss") != "https://af.example.com/dev-idp" ||
		loc.Host != strings.TrimPrefix(d.ownHost(3000), "https://") {
		t.Errorf("redirect: %s", loc)
	}
	// PKCE, nonce and redirect_uri from the original request still bind the code.
	if bad := d.exchange(code, redirect, "wrong-verifier-wrong-verifier-wrong-verifier-xx"); bad.Code == http.StatusOK {
		t.Fatal("a wrong PKCE verifier was accepted after consent")
	}
	// (that attempt spent the code) a fresh round trip with the right verifier:
	_, ticket = d.askAs(t, "preview-user", d.authQuery(redirect, challenge))
	code = codeFrom(t, d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {d.personaID(t, "admin")}}, nil))
	c := d.idTokenClaims(t, code, redirect, verifier)
	if c["sub"] != "persona:admin" || c["email"] != "admin@example.test" || c["name"] != "Admin User" ||
		c["email_verified"] != true || c["af_dev"] != true || c["nonce"] != "nonce-1" || c["aud"] != d.clientID {
		t.Errorf("persona claims: %v", c)
	}
	rows, _ := d.mgr.store.ListAuditByTenant(context.Background(), d.ws.TenantID, 100)
	found := false
	for _, r := range rows {
		if r.Action == "workspace.dev_idp_token" && strings.Contains(r.Detail, `sub="persona:admin"`) && strings.Contains(r.Detail, `persona="admin"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no token audit row names the persona: %+v", rows)
	}
}

func TestDevIdPConsentCanChooseMyself(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	redirect := d.ownHost(3000) + "/cb"
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	c := d.idTokenClaims(t, codeFrom(t, d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {"self"}}, nil)), redirect, "")
	if sub, _ := c["sub"].(string); !strings.HasPrefix(sub, "af-") {
		t.Errorf("choosing myself gave sub %q", sub)
	}
	rows, _ := d.mgr.store.ListAuditByTenant(context.Background(), d.ws.TenantID, 100)
	for _, r := range rows {
		if r.Action == "workspace.dev_idp_token" && strings.Contains(r.Detail, "persona=") {
			t.Errorf("a self sign-in is audited as a persona: %q", r.Detail)
		}
	}
}

func TestDevIdPPromptNoneStaysSilentAsMyself(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	redirect := d.ownHost(3000) + "/cb"
	q := d.authQuery(redirect, "")
	q.Set("prompt", "none")
	// Whatever the request tries to name, there is no way to ask for a persona silently.
	q.Set("persona", "admin")
	q.Set("login_hint", "admin@example.test")
	q.Set("choice", d.personaID(t, "admin"))
	c := d.idTokenClaims(t, codeFrom(t, d.authorizeAs(t, "preview-user", q)), redirect, "")
	if sub, _ := c["sub"].(string); !strings.HasPrefix(sub, "af-") {
		t.Errorf("prompt=none gave sub %q, want the member's own", sub)
	}
}

func TestDevIdPNoPersonasMeansNoConsentPage(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	redirect := d.ownHost(3000) + "/cb"
	codeFrom(t, d.authorizeAs(t, "preview-user", d.authQuery(redirect, ""))) // straight to a code, as before
}

func TestDevIdPConsentIsNotACSRFTarget(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	admin := d.personaID(t, "admin")
	redirect := d.ownHost(3000) + "/cb"

	// A cross-site POST has no ticket (the attacker cannot read the page), and cannot guess one.
	for name, form := range map[string]url.Values{
		"no ticket":     {"choice": {admin}},
		"empty ticket":  {"ticket": {""}, "choice": {admin}},
		"forged ticket": {"ticket": {"afdevask-" + devIdPRandom(32)}, "choice": {admin}},
		"a code as a ticket": {"ticket": {codeFrom(t, d.authorizeAs(t, "preview-user", func() url.Values {
			q := d.authQuery(redirect, "")
			q.Set("prompt", "none")
			return q
		}()))}, "choice": {admin}},
	} {
		if rec := d.postConsent("preview-user", form, nil); rec.Code == http.StatusFound {
			t.Errorf("%s: redirected to %s", name, rec.Header().Get("Location"))
		}
	}

	// Cross-site markers are refused before the ticket is touched, so the real form still works.
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	for name, hdr := range map[string]map[string]string{
		"foreign Origin":      {"Origin": "https://evil.example"},
		"null Origin":         {"Origin": "null"},
		"Sec-Fetch cross":     {"Sec-Fetch-Site": "cross-site"},
		"Sec-Fetch same-site": {"Sec-Fetch-Site": "same-site"},
	} {
		rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {admin}}, hdr)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}
	ok := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {admin}},
		map[string]string{"Origin": "https://af.example.com", "Sec-Fetch-Site": "same-origin"})
	codeFrom(t, ok)

	// Single use.
	if rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {admin}}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("replayed ticket: %d", rec.Code)
	}
	// Bound to the member the page was shown to.
	d.addViewer(t, "colleague")
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	_, ticket = d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	if rec := d.postConsent("colleague", url.Values{"ticket": {ticket}, "choice": {"self"}}, nil); rec.Code != http.StatusForbidden {
		t.Errorf("another member's ticket: %d, want 403", rec.Code)
	}
	// It dies with the page.
	_, ticket = d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	d.advance(devIdPConsentTTL + time.Second)
	if rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {admin}}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("expired ticket: %d", rec.Code)
	}
}

func TestDevIdPConsentGetIsNotAHandler(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	rec := d.console(t, "https://af.example.com"+devIdPConsentPath+"?ticket=x&choice=self")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET consent: %d, want 405 (a link must not be able to choose)", rec.Code)
	}
}

func TestDevIdPConsentCannotChooseAnotherWorkspacesPersona(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	otherWS := d.addViewer(t, "persona-other")
	other := store.DevIdPPersona{ID: "foreign-persona", WorkspaceID: "ws-" + otherWS.userKey, Handle: "root", CreatedAt: time.Now().Unix()}
	if err := d.mgr.store.CreateDevIdPPersona(context.Background(), other, 20); err != nil {
		t.Fatal(err)
	}
	redirect := d.ownHost(3000) + "/cb"
	page, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	if strings.Contains(page, "root@example.test") {
		t.Error("another workspace's persona is offered")
	}
	rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {other.ID}}, nil)
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
		t.Errorf("foreign persona: %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDevIdPConsentRedirectComesFromTheStoredRequest(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	redirect := d.ownHost(3000) + "/cb"
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	rec := d.postConsent("preview-user", url.Values{
		"ticket": {ticket}, "choice": {"self"},
		"redirect_uri": {"https://evil.example/steal"}, "state": {"forged"}, "next": {"https://evil.example"},
	}, nil)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Host != strings.TrimPrefix(d.ownHost(3000), "https://") || loc.Path != "/cb" || loc.Query().Get("state") != "st-1" {
		t.Errorf("form fields steered the redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDevIdPConsentRechecksTheRedirect(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	redirect := d.ownHost(3000) + "/cb"
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	// The port leaves the preview list while the page is open.
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewPorts = []int{8080} })
	rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {"self"}}, nil)
	if rec.Code == http.StatusFound {
		t.Errorf("a redirect that is no longer allowed was followed: %s", rec.Header().Get("Location"))
	}
	// And so does the deployment switch going off.
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewPorts = []int{3000, 8080} })
	_, ticket = d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	setDevIdPDeployment(t, d.mgr, false)
	if rec := d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {"self"}}, nil); rec.Code == http.StatusFound {
		t.Errorf("consent after the deployment switch was turned off: %s", rec.Header().Get("Location"))
	}
}

func TestDevIdPConsentPageEscapesAndRefusesFraming(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", `<script>alert(1)</script>"><img src=x>`)
	redirect := d.ownHost(3000) + "/cb"
	rec := d.authorizeAs(t, "preview-user", d.authQuery(redirect, ""))
	if strings.Contains(rec.Body.String(), "<script>") || strings.Contains(rec.Body.String(), "<img src=x") {
		t.Errorf("persona name reached the page unescaped: %s", rec.Body)
	}
	h := rec.Header()
	if h.Get("X-Frame-Options") != "DENY" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("consent page headers: %v", h)
	}
	// A policy that sends no Origin on the same-origin form POST (no-referrer makes it "null") is
	// refused by sameOrigin, so every submit would 403.
	if got := h.Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q, want same-origin", got)
	}
	// Chromium applies form-action to the redirect after the POST, so any value here that does not
	// list the app's origin blocks the return to the app (measured in headless Chromium).
	if strings.Contains(h.Get("Content-Security-Policy"), "form-action") {
		t.Errorf("CSP has form-action, which blocks the post-POST redirect to the app: %s", h.Get("Content-Security-Policy"))
	}
}

func TestDevIdPConsentIsNotServedOnAPreviewHost(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	host := newPreviewHostAPI(d.cfg).dispatch(d.mux)
	redirect := d.ownHost(3000) + "/cb"
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	ck := d.viewerCookie(t, previewViewer{userKey: "preview-user"}, d.slug, 3000)
	for _, withCookie := range []bool{false, true} {
		h := previewHostname(d.slug, 3000, d.domain)
		req := httptest.NewRequest(http.MethodPost, "https://"+h+devIdPConsentPath,
			strings.NewReader(url.Values{"ticket": {ticket}, "choice": {"self"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = h
		if withCookie {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		host.ServeHTTP(rec, req)
		if loc := rec.Header().Get("Location"); strings.Contains(loc, "code=") {
			t.Errorf("a code was issued through the preview host (cookie=%v): %s", withCookie, loc)
		}
	}
	// The ticket was never spent by the preview host: the Console origin still honours it.
	codeFrom(t, d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {"self"}}, nil))
}

func TestDevIdPConsentIsA404WhereTheIdPIsOff(t *testing.T) {
	t.Parallel()
	cfg := oauthTestConfig(t)
	cfg.mgr.publicBaseURL = "https://af.example.com"
	cfg.mgr.previewDomain = "pv.example.com" // the IdP is offered only with preview subdomains
	cfg.mgr.store = brandTestStore(t)
	setDevIdPDeployment(t, cfg.mgr, false)
	mux := http.NewServeMux()
	newDevIdPAPI(cfg).mount(mux)
	gate := cfg.authGate(mux)
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(m, "https://af.example.com"+devIdPConsentPath, nil)
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("signed-out %s consent with the IdP off: %d, want 404", m, rec.Code)
		}
	}
	// On, a signed-out POST is refused by the gate (no session), never handled.
	setDevIdPDeployment(t, cfg.mgr, true)
	rec := httptest.NewRecorder()
	cfg.authGate(mux).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "https://af.example.com"+devIdPConsentPath, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("signed-out consent POST with the IdP on: %d, want 401", rec.Code)
	}
}

func TestDevIdPPersonasGoWithTheWorkspace(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "")
	ctx := context.Background()
	if err := d.mgr.store.DeleteWorkspace(ctx, d.ws.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.mgr.store.ListDevIdPPersonas(ctx, d.ws.ID); len(list) != 0 {
		t.Errorf("personas outlived the workspace: %v", list)
	}
}

func TestDevIdPPersonaCollisionAfterTheCodeWasIssuedIsRefusedAtToken(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "later", "Later")
	redirect := d.ownHost(3000) + "/cb"
	issue := func() string {
		_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
		return codeFrom(t, d.postConsent("preview-user", url.Values{"ticket": {ticket}, "choice": {d.personaID(t, "later")}}, nil))
	}
	// A token issued while the address is free works, and its access token is served...
	tok := d.exchange(issue(), redirect, "")
	if tok.Code != http.StatusOK {
		t.Fatalf("baseline: %d %s", tok.Code, tok.Body)
	}
	access := idpJSON(t, tok)["access_token"].(string)
	userinfo := func() int {
		req := httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPUserinfoPath, nil)
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		d.mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if userinfo() != http.StatusOK {
		t.Fatal("baseline userinfo")
	}
	// ...then a real member takes the address after the code was issued (the 60 s window).
	code := issue()
	if _, err := d.mgr.store.UpsertIdentity(context.Background(), "later@example.test", "later-user", ""); err != nil {
		t.Fatal(err)
	}
	if rec := d.exchange(code, redirect, ""); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "id_token") {
		t.Errorf("a persona token was signed for a real member's address: %d %s", rec.Code, rec.Body)
	}
	if userinfo() != http.StatusUnauthorized {
		t.Error("userinfo kept serving a persona that now collides with a real member")
	}
}

func TestDevIdPConsentPageFollowsAcceptLanguage(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "Admin User")
	redirect := d.ownHost(3000) + "/cb"
	_, challenge := pkcePair()
	for _, c := range []struct{ header, lang, want, absent string }{
		{"en-US,en;q=0.9", "en", "Development sign-in", "開発用"},
		{"ja,en;q=0.8", "ja", "開発用サインイン", "Development sign-in"},
		{"", "ja", "開発用サインイン", "Development sign-in"},
	} {
		var rec *httptest.ResponseRecorder
		d.asUser("preview-user", func() {
			req := httptest.NewRequest(http.MethodGet, "https://af.example.com"+devIdPAuthorizePath+"?"+d.authQuery(redirect, challenge).Encode(), nil)
			if c.header != "" {
				req.Header.Set("Accept-Language", c.header)
			}
			rec = httptest.NewRecorder()
			d.mux.ServeHTTP(rec, req)
		})
		page := rec.Body.String()
		if !strings.Contains(page, `<html lang="`+c.lang+`">`) || !strings.Contains(page, c.want) || strings.Contains(page, c.absent) {
			t.Errorf("Accept-Language %q: want only %q text, got %s", c.header, c.lang, page)
		}
	}
}

func TestDevIdPConsentCancelReturnsAccessDeniedToTheVettedRedirect(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "Admin")
	redirect := d.ownHost(3000) + "/cb"
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))

	// The form cannot steer the redirect: only the parked request's URI and state count.
	form := url.Values{"ticket": {ticket}, "action": {"cancel"}, "redirect_uri": {"https://evil.example/cb"}, "state": {"forged"}}
	rec := d.postConsent("preview-user", form, map[string]string{"Origin": "https://af.example.com", "Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusFound {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	if loc.Scheme+"://"+loc.Host+loc.Path != redirect || q.Get("error") != "access_denied" || q.Get("state") != "st-1" || q.Get("code") != "" {
		t.Errorf("cancel redirect: %s", loc)
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("cancel redirect Referrer-Policy: %q", rec.Header().Get("Referrer-Policy"))
	}
	// The ticket is spent: neither a second cancel nor a continue gets anywhere.
	for _, f := range []url.Values{form, {"ticket": {ticket}, "choice": {"self"}}} {
		if rec := d.postConsent("preview-user", f, nil); rec.Code == http.StatusFound {
			t.Errorf("spent ticket redirected to %s", rec.Header().Get("Location"))
		}
	}
}

func TestDevIdPConsentCancelIsRefusedLikeContinue(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.addPersona(t, "admin", "Admin")
	redirect := d.ownHost(3000) + "/cb"
	cancel := func(ticket string) url.Values { return url.Values{"ticket": {ticket}, "action": {"cancel"}} }

	for name, ticket := range map[string]string{"none": "", "forged": "afdevask-" + devIdPRandom(32)} {
		if rec := d.postConsent("preview-user", cancel(ticket), nil); rec.Code == http.StatusFound {
			t.Errorf("%s ticket: redirected to %s", name, rec.Header().Get("Location"))
		}
	}
	_, ticket := d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	for name, hdr := range map[string]map[string]string{
		"foreign Origin": {"Origin": "https://evil.example"},
		"null Origin":    {"Origin": "null"},
		"cross-site":     {"Sec-Fetch-Site": "cross-site"},
	} {
		if rec := d.postConsent("preview-user", cancel(ticket), hdr); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}
	// Another member's ticket is refused (and spent: consumption precedes the member check).
	d.addViewer(t, "colleague")
	d.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	if rec := d.postConsent("colleague", cancel(ticket), nil); rec.Code == http.StatusFound {
		t.Errorf("another member's ticket redirected to %s", rec.Header().Get("Location"))
	}
	_, ticket = d.askAs(t, "preview-user", d.authQuery(redirect, ""))
	d.advance(devIdPConsentTTL + time.Second)
	if rec := d.postConsent("preview-user", cancel(ticket), nil); rec.Code == http.StatusFound {
		t.Errorf("expired ticket redirected to %s", rec.Header().Get("Location"))
	}
}

func TestDevIdPConsentPageIsBrandedAndEscaped(t *testing.T) {
	t.Parallel()
	d := newDevIdPTestEnv(t)
	d.cfg.brand.env, _ = buildBrand("violet", `<i>"stg"`)
	d.addPersona(t, "admin", `<b onmouseover=x>"Ünï`)
	rec := d.authorizeAs(t, "preview-user", d.authQuery(d.ownHost(3000)+"/cb", ""))
	body := rec.Body.String()
	for _, want := range []string{"#7c4dff", "&lt;i&gt;", "DEV IdP", `src="/brand/icon-192.png"`, ">:3000<", `value="cancel"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"<i>", "<b onmouseover"} {
		if strings.Contains(body, bad) {
			t.Errorf("unescaped %q reached the page", bad)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp !=
		"default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'" {
		t.Errorf("CSP = %q", csp)
	}
	// Without branding the shipped teal and no label.
	d.cfg.brand.env = brandConfig{}
	body = d.authorizeAs(t, "preview-user", d.authQuery(d.ownHost(3000)+"/cb", "")).Body.String()
	if !strings.Contains(body, "#149ba7") || strings.Contains(body, "#7c4dff") {
		t.Errorf("unbranded page accent:\n%s", body)
	}
}

// The page has no layout engine in the Go tests, so pin the two rules whose absence cut the DEV IdP
// badge off the card at 320px (measured in headless Chromium with a 16-letter brand label) and
// hid the test addresses' domain behind an ellipsis on phones.
func TestDevIdPConsentPageStaysReadableOnNarrowScreens(t *testing.T) {
	t.Parallel()
	css := devIdPConsentTmpl.Tree.Root.String() + devIdPShellTmpl.Lookup("devidp-shell-css").Tree.Root.String()
	if !strings.Contains(css, ".top{display:flex;flex-wrap:wrap;") {
		t.Error("the header must wrap so the badge stays inside the card")
	}
	if strings.Contains(css, "text-overflow:ellipsis") {
		t.Error("addresses must wrap, not be cut with an ellipsis")
	}
}
