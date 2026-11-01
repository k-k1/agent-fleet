package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// The claim is the guard against duplicate admin notices from concurrent sweeps and replicas, so
// it is proven on both dialects: exactly one of many simultaneous callers wins, a released mark
// can be claimed again, and a workspace that does not exist is never claimed.
func TestWorkspaceNoticeClaimIsExclusive(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, st *SQL) string {
		t.Helper()
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		tn, _ := st.CreateTenant(ctx, "sales", "営業部")
		id, _ := st.UpsertIdentity(ctx, "w@acme.co.jp", "w-acme-co-jp", "")
		mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
		if err != nil {
			t.Fatal(err)
		}
		ws := Workspace{ID: "W-1", TenantID: tn.ID, MembershipID: mem.ID, ContainerName: "af-ws-w",
			DataDir: "/d", AgentPort: "7731", AgentToken: "t", State: "running", CreatedAt: NowTS()}
		if err := st.CreateWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
		return ws.ID
	}
	run := func(t *testing.T, st *SQL) {
		wsID := setup(t, st)
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := st.ClaimWorkspaceNotice(ctx, wsID, WorkspaceNoticeHomeFull)
				if err != nil {
					t.Error(err)
				}
				if ok {
					mu.Lock()
					won++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Fatalf("%d callers claimed the same notice, want 1", won)
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, wsID, WorkspaceNoticeHomeFull); !on {
			t.Error("claimed notice not reported raised")
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, wsID, WorkspaceNoticeBootInstall); on {
			t.Error("another kind reported raised")
		}
		if ok, err := st.ReleaseWorkspaceNotice(ctx, wsID, WorkspaceNoticeHomeFull); err != nil || !ok {
			t.Fatalf("release = %v, %v", ok, err)
		}
		if ok, _ := st.ClaimWorkspaceNotice(ctx, wsID, WorkspaceNoticeHomeFull); !ok {
			t.Error("a released notice could not be claimed again")
		}
		if ok, _ := st.ClaimWorkspaceNotice(ctx, "no-such", WorkspaceNoticeHomeFull); ok {
			t.Error("a workspace that does not exist was claimed")
		}
		// A wiped home starts over.
		if err := st.ClearWorkspaceDisk(ctx, wsID); err != nil {
			t.Fatal(err)
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, wsID, WorkspaceNoticeHomeFull); on {
			t.Error("Clean home left the notice mark behind")
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		st, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
	t.Run("postgres", func(t *testing.T) {
		url, ok := pgtest.Schema(t)
		if !ok {
			t.Skip("set AF_TEST_DATABASE_URL to run the Postgres claim test")
		}
		st, err := OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
}
