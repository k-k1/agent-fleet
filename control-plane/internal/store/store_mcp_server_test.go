package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

func TestMCPServerOAuthClientSQLite(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkMCPServerOAuthClient(t, st)
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
}

// checkMCPServerOAuthClient: the tenant client's four columns live in their own migration per
// dialect, so each backend has to carry them through create, read, list and update.
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
		OAuthSecretEnc: "sealed", OAuthKeyRef: "k1",
		CreatedBy: "I1", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateMCPServer(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetMCPServer(ctx, tn.ID, "M1")
	if err != nil || !ok {
		t.Fatalf("get = %v %v", ok, err)
	}
	if got.Auth != "oauth" || got.OAuthIssuer != m.OAuthIssuer || got.OAuthClientID != m.OAuthClientID ||
		got.OAuthSecretEnc != "sealed" || got.OAuthKeyRef != "k1" {
		t.Fatalf("after create = %+v", got)
	}

	// Dropping to a public client clears the secret and its key.
	got.OAuthClientID, got.OAuthSecretEnc, got.OAuthKeyRef = "af-public", "", ""
	if err := st.UpdateMCPServer(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListMCPServers(ctx, tn.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if l := list[0]; l.OAuthIssuer != m.OAuthIssuer || l.OAuthClientID != "af-public" || l.OAuthSecretEnc != "" || l.OAuthKeyRef != "" {
		t.Fatalf("after update = %+v", l)
	}
}
