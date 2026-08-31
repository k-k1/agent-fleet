// preview_share_e2e_test.go — 同じテナントのメンバーへの共有（docs/81 §14 / ADR 0062
// 決定 14〜17）。newPreviewHostEnv の実ルート表をそのまま叩き、閲覧者を「所有者では
// ない、同じテナントの人」として通す。
//
// ★ 検査の核は 2 つ:
//   - 許可されているときに**通ること**（ハンドシェイクから中継まで）
//   - 許可を外した**次のリクエストで閉じること**（cookie は生きているのに）。ここが
//     壊れると、共有を切ったつもりで 12 時間見え続ける（決定 15）。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// previewViewer は同じテナントのもう 1 人（所有者ではない）。
type previewViewer struct {
	userKey      string
	identityID   string
	membershipID string
}

// addViewer creates a second active member of the same tenant, with a workspace of its
// own (資源を建てさせないため先に作る — resolveFull がそこで createWorkspace を呼ぶと
// テストがランタイムの都合に引きずられる)。
func (e *previewHostEnv) addViewer(t *testing.T, userKey string) previewViewer {
	t.Helper()
	ctx := context.Background()
	ident, err := e.mgr.store.UpsertIdentity(ctx, "", userKey, "")
	if err != nil {
		t.Fatalf("viewer identity: %v", err)
	}
	mem, err := e.mgr.store.EnsureMembership(ctx, ident.ID, e.ws.TenantID, "member")
	if err != nil {
		t.Fatalf("viewer membership: %v", err)
	}
	ws := Workspace{ID: "ws-" + userKey, TenantID: e.ws.TenantID, MembershipID: mem.ID,
		ContainerName: "c-" + userKey, Network: "n-" + userKey, DataDir: "d-" + userKey,
		AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: nowTS()}
	if err := e.mgr.store.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("viewer workspace: %v", err)
	}
	return previewViewer{userKey: userKey, identityID: ident.ID, membershipID: mem.ID}
}

// asUser runs fn with the dev-auth identity switched to userKey. AUTH=dev は
// resolveIdentity が devUser をそのまま返すので、これが「別の人としてアクセスする」
// 一番短い経路になる。
func (e *previewHostEnv) asUser(userKey string, fn func()) {
	prev := e.mgr.devUser
	e.mgr.devUser = userKey
	defer func() { e.mgr.devUser = prev }()
	fn()
}

func (e *previewHostEnv) setPreviewSettings(t *testing.T, mut func(*wsSettings)) {
	t.Helper()
	ctx := context.Background()
	raw, _ := e.mgr.store.GetWorkspaceSettings(ctx, e.ws.ID)
	st := parseWSSettings(raw)
	mut(&st)
	if err := e.mgr.store.SetWorkspaceSettings(ctx, e.ws.ID, toWSSettingsJSON(t, st)); err != nil {
		t.Fatalf("save ws settings: %v", err)
	}
}

// console issues a request against the Console-origin mux (authGate は dev 認証で素通り)。
func (e *previewHostEnv) console(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// viewerCookie walks the whole handshake as `viewer` and returns the minted af_pv.
func (e *previewHostEnv) viewerCookie(t *testing.T, v previewViewer, slug string, port int) *http.Cookie {
	t.Helper()
	var back string
	e.asUser(v.userKey, func() {
		rec := e.console(t, "https://af.example.com"+previewHandshakePath+
			"?slug="+slug+"&port="+strconv.Itoa(port)+"&next=/")
		if rec.Code != http.StatusFound {
			t.Fatalf("viewer handshake: code=%d body=%s", rec.Code, rec.Body.String())
		}
		back = rec.Header().Get("Location")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, back, nil)
	req.Host = previewHostname(slug, port, e.domain)
	req.Header.Set("X-Forwarded-Proto", "https")
	e.host.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("viewer callback: code=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == previewAuthCookie {
			return c
		}
	}
	t.Fatal("viewer callback minted no af_pv cookie")
	return nil
}

// 共有していない Workspace は、同じテナントの他人から見ても「無い」。★ 401 でも 403 でも
// なく 404 —— 「ログインすれば見えるかもしれない」と読める答えを返さない。
func TestPreviewNotSharedIsNotFoundForAColleague(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)

	e.asUser(v.userKey, func() {
		rec := e.console(t, "https://af.example.com"+previewHandshakePath+"?slug="+slug+"&port=3000&next=/")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("colleague handshake without sharing: code=%d, want 404", rec.Code)
		}
	})
}

// 共有を ON にすると、同じテナントの他人がハンドシェイクを通り、実際に中継される。
func TestPreviewTenantShareLetsAColleagueThrough(t *testing.T) {
	var seen *http.Request
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })

	ck := e.viewerCookie(t, v, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code != http.StatusOK {
		t.Fatalf("shared preview for a colleague: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen == nil {
		t.Fatal("the Agent never saw the colleague's request")
	}
	// ★ 閲覧者の cookie が焼いているのは閲覧者自身の membership（所有者のではない）。
	cl, ok := newPreviewHostAPI(e.cfg).verifyClaims(ck.Value)
	if !ok {
		t.Fatal("viewer cookie does not verify")
	}
	if cl.MembershipID != v.membershipID {
		t.Errorf("cookie membership=%q, want the VIEWER's %q", cl.MembershipID, v.membershipID)
	}
}

// ★ 共有を切ったら、生きている cookie を持っていても**次のリクエストで**閉じる
// （決定 15）。ここが cookie に焼かれていると、切ったつもりで 12 時間見え続ける。
func TestPreviewShareRevokeClosesTheNextRequest(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	ck := e.viewerCookie(t, v, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code != http.StatusOK {
		t.Fatalf("precondition: shared preview should serve, got %d", rec.Code)
	}

	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = false })

	rec := e.get(t, slug, 3000, "/", ck)
	if rec.Code == http.StatusOK {
		t.Fatal("the colleague's cookie still opened the preview after the share was revoked")
	}
	// 同じ cookie で所有者は通り続ける（切ったのは共有であって、自分の入口ではない）。
	owner := e.viewerCookie(t, previewViewer{userKey: e.mgr.devUser, membershipID: e.ws.MembershipID}, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", owner); rec.Code != http.StatusOK {
		t.Fatalf("owner lost access when the share was revoked: code=%d", rec.Code)
	}
}

// テナントから外された人は、cookie が生きていても閉じる（GetMembershipByID は active
// 行しか返さない）。
func TestPreviewShareInactiveMemberIsClosedOut(t *testing.T) {
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()
	e := newPreviewHostEnv(t, agent.URL)
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	ck := e.viewerCookie(t, v, slug, 3000)
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code != http.StatusOK {
		t.Fatalf("precondition: shared preview should serve, got %d", rec.Code)
	}

	if err := e.mgr.store.SetMembershipStatus(context.Background(), v.membershipID, "inactive"); err != nil {
		t.Fatal(err)
	}
	if rec := e.get(t, slug, 3000, "/", ck); rec.Code == http.StatusOK {
		t.Fatal("an offboarded member's cookie still opened the preview")
	}
}

// ⚠️ 共有の設定は起動をまたいで残る（決定 14）—— 公開モードとの意図的な違い。
func TestPreviewTenantShareSurvivesRestartWhilePublicDoesNot(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true; st.PreviewPublic = true })
	e.mintSlug(t)
	raw, _ := e.mgr.store.GetWorkspaceSettings(context.Background(), e.ws.ID)
	st := parseWSSettings(raw)
	if st.PreviewPublic {
		t.Error("public mode survived a start (fail-closed・決定 12)")
	}
	if !st.PreviewTenantShare {
		t.Error("tenant sharing was reset by a start — 用途が必ず再起動をまたぐので使えなくなる（決定 14）")
	}
}

// 固定リダイレクタは、起動ごとに変わる slug を毎回引き直す（決定 17）。
func TestPreviewOpenFollowsTheCurrentSlug(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	slug1 := e.mintSlug(t)

	open := "https://af.example.com" + previewOpenPath + "?owner=" + url.QueryEscape("preview-user") + "&port=3000"
	e.asUser(v.userKey, func() {
		rec := e.console(t, open)
		if rec.Code != http.StatusFound {
			t.Fatalf("/preview-open: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); !strings.Contains(got, "slug="+slug1) {
			t.Fatalf("Location=%q, want the handshake for the current slug %q", got, slug1)
		}
	})

	// 再起動 = slug の引き直し。★ 同じリンクが、新しい slug を指す。
	slug2 := e.mintSlug(t)
	if slug1 == slug2 {
		t.Fatal("precondition: the slug should have rotated")
	}
	e.asUser(v.userKey, func() {
		rec := e.console(t, open)
		if got := rec.Header().Get("Location"); !strings.Contains(got, "slug="+slug2) {
			t.Fatalf("after a restart Location=%q, want the NEW slug %q", got, slug2)
		}
	})
}

// 共有していない相手には 404、停止中は「起動していない」。★ 後者を答え分けてよいのは、
// 呼び手が見てよい人だと確定した後だから（docs/81 §14.6）。
func TestPreviewOpenRefusals(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	e.mintSlug(t)
	open := "https://af.example.com" + previewOpenPath + "?owner=preview-user&port=3000"

	e.asUser(v.userKey, func() {
		if rec := e.console(t, open); rec.Code != http.StatusNotFound {
			t.Fatalf("not shared: code=%d, want 404", rec.Code)
		}
	})
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	e.asUser(v.userKey, func() {
		if rec := e.console(t, "https://af.example.com"+previewOpenPath+"?owner=preview-user&port=9999"); rec.Code != http.StatusNotFound {
			t.Fatalf("port outside the allowlist: code=%d, want 404", rec.Code)
		}
	})
	// 停止 = slug の失効。⚠️ ここで自動起動に繋がない（決定 16）。
	if err := e.mgr.store.SetWorkspacePreviewSlug(context.Background(), e.ws.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.asUser(v.userKey, func() {
		if rec := e.console(t, open); rec.Code != http.StatusConflict {
			t.Fatalf("stopped workspace: code=%d, want 409 (not a start)", rec.Code)
		}
	})
}

// 一覧に出るのは opt-in した他人だけ（自分は出さない・共有していない人も出さない）。
func TestSharedPreviewListShowsOnlyOptedInOthers(t *testing.T) {
	e := newPreviewHostEnv(t, "http://127.0.0.1:1")
	v := e.addViewer(t, "colleague")
	slug := e.mintSlug(t)

	read := func() []map[string]any {
		t.Helper()
		var body struct {
			Domain string           `json:"domain"`
			Items  []map[string]any `json:"items"`
		}
		var rec *httptest.ResponseRecorder
		e.asUser(v.userKey, func() { rec = e.console(t, "https://af.example.com/api/preview/shared") })
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/preview/shared: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		return body.Items
	}

	if got := read(); len(got) != 0 {
		t.Fatalf("nothing is shared yet, got %v", got)
	}
	e.setPreviewSettings(t, func(st *wsSettings) { st.PreviewTenantShare = true })
	items := read()
	if len(items) != 1 {
		t.Fatalf("want exactly the owner's entry, got %v", items)
	}
	if items[0]["ownerUserKey"] != "preview-user" {
		t.Errorf("ownerUserKey=%v, want preview-user", items[0]["ownerUserKey"])
	}
	if items[0]["running"] != true {
		t.Errorf("running=%v, want true while a slug is issued", items[0]["running"])
	}
	urls, _ := items[0]["urls"].(map[string]any)
	if urls["3000"] != previewURLFor(slug, 3000, e.domain) {
		t.Errorf("urls[3000]=%v, want the current preview URL", urls["3000"])
	}

	// 停止すると行は残るが URL は消える（発行されていない URL は見せない）。
	if err := e.mgr.store.SetWorkspacePreviewSlug(context.Background(), e.ws.ID, ""); err != nil {
		t.Fatal(err)
	}
	items = read()
	if len(items) != 1 || items[0]["running"] != false {
		t.Fatalf("a stopped workspace should still be listed as not running, got %v", items)
	}
	if urls, _ := items[0]["urls"].(map[string]any); len(urls) != 0 {
		t.Errorf("urls=%v, want none while the workspace is stopped", urls)
	}
}
