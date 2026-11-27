package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

func TestMCPServerOAuthClientSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkMCPServerOAuthClient(t, st)
	t.Run("batch rollback", func(t *testing.T) { checkCreateMCPServersRollsBack(t, st) })
	t.Run("concurrent names", func(t *testing.T) { checkMCPServerConcurrentNames(t, st) })
}

func TestMCPServerOAuthClientPostgres(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run against Postgres")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkMCPServerOAuthClient(t, st)
	t.Run("batch rollback", func(t *testing.T) { checkCreateMCPServersRollsBack(t, st) })
	t.Run("concurrent names", func(t *testing.T) { checkMCPServerConcurrentNames(t, st) })
	t.Run("tenant deletion lock order", func(t *testing.T) { checkMCPServerTenantDeleteOrder(t, st) })
}

// checkMCPServerOAuthClient checks the tenant client and OAuth definition options against each
// dialect's own migrations.
func checkMCPServerOAuthClient(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, err := st.CreateTenant(ctx, "sales", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	now := NowTS()
	m := MCPServerRow{
		ID: "M1", TenantID: tn.ID, Name: "docs", Transport: "http", URL: "https://mcp.example.com/mcp",
		Targets: "sessions", TimeoutMS: 0, Enabled: true, Auth: "oauth",
		OAuthIssuer: "https://as.example.com", OAuthClientID: "af-client",
		OAuthSecretEnc: "sealed", OAuthKeyRef: "k1", OAuthScopes: "openid email",
		Elicitation: "relay",
		CreatedBy:   "I1", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateMCPServer(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetMCPServer(ctx, tn.ID, "M1")
	if err != nil || !ok {
		t.Fatalf("get = %v %v", ok, err)
	}
	if got.Auth != "oauth" || got.OAuthIssuer != m.OAuthIssuer || got.OAuthClientID != m.OAuthClientID ||
		got.OAuthSecretEnc != "sealed" || got.OAuthKeyRef != "k1" || got.OAuthScopes != "openid email" || got.Elicitation != "relay" {
		t.Fatalf("after create = %+v", got)
	}

	// Dropping to a public client clears the secret and its key.
	got.OAuthClientID, got.OAuthSecretEnc, got.OAuthKeyRef = "af-public", "", ""
	got.OAuthScopes = "https://www.googleapis.com/auth/drive.readonly openid"
	got.Elicitation = ""
	if err := st.UpdateMCPServer(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListMCPServers(ctx, tn.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if l := list[0]; l.OAuthIssuer != m.OAuthIssuer || l.OAuthClientID != "af-public" || l.OAuthSecretEnc != "" || l.OAuthKeyRef != "" ||
		l.OAuthScopes != "https://www.googleapis.com/auth/drive.readonly openid" || l.Elicitation != "" {
		t.Fatalf("after update = %+v", l)
	}
}

func checkCreateMCPServersRollsBack(t *testing.T, st *SQL) {
	t.Helper()
	ctx := context.Background()
	tn, err := st.CreateTenant(ctx, "batch", "Batch")
	if err != nil {
		t.Fatal(err)
	}
	first := MCPServerRow{ID: "first", TenantID: tn.ID, Name: "Google-GMAIL"}
	conflict := MCPServerRow{ID: "other", TenantID: tn.ID, Name: "google-gmail"}
	if name, err := st.CreateMCPServers(ctx, tn.ID, []MCPServerRow{first, conflict}); err != nil || name != conflict.Name {
		t.Fatalf("batch conflict: name=%q err=%v", name, err)
	}
	if rows, err := st.ListMCPServers(ctx, first.TenantID); err != nil || len(rows) != 0 {
		t.Fatalf("partial batch: %d rows, err=%v", len(rows), err)
	}
}

func checkMCPServerConcurrentNames(t *testing.T, st *SQL) {
	t.Helper()
	for _, rename := range []bool{false, true} {
		slug := "create"
		if rename {
			slug = "rename"
		}
		t.Run(slug, func(t *testing.T) {
			ctx := context.Background()
			tn, err := st.CreateTenant(ctx, slug, slug)
			if err != nil {
				t.Fatal(err)
			}
			manual := MCPServerRow{ID: NewID(), TenantID: tn.ID, Name: "wiki"}
			if rename {
				if err := st.CreateMCPServer(ctx, manual); err != nil {
					t.Fatal(err)
				}
			}
			manual.Name = "Google-GMAIL"
			start := make(chan struct{})
			manualResult := make(chan error, 1)
			type result struct {
				name string
				err  error
			}
			batchResult := make(chan result, 1)
			go func() {
				<-start
				if rename {
					manualResult <- st.UpdateMCPServer(ctx, manual)
				} else {
					manualResult <- st.CreateMCPServer(ctx, manual)
				}
			}()
			go func() {
				<-start
				name, err := st.CreateMCPServers(ctx, tn.ID, []MCPServerRow{
					{ID: NewID(), Name: "google-drive"}, {ID: NewID(), Name: "google-gmail"},
				})
				batchResult <- result{name, err}
			}()
			close(start)
			merr, batch := <-manualResult, <-batchResult
			rows, err := st.ListMCPServers(ctx, tn.ID)
			if batch.err != nil || err != nil {
				t.Fatalf("batch: %v, list: %v", batch.err, err)
			}
			switch {
			case merr == nil:
				if batch.name != "google-gmail" || len(rows) != 1 || rows[0].Name != manual.Name {
					t.Fatalf("manual write won but batch was not rolled back: conflict=%q, rows=%+v", batch.name, rows)
				}
			case errors.Is(merr, ErrMCPServerNameTaken):
				want := 2
				if rename {
					want++
				}
				if batch.name != "" || len(rows) != want || rows[0].Name != "google-drive" || rows[1].Name != "google-gmail" || (rename && rows[2].Name != "wiki") {
					t.Fatalf("batch won but manual write persisted: conflict=%q, rows=%+v", batch.name, rows)
				}
			default:
				t.Fatalf("manual write: %v", merr)
			}
		})
	}
}

func checkMCPServerTenantDeleteOrder(t *testing.T, st *SQL) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tn, err := st.CreateTenant(ctx, "deleted", "Deleted")
	if err != nil {
		t.Fatal(err)
	}
	row := MCPServerRow{ID: NewID(), TenantID: tn.ID, Name: "google-gmail"}
	if err := st.CreateMCPServer(ctx, row); err != nil {
		t.Fatal(err)
	}
	edit, err := st.beginMCPServerWrite(ctx, tn.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer edit.Rollback()
	var pid int
	if err := edit.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- st.DeleteTenant(ctx, tn.ID) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := st.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-deleted:
			t.Fatalf("tenant deletion bypassed the edit's lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	// A deletion waiting on the tenant must not have locked or removed the MCP row first.
	var id string
	if err := edit.QueryRowContext(ctx, `SELECT id FROM mcp_server WHERE id=? FOR UPDATE NOWAIT`, row.ID).Scan(&id); err != nil {
		t.Fatalf("delete touched the MCP row before the edit could: %v", err)
	}
	if _, err := edit.ExecContext(ctx, `UPDATE mcp_server SET label=? WHERE tenant_id=? AND id=?`, "Edited", tn.ID, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := edit.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if rows, err := st.ListMCPServers(ctx, tn.ID); err != nil || len(rows) != 0 {
		t.Fatalf("deleted tenant retained %d definitions: %v", len(rows), err)
	}
}
