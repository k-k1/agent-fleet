package main

import (
	"context"
	"os"
	"sync"
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
	var waiters sync.WaitGroup
	waiters.Add(9)
	for range 9 {
		go func() {
			defer waiters.Done()
			release, err := st.AcquireWorkspaceOperationFence(ctx, ws.ID)
			if err == nil {
				release()
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	queryCtx, queryCancel := context.WithTimeout(ctx, time.Second)
	if _, err := st.GetTenant(queryCtx, tn.ID); err != nil {
		t.Fatalf("pool exhausted by advisory waiters: %v", err)
	}
	queryCancel()
	releaseFence()
	waiters.Wait()
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
}
