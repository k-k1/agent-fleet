// mcp_oauth_authorize.go — the browser half of the /mcp authorization server: authorize vets the
// client and redirect, then either issues a code (prompt=none) or parks the request behind a
// single-use consent ticket; consent turns the member's choice of tenant and scope into a code.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// scopeLevel reads a stored scope string ("read" or "read write") as its highest tier.
func scopeLevel(scope string) int {
	n := 0
	for _, s := range strings.Fields(scope) {
		if s == scopeRead || s == scopeWrite {
			if r := scopeRank(s); r > n {
				n = r
			}
		}
	}
	return n
}

// scopeString is the wire form of a level: write includes read.
func scopeString(level int) string {
	if level >= scopeRank(scopeWrite) {
		return scopeRead + " " + scopeWrite
	}
	return scopeRead
}

// parseRequestedScope reads the scope parameter. admin:dangerous is refused outright (it is
// never grantable through OAuth); tiers it does not know are ignored; nothing recognisable
// means read.
func parseRequestedScope(s string) (level int, ok bool) {
	level = scopeRank(scopeRead)
	for _, tok := range strings.Fields(s) {
		switch tok {
		case mcpOAuthForbiddenScope:
			return 0, false
		case scopeWrite:
			level = scopeRank(scopeWrite)
		}
	}
	return level, true
}

// authorizeParams collects the query. A parameter given twice is ambiguous, so it counts as
// missing rather than being resolved by picking one.
type authorizeParams struct {
	q url.Values
}

func (p authorizeParams) get(k string) string {
	if v := p.q[k]; len(v) == 1 {
		return v[0]
	}
	return ""
}

func (p authorizeParams) dup(k string) bool { return len(p.q[k]) > 1 }

// redirectBack sends the browser to the vetted redirect_uri. iss is on every response, success
// or error (RFC 9207), byte-equal to the metadata issuer.
func (a *mcpOAuthAPI) redirectBack(w http.ResponseWriter, r *http.Request, redirectURI, state string, extra url.Values) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		a.errPage(w, r, http.StatusBadRequest, mcpErrRedirect, callbackHost(redirectURI))
		return
	}
	q := u.Query()
	for k, v := range extra {
		q[k] = v
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", a.issuer)
	u.RawQuery = q.Encode()
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func redirectErr(code, desc string) url.Values {
	return url.Values{"error": {code}, "error_description": {desc}}
}

func validS256Challenge(s string) bool { return validPKCEChallenge(s) }

// eligibleMemberships are the member's active memberships that this browser may use right now:
// the same provider and network checks every Console call goes through (ADR 0047).
func (a *mcpOAuthAPI) eligibleMemberships(r *http.Request) (store.Identity, []store.MembershipView, *apiError) {
	ctx := r.Context()
	m := a.mgr
	id := m.resolveIdentity(r)
	if id.key == "" {
		return store.Identity{}, nil, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"}
	}
	ident, err := m.upsertIdentity(ctx, id.email, id.key, m.roleHintFor(id.email))
	if err != nil {
		return store.Identity{}, nil, identityErr(err)
	}
	ms, aerr := m.membershipsFor(ctx, ident)
	if aerr != nil {
		return store.Identity{}, nil, aerr
	}
	disabled := 0
	var out []store.MembershipView
	for _, mv := range ms {
		enabled, err := m.store.GetTenantMCPOAuth(ctx, mv.TenantID)
		if err != nil {
			return store.Identity{}, nil, internalErr(err)
		}
		if !enabled {
			disabled++
		}
		if enabled && m.checkTenantProvider(ctx, mv) == nil && m.checkTenantIP(ctx, ident, mv) == nil {
			out = append(out, mv)
		}
	}
	if len(out) == 0 && disabled > 0 {
		return ident, nil, &apiError{http.StatusForbidden, "mcp_oauth_disabled", "no tenant allows MCP OAuth"}
	}
	return ident, out, nil
}

func (a *mcpOAuthAPI) authorize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authorizeParams{r.URL.Query()}
	// Client and redirect first: until both are vetted nothing may be redirected.
	if p.dup("client_id") || p.dup("redirect_uri") {
		a.refuse(w, r, "duplicate client_id or redirect_uri")
		return
	}
	trusted, _ := a.trustedNow(ctx)
	cl, err := a.loadClient(ctx, trusted, p.get("client_id"))
	switch err {
	case nil:
	case errMCPClientStore:
		a.errPage(w, r, http.StatusServiceUnavailable, mcpErrUnavailable, "")
		return
	default:
		a.refuse(w, r, "client")
		return
	}
	redirectURI := p.get("redirect_uri")
	if !a.redirectRegistered(trusted, cl.Redirects, redirectURI) {
		log.Printf("mcp oauth: authorize refused: redirect_uri")
		a.errPage(w, r, http.StatusBadRequest, mcpErrRedirect, callbackHost(redirectURI))
		return
	}
	state := p.get("state")
	fail := func(code, desc string) { a.redirectBack(w, r, redirectURI, state, redirectErr(code, desc)) }
	if p.dup("state") || p.dup("scope") || p.dup("resource") || p.dup("code_challenge") || p.dup("prompt") {
		fail("invalid_request", "a parameter was repeated")
		return
	}
	if p.get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	if mode := p.get("response_mode"); mode != "" && mode != "query" {
		fail("invalid_request", "only response_mode=query is supported")
		return
	}
	if p.get("resource") != a.resource {
		fail("invalid_target", "resource must be "+a.resource)
		return
	}
	challenge := p.get("code_challenge")
	if challenge == "" || p.get("code_challenge_method") != "S256" || !validS256Challenge(challenge) {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	level, ok := parseRequestedScope(p.get("scope"))
	if !ok {
		fail("invalid_scope", mcpOAuthForbiddenScope+" cannot be granted over OAuth")
		return
	}
	prompt := p.get("prompt")
	if prompt != "" && prompt != "none" && prompt != "consent" {
		fail("invalid_request", "prompt="+prompt+" is not supported")
		return
	}
	ident, ms, aerr := a.eligibleMemberships(r)
	if aerr != nil {
		if aerr.code == "mcp_oauth_disabled" {
			a.errPage(w, r, http.StatusForbidden, mcpErrRefused, "")
			return
		}
		if aerr.status == http.StatusUnauthorized {
			writeAPIErr(w, aerr)
			return
		}
		fail("access_denied", "no tenant is available from here")
		return
	}
	if len(ms) == 0 {
		fail("access_denied", "no tenant is available from here")
		return
	}
	req := mcpAuthRequest{cl: cl, redirectURI: redirectURI, state: state, challenge: challenge, level: level}
	if prompt == "none" {
		// Silent only for a client that already holds a live grant of this member at an equal
		// or wider scope; anything else needs the member to look at the page.
		grants, err := a.mgr.store.ListMCPOAuthGrantsByIdentity(ctx, ident.ID, a.now().Unix())
		if err != nil {
			log.Printf("mcp oauth: list grants: %v", err)
			fail("server_error", "could not read the grants")
			return
		}
		for _, g := range grants {
			if g.ClientID != cl.ID || scopeLevel(g.Scope) < level {
				continue
			}
			for _, mv := range ms {
				if mv.MembershipID == g.MembershipID {
					a.issueCode(w, r, req, ident.ID, mv.MembershipID)
					return
				}
			}
		}
		fail("consent_required", "the member has to approve this client first")
		return
	}
	a.askConsent(w, r, req, ident, ms)
}

// mcpAuthRequest is a vetted authorize request.
type mcpAuthRequest struct {
	cl                            mcpClient
	redirectURI, state, challenge string
	level                         int
}

func (a *mcpOAuthAPI) issueCode(w http.ResponseWriter, r *http.Request, req mcpAuthRequest, identityID, membershipID string) {
	code := "afmcp_code_" + devIdPRandom(32)
	err := a.mgr.store.InsertMCPOAuthToken(r.Context(), store.MCPOAuthToken{
		Hash: devIdPHash(code), Kind: store.MCPOAuthCode, ClientID: req.cl.ID, ClientName: req.cl.Name,
		IdentityID: identityID, MembershipID: membershipID, RedirectURI: req.redirectURI,
		Challenge: req.challenge, Scope: scopeString(req.level), Resource: a.resource,
		ExpiresAt: a.now().Add(mcpOAuthCodeTTL).Unix(),
	})
	if err != nil {
		log.Printf("mcp oauth: store code: %v", err)
		a.redirectBack(w, r, req.redirectURI, req.state, redirectErr("server_error", "could not issue a code"))
		return
	}
	a.redirectBack(w, r, req.redirectURI, req.state, url.Values{"code": {code}})
}

// askConsent parks the request behind a single-use ticket and shows the member what they are
// approving. The ticket is the CSRF token: random, bound to this member, rendered only into
// this page, and the redirect target comes from the stored request, never from the form.
func (a *mcpOAuthAPI) askConsent(w http.ResponseWriter, r *http.Request, req mcpAuthRequest, ident store.Identity, ms []store.MembershipView) {
	ticket := "afmcp_ask_" + devIdPRandom(32)
	err := a.mgr.store.InsertMCPOAuthToken(r.Context(), store.MCPOAuthToken{
		Hash: devIdPHash(ticket), Kind: store.MCPOAuthConsent, ClientID: req.cl.ID, ClientName: req.cl.Name,
		IdentityID: ident.ID, RedirectURI: req.redirectURI, Challenge: req.challenge,
		Scope: scopeString(req.level), Resource: a.resource, State: req.state,
		ExpiresAt: a.now().Add(mcpOAuthConsentTTL).Unix(),
	})
	if err != nil {
		log.Printf("mcp oauth: store consent: %v", err)
		a.redirectBack(w, r, req.redirectURI, req.state, redirectErr("server_error", "could not start the consent"))
		return
	}
	lang := auth.PreferredUILang(r)
	type tenant struct{ ID, Label, Role string }
	data := struct {
		mcpShell
		T                                mcpConsentStrings
		Ticket, Action, Host, ClientName string
		Loopback, CanWrite               bool
		Tenants                          []tenant
		Scope                            string
	}{
		mcpShell: a.shell(r, lang), T: mcpConsentText[lang], Ticket: ticket, Action: mcpOAuthConsent,
		Host: callbackHost(req.redirectURI), ClientName: req.cl.Name,
		Loopback: a.classifyRedirect(nil, req.redirectURI) == redirectLoopback,
		CanWrite: req.level >= scopeRank(scopeWrite), Scope: scopeRead,
	}
	for _, mv := range ms {
		label := mv.TenantName
		if label == "" {
			label = mv.TenantSlug
		}
		data.Tenants = append(data.Tenants, tenant{ID: mv.MembershipID, Label: label, Role: mv.Role})
	}
	var buf bytes.Buffer
	if err := mcpConsentTmpl.Execute(&buf, data); err != nil {
		log.Printf("mcp oauth: consent page: %v", err)
		a.errPage(w, r, http.StatusInternalServerError, mcpErrUnavailable, "")
		return
	}
	setMCPOAuthPageHeaders(w)
	_, _ = w.Write(buf.Bytes())
}

func (a *mcpOAuthAPI) consentRefuse(w http.ResponseWriter, r *http.Request, status int, why string) {
	log.Printf("mcp oauth: consent refused: %s", why)
	kind := mcpErrExpired
	if status == http.StatusInternalServerError {
		kind = mcpErrUnavailable
	}
	a.errPage(w, r, status, kind, "")
}

func (a *mcpOAuthAPI) consent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !sameConsoleOrigin(a.mgr.publicBaseURL, r) {
		a.consentRefuse(w, r, http.StatusForbidden, "cross-site")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		a.consentRefuse(w, r, http.StatusBadRequest, "unreadable form")
		return
	}
	ticket := r.PostForm.Get("ticket")
	if ticket == "" {
		a.consentRefuse(w, r, http.StatusBadRequest, "no ticket")
		return
	}
	ident, ms, aerr := a.eligibleMemberships(r)
	if aerr != nil {
		if aerr.status == http.StatusUnauthorized {
			writeAPIErr(w, aerr)
			return
		}
		a.consentRefuse(w, r, http.StatusForbidden, "membership")
		return
	}
	g, spend, err := a.mgr.store.ConsumeMCPOAuthToken(ctx, devIdPHash(ticket), store.MCPOAuthConsent, a.now().Unix())
	if err != nil {
		log.Printf("mcp oauth: consume consent: %v", err)
		a.consentRefuse(w, r, http.StatusInternalServerError, "store")
		return
	}
	if spend != store.MCPOAuthSpendOK {
		a.consentRefuse(w, r, http.StatusBadRequest, "unknown, spent or expired ticket")
		return
	}
	if g.IdentityID != ident.ID {
		a.consentRefuse(w, r, http.StatusForbidden, "ticket belongs to another member")
		return
	}
	// Everything authorize decided is decided again: the client and the trusted list may have
	// changed in the minutes the page was open.
	trusted, _ := a.trustedNow(ctx)
	cl, err := a.loadClient(ctx, trusted, g.ClientID)
	if err != nil || !a.redirectRegistered(trusted, cl.Redirects, g.RedirectURI) {
		a.consentRefuse(w, r, http.StatusBadRequest, "client or redirect_uri")
		return
	}
	req := mcpAuthRequest{cl: cl, redirectURI: g.RedirectURI, state: g.State, challenge: g.Challenge, level: scopeLevel(g.Scope)}
	// Cancel answers with the parked request's redirect and state, never the form's.
	if r.PostForm.Get("action") == "cancel" {
		a.redirectBack(w, r, req.redirectURI, req.state, redirectErr("access_denied", "the member declined"))
		return
	}
	var chosen *store.MembershipView
	for i := range ms {
		if ms[i].MembershipID == r.PostForm.Get("membership") {
			chosen = &ms[i]
		}
	}
	if chosen == nil {
		a.consentRefuse(w, r, http.StatusBadRequest, "no such tenant choice")
		return
	}
	// The member can narrow the scope, never widen it past what was asked for.
	switch r.PostForm.Get("scope") {
	case scopeRead:
		req.level = scopeRank(scopeRead)
	case scopeWrite:
	default:
		a.consentRefuse(w, r, http.StatusBadRequest, "no scope choice")
		return
	}
	a.issueCode(w, r, req, ident.ID, chosen.MembershipID)
}

func pkceMatches(verifier, challenge string) bool {
	if !validPKCEVerifier(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return constantTimeEq(base64.RawURLEncoding.EncodeToString(sum[:]), challenge)
}

// --- pages --------------------------------------------------------------------

type mcpShell struct{ Lang, Accent, BrandLabel, Icon string }

func (a *mcpOAuthAPI) shell(r *http.Request, lang string) mcpShell {
	br := a.brand.get(r.Context())
	s := mcpShell{Lang: lang, Accent: brandBaseHex, BrandLabel: br.label, Icon: "/brand/icon-192.png"}
	if br.hex != "" {
		s.Accent = br.hex
	}
	return s
}

// setMCPOAuthPageHeaders is the header set of every HTML page here: the dev IdP's (framing
// refused, no form-action, same-origin referrer, no-store), unchanged.
func setMCPOAuthPageHeaders(w http.ResponseWriter) { setDevIdPPageHeaders(w) }

type mcpErrKind int

const (
	mcpErrRefused mcpErrKind = iota
	mcpErrRedirect
	mcpErrExpired
	mcpErrUnavailable
	mcpErrMethod
)

type mcpErrText struct{ Title, What, Next string }

type mcpErrStrings struct {
	HostLabel                                       string
	Refused, Redirect, Expired, Unavailable, Method mcpErrText
}

func (s mcpErrStrings) text(k mcpErrKind) mcpErrText {
	switch k {
	case mcpErrRedirect:
		return s.Redirect
	case mcpErrExpired:
		return s.Expired
	case mcpErrUnavailable:
		return s.Unavailable
	case mcpErrMethod:
		return s.Method
	}
	return s.Refused
}

var mcpErrCatalogue = map[string]mcpErrStrings{
	"ja": {
		HostLabel:   "拒否された戻り先",
		Refused:     mcpErrText{"接続を開始できません", "この接続要求は受け付けられませんでした。", "アプリ側からもう一度接続を始めてください。"},
		Redirect:    mcpErrText{"戻り先のアドレスが許可されていません", "アプリが、この Agent Fleet では許可されていないアドレスへの戻りを求めました。", "接続先アプリの管理者に確認するか、アプリからもう一度接続を始めてください。"},
		Expired:     mcpErrText{"確認が無効です", "この確認は拒否されたか、期限が切れたか、すでに使われています。", "アプリからもう一度接続を始めてください。"},
		Unavailable: mcpErrText{"接続を完了できません", "サーバー側の問題で接続を完了できませんでした。", "少し待ってからもう一度お試しください。続く場合はこのデプロイの管理者に連絡してください。"},
		Method:      mcpErrText{"アプリから接続してください", "このアドレスは接続の途中の手順で、単独では開けません。", "MCP クライアントから接続を始めてください。"},
	},
	"en": {
		HostLabel:   "Rejected callback",
		Refused:     mcpErrText{"Can't start the connection", "This connection request was refused.", "Start the connection again from the app."},
		Redirect:    mcpErrText{"Callback address not allowed", "The app asked to return to an address this Agent Fleet does not allow.", "Ask whoever runs the app, or start the connection again from the app."},
		Expired:     mcpErrText{"Confirmation is no longer valid", "This confirmation was refused, has expired, or was already used.", "Start the connection again from the app."},
		Unavailable: mcpErrText{"Can't complete the connection", "A problem on the server stopped the connection from finishing.", "Try again in a moment. If it keeps failing, ask this deployment's administrator."},
		Method:      mcpErrText{"Connect from your app", "This address is one step of a connection and cannot be opened on its own.", "Start the connection from your MCP client."},
	},
}

const mcpTopTmpl = `{{define "mcp-top"}}<div class="top"><img src="{{.Icon}}" alt=""><span class="brand">Agent Fleet</span>{{if .BrandLabel}}<span class="dep">· {{.BrandLabel}}</span>{{end}}</div>{{end}}`

var mcpErrTmpl = template.Must(template.Must(template.Must(devIdPShellTmpl.Clone()).Parse(mcpTopTmpl)).New("error").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · Agent Fleet</title>
<style>
{{template "devidp-shell-css"}}.app{padding:10px 12px;border:1px dashed var(--border);border-radius:10px;margin-bottom:16px;font-size:13px}
.app small{display:block;color:var(--muted);font-size:12px;margin-bottom:2px}
.app code{font:12.5px ui-monospace,SFMono-Regular,Menlo,monospace;overflow-wrap:anywhere}
.next{margin:0;padding-top:14px;border-top:1px solid var(--border);font-size:14px}
</style></head><body style="--accent:{{.Accent}}">
<main class="card">
{{template "mcp-top" .}}
<div class="body">
<h1>{{.Title}}</h1>
<p class="lead">{{.What}}</p>
{{if .Host}}<div class="app"><small>{{.HostLabel}}</small><code>{{.Host}}</code></div>
{{end}}<p class="next">{{.Next}}</p>
</div>
</main>
</body></html>`))

// errPage answers a browser navigation that cannot proceed. Why it failed is logged by the
// caller and never shown; host is the rejected callback's host, or "".
func (a *mcpOAuthAPI) errPage(w http.ResponseWriter, r *http.Request, status int, kind mcpErrKind, host string) {
	lang := auth.PreferredUILang(r)
	t := mcpErrCatalogue[lang]
	data := struct {
		mcpShell
		mcpErrText
		HostLabel, Host string
	}{a.shell(r, lang), t.text(kind), t.HostLabel, host}
	var buf bytes.Buffer
	if err := mcpErrTmpl.Execute(&buf, data); err != nil {
		log.Printf("mcp oauth: error page: %v", err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	setMCPOAuthPageHeaders(w)
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (a *mcpOAuthAPI) refuse(w http.ResponseWriter, r *http.Request, why string) {
	log.Printf("mcp oauth: authorize refused: %s", why)
	a.errPage(w, r, http.StatusBadRequest, mcpErrRefused, "")
}

type mcpConsentStrings struct {
	Title, Heading, Lead, GoesTo, Local, Client, SelfDeclared, Tenant, Scope string
	Read, ReadHelp, Write, WriteHelp, Allow, Cancel, Footer                  string
}

var mcpConsentText = map[string]mcpConsentStrings{
	"ja": {
		Title: "アプリの接続を許可 · Agent Fleet", Heading: "アプリが Agent Fleet に接続しようとしています",
		Lead:   "許可すると、このアプリはあなたとして MCP ツールを呼び出せます。心当たりがなければキャンセルしてください。",
		GoesTo: "接続後の戻り先", Local: "このパソコン自身", Client: "アプリ名", SelfDeclared: "アプリ自身の申告であり、確認されたものではありません",
		Tenant: "接続するテナント", Scope: "許可する範囲",
		Read: "read", ReadHelp: "セッションの状態や出力を読み取る", Write: "write", WriteHelp: "読み取りに加えて、セッションへの送信など操作を行う",
		Allow: "許可する", Cancel: "キャンセル",
		Footer: "いつでも接続を取り消せます。admin:dangerous の権限は OAuth では付与されません。",
	},
	"en": {
		Title: "Allow app to connect · Agent Fleet", Heading: "An app wants to connect to Agent Fleet",
		Lead:   "If you allow it, this app can call MCP tools as you. Cancel if you don't recognise it.",
		GoesTo: "After connecting, you are sent back to", Local: "this computer itself", Client: "App name", SelfDeclared: "declared by the app itself, not verified",
		Tenant: "Tenant to connect", Scope: "What to allow",
		Read: "read", ReadHelp: "Read session status and output", Write: "write", WriteHelp: "Read, plus act: send to sessions and similar",
		Allow: "Allow", Cancel: "Cancel",
		Footer: "You can revoke the connection at any time. The admin:dangerous tier is never granted over OAuth.",
	},
}

var mcpConsentTmpl = template.Must(template.Must(template.Must(devIdPShellTmpl.Clone()).Parse(mcpTopTmpl)).New("consent").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.T.Title}}</title>
<style>
{{template "devidp-shell-css"}}.dest{padding:12px;border:1px solid var(--border);border-radius:10px;margin-bottom:12px}
.dest small,.cname small{display:block;color:var(--muted);font-size:12px}
.host{font:600 20px/1.3 ui-monospace,SFMono-Regular,Menlo,monospace;overflow-wrap:anywhere}
.cname{margin-bottom:16px;font-size:14px;overflow-wrap:anywhere}
.section{font-size:12px;color:var(--muted);margin:14px 0 8px;text-transform:uppercase;letter-spacing:.06em}
label.opt{display:flex;align-items:flex-start;gap:10px;padding:10px 12px;border:1px solid var(--border);border-radius:10px;margin:0 0 8px;cursor:pointer}
label.opt:has(input:checked){border-color:var(--accent);background:var(--sel)}
label.opt small{display:block;color:var(--muted)}
.actions{display:flex;gap:10px;align-items:center;margin-top:18px}
button{font:inherit;font-weight:600;border:0;border-radius:9px;padding:10px 18px;background:var(--accent);color:var(--accent-fg);cursor:pointer}
button.cancel{background:none;color:var(--muted);font-weight:400;font-size:14px;padding:10px 6px}
button.cancel:hover{text-decoration:underline}
.foot{border-top:1px solid var(--border);padding:12px 20px;font-size:12px;color:var(--muted)}
</style></head><body style="--accent:{{.Accent}}">
<main class="card">
{{template "mcp-top" .}}
<div class="body">
<h1>{{.T.Heading}}</h1>
<p class="lead">{{.T.Lead}}</p>
<div class="dest"><small>{{.T.GoesTo}}</small><div class="host">{{if .Loopback}}{{.T.Local}} ({{.Host}}){{else}}{{.Host}}{{end}}</div></div>
{{if .ClientName}}<div class="cname"><small>{{.T.Client}} — {{.T.SelfDeclared}}</small>{{.ClientName}}</div>{{end}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="ticket" value="{{.Ticket}}">
<p class="section">{{.T.Tenant}}</p>
{{range $i, $t := .Tenants}}<label class="opt"><input type="radio" name="membership" value="{{$t.ID}}"{{if eq $i 0}} checked{{end}}><span><b>{{$t.Label}}</b><small>{{$t.Role}}</small></span></label>
{{end}}<p class="section">{{.T.Scope}}</p>
<label class="opt"><input type="radio" name="scope" value="read"{{if eq .Scope "read"}} checked{{end}}><span><b>{{.T.Read}}</b><small>{{.T.ReadHelp}}</small></span></label>
{{if .CanWrite}}<label class="opt"><input type="radio" name="scope" value="write"{{if eq .Scope "write"}} checked{{end}}><span><b>{{.T.Write}}</b><small>{{.T.WriteHelp}}</small></span></label>
{{end}}<div class="actions"><button type="submit">{{.T.Allow}}</button><button type="submit" class="cancel" name="action" value="cancel" formnovalidate>{{.T.Cancel}}</button></div>
</form>
</div>
<div class="foot">{{.T.Footer}}</div>
</main>
</body></html>`))
