package store

import (
	"context"
	"testing"

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
}
