package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// handoffAPIFixture — 所有者 1 人・共有先候補 1 人・Agent スタブ 1 台。
type handoffAPIFixture struct {
	st        *store.SQL
	api       sessionHandoffAPI
	res       *resolved
	owner     store.Membership
	recipient store.Membership
	tenant    store.Tenant
	agentHits map[string]int
}

func newHandoffAPIFixture(t *testing.T, sessions []map[string]any) handoffAPIFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	oi, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	ri, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	owner, _ := st.EnsureMembership(ctx, oi.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, ri.ID, tenant.ID, "member")
	ownerViews, _ := st.ListMemberships(ctx, oi.ID)
	ws := store.Workspace{ID: "ws-owner", TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "owner",
		Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	hits := map[string]int{}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/sessions/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": sessions})
		case "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{map[string]any{"workingCopyId": "wc-1"}}})
		case "/sessions/session-1/handoff-context":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"repo": "private", "vcs": "git", "branch": "main", "remote": "https://example.com/x.git",
				"headSha": "abcdef1234567890", "ahead": 0, "dirty": false,
			})
		default:
			// 実 Agent と同じ形（JSON のエラー本文）で返す。素の http.NotFound は HTML を
			// 返すので、CP 側の「上流の応答が壊れている」経路にしか当たらない。
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "no such session"}})
		}
	}))
	t.Cleanup(agent.Close)
	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: ws,
	}}}
	share := newSessionShareAPI(mgr)
	return handoffAPIFixture{
		st: st, api: newSessionHandoffAPI(mgr, share), owner: owner, recipient: recipient, tenant: tenant,
		agentHits: hits,
		res:       &resolved{ident: oi, mv: ownerViews[0], ws: ws, rt: stubRuntime{endpoint: agent.URL, token: "tok"}},
	}
}

func (f handoffAPIFixture) getRecipients(t *testing.T, session string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session+"/handoff-recipients", nil)
	req.SetPathValue("name", session)
	rec := httptest.NewRecorder()
	f.api.recipients(rec, req, f.res)
	return rec
}

var oneSession = []map[string]any{{
	"name": "session-1", "kind": "claude", "dir": "/home/dev/repos/private", "repo": "private", "workingCopyId": "wc-1",
}}

// TestHandoffRecipientsUnsharedSessionAnswers — 誰にも共有していないセッションで開いても、
// 画面が待ち続けないこと。
//
// ⚠️ ここが実際に壊れていた。共有が 1 つも無いと `shared_session_catalog` に行そのものが
// 無く、`catalogForOwnedSession` は 404 を返していた。UI から見ると「宛先が 0 人」と
// 「セッションが見つからない」の区別が付かず、しかも本文が英語のまま出るので、利用者には
// 読み込みが終わらないのと同じに見える。共有していないことは**正常な状態**なので、
// 200 ＋ 空の候補で答えて「先に共有してください」を UI に出させる。
func TestHandoffRecipientsUnsharedSessionAnswers(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	rec := f.getRecipients(t, "session-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s — 共有していないのは正常な状態なので 200 で答えるべき", rec.Code, rec.Body.String())
	}
	var got struct {
		Members []map[string]string `json:"members"`
		Context map[string]any      `json:"context"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body=%s err=%v", rec.Body.String(), err)
	}
	if len(got.Members) != 0 {
		t.Fatalf("members=%v, want empty", got.Members)
	}
	// 共有していなくても座標は返す（push ゲートは共有の有無と独立に効く）。
	if got.Context["branch"] != "main" {
		t.Fatalf("context=%v, want the working copy's coordinates", got.Context)
	}
}

// 実在しないセッションは 404 のまま。「共有していない」と混ぜない。
func TestHandoffRecipientsUnknownSessionIs404(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	if rec := f.getRecipients(t, "no-such-session"); rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandoffRecipientsListsSharedMembers(t *testing.T) {
	ctx := context.Background()
	f := newHandoffAPIFixture(t, oneSession)
	// 共有規則を張ってから同期させる（catalog 行は同期で作られる）。
	if err := f.st.PutSessionShare(ctx, store.SessionShare{ID: "share-1", TenantID: f.tenant.ID,
		OwnerMembershipID: f.owner.ID, RecipientMembershipID: f.recipient.ID, ScopeType: "session",
		ScopeKey: "session-1", Permission: "ro", CreatedAt: store.NowTS(), UpdatedAt: store.NowTS()}); err != nil {
		t.Fatal(err)
	}
	rec := f.getRecipients(t, "session-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "recipient@example.com") {
		t.Fatalf("RO 共有の相手が宛先候補に出ていない: %s", rec.Body.String())
	}
}

// TestHandoffRecipientsThrottlesInventorySync — 宛先一覧の**読み**が所有者 Workspace の
// 在庫同期を毎回走らせないこと。
//
// ⚠️ `/repos` は作業コピーごとに git を回すので、worktree が増えるほど重い。モーダルを
// 開くたびに走らせると「読み込み中」のまま数秒止まって見える（共有の読み取りが
// `freshCatalog` で間引いているのと同じ理由）。差し出しの瞬間だけは exact に取り直す。
func TestHandoffRecipientsThrottlesInventorySync(t *testing.T) {
	f := newHandoffAPIFixture(t, oneSession)
	if rec := f.getRecipients(t, "session-1"); rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	first := f.agentHits["/repos"]
	if first == 0 {
		t.Fatal("最初の 1 回は同期するはず")
	}
	for i := 0; i < 3; i++ {
		if rec := f.getRecipients(t, "session-1"); rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	if got := f.agentHits["/repos"]; got != first {
		t.Fatalf("/repos hits=%d (first=%d) — TTL 内の再取得で在庫同期が走っている", got, first)
	}
}
