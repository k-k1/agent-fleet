package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// marksFixture は所有者 1・共有先 1（権限は permission）・部外者 1 の共有を組み立てて、
// 所有者 Workspace の Agent を stub で置き換える。agentMarks は GET が返す一覧、
// seen は Agent が実際に受け取った書き込みの記録。
type marksFixture struct {
	api       sessionShareAPI
	catalogID string

	recipientIdent Identity
	recipientView  MembershipView
	strangerIdent  Identity
	strangerView   MembershipView

	agentMarks []any
	seenPOST   map[string]any
	seenDELETE string
}

func newMarksFixture(t *testing.T, permission string) *marksFixture {
	t.Helper()
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner@example.com", "owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient@example.com", "recipient", "")
	strangerIdentity, _ := st.UpsertIdentity(ctx, "stranger@example.com", "stranger", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	_, _ = st.EnsureMembership(ctx, strangerIdentity.ID, tenant.ID, "member")
	recipientViews, _ := st.ListMemberships(ctx, recipientIdentity.ID)
	strangerViews, _ := st.ListMemberships(ctx, strangerIdentity.ID)

	workspace := Workspace{ID: "ws-owner", TenantID: tenant.ID, MembershipID: owner.ID,
		ContainerName: "owner", Network: "test", DataDir: "/data/owner", AgentPort: "1", AgentToken: "tok",
		State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	f := &marksFixture{
		catalogID:      "catalog-1",
		recipientIdent: recipientIdentity,
		recipientView:  recipientViews[0],
		strangerIdent:  strangerIdentity,
		strangerView:   strangerViews[0],
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sessions/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{map[string]any{
				"name": "session-1", "kind": "claude", "dir": "/home/dev/repos/private", "repo": "private", "workingCopyId": "wc-1",
			}}})
		case r.URL.Path == "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{map[string]any{"workingCopyId": "wc-1"}}})
		case r.URL.Path == "/sessions/session-1/marks" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"marks": f.agentMarks})
		case r.URL.Path == "/sessions/session-1/marks" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &f.seenPOST)
			_ = json.NewEncoder(w).Encode(map[string]any{"mark": f.seenPOST})
		case r.URL.Path == "/sessions/session-1/marks" && r.Method == http.MethodDelete:
			f.seenDELETE = r.URL.RawQuery
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected agent call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(agent.Close)

	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace,
	}}}
	f.api = newSessionShareAPI(mgr)
	catalog := SharedSessionCatalog{ID: f.catalogID, WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID,
		Name: "session-1", Kind: "claude", WorkingCopyID: "wc-1", LastSeen: nowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSessionShare(ctx, SessionShare{ID: "share-1", TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "session-1", Permission: permission,
		CreatedAt: nowTS(), UpdatedAt: nowTS()}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *marksFixture) call(method, query string, body string, ident Identity, mv MembershipView) *httptest.ResponseRecorder {
	url := "/api/shared-sessions/" + f.catalogID + "/marks"
	if query != "" {
		url += "?" + query
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, url, rdr)
	req.SetPathValue("id", f.catalogID)
	rec := httptest.NewRecorder()
	f.api.marks(rec, req, ident, mv)
	return rec
}

func TestSharedMarksRead(t *testing.T) {
	f := newMarksFixture(t, "ro")
	f.agentMarks = []any{map[string]any{
		"id": "mk_1", "turn": "uuid-1", "part": float64(0), "kind": "text",
		"quote": "the sentence", "nth": float64(0), "color": "yellow",
		"author": "recipient@example.com", "created_at": float64(1700000000000),
	}}

	rec := f.call(http.MethodGet, "", "", f.recipientIdent, f.recipientView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	for _, want := range []string{"mk_1", "uuid-1", "the sentence", "recipient@example.com"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("visible field %q removed: %s", want, rec.Body.String())
		}
	}

	// 権限が無い相手には存在すら答えない。
	if denied := f.call(http.MethodGet, "", "", f.strangerIdent, f.strangerView); denied.Code != http.StatusNotFound {
		t.Fatalf("stranger status=%d body=%s", denied.Code, denied.Body.String())
	}
}

// ⚠️ 印の quote は位置復元のために共有先へ渡る。ツール行のように共有 DTO が座標を落として
// いる part の上の印まで中継すると、落としたはずのパスが quote として出て行く
// （docs/log/69 §69.4）。塗る場所の制限は Console と Agent にも掛かっているが、中継の出口でも
// 落とす — 片側が緩んだだけでは漏れないように。
func TestSharedMarksDropNonProseKind(t *testing.T) {
	f := newMarksFixture(t, "ro")
	f.agentMarks = []any{
		map[string]any{"id": "mk_ok", "turn": "u", "part": float64(0), "kind": "text", "quote": "prose", "nth": float64(0), "color": "blue"},
		map[string]any{"id": "mk_bad", "turn": "u", "part": float64(1), "kind": "tool", "quote": "/home/dev/repos/private/secret.ts", "nth": float64(0), "color": "blue"},
	}
	rec := f.call(http.MethodGet, "", "", f.recipientIdent, f.recipientView)
	body := rec.Body.String()
	if !strings.Contains(body, "mk_ok") {
		t.Fatalf("prose mark dropped: %s", body)
	}
	for _, secret := range []string{"mk_bad", "/home/dev/repos/private/secret.ts"} {
		if strings.Contains(body, secret) {
			t.Fatalf("非本文の印が中継された（%q）: %s", secret, body)
		}
	}
}

// RO は読めても書けない。書き込みは docs/log/59 §2 の RW と同じ線で切る（承認フローには
// 載せないが、権限そのものは同じ）。
func TestSharedMarksWriteNeedsRW(t *testing.T) {
	f := newMarksFixture(t, "ro")
	body := `{"id":"mk_2","turn":"uuid-1","part":0,"kind":"text","quote":"q","nth":0,"color":"green"}`
	if rec := f.call(http.MethodPost, "", body, f.recipientIdent, f.recipientView); rec.Code != http.StatusNotFound {
		t.Fatalf("RO POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := f.call(http.MethodDelete, "id=mk_2", "", f.recipientIdent, f.recipientView); rec.Code != http.StatusNotFound {
		t.Fatalf("RO DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.seenPOST != nil || f.seenDELETE != "" {
		t.Fatalf("RO の書き込みが Agent へ届いた: post=%+v delete=%q", f.seenPOST, f.seenDELETE)
	}
}

// ⚠️ author は申告を採らない。採ると共有先が所有者や別の共有先になりすませる。
func TestSharedMarksStampAuthor(t *testing.T) {
	f := newMarksFixture(t, "rw")
	body := `{"id":"mk_2","turn":"uuid-1","part":0,"kind":"text","quote":"q","nth":0,"color":"green","author":"owner@example.com"}`
	rec := f.call(http.MethodPost, "", body, f.recipientIdent, f.recipientView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f.seenPOST["author"] != "recipient@example.com" {
		t.Fatalf("author が上書きされていない: %+v", f.seenPOST)
	}
	if !strings.Contains(rec.Body.String(), "recipient@example.com") {
		t.Fatalf("応答に作成者が無い: %s", rec.Body.String())
	}

	// 座標を持つ part の上には、RW でも置けない。
	bad := `{"id":"mk_3","turn":"uuid-1","part":1,"kind":"tool","quote":"/private/x.ts","nth":0,"color":"green"}`
	if r := f.call(http.MethodPost, "", bad, f.recipientIdent, f.recipientView); r.Code != http.StatusBadRequest {
		t.Fatalf("non-prose POST status=%d body=%s", r.Code, r.Body.String())
	}
}

// 消せるのは自分の印だけ。判定そのものは Agent 側だが、CP が author を必ず添えることで
// 初めて成立する。
func TestSharedMarksDeleteCarriesAuthor(t *testing.T) {
	f := newMarksFixture(t, "rw")
	rec := f.call(http.MethodDelete, "id=mk_9", "", f.recipientIdent, f.recipientView)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(f.seenDELETE, "author=recipient%40example.com") || !strings.Contains(f.seenDELETE, "id=mk_9") {
		t.Fatalf("Agent へ渡ったクエリ: %q", f.seenDELETE)
	}
}
