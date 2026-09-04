package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgresStore runs a broad round-trip against a real Postgres to validate the
// pg adapter end to end: the consolidated schema, the ?→$n rebind, and the UPSERT /
// partial-index / accumulate paths that differ most between dialects. Skipped unless
// AF_TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --rm -e POSTGRES_PASSWORD=pw -p 5433:5432 postgres:16
//	AF_TEST_DATABASE_URL='postgres://postgres:pw@localhost:5433/postgres?sslmode=disable' \
//	  go test -run TestPostgresStore -v
func TestPostgresStore(t *testing.T) {
	url := os.Getenv("AF_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres conformance test")
	}
	ctx := context.Background()
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	// Clean slate so the test is repeatable against a persistent DB.
	if _, err := st.db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.Migrate(ctx); err != nil { // idempotent
		t.Fatalf("migrate again: %v", err)
	}

	// tenant + limits
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil || tn.ID != "default" {
		t.Fatalf("default tenant: %v %+v", err, tn)
	}
	if err := st.SetTenantLimits(ctx, tn.ID, `{"max_workspaces":5}`); err != nil {
		t.Fatalf("set limits: %v", err)
	}
	if got, err := st.GetTenant(ctx, tn.ID); err != nil || got.Limits != `{"max_workspaces":5}` {
		t.Fatalf("get tenant limits: %v %+v", err, got)
	}

	// identity upsert (idempotent on user_key; role upgrades; partial email index)
	i1, err := st.UpsertIdentity(ctx, "", "dev-example-com", "")
	if err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	i2, err := st.UpsertIdentity(ctx, "dev@example.com", "dev-example-com", "super_admin")
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if i1.ID != i2.ID || i2.Role != "super_admin" || i2.Email != "dev@example.com" {
		t.Fatalf("identity upsert: %+v", i2)
	}

	// membership across two tenants
	t2, err := st.CreateTenant(ctx, "security", "Security")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	m1, err := st.EnsureMembership(ctx, i2.ID, tn.ID, "member")
	if err != nil {
		t.Fatalf("mem1: %v", err)
	}
	if _, err := st.EnsureMembership(ctx, i2.ID, t2.ID, "tenant_admin"); err != nil {
		t.Fatalf("mem2: %v", err)
	}
	if ms, err := st.ListMemberships(ctx, i2.ID); err != nil || len(ms) != 2 {
		t.Fatalf("memberships n=%d: %v", len(ms), err)
	}

	// workspace + state + wrapped dek upsert
	ws := Workspace{ID: NewID(), TenantID: tn.ID, MembershipID: m1.ID, ContainerName: "af-ws-k1",
		Network: "n", DataDir: "/d", AgentPort: "7700", AgentToken: "tok", State: "stopped", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create ws: %v", err)
	}
	if err := st.SetWorkspaceState(ctx, ws.ID, "running"); err != nil {
		t.Fatalf("set state: %v", err)
	}
	if got, ok, err := st.GetWorkspaceByMembership(ctx, m1.ID); err != nil || !ok || got.State != "running" {
		t.Fatalf("get ws: ok=%v err=%v %+v", ok, err, got)
	}
	// Workspace settings round trip. `workspace.settings` existed only on SQLite, so on a
	// Postgres deployment PUT /api/env/ws-settings answered 500 every time. The read side
	// hides it — its callers swallow the error — so nothing catches this unless the test
	// actually writes; don't leave it to the schema parity check, which is easily skipped.
	if err := st.SetWorkspaceSettings(ctx, ws.ID, `{"previewPorts":[3000]}`); err != nil {
		t.Fatalf("set ws settings: %v", err)
	}
	if got, err := st.GetWorkspaceSettings(ctx, ws.ID); err != nil || got != `{"previewPorts":[3000]}` {
		t.Fatalf("get ws settings: %q err=%v", got, err)
	}
	// Nine same-key waiters must poll without pinning the remaining nine pool
	// connections; the holder still needs one for checkpoint/finalization work.
	releaseFence, err := st.AcquireWorkspaceOperationFence(ctx, ws.ID)
	if err != nil {
		t.Fatalf("advisory holder: %v", err)
	}
	defer releaseFence()
	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	results := make(chan error, 9)
	for range 9 {
		go func() {
			release, err := st.AcquireWorkspaceOperationFence(waitCtx, ws.ID)
			if err != nil {
				results <- err
				return
			}
			release()
			results <- nil
		}()
	}
	time.Sleep(100 * time.Millisecond)
	queryCtx, queryCancel := context.WithTimeout(ctx, time.Second)
	if _, err := st.GetTenant(queryCtx, tn.ID); err != nil {
		t.Fatalf("pool exhausted by advisory waiters: %v", err)
	}
	queryCancel()
	releaseFence()
	for i := 0; i < 9; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("advisory waiter %d: %v", i, err)
			}
		case <-waitCtx.Done():
			t.Fatalf("advisory waiters did not finish: completed=%d/9: %v", i, waitCtx.Err())
		}
	}
	if err := st.PutWrappedDEK(ctx, ws.ID, "ct1", "kref"); err != nil {
		t.Fatalf("put dek: %v", err)
	}
	if err := st.PutWrappedDEK(ctx, ws.ID, "ct2", "kref"); err != nil { // ON CONFLICT DO UPDATE
		t.Fatalf("put dek upsert: %v", err)
	}
	if ct, kr, ok, err := st.GetWrappedDEK(ctx, ws.ID); err != nil || !ok || ct != "ct2" || kr != "kref" {
		t.Fatalf("get dek: %s %s ok=%v err=%v", ct, kr, ok, err)
	}

	// user limit upsert
	if err := st.PutUserLimit(ctx, m1.ID, UserQuota{MaxSessions: 3}); err != nil {
		t.Fatalf("put ulimit: %v", err)
	}
	if err := st.PutUserLimit(ctx, m1.ID, UserQuota{MaxSessions: 4, MemLimit: 2 * gib}); err != nil {
		t.Fatalf("upsert ulimit: %v", err)
	}
	if ul, ok, err := st.GetUserLimit(ctx, m1.ID); err != nil || !ok || ul.MaxSessions != 4 || ul.MemLimit != 2*gib {
		t.Fatalf("get ulimit: %+v ok=%v err=%v", ul, ok, err)
	}

	// session mirror
	if err := st.ReplaceSessions(ctx, ws.ID, []SessionRow{{WorkspaceID: ws.ID, Name: "s1", Kind: "shell",
		State: "running", CreatedAt: NowTS(), LastSeen: NowTS()}}); err != nil {
		t.Fatalf("replace sess: %v", err)
	}
	if rows, err := st.ListSessions(ctx, ws.ID); err != nil || len(rows) != 1 || rows[0].Name != "s1" {
		t.Fatalf("list sess: %v %+v", err, rows)
	}

	// pat
	pat := PAT{ID: NewID(), IdentityID: i2.ID, MembershipID: m1.ID, Scope: "read", Name: "cli", CreatedAt: NowTS()}
	if err := st.CreatePAT(ctx, pat, "hash1"); err != nil {
		t.Fatalf("create pat: %v", err)
	}
	if p, ok, err := st.GetPATByHash(ctx, "hash1"); err != nil || !ok || p.ID != pat.ID {
		t.Fatalf("get pat: ok=%v err=%v %+v", ok, err, p)
	}

	// audit
	if err := st.InsertAudit(ctx, AuditLog{ID: NewID(), TenantID: tn.ID, ActorKind: "user",
		ActorID: i2.ID, Action: "start", Target: "ws", At: NowTS()}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if al, err := st.ListAuditByTenant(ctx, tn.ID, 10); err != nil || len(al) != 1 {
		t.Fatalf("list audit: %v n=%d", err, len(al))
	}

	// usage accumulate per (membership, day)
	if err := st.AddUsage(ctx, m1.ID, tn.ID, "2026-07-01", 300); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if err := st.AddUsage(ctx, m1.ID, tn.ID, "2026-07-01", 300); err != nil {
		t.Fatalf("usage2: %v", err)
	}
	if rows, err := st.ListUsage(ctx, "", "2026-07-01", "2026-07-01"); err != nil || len(rows) != 1 || rows[0].RunningSecs != 600 {
		t.Fatalf("usage list: %v %+v", err, rows)
	}

	// ssm profile
	if err := st.CreateSSMProfile(ctx, SSMProfile{ID: NewID(), MembershipID: m1.ID, Label: "p", CreatedAt: NowTS()}); err != nil {
		t.Fatalf("ssm profile: %v", err)
	}

	// memo categories (docs/log/21 UI overhaul / migrations-pg/0030). This whole table had
	// only ever existed on SQLite: the mirror migration was never written, so on a
	// Postgres deployment every category endpoint answered 500 and the Console — which
	// folds a non-array answer into an empty list — simply showed no categories. The
	// round trip below is what "the feature works here" means; TestSchemaDialectParity
	// next door is what stops the two series diverging again.
	cat := MemoCategory{ID: NewID(), MembershipID: m1.ID, Repo: "app", Name: "後で", Position: 0, CreatedAt: NowTS()}
	if err := st.CreateCategory(ctx, cat); err != nil {
		t.Fatalf("create memo category: %v", err)
	}
	if got, ok, err := st.GetCategory(ctx, cat.ID); err != nil || !ok || got.Name != "後で" {
		t.Fatalf("get memo category = (%+v,%v,%v)", got, ok, err)
	}
	cat.Name, cat.Position = "あとで", 3
	if err := st.UpdateCategory(ctx, cat); err != nil {
		t.Fatalf("update memo category: %v", err)
	}
	// The rename cascades onto the memos that carry the name (memo.category is the
	// grouping key; this table holds the order and the empty ones).
	if err := st.CreateMemo(ctx, Memo{ID: NewID(), MembershipID: m1.ID, Repo: "app",
		Category: "後で", Kind: "text", Body: "b", CreatedAt: NowTS()}); err != nil {
		t.Fatalf("create memo: %v", err)
	}
	if err := st.ReassignMemoCategory(ctx, m1.ID, "app", "後で", "あとで"); err != nil {
		t.Fatalf("reassign memo category: %v", err)
	}
	memos, err := st.ListMemos(ctx, m1.ID, "")
	if err != nil || len(memos) != 1 || memos[0].Category != "あとで" {
		t.Fatalf("memos after rename: %v %+v", err, memos)
	}
	cats, err := st.ListCategories(ctx, m1.ID)
	if err != nil || len(cats) != 1 || cats[0].Name != "あとで" || cats[0].Position != 3 {
		t.Fatalf("list memo categories: %v %+v", err, cats)
	}
	if err := st.DeleteCategory(ctx, cat.ID, m1.ID); err != nil {
		t.Fatalf("delete memo category: %v", err)
	}
	if _, ok, _ := st.GetCategory(ctx, cat.ID); ok {
		t.Fatal("memo category survived the delete")
	}

	// egress daily upsert + allowlist + setting
	if err := st.RecordEgress(ctx, "2026-07-01", "github.com", true, 2); err != nil {
		t.Fatalf("egress: %v", err)
	}
	if err := st.RecordEgress(ctx, "2026-07-01", "github.com", true, 3); err != nil { // accumulate
		t.Fatalf("egress upsert: %v", err)
	}
	if err := st.RecordEgress(ctx, "2026-07-01", "evil.example", false, 1); err != nil {
		t.Fatalf("egress blocked: %v", err)
	}
	// Read side too: the aggregate + ORDER BY is where the dialects differ, and the
	// admin traffic page is the only caller. Ordering must put the busiest host first.
	eg, err := st.ListEgress(ctx, "2026-01-01", 100)
	if err != nil {
		t.Fatalf("list egress: %v", err)
	}
	if len(eg) != 2 || eg[0].Host != "github.com" || eg[0].Allowed != 5 || eg[0].Blocked != 0 {
		t.Fatalf("list egress: %+v", eg)
	}
	if eg[1].Host != "evil.example" || eg[1].Allowed != 0 || eg[1].Blocked != 1 {
		t.Fatalf("list egress blocked row: %+v", eg)
	}
	if err := st.AddAllowlist(ctx, AllowlistEntry{ID: NewID(), Entry: "github.com", State: "active", AddedAt: NowTS()}); err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	if al, err := st.EffectiveAllowlist(ctx); err != nil || len(al) == 0 {
		t.Fatalf("eff allowlist: %v %+v", err, al)
	}
	if err := st.SetSetting(ctx, "egress_mode", "log"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if v, err := st.GetSetting(ctx, "egress_mode"); err != nil || v != "log" {
		t.Fatalf("get setting: %v %q", err, v)
	}

	// identity_provider round trip (docs/log/61 P1 / migrations-pg/0021). Everything
	// below had only ever run on SQLite: the pair table, its
	// ON CONFLICT(provider, subject) upsert, and the LOWER(email)=? lookup. Postgres
	// is the stricter dialect about placeholder types, which is why identityByEmail
	// lowercases in Go rather than calling LOWER(?) and touchIdentity has two
	// statements instead of one with NULLIF.
	const pgEmail = "Yamada@Acme.co.jp" // stored with the case the IdP asserted
	seed, err := st.UpsertIdentity(ctx, pgEmail, sanitizeUser(pgEmail), "")
	if err != nil {
		t.Fatalf("link seed: %v", err)
	}
	// Rule 2: no pair recorded yet, but the address is already someone's — join that
	// person. Matching is case-insensitive even though the unique index is on the
	// exact string, so a differently-cased login must not fork a second identity.
	joined, isNew, err := st.LinkIdentity(ctx, IdentityLink{Provider: googleProviderID, Subject: "pg-sub-1", Email: "yamada@acme.co.jp", FallbackKey: sanitizeUser(pgEmail), RoleHint: "", EmailJoin: true})
	if err != nil || isNew || joined.ID != seed.ID || joined.UserKey != seed.UserKey {
		t.Fatalf("link rule 2: %+v isNew=%v err=%v want id=%s key=%s", joined, isNew, err, seed.ID, seed.UserKey)
	}
	// Rule 1 + rename: the recorded pair outranks the address, so user_key — the home
	// directory name — stays put and only the display email moves.
	const pgRenamed = "yamada-hanako@acme.co.jp"
	moved, isNew, err := st.LinkIdentity(ctx, linkOf(googleProviderID, "pg-sub-1", pgRenamed, true))
	if err != nil || isNew || moved.ID != seed.ID || moved.UserKey != seed.UserKey || moved.Email != pgRenamed {
		t.Fatalf("link rule 1: %+v isNew=%v err=%v want id=%s key=%s", moved, isNew, err, seed.ID, seed.UserKey)
	}
	// touchIdentity's other statement: nothing asserted, so last_login_at moves and
	// the display email must survive rather than be blanked.
	if _, _, err := st.LinkIdentity(ctx, IdentityLink{Provider: googleProviderID, Subject: "pg-sub-1", Email: "", FallbackKey: sanitizeUser(pgRenamed), RoleHint: "", EmailJoin: true}); err != nil {
		t.Fatalf("link without an asserted email: %v", err)
	}
	if got, ok, err := st.GetIdentityByID(ctx, seed.ID); err != nil || !ok || got.Email != pgRenamed {
		t.Fatalf("empty assertion cleared the email: %+v ok=%v err=%v", got, ok, err)
	}
	// Rule 3: an address nobody owns is a new person — the isNew that raises the
	// "this is a new workspace" notice on a multi-IdP deployment.
	const pgOther = "tanaka@acme.co.jp"
	fresh, isNew, err := st.LinkIdentity(ctx, linkOf("entra", "pg-sub-2", pgOther, true))
	if err != nil || !isNew || fresh.ID == seed.ID || fresh.UserKey != sanitizeUser(pgOther) {
		t.Fatalf("link rule 3: %+v isNew=%v err=%v", fresh, isNew, err)
	}
	if n := countRows(t, st, "identity_provider"); n != 2 {
		t.Fatalf("identity_provider rows = %d, want 2", n)
	}

	// tenant login rules round trip (docs/log/61 P3 / migrations-pg/0022). The columns
	// arrive by ALTER on an existing table, and the entry gate reads them on every
	// request, so a dialect slip here would take the whole login down.
	if err := st.SetTenantLogin(ctx, t2.ID, "entra,github", "acme.co.jp", "acme.co.jp", ""); err != nil {
		t.Fatalf("set tenant login: %v", err)
	}
	if got, err := st.GetTenant(ctx, t2.ID); err != nil ||
		got.AllowedProviders != "entra,github" || got.AutoJoinDomains != "acme.co.jp" || got.AllowedDomains != "acme.co.jp" {
		t.Fatalf("tenant login round trip: %v %+v", err, got)
	}
	// The default tenant was created before the ALTER, so it must read back as the
	// NOT NULL DEFAULT '' rather than a NULL that would fail the Scan.
	if got, err := st.GetTenant(ctx, tn.ID); err != nil || got.AllowedProviders != "" {
		t.Fatalf("pre-existing tenant row: %v %+v", err, got)
	}
	// The tenant's source-network restriction rides the same row and the same bulk
	// read (docs/log/66 / migrations-pg/0028). It is consulted on EVERY request, so a
	// dialect slip here would 403 an entire tenant rather than just fail a screen.
	if err := st.SetTenantAllowedCIDRs(ctx, t2.ID, "203.0.113.0/24,198.51.100.7/32"); err != nil {
		t.Fatalf("set allowed_cidrs: %v", err)
	}
	if got, err := st.GetTenantAllowedCIDRs(ctx, t2.ID); err != nil || got != "203.0.113.0/24,198.51.100.7/32" {
		t.Fatalf("allowed_cidrs round trip: %v %q", err, got)
	}
	if got, err := st.GetTenantAllowedCIDRs(ctx, tn.ID); err != nil || got != "" {
		t.Fatalf("a tenant created before the ALTER must read back as empty: %v %q", err, got)
	}
	rules, err := st.ListTenantLoginRules(ctx)
	if err != nil {
		t.Fatalf("list tenant login rules: %v", err)
	}
	var found bool
	for _, r := range rules {
		if r.ID == t2.ID {
			found = true
			if len(r.AllowedProviders) != 2 || r.AutoJoinDomains[0] != "acme.co.jp" {
				t.Fatalf("rules split wrong: %+v", r)
			}
			if len(r.AllowedCIDRs) != 2 || r.AllowedCIDRs[0] != "203.0.113.0/24" {
				t.Fatalf("allowed_cidrs not carried by the bulk read: %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("t2 missing from %+v", rules)
	}

	// membership deactivate + the entry gate's roster lookup (LOWER on the column,
	// not on the placeholder — Postgres cannot infer a type for LOWER($1)).
	if ok, err := st.EmailHasActiveMembership(ctx, "DEV@example.com"); err != nil || !ok {
		t.Fatalf("EmailHasActiveMembership = (%v,%v), want true", ok, err)
	}
	if any, err := st.AnyActiveMembership(ctx); err != nil || !any {
		t.Fatalf("AnyActiveMembership = (%v,%v), want true", any, err)
	}
	memT2, _, err := st.GetMembership(ctx, i2.ID, t2.ID)
	if err != nil {
		t.Fatalf("get membership: %v", err)
	}
	if err := st.SetMembershipStatus(ctx, memT2.ID, "inactive"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if got, _, err := st.GetMembership(ctx, i2.ID, t2.ID); err != nil || got.Status != "inactive" {
		t.Fatalf("membership status = %+v %v", got, err)
	}
	// EnsureMembership must NOT revive it (that is the invite path's explicit job).
	if _, err := st.EnsureMembership(ctx, i2.ID, t2.ID, "member"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got, _, _ := st.GetMembership(ctx, i2.ID, t2.ID); got.Status != "inactive" {
		t.Fatalf("EnsureMembership revived a deactivated membership: %+v", got)
	}

	// tenant_idp round trip (docs/log/61 P4 / migrations-pg/0023). The table is created
	// by this migration, the deployment-wide read JOINs tenant, and the approval path
	// is a second UPDATE — all of it had only ever run on SQLite.
	idpRow := TenantIdP{
		ID: NewID(), TenantID: t2.ID, Name: "entra",
		Issuer: "https://login.microsoftonline.com/guid-pg/v2.0", ClientID: "c",
		SecretEnc: "sealed", KeyRef: t2.ID, Trust: trustIssuer,
		AllowedDomains: "sub.example", Status: "pending", CreatedAt: NowTS(), UpdatedAt: NowTS(),
	}
	if err := st.CreateTenantIdP(ctx, idpRow); err != nil {
		t.Fatalf("create tenant_idp: %v", err)
	}
	if got, ok, err := st.GetTenantIdP(ctx, t2.ID, idpRow.ID); err != nil || !ok || got.Status != "pending" || got.SecretEnc != "sealed" {
		t.Fatalf("get tenant_idp: %+v ok=%v err=%v", got, ok, err)
	}
	// Only APPROVED rows reach the login layer — the property the whole feature rests on.
	if act, _, err := st.ListActiveTenantIdPs(ctx); err != nil || len(act) != 0 {
		t.Fatalf("pending row must not be active: %+v %v", act, err)
	}
	if err := st.SetTenantIdPStatus(ctx, t2.ID, idpRow.ID, "active", "boss", NowTS(), NowTS()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// ★ The display name travels with the slug: it is what the generated button label
	// says to tell this tenant's method apart from the deployment's (docs/log/61 §61.15.10).
	act, tenants, err := st.ListActiveTenantIdPs(ctx)
	if err != nil || len(act) != 1 || tenants[t2.ID].Slug != t2.Slug ||
		tenants[t2.ID].Name != t2.Name || act[0].ApprovedBy != "boss" {
		t.Fatalf("active tenant_idp: %+v tenants=%v err=%v", act, tenants, err)
	}
	// The tenant-scoped roster lookup. dev@example.com is an ACTIVE member of the
	// default tenant and was deactivated in t2 just above, so this one call proves
	// both halves: the tenant scoping and the active-only filter.
	if ok, err := st.EmailHasActiveMembershipInTenant(ctx, "DEV@example.com", tn.ID); err != nil || !ok {
		t.Fatalf("EmailHasActiveMembershipInTenant = (%v,%v), want true", ok, err)
	}
	if ok, err := st.EmailHasActiveMembershipInTenant(ctx, "DEV@example.com", t2.ID); err != nil || ok {
		t.Fatalf("a deactivated membership must not count = (%v,%v), want false", ok, err)
	}
	if err := st.DeleteTenantIdP(ctx, t2.ID, idpRow.ID); err != nil {
		t.Fatalf("delete tenant_idp: %v", err)
	}
	if _, ok, _ := st.GetTenantIdP(ctx, t2.ID, idpRow.ID); ok {
		t.Fatal("tenant_idp row survived the delete")
	}

	// super_admin revocation (decision 24): i2 was upgraded above, and dropping it from
	// the env list must take it away.
	demoted, err := st.DemoteSuperAdmins(ctx, []string{"someone-else@example.com"})
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if len(demoted) != 1 || demoted[0] != "dev@example.com" {
		t.Fatalf("demoted = %v, want dev@example.com", demoted)
	}
	if got, _, _ := st.GetIdentityByID(ctx, i2.ID); got.Role != "user" {
		t.Fatalf("role after demotion = %q", got.Role)
	}
}
