package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/auth"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Linking a SECOND sign-in method to an account, with the account owner's consent
// (docs/log/61 §61.16 + ADR0043 決定 37).
//
// The problem it closes: two different IdPs asserting the SAME address are refused
// (決定 32 / rule 2'), and rule 1.5 only joins two buttons onto ONE IdP. So the
// person who signs in at head office with Google and is also in a subsidiary's
// GitHub org cannot reach one account from both doors. Opening it by matching the
// email would let a subsidiary's administrator take over an account created by a
// different authority, which is exactly what 決定 32 refuses.
//
// What makes this safe is WHO asks. The flow starts from a session that already
// proves the account, and the second method is proven by its own IdP in the usual
// callback. Nothing here trusts an assertion about somebody else:
//
//   - a live session is required, and the callback re-checks it is still the same
//     person the flow started as (the state cookie is signed, but a browser can be
//     logged out and back in as somebody else in between);
//   - the method's own gate must pass — Allowed() runs exactly as at login, so the
//     org / domain rules are not bypassed by linking (a link is not a side door);
//   - the address the IdP asserts must be the account's own (決定 37). Two DIFFERENT
//     addresses are still never joined: that is a merge, and §61.5 says a merge
//     cannot be undone;
//   - the IdP account must not already belong to anybody (AttachProvider), and
//   - no deployment role is granted or refreshed (決定 31): AttachProvider does not
//     touch the identity row at all, so a linked method cannot be a path to
//     super_admin the way roleHint would be.
//
// ★ /oauth2/ is auth-EXEMPT (routes.go), so this handler owns its own session gate.
// authGate never runs the "is there a session" check for this path — it only strips
// the inbound identity header — and a missing check here would be an unauthenticated
// endpoint that writes identity rows.

// linkResult is the outcome shown on the small CP-rendered page the browser lands
// on after the round trip. Redirecting silently back into the Console instead would
// mean the Console had to explain failures it cannot see the reason for.
type linkResult string

const (
	linkOK        linkResult = "ok"
	linkErrTaken  linkResult = "taken"    // the method belongs to another account
	linkErrEmail  linkResult = "email"    // the IdP asserted a different address
	linkErrGate   linkResult = "gate"     // org / domain gate refused
	linkErrSess   linkResult = "session"  // no session, or not the same person
	linkErrProv   linkResult = "provider" // unknown / inactive provider
	linkErrFailed linkResult = "failed"   // exchange or storage failed
)

// handleOAuthLink starts a link flow: same authorization code round trip as a
// login, with the state cookie marked so the callback attaches instead of issuing
// a session. The redirect_uri stays /oauth2/callback — an IdP would otherwise need
// a second registered URI per provider (決定 8 の理由と同じ).
func (c config) handleOAuthLink(w http.ResponseWriter, r *http.Request) {
	if !c.oauthConfigured() {
		http.Error(w, "oauth not configured", http.StatusServiceUnavailable)
		return
	}
	claims, ok := c.session(r)
	if !ok {
		// Not signed in: this flow has no account to link TO. Send them to the login
		// page carrying where they were, so the button works again afterwards.
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	ident, ok := c.linkCaller(r, claims)
	if !ok {
		c.writeLinkResult(w, r, linkErrSess, "", sanitizeNext(r.URL.Query().Get("next")))
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("provider"))
	next := sanitizeNext(r.URL.Query().Get("next"))
	p := c.providerFor(r.Context(), id)
	// An empty id would resolve to "the deployment's first button" (providerFor's
	// login convenience). Linking has no such default — the person picked a method.
	if id == "" || p == nil || !c.linkableFor(r.Context(), ident, p.ID()) {
		c.writeLinkResult(w, r, linkErrProv, "", next)
		return
	}
	nonce := randHex(16)
	b, _ := json.Marshal(oauthState{
		Nonce: nonce, Next: next, Exp: time.Now().Add(10 * time.Minute).Unix(),
		Prov: p.ID(),
		// Bind the flow to the person who started it: the identity to attach to, and
		// the session that proved it. Both are re-checked at the callback.
		Link: ident.ID, LEmail: claims.Email, LProv: claims.provider(), LSub: claims.Sub,
	})
	c.setCookie(w, stateCookie, c.signCookie(b), 600)
	au, err := p.AuthorizeURL(r.Context(), nonce, c.oauthRedirectURI())
	if err != nil {
		log.Printf("oauth: link %s authorize: %v", p.ID(), err)
		c.writeLinkResult(w, r, linkErrFailed, "", next)
		return
	}
	http.Redirect(w, r, au, http.StatusFound)
}

// linkCaller resolves the signed-in person the way every API request does. The
// header is set here because /oauth2/ is auth-exempt: authGate stripped whatever
// the client sent (that is unconditional) but did not put ours back.
func (c config) linkCaller(r *http.Request, claims sessionClaims) (store.Identity, bool) {
	if c.mgr == nil {
		return store.Identity{}, false
	}
	r.Header.Set(c.mgr.emailHeader, claims.Email)
	ctx := withLoginRef(r.Context(), loginRef{claims.provider(), claims.Sub})
	ident, aerr := c.mgr.identityFor(ctx, r)
	if aerr != nil {
		return store.Identity{}, false
	}
	return ident, true
}

// linkableFor reports whether this person may be sent to this provider's IdP at
// all. Env providers are the deployment's own doors and open to everyone who can
// sign in; a TENANT-defined one is offered only to a member of that tenant, for the
// same reason its button is absent from the generic login page — the list of them is
// a directory of the group's subsidiaries (決定 32-4). It is not the authorization
// (Allowed() still runs at the callback), it is what this person is shown and may
// probe.
func (c config) linkableFor(ctx context.Context, ident store.Identity, providerID string) bool {
	slug, _, ok := auth.ParseTenantProviderID(providerID)
	if !ok {
		return true
	}
	if c.mgr == nil {
		return false
	}
	ms, err := c.mgr.store.ListMemberships(ctx, ident.ID)
	if err != nil {
		return false
	}
	// ListMemberships returns ACTIVE memberships only, so being on this list is the
	// membership check — an offboarded person's row is already gone from it.
	for _, m := range ms {
		if m.TenantSlug == slug {
			return true
		}
	}
	return false
}

// finishLink is the callback branch for a link flow. It runs after the state cookie
// has been verified and the provider resolved, and it never issues a session: the
// person is already signed in as somebody, and swapping that under them mid-flow is
// how "I linked a method and ended up in the other account" happens.
func (c config) finishLink(w http.ResponseWriter, r *http.Request, st oauthState, p auth.LoginProvider) {
	next := sanitizeNext(st.Next)
	// Still the same person? The state is signed and short-lived, but the session
	// cookie can have changed in another tab between the two legs.
	claims, ok := c.session(r)
	if !ok || !strings.EqualFold(claims.Email, st.LEmail) ||
		claims.provider() != st.LProv || claims.Sub != st.LSub {
		c.writeLinkResult(w, r, linkErrSess, "", next)
		return
	}
	ident, ok := c.linkCaller(r, claims)
	if !ok || ident.ID != st.Link {
		c.writeLinkResult(w, r, linkErrSess, "", next)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		c.writeLinkResult(w, r, linkErrFailed, p.Label(auth.PreferredUILang(r)), next)
		return
	}
	pr, err := p.Exchange(r.Context(), code, c.oauthRedirectURI())
	if err != nil {
		log.Printf("oauth: link %s exchange: %v", p.ID(), err)
		if errors.Is(err, auth.ErrNotAllowed) {
			c.writeLinkResult(w, r, linkErrGate, p.Label(auth.PreferredUILang(r)), next)
		} else {
			c.writeLinkResult(w, r, linkErrFailed, p.Label(auth.PreferredUILang(r)), next)
		}
		return
	}
	if pr.Email == "" || !pr.Verified {
		c.writeLinkResult(w, r, linkErrGate, p.Label(auth.PreferredUILang(r)), next)
		return
	}
	// ★ Same stamp as the login callback, from the provider object — never from the
	// exchange and never from a tenant's row (docs/log/61 §61.15).
	pr.Realm = auth.ProviderRealm(p)
	// ★ The method's own gate. Linking must not be a way past the org membership or
	// the allowed domains: someone who cannot sign in with a method cannot link it.
	allowed, err := p.Allowed(r.Context(), pr)
	if err != nil || !allowed {
		c.writeLinkResult(w, r, linkErrGate, p.Label(auth.PreferredUILang(r)), next)
		return
	}
	// ★ 決定 37: the same address only. A different one would join two addresses into
	// one account, which §61.5 refuses whichever direction it is done from — being
	// able to sign in to both proves control of both, not that they are one person.
	if !strings.EqualFold(strings.TrimSpace(pr.Email), strings.TrimSpace(ident.Email)) {
		c.writeLinkResult(w, r, linkErrEmail, p.Label(auth.PreferredUILang(r)), next)
		return
	}
	err = c.mgr.store.AttachProvider(r.Context(), ident.ID, store.IdentityLink{
		Provider: pr.Provider, Subject: pr.Subject, Realm: pr.Realm,
		RealmClaim: pr.RealmClaim, RealmSubject: pr.RealmSubject, Email: pr.Email,
	})
	switch {
	case errors.Is(err, store.ErrLinkTaken):
		c.writeLinkResult(w, r, linkErrTaken, p.Label(auth.PreferredUILang(r)), next)
		return
	case err != nil:
		log.Printf("oauth: link attach %s/%s: %v", pr.Provider, pr.Subject, err)
		c.writeLinkResult(w, r, linkErrFailed, p.Label(auth.PreferredUILang(r)), next)
		return
	}
	log.Printf("oauth: linked %s to identity %s", pr.Provider, ident.ID)
	// ★ Both directions are in the ledger (§61.16.4). A link is a NEW door into a
	// workspace, and an audit that only records removals cannot answer "since when
	// could that account also be reached with the subsidiary's GitHub".
	_ = c.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), ActorKind: "user", ActorID: ident.ID,
		Action: "identity_provider.attach", Target: pr.Provider,
		Detail: "subject=" + pr.Subject, HTTPStatus: http.StatusOK, At: store.NowTS(),
	})
	c.writeLinkResult(w, r, linkOK, p.Label(auth.PreferredUILang(r)), next)
}

// writeLinkResult renders the outcome on the same self-contained page the login and
// new-account notices use, with one button back to where the person was.
func (c config) writeLinkResult(w http.ResponseWriter, r *http.Request, res linkResult, label, next string) {
	lang := auth.PreferredUILang(r)
	t := auth.LoginText[lang]
	if next == "" {
		next = "/"
	}
	body, class := t.LinkFailed, "err"
	switch res {
	case linkOK:
		body, class = t.LinkOK, "msg"
	case linkErrTaken:
		body = t.LinkTaken
	case linkErrEmail:
		body = t.LinkEmail
	case linkErrGate:
		body = t.LinkGate
	case linkErrSess:
		body = t.LinkSession
	case linkErrProv:
		body = t.LinkProvider
	}
	if label != "" {
		body = html.EscapeString(label) + " — " + body
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	page := strings.NewReplacer(
		"{{LANG}}", lang,
		"{{TITLE}}", html.EscapeString(t.LinkTitle),
		"{{NOTE}}", t.LinkNote,
		"{{ERROR}}", `<div class="`+class+`">`+body+`</div>`,
		"{{BUTTONS}}", `<a class="gbtn" href="`+html.EscapeString(next)+`">`+t.LinkBack+`</a>`,
	).Replace(auth.LoginPageHTML)
	_, _ = w.Write([]byte(page))
}

// --- the account panel's API ----------------------------------------------

// accountAPI serves the person's OWN sign-in methods (docs/log/61 §61.16): what is
// linked, and what may be linked next. Deliberately not an admin endpoint — it
// answers about the caller only, and every row in it is something the caller
// already proved by signing in.
type accountAPI struct {
	memberAuth
	provs   []auth.LoginProvider // env-defined, startup-fixed (same snapshot as the admin list)
	enabled bool                 // AUTH=oauth with a working config: linking is possible at all
}

func newAccountAPI(cfg config) accountAPI {
	return accountAPI{memberAuth{cfg.mgr}, cfg.providers,
		cfg.mgr != nil && cfg.mgr.authMode == "oauth" && cfg.oauthConfigured()}
}

// loginMethods (GET /api/me/login-methods).
//
// ★ The linkable list is narrowed to the caller's own tenants for tenant-defined
// providers (linkableFor), and the endpoint that starts the flow applies the SAME
// rule — the list is a view, never the gate (決定 14).
func (a accountAPI) loginMethods(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	linked, err := a.mgr.store.ListLinkedProviders(r.Context(), ident.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	cur, hasCur := sessionLoginRef(r.Context())
	have := make(map[string]bool, len(linked))
	out := make([]map[string]any, 0, len(linked))
	for _, lp := range linked {
		have[lp.Provider] = true
		row := map[string]any{
			"provider": lp.Provider,
			// ★ subject is what identifies the ROW. One provider can hold two of them
			// (the same button pressed as two IdP accounts is refused today, but rows
			// written before that guard, or by two adapters sharing an id, are not
			// impossible), and without it "remove this one" has nothing to name.
			"subject":       lp.Subject,
			"email":         lp.Email,
			"last_login_at": lp.LastLoginAt,
			"current":       hasCur && currentMethod(cur, lp.Provider, lp.Subject),
			// ★ Whether it CAN be removed is answered here, by the server, for the same
			// reason the list is a view and not the gate (決定 14): the Console only
			// mirrors it, and detach re-checks both rules itself.
			"removable": len(linked) > 1 && !(hasCur && currentMethod(cur, lp.Provider, lp.Subject)),
		}
		// A provider that is gone (removed from env, or a suspended tenant row) has no
		// label to show. The row stays visible — it is still how that person can sign
		// in if it comes back — and the Console falls back to the id.
		if p := a.providerByID(r.Context(), lp.Provider); p != nil {
			row["label_ja"], row["label_en"] = p.Label("ja"), p.Label("en")
		}
		out = append(out, row)
	}
	cand := make([]map[string]any, 0, 4)
	for _, p := range a.linkCandidates(r.Context(), ident) {
		if have[p.ID()] {
			continue
		}
		row := map[string]any{"provider": p.ID(), "label_ja": p.Label("ja"), "label_en": p.Label("en")}
		if slug, _, ok := auth.ParseTenantProviderID(p.ID()); ok {
			row["tenant"] = slug
		}
		cand = append(cand, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": a.enabled, "linked": out, "linkable": cand,
	})
}

// currentMethod reports whether a linked row is the one this session is signed in
// with. A session that carries no subject (a cookie minted before P0) matches every
// row of its provider — see sessionLoginRef for why that direction is the safe one.
func currentMethod(cur loginRef, provider, subject string) bool {
	return cur.provider == provider && (cur.subject == "" || cur.subject == subject)
}

// detachLoginMethod (DELETE /api/me/login-methods?provider=…&subject=…) removes one
// sign-in method from the caller's OWN account (docs/log/61 §61.16.4).
//
// ★ provider and subject arrive as QUERY parameters, not path segments: a
// tenant-defined provider id is "t:<slug>:<name>" and the colons make it a poor path
// component — encoded, it is at the mercy of every proxy in front of CP, and
// unencoded it silently splits into segments that never match the route.
//
// Three guards, and each closes a different hole:
//
//	the last one           removing it locks the account out for good (store layer,
//	                       counted inside the DELETE so two tabs cannot race to zero)
//	the one in use         you would still be signed in on a method you just deleted,
//	                       and the next request would resolve to nothing
//	somebody else's row    identityID is in the WHERE, always
func (a accountAPI) detachLoginMethod(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	subject := strings.TrimSpace(r.URL.Query().Get("subject"))
	if provider == "" || subject == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request",
			"provider and subject are required"})
		return
	}
	// ★ The method that minted THIS session. Checked here rather than in the store
	// because the store has no session — and it is checked at all because "sign in
	// with the other method first, then remove this one" is a step people skip.
	if cur, ok := sessionLoginRef(r.Context()); ok && currentMethod(cur, provider, subject) {
		writeAPIErr(w, &apiError{http.StatusConflict, "current_login_method",
			"you are signed in with this method right now — sign in with another one first, then remove it"})
		return
	}
	err := a.mgr.store.DetachProvider(r.Context(), ident.ID, provider, subject)
	switch {
	case errors.Is(err, store.ErrNoSuchLoginMethod):
		writeAPIErr(w, &apiError{http.StatusNotFound, "login_method_not_found", err.Error()})
		return
	case errors.Is(err, store.ErrLastLoginMethod):
		writeAPIErr(w, &apiError{http.StatusConflict, "last_login_method", err.Error()})
		return
	case err != nil:
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditMethod(r, ident, "identity_provider.detach", provider, subject)
	writeJSON(w, http.StatusOK, map[string]any{"detached": provider})
}

// auditMethod records a change to WHICH doors open an account. Both directions are
// written (docs/log/61 §61.16.4): a linked method is a new way into a workspace, and the
// question an audit answers afterwards is "when did that become possible", which a
// detach-only ledger cannot.
//
// TenantID is empty on purpose — this is a deployment-level fact about one person,
// not an action inside a tenant, and filing it under whichever tenant happened to be
// selected would hide it from everybody else's view of the same account.
func (a accountAPI) auditMethod(r *http.Request, ident store.Identity, action, provider, subject string) {
	if a.mgr == nil || a.mgr.store == nil {
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), ActorKind: "user", ActorID: ident.ID,
		Action: action, Target: provider, Detail: "subject=" + subject,
		HTTPStatus: http.StatusOK, At: store.NowTS(),
	})
}

// providerByID resolves a provider for display only (env set, then the tenant
// registry, which holds active rows only).
func (a accountAPI) providerByID(ctx context.Context, id string) auth.LoginProvider {
	for _, p := range a.provs {
		if p.ID() == id {
			return p
		}
	}
	if auth.IsTenantProviderID(id) && a.mgr != nil {
		return a.mgr.tenantIdP.ProviderFor(ctx, id)
	}
	return nil
}

// linkCandidates is every method this person could add: the deployment's own
// buttons plus the active providers of the tenants they belong to.
func (a accountAPI) linkCandidates(ctx context.Context, ident store.Identity) []auth.LoginProvider {
	out := append([]auth.LoginProvider(nil), a.provs...)
	if a.mgr == nil {
		return out
	}
	ms, err := a.mgr.store.ListMemberships(ctx, ident.ID)
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, m := range ms {
		if seen[m.TenantSlug] { // active-only by construction (ListMemberships)
			continue
		}
		seen[m.TenantSlug] = true
		out = append(out, a.mgr.tenantIdP.ProvidersForSlug(ctx, m.TenantSlug)...)
	}
	return out
}
