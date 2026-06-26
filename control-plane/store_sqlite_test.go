package main

import (
	"context"
	"path/filepath"
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
	// migrate is idempotent.
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("migrate again: %v", err)
	}

	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil || tn.ID != "default" {
		t.Fatalf("tenant: %v %+v", err, tn)
	}

	// UpsertUser is idempotent on (tenant, user_key); email updated when non-empty.
	u1, err := st.UpsertUser(ctx, tn.ID, "", "k1-kami-gmail-com")
	if err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	u2, err := st.UpsertUser(ctx, tn.ID, "k1.kami@gmail.com", "k1-kami-gmail-com")
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if u1.ID != u2.ID {
		t.Fatalf("upsert not idempotent: %s != %s", u1.ID, u2.ID)
	}
	if u2.Email != "k1.kami@gmail.com" {
		t.Fatalf("email not updated: %q", u2.Email)
	}

	// No workspace yet.
	if _, ok, err := st.GetWorkspaceByUser(ctx, u2.ID); err != nil || ok {
		t.Fatalf("expected no workspace: ok=%v err=%v", ok, err)
	}

	ws := Workspace{
		ID: newID(), TenantID: tn.ID, UserID: u2.ID,
		ContainerName: "af-ws-k1-kami-gmail-com", Network: "af-net-k1-kami-gmail-com",
		DataDir: "/tmp/af-data/k1-kami-gmail-com", AgentPort: "7700",
		AgentToken: "tok", State: "stopped", CreatedAt: nowTS(),
	}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create ws: %v", err)
	}
	got, ok, err := st.GetWorkspaceByUser(ctx, u2.ID)
	if err != nil || !ok || got.AgentPort != "7700" || got.ContainerName != ws.ContainerName {
		t.Fatalf("get ws: ok=%v err=%v %+v", ok, err, got)
	}

	mx, err := st.MaxAgentPort(ctx)
	if err != nil || mx != 7700 {
		t.Fatalf("maxport: %v %d", err, mx)
	}

	list, err := st.ListWorkspaces(ctx, tn.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list))
	}
}
