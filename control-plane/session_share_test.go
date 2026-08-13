package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type shareStopRuntime struct{ stopped chan struct{} }

func (r shareStopRuntime) Start(context.Context) error  { return nil }
func (r shareStopRuntime) Stop(context.Context) error   { close(r.stopped); return nil }
func (r shareStopRuntime) State(context.Context) string { return "running" }
func (r shareStopRuntime) Endpoint() string             { return "" }
func (r shareStopRuntime) Token() string                { return "" }
func (r shareStopRuntime) Name() string                 { return "share-stop" }

type shareLifecycleRuntime struct {
	starts atomic.Int32
	stops  atomic.Int32
}

func (r *shareLifecycleRuntime) Start(context.Context) error  { r.starts.Add(1); return nil }
func (r *shareLifecycleRuntime) Stop(context.Context) error   { r.stops.Add(1); return nil }
func (r *shareLifecycleRuntime) State(context.Context) string { return "running" }
func (r *shareLifecycleRuntime) Endpoint() string             { return "" }
func (r *shareLifecycleRuntime) Token() string                { return "" }
func (r *shareLifecycleRuntime) Name() string                 { return "share-lifecycle" }

func TestEffectiveSharePermission(t *testing.T) {
	c := SharedSessionCatalog{OwnerMembershipID: "owner", Name: "s1", WorkingCopyID: "wc1"}
	shares := []SessionShare{
		{OwnerMembershipID: "owner", ScopeType: "session", ScopeKey: "s1", Permission: "ro"},
		{OwnerMembershipID: "owner", ScopeType: "worktree", ScopeKey: "wc1", Permission: "rw"},
		{OwnerMembershipID: "other", ScopeType: "session", ScopeKey: "s1", Permission: "rw"},
	}
	if got := effectivePermission(shares, c); got != "rw" {
		t.Fatalf("permission=%q", got)
	}
	if got := effectivePermission(shares[:1], c); got != "ro" {
		t.Fatalf("permission=%q", got)
	}
	if got := effectivePermission(shares[2:], c); got != "" {
		t.Fatalf("foreign owner permission=%q", got)
	}
}

// repo 共有はプロジェクト全体 — ベース直下だけでなく、その配下 worktree のセッションにも
// 効く(docs/59 §1)。所有者の作業は worktree 側で進むので、ここが効かないと「リポジトリを
// 共有した」のに共有先には何も見えない。逆に worktree 共有はその1つに閉じたままにする。
func TestRepoShareCoversWorktreeSessionsButWorktreeShareStaysNarrow(t *testing.T) {
	base := SharedSessionCatalog{OwnerMembershipID: "owner", Name: "s-base", WorkingCopyID: "wc-base"}
	wt := SharedSessionCatalog{OwnerMembershipID: "owner", Name: "s-wt", WorkingCopyID: "wc-wt",
		Worktree: true, Parent: "proj", ParentWorkingCopyID: "wc-base"}
	repoShare := []SessionShare{{OwnerMembershipID: "owner", ScopeType: "repo", ScopeKey: "wc-base", Permission: "ro"}}
	if got := effectivePermission(repoShare, base); got != "ro" {
		t.Fatalf("base under repo share=%q", got)
	}
	if got := effectivePermission(repoShare, wt); got != "ro" {
		t.Fatalf("worktree under repo share=%q", got)
	}
	wtShare := []SessionShare{{OwnerMembershipID: "owner", ScopeType: "worktree", ScopeKey: "wc-wt", Permission: "rw"}}
	if got := effectivePermission(wtShare, base); got != "" {
		t.Fatalf("worktree share must not reach the base copy: %q", got)
	}
	// 親不明(旧行 / marker 無し)は ParentWorkingCopyID が空。scope_key は空を拒否して
	// いるので、空同士でどの規則にも吸い寄せられないことを固定する。
	orphan := SharedSessionCatalog{OwnerMembershipID: "owner", Name: "s-orphan", WorkingCopyID: "wc-x"}
	if got := effectivePermission(repoShare, orphan); got != "" {
		t.Fatalf("unrelated copy matched a repo share: %q", got)
	}
	if got := effectivePermission([]SessionShare{{OwnerMembershipID: "owner", ScopeType: "repo", ScopeKey: "", Permission: "rw"}}, orphan); got != "" {
		t.Fatalf("empty scope key matched an empty parent: %q", got)
	}
}

func TestSharedTranscriptDTOAllowsContentAndRejectsEveryUnknownCoordinate(t *testing.T) {
	v := map[string]any{"jsonlPath": "/secret/log", "futureTopSecret": "hidden", "messages": []any{map[string]any{
		"cwd": "/home/dev/repos/private", "text": "visible", "role": "assistant", "idx": float64(1),
		"compact":   true,
		"branch":    "feature/secret-branch",
		"file_path": "/top/path", "parts": []any{map[string]any{
			"kind": "tool", "file": "secret.txt", "files": []any{"attachment.png"}, "filePath": "/camel",
			"file_path": "/snake", "path": "/generic", "attachmentPath": "/attach", "unknownCoordinate": "/future",
			"info": "edited", "output": "visible tool output", "edits": []any{map[string]any{"old": "a", "new": "b", "path": "/edit/path"}},
		}},
	}}}
	out := sharedTranscriptDTO(v)
	encoded, _ := json.Marshal(out)
	for _, secret := range []string{"jsonlPath", "futureTopSecret", "cwd", "file_path", "filePath", "attachmentPath", "unknownCoordinate", "secret.txt", "attachment.png", "/generic", "/edit/path",
		// branch names the owner's work in their repo and is not needed to render the
		// conversation, so it stays out of the allowlist like every other coordinate.
		"branch", "feature/secret-branch"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private/unknown field %q survived: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), "visible tool output") || !strings.Contains(string(encoded), `"old":"a"`) {
		t.Fatalf("visible allowlisted content removed: %s", encoded)
	}
	// compact is a display flag, not a coordinate: the recipient needs it to render a
	// compaction summary as a folded summary rather than one enormous turn.
	if !strings.Contains(string(encoded), `"compact":true`) {
		t.Fatalf("compact flag dropped, compaction summaries would render as plain turns: %s", encoded)
	}
}

func TestSharedMessagesAuthorizeAndRemoveWorkspacePaths(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
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

	var catalogDeleted atomic.Bool
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/sessions/catalog":
			if catalogDeleted.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{map[string]any{
				"name": "session-1", "kind": "codex", "dir": "/home/dev/repos/private", "repo": "private", "workingCopyId": "wc-1",
			}}})
		case "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{map[string]any{"workingCopyId": "wc-1"}}})
		case "/sessions/session-1/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonlPath": "/home/dev/.claude/session.jsonl",
				"messages": []any{map[string]any{
					"text": "visible", "cwd": "/home/dev/repos/private",
					"parts": []any{map[string]any{"info": "edited", "filePath": "/home/dev/repos/private/secret.go"}},
				}},
			})
		default:
			t.Errorf("path=%q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()

	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace,
	}}}
	api := newSessionShareAPI(mgr)
	catalog := SharedSessionCatalog{ID: "catalog-1", WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID,
		Name: "session-1", Kind: "codex", WorkingCopyID: "wc-1", LastSeen: nowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSessionShare(ctx, SessionShare{ID: "share-1", TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "session-1", Permission: "ro",
		CreatedAt: nowTS(), UpdatedAt: nowTS()}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/shared-sessions/catalog-1/messages", nil)
	req.SetPathValue("id", catalog.ID)
	rec := httptest.NewRecorder()
	api.messages(rec, req, recipientIdentity, recipientViews[0])
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["jsonlPath"]; ok {
		t.Fatal("jsonlPath survived")
	}
	message := payload["messages"].([]any)[0].(map[string]any)
	if _, ok := message["cwd"]; ok {
		t.Fatal("cwd survived")
	}
	part := message["parts"].([]any)[0].(map[string]any)
	if _, ok := part["filePath"]; ok {
		t.Fatal("filePath survived")
	}

	deniedReq := httptest.NewRequest(http.MethodGet, "/api/shared-sessions/catalog-1/messages", nil)
	deniedReq.SetPathValue("id", catalog.ID)
	denied := httptest.NewRecorder()
	api.messages(denied, deniedReq, strangerIdentity, strangerViews[0])
	if denied.Code != http.StatusNotFound {
		t.Fatalf("unauthorized status=%d body=%s", denied.Code, denied.Body.String())
	}

	// A bookmarked catalog id cannot bypass a live deletion simply because the recipient
	// skipped the shared-session list endpoint. Inventory reconciliation is throttled per
	// owner (freshCatalog), so the deletion is noticed on the next sync rather than on the
	// very next request — invalidateCatalog stands in for that window elapsing.
	//
	// Note what is NOT throttled: the share rules are re-evaluated from the database on
	// every request, so an explicit unshare still takes effect immediately (the stranger
	// case above runs through the same freshCatalog path and is still refused).
	catalogDeleted.Store(true)
	api.invalidateCatalog(owner.ID)
	deletedReq := httptest.NewRequest(http.MethodGet, "/api/shared-sessions/catalog-1/messages", nil)
	deletedReq.SetPathValue("id", catalog.ID)
	deleted := httptest.NewRecorder()
	api.messages(deleted, deletedReq, recipientIdentity, recipientViews[0])
	if deleted.Code != http.StatusNotFound {
		t.Fatalf("deleted direct-id status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

// The throttle must bound the staleness it introduces, and must not touch authorization.
// Both halves matter: skipping the sync is what makes a shared transcript load quickly,
// and re-checking the rules every time is what keeps an unshare immediate.
func TestFreshCatalogThrottlesInventoryButNotAuthorization(t *testing.T) {
	api := newSessionShareAPI(&manager{})
	if api.freshCatalog("owner-a") {
		t.Fatal("first call must reconcile — nothing has been synced yet")
	}
	if !api.freshCatalog("owner-a") {
		t.Fatal("second call within the TTL must reuse the reconciled inventory")
	}
	if api.freshCatalog("owner-b") {
		t.Fatal("the window is per owner — owner-b must not ride owner-a's sync")
	}
	api.invalidateCatalog("owner-a")
	if api.freshCatalog("owner-a") {
		t.Fatal("invalidateCatalog must force the next reconciliation")
	}
	// An expired stamp reconciles again, so staleness is bounded by shareCatalogTTL.
	api.syncedAt["owner-a"] = time.Now().Add(-shareCatalogTTL - time.Second)
	if api.freshCatalog("owner-a") {
		t.Fatal("a stamp older than shareCatalogTTL must reconcile")
	}
}

func TestSyncCatalogCapturesWorktreeAndParent(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner-wt@example.com", "owner-wt", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	ownerViews, _ := st.ListMemberships(ctx, ownerIdentity.ID)
	workspace := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n",
		DataDir: "d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{
				map[string]any{"name": "base-session", "kind": "codex", "dir": "/home/dev/repos/proj", "repo": "proj", "workingCopyId": "wc-base"},
				map[string]any{"name": "wt-session", "kind": "codex", "dir": "/home/dev/repos/proj@feat", "repo": "proj@feat", "workingCopyId": "wc-wt"},
			}})
		case "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{
				map[string]any{"workingCopyId": "wc-base"},
				map[string]any{"workingCopyId": "wc-wt", "worktree": true, "parent": "proj"},
			}})
		default:
			t.Errorf("path=%q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()

	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace,
	}}}
	api := newSessionShareAPI(mgr)
	resolvedOwner := &resolved{rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace, mv: ownerViews[0]}
	if err := api.syncCatalog(ctx, resolvedOwner); err != nil {
		t.Fatal(err)
	}

	catalog, err := st.ListSharedSessionCatalogByOwner(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("catalog rows=%d", len(catalog))
	}
	byName := map[string]SharedSessionCatalog{}
	for _, c := range catalog {
		byName[c.Name] = c
	}
	base, ok := byName["base-session"]
	if !ok || base.Worktree || base.Parent != "" {
		t.Fatalf("base row=%+v ok=%v", base, ok)
	}
	wt, ok := byName["wt-session"]
	if !ok || !wt.Worktree || wt.Parent != "proj" {
		t.Fatalf("worktree row=%+v ok=%v", wt, ok)
	}
}

// 受信側の一覧は「今そこにある会話」だけを出す: repo 共有はベース＋配下 worktree の
// セッションを覆い、所有者がアーカイブした行は(規則は残したまま)隠す。アーカイブ済みが
// 出続けると、所有者が畳んだ古いセッションが延々と残っているように見える — これが
// 「削除したはずのセッションが消えない」という報告の実体だった。
func TestListReceivedCoversWorktreesAndHidesArchived(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner-list@example.com", "owner-list", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient-list@example.com", "recipient-list", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	recipientViews, _ := st.ListMemberships(ctx, recipientIdentity.ID)
	workspace := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n",
		DataDir: "d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sessions/catalog":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{
				map[string]any{"name": "base-live", "kind": "codex", "repo": "proj", "workingCopyId": "wc-base"},
				map[string]any{"name": "wt-live", "kind": "codex", "repo": "proj@feat", "workingCopyId": "wc-wt"},
				map[string]any{"name": "base-archived", "kind": "codex", "repo": "proj", "workingCopyId": "wc-base", "archived": true},
			}})
		case "/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repos": []any{
				map[string]any{"name": "proj", "workingCopyId": "wc-base"},
				map[string]any{"name": "proj@feat", "workingCopyId": "wc-wt", "worktree": true, "parent": "proj"},
			}})
		default:
			t.Errorf("path=%q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()

	mgr := &manager{store: st, rts: map[string]cachedRT{owner.ID: {
		rt: stubRuntime{endpoint: agent.URL, token: "tok"}, ws: workspace,
	}}}
	api := newSessionShareAPI(mgr)
	if err := st.PutSessionShare(ctx, SessionShare{ID: newID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "repo", ScopeKey: "wc-base", Permission: "ro",
		CreatedAt: nowTS(), UpdatedAt: nowTS()}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	api.listReceived(rec, httptest.NewRequest(http.MethodGet, "/api/shared-sessions", nil), recipientIdentity, recipientViews[0])
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Sessions []struct {
			ID, Name, Permission string
			Worktree             bool
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, s := range payload.Sessions {
		listed[s.Name] = true
		if s.Permission != "ro" {
			t.Fatalf("permission=%q for %s", s.Permission, s.Name)
		}
	}
	if !listed["base-live"] || !listed["wt-live"] {
		t.Fatalf("repo share must cover the base copy AND its worktrees: %+v", payload.Sessions)
	}
	if listed["base-archived"] {
		t.Fatalf("archived session stayed in the recipient list: %+v", payload.Sessions)
	}

	// 直リンク(catalog id)からの読みも同じ扱い: 一覧から隠れているものは開けない。
	catalog, err := st.ListSharedSessionCatalogByOwner(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var archivedID string
	for _, c := range catalog {
		if c.Name == "base-archived" {
			archivedID = c.ID
		}
		if c.Name == "wt-live" && c.ParentWorkingCopyID != "wc-base" {
			t.Fatalf("worktree row lost its parent working copy id: %+v", c)
		}
	}
	if archivedID == "" {
		t.Fatal("archived row must stay in the catalog (restore brings it back)")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/shared-sessions/"+archivedID+"/messages", nil)
	req.SetPathValue("id", archivedID)
	denied := httptest.NewRecorder()
	api.messages(denied, req, recipientIdentity, recipientViews[0])
	if denied.Code != http.StatusConflict || !strings.Contains(denied.Body.String(), "owner_session_archived") {
		t.Fatalf("archived direct read status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func TestSearchRecipientsFiltersByEmailAndExcludesSelf(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	selfIdentity, _ := st.UpsertIdentity(ctx, "self@example.com", "self", "")
	aliceIdentity, _ := st.UpsertIdentity(ctx, "alice@acme.example", "alice", "")
	bobIdentity, _ := st.UpsertIdentity(ctx, "bob@other.com", "bob", "")
	if _, err := st.EnsureMembership(ctx, selfIdentity.ID, tenant.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, aliceIdentity.ID, tenant.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureMembership(ctx, bobIdentity.ID, tenant.ID, "member"); err != nil {
		t.Fatal(err)
	}
	selfViews, _ := st.ListMemberships(ctx, selfIdentity.ID)
	mgr := &manager{store: st}
	api := newSessionShareAPI(mgr)

	search := func(q string) []map[string]string {
		req := httptest.NewRequest(http.MethodGet, "/api/session-share-recipients?q="+q, nil)
		rec := httptest.NewRecorder()
		api.searchRecipients(rec, req, selfIdentity, selfViews[0])
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Members []map[string]string `json:"members"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Members
	}

	// 空クエリ: 自分以外の全active メンバーが返る(自分は除外)。
	all := search("")
	if len(all) != 2 {
		t.Fatalf("empty query members=%v", all)
	}
	for _, m := range all {
		if m["email"] == "self@example.com" {
			t.Fatalf("self appeared in results: %v", all)
		}
	}

	// email 部分一致で絞り込める。
	acme := search("acme.example")
	if len(acme) != 1 || acme[0]["email"] != "alice@acme.example" {
		t.Fatalf("filtered members=%v", acme)
	}

	// 大文字小文字は無視される。
	upper := search("ALICE")
	if len(upper) != 1 || upper[0]["userKey"] != "alice" {
		t.Fatalf("case-insensitive members=%v", upper)
	}

	if none := search("nobody-matches-this"); len(none) != 0 {
		t.Fatalf("unexpected match=%v", none)
	}
}

func TestWorkspaceStopWaitsForSharedApprovalLifecycleLock(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	identity, _ := st.UpsertIdentity(ctx, "owner-stop@example.com", "owner-stop", "")
	membership, _ := st.EnsureMembership(ctx, identity.ID, tenant.ID, "member")
	workspace := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: membership.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runtime := shareStopRuntime{stopped: make(chan struct{})}
	mgr := &manager{store: st, rts: map[string]cachedRT{}}
	lock := mgr.startLockFor(workspace.ID)
	lock.Lock() // the approval path holds this across its Agent operation
	done := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/workspace/stop", nil)
		newWorkspaceAPI(mgr, false).stop(rec, req, &resolved{rt: runtime, ws: workspace, mv: MembershipView{MembershipID: membership.ID}})
		close(done)
	}()
	select {
	case <-runtime.stopped:
		t.Fatal("workspace stopped while an approved operation held the lifecycle lock")
	case <-time.After(50 * time.Millisecond):
	}
	lock.Unlock()
	select {
	case <-runtime.stopped:
	case <-time.After(time.Second):
		t.Fatal("workspace stop did not continue after approval released the lifecycle lock")
	}
	<-done
}

func TestShareDowngradeWaitsForAuthorizedAgentOperation(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "owner-lock@example.com", "owner-lock", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "recipient-lock@example.com", "recipient-lock", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	ownerViews, _ := st.ListMemberships(ctx, ownerIdentity.ID)
	share := SessionShare{ID: newID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: "s1", Permission: "rw",
		CreatedAt: nowTS(), UpdatedAt: nowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	mgr := &manager{store: st}
	lock := mgr.shareLockFor(owner.ID)
	lock.Lock() // approval holds this only across its authorized Agent HTTP call
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPatch, "/api/session-shares/"+share.ID, strings.NewReader(`{"permission":"ro"}`))
		req.SetPathValue("id", share.ID)
		rec := httptest.NewRecorder()
		newSessionShareAPI(mgr).patch(rec, req, ownerIdentity, ownerViews[0])
		done <- rec
	}()
	select {
	case <-done:
		t.Fatal("share downgrade crossed an authorized Agent operation")
	case <-time.After(50 * time.Millisecond):
	}
	lock.Unlock()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("share downgrade did not continue after Agent operation finished")
	}
}

func TestOwnerLeaseSerializesShareAndLifecycleAcrossManagers(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	ownerIdentity, _ := st.UpsertIdentity(ctx, "ha-owner@example.com", "ha-owner", "")
	recipientIdentity, _ := st.UpsertIdentity(ctx, "ha-recipient@example.com", "ha-recipient", "")
	owner, _ := st.EnsureMembership(ctx, ownerIdentity.ID, tenant.ID, "member")
	recipient, _ := st.EnsureMembership(ctx, recipientIdentity.ID, tenant.ID, "member")
	ownerViews, _ := st.ListMemberships(ctx, ownerIdentity.ID)
	workspace := Workspace{ID: newID(), TenantID: tenant.ID, MembershipID: owner.ID, ContainerName: "c", Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	catalog := SharedSessionCatalog{ID: newID(), WorkspaceID: workspace.ID, OwnerMembershipID: owner.ID, Name: "ha-session", Kind: "codex", WorkingCopyID: "wc-ha", LastSeen: nowTS()}
	if err := st.ReplaceSharedSessionCatalog(ctx, workspace.ID, owner.ID, []SharedSessionCatalog{catalog}); err != nil {
		t.Fatal(err)
	}
	share := SessionShare{ID: newID(), TenantID: tenant.ID, OwnerMembershipID: owner.ID,
		RecipientMembershipID: recipient.ID, ScopeType: "session", ScopeKey: catalog.Name, Permission: "rw",
		CreatedAt: nowTS(), UpdatedAt: nowTS()}
	if err := st.PutSessionShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	proposal := SessionShareProposal{ID: newID(), TenantID: tenant.ID, CatalogID: catalog.ID,
		OwnerMembershipID: owner.ID, ProposerMembershipID: recipient.ID, Action: "turn", Ciphertext: "opaque",
		Status: "pending", CreatedAt: nowTS(), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if err := st.CreateSessionShareProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	managerA := &manager{store: st}
	managerB := &manager{store: st}
	localA := managerA.shareLockFor(owner.ID)
	localA.Lock() // a different manager has a distinct in-process mutex
	claimAt := time.Now().UTC()
	_, _, state, err := managerA.store.ClaimSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID,
		claimAt.Format(time.RFC3339), claimAt.Add(shareOwnerLease).Format(time.RFC3339))
	if err != nil || state != "claimed" {
		localA.Unlock()
		t.Fatalf("claim state=%q err=%v", state, err)
	}
	patch := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/session-shares/"+share.ID, strings.NewReader(`{"permission":"ro"}`))
		req.SetPathValue("id", share.ID)
		rec := httptest.NewRecorder()
		newSessionShareAPI(managerB).patch(rec, req, ownerIdentity, ownerViews[0])
		return rec
	}
	blocked := patch()
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "share_operation_in_progress") {
		localA.Unlock()
		t.Fatalf("cross-manager downgrade status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	runtime := &shareLifecycleRuntime{}
	resolvedOwner := &resolved{rt: runtime, ws: workspace, mv: ownerViews[0]}
	for _, invoke := range []func(http.ResponseWriter, *http.Request, *resolved){
		newWorkspaceAPI(managerB, false).start,
		newWorkspaceAPI(managerB, false).stop,
		newWorkspaceAPI(managerB, false).recreate,
		newWorkspaceAPI(managerB, false).cleanHome,
	} {
		rec := httptest.NewRecorder()
		invoke(rec, httptest.NewRequest(http.MethodPost, "/api/workspace/lifecycle", nil), resolvedOwner)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workspace_operation_in_progress") {
			localA.Unlock()
			t.Fatalf("cross-manager lifecycle status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	if runtime.stops.Load() != 0 || runtime.starts.Load() != 0 {
		localA.Unlock()
		t.Fatalf("blocked lifecycle reached runtime: starts=%d stops=%d", runtime.starts.Load(), runtime.stops.Load())
	}
	localA.Unlock()
	changed, err := st.FinalizeSessionShareProposal(ctx, proposal.ID, owner.ID, ownerIdentity.ID, nowTS())
	if err != nil || !changed {
		t.Fatalf("finalize changed=%v err=%v", changed, err)
	}
	after := patch()
	if after.Code != http.StatusOK {
		t.Fatalf("post-finalize downgrade status=%d body=%s", after.Code, after.Body.String())
	}

	second := proposal
	second.ID = newID()
	second.Status = "pending"
	second.Ciphertext = "opaque-2"
	if err := st.CreateSessionShareProposal(ctx, second); err != nil {
		t.Fatal(err)
	}
	lifecycleToken := newID()
	lifecycleNow := time.Now().UTC()
	acquired, err := managerB.store.AcquireSessionShareOwnerLease(ctx, owner.ID, lifecycleToken,
		lifecycleNow.Format(time.RFC3339), lifecycleNow.Add(workspaceLifecycleLease).Format(time.RFC3339))
	if err != nil || !acquired {
		t.Fatalf("lifecycle lease acquired=%v err=%v", acquired, err)
	}
	_, _, state, err = managerA.store.ClaimSessionShareProposal(ctx, second.ID, owner.ID, ownerIdentity.ID,
		lifecycleNow.Format(time.RFC3339), lifecycleNow.Add(shareOwnerLease).Format(time.RFC3339))
	if err != nil || state != "busy" {
		t.Fatalf("claim crossed lifecycle lease: state=%q err=%v", state, err)
	}
	if err := managerB.store.ReleaseSessionShareOwnerLease(ctx, owner.ID, lifecycleToken); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleLeaseHeartbeatAndFencingCheckpoint(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, _ := st.EnsureDefaultTenant(ctx)
	identity, _ := st.UpsertIdentity(ctx, "lease-heartbeat@example.com", "lease-heartbeat", "")
	member, _ := st.EnsureMembership(ctx, identity.ID, tenant.ID, "member")

	// A live holder renews beyond its original TTL, so another manager cannot
	// acquire the owner even after that first deadline has passed.
	live, err := acquireWorkspaceLifecycleLeaseWithTiming(ctx, st, member.ID, 300*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(650 * time.Millisecond)
	now := time.Now().UTC()
	acquired, err := st.AcquireSessionShareOwnerLease(ctx, member.ID, newID(), leaseTS(now), leaseTS(now.Add(time.Second)))
	if err != nil {
		live.Close()
		t.Fatal(err)
	}
	if acquired {
		live.Close()
		t.Fatal("heartbeat did not preserve lifecycle lease ownership")
	}
	live.Close()

	// Simulate a paused CP whose heartbeat cannot run. Once a new holder acquires
	// the expired row, the old holder's CAS checkpoint must fail before the next
	// destructive lifecycle stage and its Close must not delete the new lease.
	old, err := acquireWorkspaceLifecycleLeaseWithTiming(ctx, st, member.ID, 100*time.Millisecond, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	newToken := newID()
	now = time.Now().UTC()
	acquired, err = st.AcquireSessionShareOwnerLease(ctx, member.ID, newToken, leaseTS(now), leaseTS(now.Add(time.Second)))
	if err != nil || !acquired {
		old.Close()
		t.Fatalf("new holder acquired=%v err=%v", acquired, err)
	}
	runtime := &shareLifecycleRuntime{}
	if err := old.checkpoint(ctx); err == nil {
		_ = runtime.Start(ctx) // represents the next wipe/start stage
	}
	if runtime.starts.Load() != 0 {
		old.Close()
		t.Fatal("expired holder advanced past its fencing checkpoint")
	}
	fencedDir := filepath.Join(t.TempDir(), "must-survive")
	if err := os.MkdirAll(fencedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fencedFile := filepath.Join(fencedDir, "data")
	if err := os.WriteFile(fencedFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeAllContext(old.Context(), fencedDir); err == nil {
		old.Close()
		t.Fatal("fenced holder entered cancellable wipe")
	}
	if _, err := os.Stat(fencedFile); err != nil {
		old.Close()
		t.Fatalf("fenced holder removed data: %v", err)
	}
	old.Close()
	now = time.Now().UTC()
	if renewed, err := st.RenewSessionShareOwnerLease(ctx, member.ID, newToken, leaseTS(now), leaseTS(now.Add(time.Second))); err != nil || !renewed {
		t.Fatalf("old holder removed/replaced new lease: renewed=%v err=%v", renewed, err)
	}
	if err := st.ReleaseSessionShareOwnerLease(ctx, member.ID, newToken); err != nil {
		t.Fatal(err)
	}
}
