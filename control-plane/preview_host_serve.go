// preview_host_serve.go — ホスト方式プレビューの経路（docs/81 §6・§7 / ADR 0062）。
//
//	ブラウザ → {slug}-{port}.{AF_PREVIEW_DOMAIN} → CP（この層）→ Agent /proxy/{port}/… → 127.0.0.1:{port}
//
// ★ この層は authGate の外側・gzip / etag の外側に置く（決定 8）。プレビューのホストは
// Console とは別の認証（下のハンドシェイク）を持ち、中身は CP の JSON ではない。
//
// ★ プレビューのホストでは CP の API も Console も一切出さない。応答するのは
// __af/preview-auth とプロキシ本体だけで、それ以外は 404。同じプロセスが両方を持って
// いるので、ここを緩めると「プレビューのオリジンから CP を叩ける」という、別オリジンに
// したことで閉じたはずの穴が裏口から開く。
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// previewAuthCookie is minted per PREVIEW HOST (host-only, Path=/). Never the
// Console's session cookie: the previewed app is arbitrary user code, and a browser
// that would send af_session to it hands it the signed-in person's API access.
const previewAuthCookie = "af_pv"

// previewAuthCallbackPath is the one path the preview host answers besides the proxy.
// Under /__af/ so it cannot collide with a real app route by accident.
const previewAuthCallbackPath = "/__af/preview-auth"

// previewHandshakePath is the Console-origin half of the handshake (inside authGate,
// so an unauthenticated visitor lands on the normal login first).
const previewHandshakePath = "/preview-auth"

// previewTokenTTL is how long the one-shot token minted on the Console origin stays
// valid — it only has to survive one redirect.
const previewTokenTTL = 30 * time.Second

// previewSessionTTL is the preview host cookie's lifetime. Deliberately shorter than
// the Console session: this cookie lives on an origin running the user's own code.
const previewSessionTTL = 12 * time.Hour

// previewClaims is both the one-shot token and the cookie payload. The slug is IN the
// signature on purpose: restarting the workspace mints a new slug, which invalidates
// every cookie issued for the previous one — no separate revocation path.
type previewClaims struct {
	Slug         string `json:"slug"`
	Port         int    `json:"port"`
	MembershipID string `json:"ms"`
	Exp          int64  `json:"exp"`
}

// previewHostAPI serves everything that arrives on a preview hostname.
type previewHostAPI struct {
	cfg config
	mgr *manager
}

func newPreviewHostAPI(cfg config) previewHostAPI { return previewHostAPI{cfg: cfg, mgr: cfg.mgr} }

// dispatch wraps the whole CP handler chain. A request whose Host does not parse as a
// preview hostname passes through untouched — including the ALB health check (whose
// Host is the task IP) and every Console request.
func (a previewHostAPI) dispatch(next http.Handler) http.Handler {
	if a.mgr == nil || a.mgr.previewDomain == "" {
		return next // host-mode preview not configured: nothing changes
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ph, ok := parsePreviewHost(r.Host, a.mgr.previewDomain)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		a.serve(w, r, ph)
	})
}

func (a previewHostAPI) serve(w http.ResponseWriter, r *http.Request, ph previewHost) {
	ctx := r.Context()
	ws, ok, err := a.mgr.store.GetWorkspaceByPreviewSlug(ctx, ph.slug)
	if err != nil {
		http.Error(w, "preview lookup failed", http.StatusInternalServerError)
		return
	}
	// 停止中の Workspace は slug を持たないので、ここで落ちる。★ 「停止中です」と
	// 答え分けない —— 存在する slug と存在しない slug を外から区別させない。
	if !ok {
		previewNotFound(w)
		return
	}
	st := parseWSSettings(mustSettings(ctx, a.mgr, ws.ID))
	if !previewPortAllowed(st, ph.port) {
		previewNotFound(w) // 許可外のポートは「存在しない」（決定 6）
		return
	}
	// テナントのネットワーク制限は公開モードでも効かせる（決定 12）。テナントが自分の
	// ネットワークを絞っているなら、プレビューも絞られている側にある。
	if !a.mgr.tenantLogin.networkAllowed(ctx, ws.TenantID, clientIPFrom(ctx)) {
		http.Error(w, "not allowed from this network", http.StatusForbidden)
		return
	}
	if r.URL.Path == previewAuthCallbackPath {
		a.acceptToken(w, r, ph)
		return
	}
	if !st.PreviewPublic && !a.authorized(r, ph, ws) {
		a.startHandshake(w, r, ph)
		return
	}
	if err := a.mgr.touchWorkspace(ctx, ws.ID); err != nil {
		http.Error(w, "workspace unavailable", http.StatusServiceUnavailable)
		return
	}
	if unsafeRelayPath(strings.TrimPrefix(r.URL.Path, "/")) {
		http.Error(w, "bad preview path", http.StatusBadRequest)
		return
	}
	rt := a.mgr.runtimeFor(ws, "")
	opts := previewRelayOptions{
		port:           ph.port,
		path:           r.URL.Path,
		publicHost:     r.Host,
		proto:          forwardedProto(r),
		public:         st.PreviewPublic,
		identityHeader: a.mgr.emailHeader,
	}
	relayPreview(w, r, rt, opts)
}

// previewNotFound answers everything we refuse with the same bare 404 — a wrong slug,
// a stopped workspace and a port that is not on the allowlist must be indistinguishable
// from outside.
func previewNotFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

func mustSettings(ctx context.Context, m *manager, wsID string) string {
	raw, err := m.store.GetWorkspaceSettings(ctx, wsID)
	if err != nil {
		return ""
	}
	return raw
}

// authorized reports whether this request already carries a valid preview cookie for
// THIS host (slug + port both checked — a cookie is host-scoped by the browser, but we
// do not rely on that alone).
func (a previewHostAPI) authorized(r *http.Request, ph previewHost, ws Workspace) bool {
	c, err := r.Cookie(previewAuthCookie)
	if err != nil || c.Value == "" {
		return false
	}
	cl, ok := a.verifyClaims(c.Value)
	if !ok {
		return false
	}
	return cl.Slug == ph.slug && cl.Port == ph.port && cl.MembershipID == ws.MembershipID
}

func (a previewHostAPI) verifyClaims(s string) (previewClaims, bool) {
	payload, ok := a.cfg.verifyCookie(s)
	if !ok {
		return previewClaims{}, false
	}
	var cl previewClaims
	if err := json.Unmarshal(payload, &cl); err != nil {
		return previewClaims{}, false
	}
	if cl.Exp <= time.Now().Unix() {
		return previewClaims{}, false
	}
	return cl, true
}

func (a previewHostAPI) sign(cl previewClaims) string {
	b, _ := json.Marshal(cl)
	return a.cfg.signCookie(b)
}

// startHandshake bounces an unauthenticated preview request to the Console origin,
// which knows how to log someone in. Only a top-level navigation is bounced: a
// sub-resource (fetch/XHR) would follow the redirect into the Console's HTML and
// report a baffling parse error, so those get a plain 401.
func (a previewHostAPI) startHandshake(w http.ResponseWriter, r *http.Request, ph previewHost) {
	if !wantsHTML(r) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.Error(w, "preview requires sign-in", http.StatusUnauthorized)
		return
	}
	base := strings.TrimRight(a.cfg.publicBaseURL, "/")
	if base == "" {
		http.Error(w, "preview auth is not configured (PUBLIC_BASE_URL)", http.StatusServiceUnavailable)
		return
	}
	q := url.Values{}
	q.Set("slug", ph.slug)
	q.Set("port", strconv.Itoa(ph.port))
	q.Set("next", safePreviewNext(r.URL.RequestURI()))
	http.Redirect(w, r, base+previewHandshakePath+"?"+q.Encode(), http.StatusFound)
}

// acceptToken is the preview-origin end of the handshake: verify the one-shot token,
// mint the host-scoped cookie, and send the browser to where it was going.
func (a previewHostAPI) acceptToken(w http.ResponseWriter, r *http.Request, ph previewHost) {
	cl, ok := a.verifyClaims(r.URL.Query().Get("t"))
	if !ok || cl.Slug != ph.slug || cl.Port != ph.port {
		http.Error(w, "preview sign-in expired — reload from the Console", http.StatusForbidden)
		return
	}
	cl.Exp = time.Now().Add(previewSessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     previewAuthCookie,
		Value:    a.sign(cl),
		Path:     "/",
		HttpOnly: true,
		Secure:   forwardedProto(r) == "https",
		// Lax: a top-level navigation carries it, a cross-site sub-request does not.
		// Loosening this to None is the opt-in for apps that call a sibling preview
		// origin directly (docs/81 §2.4) — not the default.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(previewSessionTTL.Seconds()),
	})
	http.Redirect(w, r, safePreviewNext(r.URL.Query().Get("next")), http.StatusFound)
}

// safePreviewNext keeps the post-handshake destination on the preview host: a
// same-origin absolute path, never a scheme or a protocol-relative URL.
func safePreviewNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.HasPrefix(next, "/\\") || next == previewAuthCallbackPath {
		return "/"
	}
	return next
}

// --- Console-origin half (registered on the normal mux, inside authGate) ---------

// handshake (GET /preview-auth) runs on the CONSOLE origin, where the person is (or
// can be) signed in. It re-resolves them exactly like any other API call — identity,
// membership, tenant provider and tenant network are all re-checked — and only then
// mints a 30-second token for the preview host.
func (a previewHostAPI) handshake(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	if !validPreviewSlug(slug) || port < 1 || port > 65535 {
		http.Error(w, "bad preview request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	ws, ok, err := a.mgr.store.GetWorkspaceByPreviewSlug(ctx, slug)
	if err != nil {
		http.Error(w, "preview lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		previewNotFound(w)
		return
	}
	id := a.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return
	}
	// Resolve within the workspace's OWN tenant and compare workspaces: a member has
	// one workspace per membership, so "the workspace I resolve to in that tenant is
	// the one being asked for" is exactly "this is mine". Everything the normal API
	// path checks (membership, required provider, tenant network) is checked here too,
	// because it is the same resolver.
	res, aerr := a.mgr.resolveFull(ctx, id.key, id.email, ws.TenantID)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if res.ws.ID != ws.ID {
		// Not theirs. Same 404 as an unknown slug — being told "that exists but is
		// someone else's" is itself information about another tenant's workspace.
		previewNotFound(w)
		return
	}
	st := parseWSSettings(mustSettings(ctx, a.mgr, ws.ID))
	if !previewPortAllowed(st, port) {
		previewNotFound(w)
		return
	}
	tok := a.sign(previewClaims{
		Slug: slug, Port: port, MembershipID: ws.MembershipID,
		Exp: time.Now().Add(previewTokenTTL).Unix(),
	})
	q := url.Values{}
	q.Set("t", tok)
	q.Set("next", safePreviewNext(r.URL.Query().Get("next")))
	http.Redirect(w, r,
		previewURLFor(slug, port, a.mgr.previewDomain)+previewAuthCallbackPath+"?"+q.Encode(),
		http.StatusFound)
}

// --- the relay itself -----------------------------------------------------------

type previewRelayOptions struct {
	port       int
	path       string // path as the browser asked for it (host mode: already at root)
	publicHost string // what to put in X-Forwarded-Host
	proto      string
	prefix     string // X-Forwarded-Prefix (path mode only; empty in host mode)
	public     bool   // public mode: add X-Robots-Tag
	// identityHeader is manager.emailHeader — the deployment's own identity header,
	// which must never reach the previewed app. Passed in rather than read from a
	// global so a handler built in a test strips exactly what the real one does.
	identityHeader string
}

// previewProxyTransport is the CP→Agent transport (Service Connect / Cloud Map aware),
// shared by every preview relay.
var previewProxyTransport = newAgentTransport()

// relayPreview proxies one request to the workspace Agent's /proxy/{port}{path} with
// httputil.ReverseProxy — which is what makes WebSocket (Vite / Next HMR, Spring STOMP)
// and streaming responses work at all (ADR 0062 決定 10). The old hand-rolled
// http.Client round trip could do neither.
func relayPreview(w http.ResponseWriter, r *http.Request, rt Runtime, o previewRelayOptions) {
	target, err := url.Parse(rt.Endpoint())
	if err != nil || target.Host == "" {
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
		return
	}
	path := o.path
	if path == "" {
		path = "/"
	}
	rp := &httputil.ReverseProxy{
		Transport: previewProxyTransport,
		// -1 = flush as soon as anything is written. Required for SSE and for the
		// App Router's streamed HTML: buffering it turns "slow" into "white page"
		// (docs/81 §2.5 (f)).
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = "/proxy/" + strconv.Itoa(o.port) + path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
			stripConsoleCredentials(pr.Out, r, o.identityHeader)
			if tok := rt.Token(); tok != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+tok) // CP↔Agent auth
			}
			// ★ Rewrite モードでは Out から X-Forwarded-* が削除済みなので、公開名は
			// ここで明示的に入れ直す（Agent 側も同じ理由で入れ直している）。これが
			// Next.js の Server Actions が 403 にならない条件そのもの（決定 9）。
			pr.Out.Header.Set("X-Forwarded-Host", o.publicHost)
			pr.Out.Header.Set("X-Forwarded-Proto", o.proto)
			if o.prefix != "" {
				pr.Out.Header.Set("X-Forwarded-Prefix", o.prefix)
			} else {
				// ホスト方式ではアプリはルート直下に居る。prefix を送ると
				// 「間違った前置き」になる。
				pr.Out.Header.Del("X-Forwarded-Prefix")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			stripAppLoginCookies(resp)
			if o.public {
				// 公開中の画面が検索結果に載るのは事故なので、常に付ける。
				resp.Header.Set("X-Robots-Tag", "noindex, nofollow")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("preview relay (port=%d): %v", o.port, err)
			http.Error(w, "nothing is listening on 127.0.0.1:"+strconv.Itoa(o.port)+
				" inside the workspace (or the workspace is not running)", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// stripConsoleCredentials removes everything that would let the previewed app act as
// the signed-in person. On a preview HOST the browser does not even send the Console's
// cookies (different origin), but a front auth gateway's headers and the path-mode
// caller's cookies still arrive here, and this is the one place both are cleaned.
func stripConsoleCredentials(out *http.Request, in *http.Request, identityHeader string) {
	out.Header.Del("Authorization")
	for _, h := range []string{
		"X-Forwarded-Email", "X-Forwarded-User", "X-Forwarded-Preferred-Username",
		"X-Auth-Request-Email", "X-Auth-Request-User", "X-Auth-Request-Access-Token",
	} {
		out.Header.Del(h)
	}
	if identityHeader != "" {
		out.Header.Del(identityHeader)
	}
	out.Header.Del("Cookie")
	var kept []string
	for _, c := range in.Cookies() {
		if sensitiveBrowserCookie(c.Name) {
			continue
		}
		kept = append(kept, c.Name+"="+c.Value)
	}
	if len(kept) > 0 {
		out.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// stripAppLoginCookies drops Set-Cookie lines that would (re)issue a login on the
// Console or on a front auth gateway. The app's own cookies pass.
func stripAppLoginCookies(resp *http.Response) {
	vals := resp.Header.Values("Set-Cookie")
	if len(vals) == 0 {
		return
	}
	kept := vals[:0:0]
	for _, v := range vals {
		if sensitiveBrowserCookie(setCookieName(v)) {
			continue
		}
		kept = append(kept, v)
	}
	resp.Header.Del("Set-Cookie")
	for _, v := range kept {
		resp.Header.Add("Set-Cookie", v)
	}
}
