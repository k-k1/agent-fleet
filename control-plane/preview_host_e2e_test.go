package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// previewHostFactory hands every workspace the same stub runtime pointed at the fake
// Agent, so the relay can be exercised without docker.
type previewHostFactory struct{ endpoint string }

func (f previewHostFactory) New(ws runtime.Workspace, secretKey string, extraEnv []string) Runtime {
	return previewTestRuntime{endpoint: f.endpoint, token: "tok-" + ws.ID}
}

type previewHostEnv struct {
	mgr    *manager
	cfg    config
	mux    *http.ServeMux // Console オリジン（authGate 相当は dev 認証で素通り）
	host   http.Handler   // プレビュー用ホストの入口（dispatch）
	ws     store.Workspace
	domain string
}

func newPreviewHostEnv(t *testing.T, agentURL string) *previewHostEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dflt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "", "preview-user", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	ws := store.Workspace{ID: "ws-1", TenantID: dflt.ID, MembershipID: mem.ID, ContainerName: "c",
		Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	mgr := &manager{
		rts:             map[string]cachedRT{},
		store:           st,
		authMode:        "dev",
		devUser:         "preview-user",
		emailHeader:     "X-Forwarded-Email",
		provisionMode:   "auto",
		defaultTenantID: dflt.ID,
		conns:           newConnRegistry(),
		tenantLogin:     newTenantLoginCache(st),
		rtFactory:       previewHostFactory{endpoint: agentURL},
		previewDomain:   "pv.example.com",
		publicBaseURL:   "https://af.example.com",
	}
	cfg := config{
		mgr:           mgr,
		publicBaseURL: "https://af.example.com",
		cookieSecret:  []byte("test-cookie-secret-0123456789"),
		previewDomain: mgr.previewDomain,
	}
	mux := http.NewServeMux()
	registerTerminalPreviewRoutes(mux, cfg)
	registerAgentEnvRoutes(mux, cfg) // ws-settings（許可ポート・再発行）も実ルート表から叩く
	return &previewHostEnv{
		mgr: mgr, cfg: cfg, mux: mux,
		host:   newPreviewHostAPI(cfg).dispatch(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })),
		ws:     ws,
		domain: mgr.previewDomain,
	}
}

// toWSSettingsJSON marshals the settings blob the way the API does.
func toWSSettingsJSON(t *testing.T, st wsSettings) string {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal ws settings: %v", err)
	}
	return string(b)
}

func (e *previewHostEnv) mintSlug(t *testing.T) string {
	t.Helper()
	slug, err := e.mgr.rotatePreviewSlug(context.Background(), e.ws)
	if err != nil {
		t.Fatalf("rotatePreviewSlug: %v", err)
	}
	return slug
}

func (e *previewHostEnv) get(t *testing.T, slug string, port int, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	host := previewHostname(slug, port, e.domain)
	req := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
	req.Host = host
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.host.ServeHTTP(rec, req)
	return rec
}

// TestPreviewHostHandshakeThenProxy は docs/log/81 §6 のハンドシェイクを端から端まで通す:
// 未認証のプレビュー要求 → Console オリジンへ 302 → ワンタイム token → プレビュー
// ホスト限定の cookie → 実際の中継、まで。
func TestPreviewHostHandshakeThenProxy(t *testing.T) {
	var seen *http.Request
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)

	// 1) cookie 無しの最初のアクセスは Console オリジンへ跳ぶ。
	rec := e.get(t, slug, 3000, "/dashboard")
	if rec.Code != http.StatusFound {
		t.Fatalf("first hit: code=%d, want 302 to the Console origin", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://af.example.com"+previewHandshakePath+"?") {
		t.Fatalf("first hit Location=%q, want the Console handshake", loc)
	}

	// 2) Console オリジン側（認証済み）はワンタイム token を発行して戻す。
	rec2 := httptest.NewRecorder()
	e.mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, loc, nil))
	if rec2.Code != http.StatusFound {
		t.Fatalf("handshake: code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	back := rec2.Header().Get("Location")
	if !strings.HasPrefix(back, "https://"+previewHostname(slug, 3000, e.domain)+previewAuthCallbackPath) {
		t.Fatalf("handshake Location=%q, want the preview host callback", back)
	}

	// 3) コールバックが cookie を発行し、元の場所へ戻す。
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, back, nil)
	req3.Host = previewHostname(slug, 3000, e.domain)
	req3.Header.Set("X-Forwarded-Proto", "https")
	e.host.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusFound {
		t.Fatalf("callback: code=%d body=%s", rec3.Code, rec3.Body.String())
	}
	if got := rec3.Header().Get("Location"); got != "/dashboard" {
		t.Fatalf("callback Location=%q, want the originally requested path", got)
	}
	var ck *http.Cookie
	for _, c := range rec3.Result().Cookies() {
		if c.Name == previewAuthCookie {
			ck = c
		}
	}
	if ck == nil {
		t.Fatal("callback minted no af_pv cookie")
	}
	if !ck.HttpOnly || !ck.Secure || ck.Path != "/" {
		t.Fatalf("af_pv cookie must be HttpOnly+Secure at Path=/ (got HttpOnly=%v Secure=%v Path=%q)", ck.HttpOnly, ck.Secure, ck.Path)
	}

	// 4) cookie 付きなら中継される。
	rec4 := e.get(t, slug, 3000, "/dashboard", ck)
	if rec4.Code != http.StatusOK {
		t.Fatalf("proxied request: code=%d body=%s", rec4.Code, rec4.Body.String())
	}
	if seen == nil {
		t.Fatal("the Agent never saw the request")
	}
	if seen.URL.Path != "/proxy/3000/dashboard" {
		t.Errorf("Agent path=%q, want /proxy/3000/dashboard", seen.URL.Path)
	}
	// ★ 公開名は X-Forwarded-Host で渡す（Next.js の Server Actions が 403 に
	// ならない条件・決定 9）。
	if got := seen.Header.Get("X-Forwarded-Host"); got != previewHostname(slug, 3000, e.domain) {
		t.Errorf("X-Forwarded-Host=%q, want the public preview host", got)
	}
	if got := seen.Header.Get("X-Forwarded-Proto"); got != "https" {
		t.Errorf("X-Forwarded-Proto=%q, want https", got)
	}
	// ★ ホスト方式ではアプリはルート直下に居るので prefix は送らない。
	if got := seen.Header.Get("X-Forwarded-Prefix"); got != "" {
		t.Errorf("X-Forwarded-Prefix=%q, want it absent in host mode", got)
	}
	// ★ プレビューの入場券をアプリへ渡さない。
	if strings.Contains(seen.Header.Get("Cookie"), previewAuthCookie) {
		t.Errorf("af_pv leaked to the previewed app: %q", seen.Header.Get("Cookie"))
	}

	// 5) 許可していないポートは「存在しない」。
	if rec := e.get(t, slug, 5432, "/", ck); rec.Code != http.StatusNotFound {
		t.Errorf("disallowed port: code=%d, want 404", rec.Code)
	}

	// 6) 停止すると slug ごと消える ＝ 前回の URL は死ぬ（決定 3）。
	if err := e.mgr.store.SetWorkspaceState(context.Background(), e.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if rec := e.get(t, slug, 3000, "/dashboard", ck); rec.Code != http.StatusNotFound {
		t.Errorf("after stop: code=%d, want 404 (the slug must not resolve)", rec.Code)
	}
}

// 起動のたびに引き直され、停止で消える。★ 固定を選んだ Workspace だけは、停止を
// 挟んでも同じ URL に戻る（docs/log/81 §4.1 — 外部 IdP の redirect URI 登録のため）。
func TestPreviewSlugRotationAndFixedOptIn(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	ctx := context.Background()

	first := e.mintSlug(t)
	second := e.mintSlug(t)
	if first == second {
		t.Fatal("the slug must be re-minted on every start")
	}
	if err := e.mgr.store.SetWorkspaceState(ctx, e.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if ws, ok, _ := e.mgr.store.GetWorkspaceByPreviewSlug(ctx, second); ok {
		t.Fatalf("a stopped workspace still resolves by slug (%s)", ws.ID)
	}

	// 固定を選ぶ。
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewFixedSlug = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	fixed := e.mintSlug(t)
	if err := e.mgr.store.SetWorkspaceState(ctx, e.ws.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if again := e.mintSlug(t); again != fixed {
		t.Fatalf("fixed slug changed across a stop: %q → %q", fixed, again)
	}
}

// ★ 公開モードは起動のたびに必ず OFF へ戻る（fail-closed・決定 12）。この機能の事故は
// 「公開のままにしていたのを忘れる」以外にほぼ無い。
func TestPreviewPublicResetsOnEveryStart(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewPublic = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	e.mintSlug(t)
	raw, _ = e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	if parseWSSettings(raw).PreviewPublic {
		t.Fatal("public mode survived a container start")
	}
}

// 公開モードなら、cookie もハンドシェイクも無しで中継される（かつ noindex が付く）。
func TestPreviewPublicModeServesWithoutSignIn(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewPublic = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	rec := e.get(t, slug, 8080, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("public preview: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("X-Robots-Tag=%q, want noindex on a public preview", got)
	}
}

// 未知の slug は、認証を求めずに 404。★ 「ログインすれば見られるかもしれない」と
// 誘導しないこと自体が、slug の存在を外から確かめさせない条件になっている。
func TestPreviewUnknownSlugIsNotFound(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	if rec := e.get(t, "zzzzzzzzzzzzzzzzzzzz", 3000, "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug: code=%d, want 404", rec.Code)
	}
}

// 兄弟オリジンの opt-in（docs/log/81 §2.4・決定 11）。★ 既定では CP は CORS を一切足さない
// —— 「クロスオリジンを既定で通す」は、URL を知っている第三者のページから利用者の
// ブラウザ経由でプレビューを叩ける状態を既定にすること。
func TestPreviewSiblingOriginOptIn(t *testing.T) {
	var appSaw string
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appSaw = r.Method
		// アプリが自前で付けた CORS。opt-in のときは CP の値で上書きされる
		// （2 つ並ぶとブラウザは両方無視する）。
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	slug := e.mintSlug(t)
	ctx := context.Background()

	// 公開モードにして cookie の往復を省く（見たいのは CORS の有無だけ）。
	setPreview := func(mut func(*wsSettings)) {
		raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
		st := parseWSSettings(raw)
		mut(&st)
		if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
			t.Fatal(err)
		}
	}
	setPreview(func(st *wsSettings) { st.PreviewPublic = true })

	sibling := "https://" + previewHostname(slug, 3000, e.domain)
	call := func(method string, hdr map[string]string) *httptest.ResponseRecorder {
		host := previewHostname(slug, 8080, e.domain)
		req := httptest.NewRequest(method, "https://"+host+"/api/orders", nil)
		req.Host = host
		req.Header.Set("X-Forwarded-Proto", "https")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.host.ServeHTTP(rec, req)
		return rec
	}

	// 既定（OFF）: CP は何も足さない。アプリの `*` はそのまま通る（=アプリの自由）。
	rec := call(http.MethodGet, map[string]string{"Origin": sibling})
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("CORS credentials allowed with the opt-in OFF: %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("the app's own CORS header was rewritten with the opt-in OFF: %q", got)
	}

	setPreview(func(st *wsSettings) { st.PreviewCrossOrigin = true })

	// preflight は CP が答え、アプリには届かない（素の dev サーバは OPTIONS に
	// 405 を返すのが普通で、そこで落ちると原因が最も分かりにくい）。
	appSaw = ""
	rec = call(http.MethodOptions, map[string]string{
		"Origin":                         sibling,
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: code=%d, want 204", rec.Code)
	}
	if appSaw != "" {
		t.Errorf("preflight reached the app as %s", appSaw)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != sibling {
		t.Errorf("preflight ACAO=%q, want the sibling origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "content-type" {
		t.Errorf("preflight ACAH=%q", got)
	}

	// 実リクエストには CP の値が 1 つだけ乗る（アプリの `*` は捨てる）。
	rec = call(http.MethodGet, map[string]string{"Origin": sibling})
	if got := rec.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != sibling {
		t.Errorf("ACAO=%v, want exactly [%s]", got, sibling)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("ACAC=%q, want true", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary=%q, want it to include Origin", got)
	}

	// ★ 他人の Workspace のプレビューからは通らない（slug が違う）。
	rec = call(http.MethodGet, map[string]string{"Origin": "https://zzzzzzzzzzzzzzzzzzzz-3000." + e.domain})
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("a foreign workspace's preview origin was allowed: %q", got)
	}
	// 許可していないポートを名乗るオリジンも通らない。
	rec = call(http.MethodGet, map[string]string{"Origin": "https://" + previewHostname(slug, 5432, e.domain)})
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("a non-allowlisted port was accepted as an origin: %q", got)
	}
}

// 再発行（docs/log/81 §4.1）: 配ってしまった URL をその場で捨てられる。
func TestPreviewReissueKillsTheCurrentURL(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	ctx := context.Background()
	// 固定 ON で予約を作ってから再発行する（予約も捨てないと、次の起動で
	// 「捨てたはずの URL」が戻ってくる）。
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	st.PreviewFixedSlug = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	old := e.mintSlug(t)
	// 公開は起動の **あと** に入れる —— 起動のたびに OFF へ戻る（fail-closed）ので、
	// 先に入れても消える。ここで見たいのは再発行の効きなので、認証の往復は省く。
	raw, _ = e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st = parseWSSettings(raw)
	st.PreviewPublic = true
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatal(err)
	}
	if rec := e.get(t, old, 3000, "/"); rec.Code != http.StatusOK {
		t.Fatalf("before reissue: code=%d, want 200", rec.Code)
	}

	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/env/ws-settings/preview/reissue", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reissue: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec2 := e.get(t, old, 3000, "/"); rec2.Code != http.StatusNotFound {
		t.Fatalf("after reissue the old URL still resolves: code=%d", rec2.Code)
	}
	ws, ok, err := e.mgr.store.GetWorkspaceByMembership(ctx, e.ws.MembershipID)
	if err != nil || !ok || ws.PreviewSlug == "" || ws.PreviewSlug == old {
		t.Fatalf("reissue did not mint a new slug (got %q, old %q)", ws.PreviewSlug, old)
	}
	raw, _ = e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	if parseWSSettings(raw).PreviewReservedSlug == old {
		t.Fatal("the discarded slug is still reserved — it would come back on the next start")
	}
	// ★ 「その場で引き直した」ことを応答で言う。Console はこれで文言を分けるので、
	// 落とすと成功が「押しても無反応」に見える（実際にそう報告された）。
	if !decodeReissued(t, rec) {
		t.Error("previewReissued=false although a new slug was minted")
	}
}

// 停止中（slug 未発行）の再発行は **成功するが何も起きない**。★ その区別が応答に
// 出ていることを固定する —— ここが無いと、Console は成功と無反応を言い分けられない。
func TestPreviewReissueOnStoppedWorkspaceSaysNothingHappened(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	ctx := context.Background()
	e.mintSlug(t)
	// 停止 = slug の失効。
	if err := e.mgr.store.SetWorkspacePreviewSlug(ctx, e.ws.ID, ""); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/env/ws-settings/preview/reissue", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reissue while stopped: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if decodeReissued(t, rec) {
		t.Error("previewReissued=true although the workspace had no slug to discard")
	}
}

func decodeReissued(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		PreviewReissued bool `json:"previewReissued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode reissue response: %v (%s)", err, rec.Body.String())
	}
	return body.PreviewReissued
}
