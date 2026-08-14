package main

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
	st, err := openPostgres(url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	// Clean slate so the test is repeatable against a persistent DB.
	if _, err := st.db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.migrate(ctx); err != nil { // idempotent
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
	ws := Workspace{ID: newID(), TenantID: tn.ID, MembershipID: m1.ID, ContainerName: "af-ws-k1",
		Network: "n", DataDir: "/d", AgentPort: "7700", AgentToken: "tok", State: "stopped", CreatedAt: nowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create ws: %v", err)
	}
	if err := st.SetWorkspaceState(ctx, ws.ID, "running"); err != nil {
		t.Fatalf("set state: %v", err)
	}
	if got, ok, err := st.GetWorkspaceByMembership(ctx, m1.ID); err != nil || !ok || got.State != "running" {
		t.Fatalf("get ws: ok=%v err=%v %+v", ok, err, got)
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
	if err := st.PutUserLimit(ctx, m1.ID, 3, 0, 0); err != nil {
		t.Fatalf("put ulimit: %v", err)
	}
	if err := st.PutUserLimit(ctx, m1.ID, 4, 0, 2*gib); err != nil {
		t.Fatalf("upsert ulimit: %v", err)
	}
	if ul, ok, err := st.GetUserLimit(ctx, m1.ID); err != nil || !ok || ul.MaxSessions != 4 || ul.MemLimit != 2*gib {
		t.Fatalf("get ulimit: %+v ok=%v err=%v", ul, ok, err)
	}

	// session mirror
	if err := st.ReplaceSessions(ctx, ws.ID, []SessionRow{{WorkspaceID: ws.ID, Name: "s1", Kind: "shell",
		State: "running", CreatedAt: nowTS(), LastSeen: nowTS()}}); err != nil {
		t.Fatalf("replace sess: %v", err)
	}
	if rows, err := st.ListSessions(ctx, ws.ID); err != nil || len(rows) != 1 || rows[0].Name != "s1" {
		t.Fatalf("list sess: %v %+v", err, rows)
	}

	// pat
	pat := PAT{ID: newID(), IdentityID: i2.ID, MembershipID: m1.ID, Scope: "read", Name: "cli", CreatedAt: nowTS()}
	if err := st.CreatePAT(ctx, pat, "hash1"); err != nil {
		t.Fatalf("create pat: %v", err)
	}
	if p, ok, err := st.GetPATByHash(ctx, "hash1"); err != nil || !ok || p.ID != pat.ID {
		t.Fatalf("get pat: ok=%v err=%v %+v", ok, err, p)
	}

	// audit
	if err := st.InsertAudit(ctx, AuditLog{ID: newID(), TenantID: tn.ID, ActorKind: "user",
		ActorID: i2.ID, Action: "start", Target: "ws", At: nowTS()}); err != nil {
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
	if err := st.CreateSSMProfile(ctx, SSMProfile{ID: newID(), MembershipID: m1.ID, Label: "p", CreatedAt: nowTS()}); err != nil {
		t.Fatalf("ssm profile: %v", err)
	}

	// egress daily upsert + allowlist + setting
	if err := st.RecordEgress(ctx, "2026-07-01", "github.com", true, 2); err != nil {
		t.Fatalf("egress: %v", err)
	}
	if err := st.RecordEgress(ctx, "2026-07-01", "github.com", true, 3); err != nil { // accumulate
		t.Fatalf("egress upsert: %v", err)
	}
	if err := st.AddAllowlist(ctx, AllowlistEntry{ID: newID(), Entry: "github.com", State: "active", AddedAt: nowTS()}); err != nil {
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

	// identity_provider round trip (docs/61 P1 / migrations-pg/0021). Everything
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
	joined, isNew, err := st.LinkIdentity(ctx, googleProviderID, "pg-sub-1", "yamada@acme.co.jp", sanitizeUser(pgEmail), "")
	if err != nil || isNew || joined.ID != seed.ID || joined.UserKey != seed.UserKey {
		t.Fatalf("link rule 2: %+v isNew=%v err=%v want id=%s key=%s", joined, isNew, err, seed.ID, seed.UserKey)
	}
	// Rule 1 + rename: the recorded pair outranks the address, so user_key — the home
	// directory name — stays put and only the display email moves.
	const pgRenamed = "yamada-hanako@acme.co.jp"
	moved, isNew, err := st.LinkIdentity(ctx, googleProviderID, "pg-sub-1", pgRenamed, sanitizeUser(pgRenamed), "")
	if err != nil || isNew || moved.ID != seed.ID || moved.UserKey != seed.UserKey || moved.Email != pgRenamed {
		t.Fatalf("link rule 1: %+v isNew=%v err=%v want id=%s key=%s", moved, isNew, err, seed.ID, seed.UserKey)
	}
	// touchIdentity's other statement: nothing asserted, so last_login_at moves and
	// the display email must survive rather than be blanked.
	if _, _, err := st.LinkIdentity(ctx, googleProviderID, "pg-sub-1", "", sanitizeUser(pgRenamed), ""); err != nil {
		t.Fatalf("link without an asserted email: %v", err)
	}
	if got, ok, err := st.GetIdentityByID(ctx, seed.ID); err != nil || !ok || got.Email != pgRenamed {
		t.Fatalf("empty assertion cleared the email: %+v ok=%v err=%v", got, ok, err)
	}
	// Rule 3: an address nobody owns is a new person — the isNew that raises the
	// "this is a new workspace" notice on a multi-IdP deployment.
	const pgOther = "tanaka@acme.co.jp"
	fresh, isNew, err := st.LinkIdentity(ctx, "entra", "pg-sub-2", pgOther, sanitizeUser(pgOther), "")
	if err != nil || !isNew || fresh.ID == seed.ID || fresh.UserKey != sanitizeUser(pgOther) {
		t.Fatalf("link rule 3: %+v isNew=%v err=%v", fresh, isNew, err)
	}
	if n := countRows(t, st, "identity_provider"); n != 2 {
		t.Fatalf("identity_provider rows = %d, want 2", n)
	}
}
