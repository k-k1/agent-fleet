package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.migrate(ctx); err != nil { // idempotent
		t.Fatalf("migrate again: %v", err)
	}

	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil || tn.ID != "default" {
		t.Fatalf("tenant: %v %+v", err, tn)
	}

	// UpsertIdentity is idempotent on user_key; email updated when non-empty;
	// role upgrades but does not downgrade.
	i1, err := st.UpsertIdentity(ctx, "", "dev-example-com", "")
	if err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	i2, err := st.UpsertIdentity(ctx, "dev@example.com", "dev-example-com", "super_admin")
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if i1.ID != i2.ID {
		t.Fatalf("identity not idempotent: %s != %s", i1.ID, i2.ID)
	}
	if i2.Email != "dev@example.com" || i2.Role != "super_admin" {
		t.Fatalf("identity not updated: %+v", i2)
	}

	// Membership: person joins two tenants.
	t2, err := st.CreateTenant(ctx, "security", "Security")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, i2.ID, tn.ID, "member"); err != nil {
		t.Fatalf("membership default: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, i2.ID, t2.ID, "tenant_admin"); err != nil {
		t.Fatalf("membership security: %v", err)
	}
	ms, err := st.ListMemberships(ctx, i2.ID)
	if err != nil || len(ms) != 2 {
		t.Fatalf("memberships: %v n=%d", err, len(ms))
	}

	// GetTenantBySlug
	if got, ok, err := st.GetTenantBySlug(ctx, "security"); err != nil || !ok || got.ID != t2.ID {
		t.Fatalf("by slug: ok=%v err=%v %+v", ok, err, got)
	}

	// Workspace per membership.
	var defMem string
	for _, m := range ms {
		if m.TenantSlug == "default" {
			defMem = m.MembershipID
		}
	}
	if _, ok, err := st.GetWorkspaceByMembership(ctx, defMem); err != nil || ok {
		t.Fatalf("expected no workspace: ok=%v err=%v", ok, err)
	}
	ws := Workspace{
		ID: newID(), TenantID: tn.ID, MembershipID: defMem,
		ContainerName: "af-ws-dev-example-com", Network: "af-net-dev-example-com",
		DataDir: "/tmp/af-data/dev-example-com", AgentPort: "7700",
		AgentToken: "tok", State: "stopped", CreatedAt: nowTS(),
	}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create ws: %v", err)
	}
	got, ok, err := st.GetWorkspaceByMembership(ctx, defMem)
	if err != nil || !ok || got.AgentPort != "7700" || got.ContainerName != ws.ContainerName {
		t.Fatalf("get ws: ok=%v err=%v %+v", ok, err, got)
	}
	if mx, err := st.MaxAgentPort(ctx); err != nil || mx != 7700 {
		t.Fatalf("maxport: %v %d", err, mx)
	}
}

// Memo queue (docs/log/21): CRUD is membership-scoped; ListMemos returns unsent plus
// sent-within-retention; MarkMemosSent stamps only owned ids; SweepSentMemos drops
// memos sent before the cutoff.
func TestSQLiteMemo(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tn, _ := st.EnsureDefaultTenant(ctx)
	iA, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	iB, _ := st.UpsertIdentity(ctx, "b@x.com", "b-x-com", "")
	memA, _ := st.EnsureMembership(ctx, iA.ID, tn.ID, "member")
	memB, _ := st.EnsureMembership(ctx, iB.ID, tn.ID, "member")

	past := "2000-01-01T00:00:00Z" // retention cutoff far in the future relative to this

	m1 := Memo{ID: newID(), MembershipID: memA.ID, Repo: "repo-a", Category: "frontend",
		Kind: "text", Body: "tighten padding", Position: 0, CreatedAt: nowTS()}
	m2 := Memo{ID: newID(), MembershipID: memA.ID, Repo: "repo-a", Category: "api",
		Kind: "file", RefPath: "~/repos/repo-a/x.go", Position: 1, CreatedAt: nowTS()}
	mOther := Memo{ID: newID(), MembershipID: memB.ID, Repo: "repo-a",
		Kind: "text", Body: "not yours", CreatedAt: nowTS()}
	for _, m := range []Memo{m1, m2, mOther} {
		if err := st.CreateMemo(ctx, m); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	// A newly-created memo joins the end of its repo/category group, rather than
	// inheriting the zero-value position supplied by an omitted API field.
	created, aerr := memoCreateFor(ctx, st, MembershipView{MembershipID: memA.ID}, memoDTO{
		Repo: "repo-a", Category: "frontend", Kind: "text", Body: "add at bottom",
	})
	if aerr != nil || created.Position != 1 {
		t.Fatalf("new memo position: err=%v memo=%+v", aerr, created)
	}

	// List is scoped to the caller's membership.
	rows, err := st.ListMemos(ctx, memA.ID, past)
	if err != nil || len(rows) != 3 {
		t.Fatalf("list A: err=%v n=%d", err, len(rows))
	}

	// Update overlays fields and stays ownership-guarded.
	m1.Body = "tighten padding a lot"
	m1.Category = "ui"
	if err := st.UpdateMemo(ctx, m1); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok, err := st.GetMemo(ctx, m1.ID)
	if err != nil || !ok || got.Body != "tighten padding a lot" || got.Category != "ui" {
		t.Fatalf("get after update: ok=%v err=%v %+v", ok, err, got)
	}

	// MarkMemosSent stamps only owned ids (mOther is memB's, must be ignored).
	sent := nowTS()
	if err := st.MarkMemosSent(ctx, memA.ID, []string{m1.ID, m2.ID, mOther.ID}, sent); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if o, _, _ := st.GetMemo(ctx, mOther.ID); o.SentAt != "" {
		t.Fatalf("foreign memo was stamped: %+v", o)
	}
	// Sent-but-within-retention memos still list (cutoff in the far past).
	if rows, _ := st.ListMemos(ctx, memA.ID, past); len(rows) != 3 {
		t.Fatalf("sent-within-retention list = %d, want 3", len(rows))
	}
	// A cutoff after the sent stamp hides them from the list.
	future := "2999-01-01T00:00:00Z"
	if rows, _ := st.ListMemos(ctx, memA.ID, future); len(rows) != 1 {
		t.Fatalf("expired list = %d, want 1", len(rows))
	}

	// Sweep drops sent memos before the cutoff; unsent survive.
	if err := st.SweepSentMemos(ctx, future); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, ok, _ := st.GetMemo(ctx, m1.ID); ok {
		t.Fatalf("swept memo still present")
	}
	if _, ok, _ := st.GetMemo(ctx, mOther.ID); !ok {
		t.Fatalf("unsent memo was swept")
	}

	// Delete is ownership-guarded (wrong membership is a no-op).
	if err := st.DeleteMemo(ctx, mOther.ID, memA.ID); err != nil {
		t.Fatalf("delete wrong-owner: %v", err)
	}
	if _, ok, _ := st.GetMemo(ctx, mOther.ID); !ok {
		t.Fatalf("memo deleted by non-owner")
	}
	if err := st.DeleteMemo(ctx, mOther.ID, memB.ID); err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	if _, ok, _ := st.GetMemo(ctx, mOther.ID); ok {
		t.Fatalf("memo not deleted by owner")
	}
}

// Memo categories (docs/log/21 UI刷新): first-class rows, membership-scoped, with a rename
// that cascades onto the memos and ReassignMemoCategory that empties/moves them.
func TestSQLiteMemoCategory(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tn, _ := st.EnsureDefaultTenant(ctx)
	iA, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	memA, _ := st.EnsureMembership(ctx, iA.ID, tn.ID, "member")

	// Two categories in the same repo bucket, plus a memo tagged with the first.
	c1 := MemoCategory{ID: newID(), MembershipID: memA.ID, Repo: "repo-a", Name: "frontend", Position: 0, CreatedAt: nowTS()}
	c2 := MemoCategory{ID: newID(), MembershipID: memA.ID, Repo: "repo-a", Name: "api", Position: 1, CreatedAt: nowTS()}
	for _, c := range []MemoCategory{c1, c2} {
		if err := st.CreateCategory(ctx, c); err != nil {
			t.Fatalf("create cat: %v", err)
		}
	}
	m := Memo{ID: newID(), MembershipID: memA.ID, Repo: "repo-a", Category: "frontend",
		Kind: "text", Body: "note", CreatedAt: nowTS()}
	if err := st.CreateMemo(ctx, m); err != nil {
		t.Fatalf("create memo: %v", err)
	}

	// List returns both, ordered by position.
	cats, err := st.ListCategories(ctx, memA.ID)
	if err != nil || len(cats) != 2 || cats[0].Name != "frontend" || cats[1].Name != "api" {
		t.Fatalf("list cats: err=%v %+v", err, cats)
	}

	// Rename cascade: category "frontend" -> "ui" must move the memo's category too.
	if err := st.ReassignMemoCategory(ctx, memA.ID, "repo-a", "frontend", "ui"); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	c1.Name = "ui"
	if err := st.UpdateCategory(ctx, c1); err != nil {
		t.Fatalf("update cat: %v", err)
	}
	if got, _, _ := st.GetMemo(ctx, m.ID); got.Category != "ui" {
		t.Fatalf("memo category not cascaded: %q", got.Category)
	}

	// Delete-empty: moving memos of "ui" to "" then deleting the row keeps the memo.
	if err := st.ReassignMemoCategory(ctx, memA.ID, "repo-a", "ui", ""); err != nil {
		t.Fatalf("reassign empty: %v", err)
	}
	if err := st.DeleteCategory(ctx, c1.ID, memA.ID); err != nil {
		t.Fatalf("delete cat: %v", err)
	}
	if got, ok, _ := st.GetMemo(ctx, m.ID); !ok || got.Category != "" {
		t.Fatalf("memo lost or not emptied on category delete: ok=%v %+v", ok, got)
	}
	if cats, _ := st.ListCategories(ctx, memA.ID); len(cats) != 1 || cats[0].Name != "api" {
		t.Fatalf("cats after delete: %+v", cats)
	}
}

// Showback usage accounting (P3-9): AddUsage must accumulate per (membership, day),
// ListUsage must window by day + enrich with tenant slug / member key, and the
// tenant filter must scope correctly.
func TestSQLiteUsage(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tn, _ := st.EnsureDefaultTenant(ctx)
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}

	// Two samples same day accumulate; a different day is its own bucket.
	for _, s := range []int{300, 300} {
		if err := st.AddUsage(ctx, mem.ID, tn.ID, "2026-06-30", s); err != nil {
			t.Fatalf("add usage: %v", err)
		}
	}
	if err := st.AddUsage(ctx, mem.ID, tn.ID, "2026-07-01", 600); err != nil {
		t.Fatalf("add usage day2: %v", err)
	}

	rows, err := st.ListUsage(ctx, "", "2026-06-01", "2026-06-30")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].RunningSecs != 600 {
		t.Fatalf("windowed rows = %+v, want 1 row of 600s", rows)
	}
	if rows[0].TenantSlug != "default" || rows[0].UserKey != "a-x-com" {
		t.Fatalf("enrichment missing: %+v", rows[0])
	}

	// Full window sees both days; tenant filter for a foreign tenant sees none.
	all, _ := st.ListUsage(ctx, "", "2026-06-01", "2026-07-31")
	if len(all) != 2 {
		t.Fatalf("full window rows = %d, want 2", len(all))
	}
	none, _ := st.ListUsage(ctx, "no-such-tenant", "2026-06-01", "2026-07-31")
	if len(none) != 0 {
		t.Fatalf("foreign tenant rows = %d, want 0", len(none))
	}
}

// aggregateUsage must sum per member across days and compute hours.
func TestAggregateUsage(t *testing.T) {
	rows := []UsageRow{
		{TenantID: "default", TenantSlug: "default", MembershipID: "m1", UserKey: "a", Day: "2026-06-30", RunningSecs: 3600},
		{TenantID: "default", TenantSlug: "default", MembershipID: "m1", UserKey: "a", Day: "2026-07-01", RunningSecs: 1800},
		{TenantID: "default", TenantSlug: "default", MembershipID: "m2", UserKey: "b", Day: "2026-07-01", RunningSecs: 900},
	}
	got := aggregateUsage(rows)
	if len(got) != 2 {
		t.Fatalf("totals = %d, want 2 members", len(got))
	}
	if got[0].UserKey != "a" || got[0].RunningSecs != 5400 || got[0].RunningHrs != 1.5 {
		t.Fatalf("member a total wrong: %+v", got[0])
	}
	if got[1].UserKey != "b" || got[1].RunningHrs != 0.25 {
		t.Fatalf("member b total wrong: %+v", got[1])
	}
}

// Workspace reads carry the owning tenant's SLUG (docs/log/67, ADR 0048 決定 3). It is not a
// column on the row — the AWS adapters need it to stamp `af-tenant`, and reading it
// there would mean a store call from inside a tag write, on every Start.
func TestSQLiteWorkspaceCarriesTenantSlug(t *testing.T) {
	ctx := context.Background()
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tn, err := st.CreateTenant(ctx, "acme", "Acme")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	ident, _ := st.UpsertIdentity(ctx, "a@x.com", "a-x-com", "")
	mem, err := st.EnsureMembership(ctx, ident.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	ws := Workspace{
		ID: newID(), TenantID: tn.ID, MembershipID: mem.ID,
		ContainerName: "af-ws-acme-a", Network: "n", DataDir: "/d",
		AgentPort: "7700", AgentToken: "tok", State: "stopped", CreatedAt: nowTS(),
	}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	got, ok, err := st.GetWorkspaceByMembership(ctx, mem.ID)
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if got.TenantSlug != "acme" {
		t.Errorf("GetWorkspaceByMembership TenantSlug = %q, want acme", got.TenantSlug)
	}
	list, err := st.ListWorkspaces(ctx, tn.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if list[0].TenantSlug != "acme" {
		t.Errorf("ListWorkspaces TenantSlug = %q, want acme", list[0].TenantSlug)
	}
}

// The migration runner splits statements on a naive `;` (see sqlStore.migrate), so a
// semicolon inside a `--` comment can cut a statement in half. The failure is a SQL
// syntax error pointing at a random English word from the prose, which reads like
// anything except "your comment has a semicolon in it" — measured while adding
// 0046_cloud_cost.sql.
//
// Only the two shapes that actually break are flagged, which is why three existing
// migrations with a trailing `;` in prose are legal:
//
//	-- ...prose; more prose        BREAKS: " more prose" becomes a bare statement
//	  a INT,  -- note;             BREAKS: the fragment before it is half a CREATE TABLE
//	-- ...prose;                   fine: the next fragment starts with `--` again
func TestMigrationsHaveNoStatementSplittingSemicolonInComments(t *testing.T) {
	for _, dir := range []struct {
		fs   fs.FS
		name string
	}{{migrationFS, "migrations"}, {pgMigrationFS, "migrations-pg"}} {
		entries, err := fs.ReadDir(dir.fs, dir.name)
		if err != nil {
			t.Fatalf("read %s: %v", dir.name, err)
		}
		for _, e := range entries {
			body, err := fs.ReadFile(dir.fs, dir.name+"/"+e.Name())
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			for n, line := range strings.Split(string(body), "\n") {
				i := strings.Index(line, "--")
				if i < 0 {
					continue
				}
				j := strings.Index(line[i:], ";")
				if j < 0 {
					continue
				}
				trailing := strings.TrimSpace(line[i+j+1:])
				code := strings.TrimSpace(line[:i])
				if trailing == "" && code == "" {
					continue
				}
				t.Errorf("%s/%s:%d has a `;` inside a comment that will cut the statement:\n  %s",
					dir.name, e.Name(), n+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestFreshSQLiteKeepsWorkspaceColumns は「新規 DB でも workspace の列が揃っている」を
// 固定する。
//
// ★ なぜ要るか（2026-08-31 に実測で見つけた）。migrate() は番号付きマイグレーションを
// 全部流したあとに legacyHook（migrateMemberships）を呼び、そのフックは新規 DB で
// `DROP TABLE workspace` → `ALTER TABLE workspace_new RENAME TO workspace` を実行する。
// つまり **0002 より後に ALTER で足した列は、足した直後に捨てられる**。schema_migrations
// には適用済みと記録されているので、次の起動でも復活しない。
//
// 実害は静かだった: `settings`（ワークスペース設定）は新規 SQLite デプロイにだけ存在せず、
// 読み出しはエラーを握りつぶすので「保存だけが 500」という形でしか出なかった。
// preview_slug（docs/log/81）も同じ穴に落ちる。
func TestFreshSQLiteKeepsWorkspaceColumns(t *testing.T) {
	st, err := openSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	have := map[string]bool{}
	rows, err := st.db.QueryContext(ctx, `PRAGMA table_info(workspace)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		have[name] = true
	}
	rows.Close()
	for _, col := range []string{"settings", "preview_slug"} {
		if !have[col] {
			t.Errorf("fresh SQLite workspace table is missing %q (the membership swap dropped it)", col)
		}
	}
	// 実際に読み書きできるところまで確かめる（列があるだけでは索引の張り直し漏れを拾えない）。
	dflt, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	ident, err := st.UpsertIdentity(ctx, "", "colcheck", "")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	mem, err := st.EnsureMembership(ctx, ident.ID, dflt.ID, "member")
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	ws := Workspace{ID: "ws-cols", TenantID: dflt.ID, MembershipID: mem.ID, ContainerName: "c",
		Network: "n", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "stopped", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.SetWorkspaceSettings(ctx, ws.ID, `{"agentUpdate":true}`); err != nil {
		t.Fatalf("SetWorkspaceSettings on a fresh DB: %v", err)
	}
	if err := st.SetWorkspacePreviewSlug(ctx, ws.ID, "abcdefghij0123456789"); err != nil {
		t.Fatalf("SetWorkspacePreviewSlug on a fresh DB: %v", err)
	}
	if got, ok, err := st.GetWorkspaceByPreviewSlug(ctx, "abcdefghij0123456789"); err != nil || !ok || got.ID != ws.ID {
		t.Fatalf("GetWorkspaceByPreviewSlug = (%v, %v, %v), want the workspace back", got.ID, ok, err)
	}
}
