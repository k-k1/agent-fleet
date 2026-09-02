package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// smokeEnv wires the real route table (buildMux) to a real SQLite store in dev
// auth mode — no docker / agent involved. docs/log/23 P0-2: these are the regression
// detectors for handler moves; they assert status + known JSON keys, not shapes.
func smokeEnv(t *testing.T) (config, *http.ServeMux) {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	mgr := &manager{
		rts:             map[string]cachedRT{},
		store:           st,
		dataRoot:        t.TempDir(),
		authMode:        "dev",
		devUser:         "smoke",
		provisionMode:   "auto",
		defaultTenantID: dt.ID,
		conns:           newConnRegistry(),
	}
	cfg := config{consoleDir: t.TempDir(), mgr: mgr, egressDedup: &egressAuditDedup{}}
	return cfg, buildMux(cfg)
}

func smokeGet(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestSmokeHealthz(t *testing.T) {
	_, mux := smokeEnv(t)
	w := smokeGet(t, mux, "GET", "/healthz")
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("healthz: %d %q", w.Code, w.Body.String())
	}
}

func TestSmokeVersion(t *testing.T) {
	_, mux := smokeEnv(t)
	w := smokeGet(t, mux, "GET", "/api/version")
	if w.Code != http.StatusOK {
		t.Fatalf("version: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got["version"] != "dev" {
		t.Fatalf("version payload: %v", got)
	}
	// /api/version must stay behind the auth gate (docs/log/35 §35.6.1) — only
	// /healthz is the unauthenticated probe.
	if isAuthExempt("/api/version") {
		t.Fatalf("/api/version must not be auth-exempt")
	}
}

func TestSmokeWhoami(t *testing.T) {
	_, mux := smokeEnv(t)
	w := smokeGet(t, mux, "GET", "/api/whoami")
	if w.Code != http.StatusOK {
		t.Fatalf("whoami: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got["auth_mode"] != "dev" || got["resolved_user"] != "smoke" {
		t.Fatalf("whoami payload: %v", got)
	}
}

// smokeProxyEnv is smokeEnv in AUTH=proxy with a plain member — the only way to get a
// NON-administrator now that AUTH=dev's fixed user is a super_admin (docs/log/71 §71.6).
func smokeProxyEnv(t *testing.T) (config, *http.ServeMux) {
	t.Helper()
	cfg, _ := smokeEnv(t)
	cfg.mgr.authMode = "proxy"
	cfg.mgr.emailHeader = "X-Forwarded-Email"
	cfg.mgr.superAdmins = map[string]bool{} // nobody: the caller below is a member
	return cfg, buildMux(cfg)
}

// /api/tenants auto-provisions the dev user into the default tenant (AF_PROVISION=auto)
// and reports super_admin=true — in dev mode the single fixed user IS the operator, so
// the tenant settings screen is reachable on a native / WSL deployment (docs/log/71 §71.6).
// The MEMBERSHIP role is a different axis and stays "member".
func TestSmokeTenants(t *testing.T) {
	_, mux := smokeEnv(t)
	w := smokeGet(t, mux, "GET", "/api/tenants")
	if w.Code != http.StatusOK {
		t.Fatalf("tenants: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Tenants []struct {
			Slug string `json:"slug"`
			Role string `json:"role"`
		} `json:"tenants"`
		SuperAdmin bool `json:"super_admin"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(got.Tenants) != 1 || got.Tenants[0].Slug != "default" || got.Tenants[0].Role != "member" {
		t.Fatalf("tenants payload: %+v", got)
	}
	if !got.SuperAdmin {
		t.Fatal("dev user must be super_admin (docs/log/71 §71.6)")
	}
}

// A super_admin-only route refuses a plain member with the shared error shape
// {error:{code,message}} — the contract ERR_TEXT keys off (client.ts).
func TestSmokeAdminForbidden(t *testing.T) {
	_, mux := smokeProxyEnv(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/admin/tenants", nil)
	r.Header.Set("X-Forwarded-Email", "member@example.com")
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin create tenant: want 403 got %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got.Error.Code != "forbidden" {
		t.Fatalf("error code: %q", got.Error.Code)
	}
}

// Memo CRUD is membership-scoped and store-only (no workspace build) — the
// lightest authenticated member route.
func TestSmokeMemosEmpty(t *testing.T) {
	_, mux := smokeEnv(t)
	w := smokeGet(t, mux, "GET", "/api/memos")
	if w.Code != http.StatusOK {
		t.Fatalf("memos: %d %s", w.Code, w.Body.String())
	}
	var got []any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) != 0 {
		t.Fatalf("memos payload: %v err=%v", got, err)
	}
}

// The catch-all static Console 404s unknown paths (and stays last in the table).
func TestSmokeStaticCatchAll(t *testing.T) {
	_, mux := smokeEnv(t)
	if w := smokeGet(t, mux, "GET", "/no-such-asset.js"); w.Code != http.StatusNotFound {
		t.Fatalf("static catch-all: want 404 got %d", w.Code)
	}
}

// The Chromium attachment action link (docs/log/53 §53.7) is a Console route with no
// file behind it. It regressed to the catch-all's 404 once — the whole one-click
// hand-off is dead when this 404s, and no unit test above the parser noticed.
func TestBrowserAttachmentActionServesConsoleShell(t *testing.T) {
	cfg, mux := smokeEnv(t)
	shell := []byte("<!doctype html><title>console</title>")
	if err := os.WriteFile(filepath.Join(cfg.consoleDir, "index.html"), shell, 0o644); err != nil {
		t.Fatalf("seed console shell: %v", err)
	}
	id := "ba_0123456789abcdef0123456789abcdef"
	for _, path := range []string{"/open/browser-attachment/" + id, "/open/browser-attachment/" + id + "/"} {
		w := smokeGet(t, mux, "GET", path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: want 200 got %d", path, w.Code)
		}
		if !bytes.Equal(w.Body.Bytes(), shell) {
			t.Fatalf("%s: want the console shell, got %q", path, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control=%q", path, got)
		}
	}
	// The action route must stay session-gated — it is a Console surface, and the
	// id is not an authorization token (docs/log/53 §53.6).
	if isAuthExempt("/open/browser-attachment/" + id) {
		t.Fatal("the action route must NOT be auth exempt")
	}
}

// The registry-based isAuthExempt must reproduce the historical hardcoded set
// (oauth_google.go pre-P2-W1) exactly — a drifted exemption either locks out an
// internal caller or exposes a session-gated path.
func TestAuthExemptRegistry(t *testing.T) {
	smokeEnv(t) // buildMux registers the exemptions
	exempt := []string{
		"/login", "/healthz", "/oauth2/callback", "/mcp", "/mcp/sub",
		"/internal/egress", "/git/default/repo.git/info/refs", "/brand/banner.png",
		"/agent-fleet", "/agent-fleet/old/path",
	}
	for _, p := range exempt {
		if !isAuthExempt(p) {
			t.Errorf("%s must be exempt", p)
		}
	}
	gated := []string{"/", "/api/tenants", "/api/sessions", "/ws/terminal", "/ws/browser", "/ws/browser-attachments", "/gitx", "/mcpx", "/loginx"}
	for _, p := range gated {
		if isAuthExempt(p) {
			t.Errorf("%s must NOT be exempt", p)
		}
	}
}

// CP は明示許可リストなので、Agent 側に増えた収集ルートの登録漏れを回帰検知する。
func TestBrowserAttachmentListProxyRouteRegistered(t *testing.T) {
	_, mux := smokeEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/browser/attachments", nil)
	_, pattern := mux.Handler(req)
	if pattern != "GET /api/browser/attachments" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// ミラー本文のパス参照解決: 同じく明示許可リスト方式なので、Agent 側 POST /fs/resolve に
// 対応する登録漏れを回帰検知する。落ちていると、リンクが 1 つも付かないという「静かな」
// 壊れ方をする（Console 側は解決できなかった＝実在しない、と読むため）。
func TestFSResolveProxyRouteRegistered(t *testing.T) {
	_, mux := smokeEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fs/resolve", nil)
	_, pattern := mux.Handler(req)
	if pattern != "POST /api/fs/resolve" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// エディタ AI 変更提案（docs/log/44 Phase 4）: CP は明示許可リスト方式なので、Agent 側
// ルートに対応する /api/fs/suggest-edit の登録漏れを回帰検知する。
func TestFSSuggestEditProxyRouteRegistered(t *testing.T) {
	_, mux := smokeEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/fs/suggest-edit", nil)
	_, pattern := mux.Handler(req)
	if pattern != "POST /api/fs/suggest-edit" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// ★ CP は明示許可リストなので、Agent 側にルートを足しただけでは Console から届かない
// （再発常習: docs/log/78 の取り込みジョブも Agent → CP の順で 2 か所要る）。ここは CP 側の
// 登録と、監査分類が付いていることを固定する。
func TestCPRegistersRepoJobRoutes(t *testing.T) {
	_, mux := smokeEnv(t)
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/repo-jobs", "GET /api/repo-jobs"},
		{http.MethodDelete, "/api/repo-jobs/rj123", "DELETE /api/repo-jobs/{id}"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if _, pattern := mux.Handler(req); pattern != tc.want {
			t.Errorf("%s %s → pattern=%q, want %q", tc.method, tc.path, pattern, tc.want)
		}
	}
	// 中止／既読は変更操作なので監査に載る（誰が取り込みを止めたかは後から要る）。
	req := httptest.NewRequest(http.MethodDelete, "/api/repo-jobs/rj123", nil)
	action, target, ok := auditActionTarget(req)
	if !ok || action != "repo.job.cancel" || target != "rj123" {
		t.Errorf("audit = %q/%q/%v, want repo.job.cancel/rj123/true", action, target, ok)
	}
}
