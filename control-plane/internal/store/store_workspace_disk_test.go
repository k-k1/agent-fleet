package store

import (
	"context"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// The conditional upsert is dialect-sensitive, so the same refusal of an older reading is
// checked on Postgres as well (skipped without AF_TEST_DATABASE_URL).
func TestPostgresWorkspaceDiskKeepsTheNewerReading(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres test")
	}
	ctx := context.Background()
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertIdentity(ctx, "d@example.com", "d-example-com", "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(ctx, Workspace{ID: "W", TenantID: tn.ID, MembershipID: mem.ID,
		ContainerName: "af-w", DataDir: "d", AgentPort: "1", AgentToken: "t", State: "stopped", CreatedAt: NowTS()}); err != nil {
		t.Fatal(err)
	}
	// A reading for a workspace that does not exist is not stored.
	if err := st.PutWorkspaceDisk(ctx, WorkspaceDisk{WorkspaceID: "ghost", Used: 1, ObservedAt: DiskObservedAt(time.Now())}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetWorkspaceDisk(ctx, "ghost"); ok {
		t.Fatal("stored a reading for a workspace that does not exist")
	}
	if _, ok, err := st.GetWorkspaceDisk(ctx, "W"); ok || err != nil {
		t.Fatalf("unmeasured: ok=%v err=%v", ok, err)
	}
	newer := WorkspaceDisk{WorkspaceID: "W", Used: 9 << 40, Total: 10 << 40, ObservedAt: "2026-10-20T10:05:00Z"}
	older := WorkspaceDisk{WorkspaceID: "W", Used: 1, Total: 10, ObservedAt: "2026-10-20T10:00:00Z"}
	for _, d := range []WorkspaceDisk{newer, older} {
		if err := st.PutWorkspaceDisk(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := st.GetWorkspaceDisk(ctx, "W"); got != newer {
		t.Errorf("stored %+v, want %+v", got, newer)
	}
	if err := st.ClearWorkspaceDisk(ctx, "W"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetWorkspaceDisk(ctx, "W"); ok {
		t.Error("reading survived the clear")
	}
}
