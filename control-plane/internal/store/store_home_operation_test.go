package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

// homeOpStores is the SQLite store, and the Postgres one where AF_TEST_DATABASE_URL is set:
// the finishing transaction's claim has to hold on both.
func homeOpStores(t *testing.T) map[string]*SQL {
	t.Helper()
	ctx := context.Background()
	out := map[string]*SQL{}
	lite, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lite.Close() })
	out["sqlite"] = lite
	if url, ok := pgtest.Schema(t); ok {
		pg, err := OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pg.Close() })
		out["postgres"] = pg
	}
	for name, st := range out {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("%s: migrate: %v", name, err)
		}
	}
	return out
}

func homeOpWorkspace(t *testing.T, st *SQL) Workspace {
	t.Helper()
	ctx := context.Background()
	tn, err := st.EnsureDefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertIdentity(ctx, "", "u-"+NewID()[:8], "")
	if err != nil {
		t.Fatal(err)
	}
	mem, err := st.EnsureMembership(ctx, id.ID, tn.ID, "member")
	if err != nil {
		t.Fatal(err)
	}
	ws := Workspace{ID: NewID(), TenantID: tn.ID, MembershipID: mem.ID, ContainerName: "c-" + NewID()[:8], Network: "n",
		DataDir: "d", AgentPort: "1", AgentToken: "t", State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

// One open operation per workspace; the record keeps its audit template and the task's
// ARN; finishing it is claimed once, and only the claim writes the audit entry and deletes
// the workspace row.
func TestHomeOperationRecordIsFinishedOnce(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		ws := homeOpWorkspace(t, st)
		audit := &HomeOpAudit{Base: AuditLog{TenantID: ws.TenantID, ActorKind: "user", ActorID: "admin",
			Action: "workspace.destroy", Target: "u"}, OK: "destroyed", Leftovers: true, OKStatus: 200,
			FailPrefix: "error: ", FailStatus: 500}
		op := HomeOperation{ID: NewID(), WorkspaceID: ws.ID, MembershipID: ws.MembershipID, Kind: HomeOpDestroy,
			Op: "destroy", Audit: audit}
		if err := st.InsertHomeOperation(ctx, op); err != nil {
			t.Fatalf("%s: insert: %v", name, err)
		}
		second := op
		second.ID = NewID()
		if err := st.InsertHomeOperation(ctx, second); !errors.Is(err, ErrHomeOperationOpen) {
			t.Errorf("%s: a second open operation = %v, want ErrHomeOperationOpen", name, err)
		}
		if err := st.SetHomeOperationTask(ctx, op.ID, "arn:task/1"); err != nil {
			t.Fatalf("%s: set task: %v", name, err)
		}
		got, open, err := st.GetHomeOperationByWorkspace(ctx, ws.ID)
		if err != nil || !open || got.TaskARN != "arn:task/1" || got.Audit == nil || got.Audit.Base.Action != "workspace.destroy" {
			t.Fatalf("%s: read back = %+v open=%v err=%v", name, got, open, err)
		}
		if all, err := st.ListHomeOperations(ctx); err != nil || len(all) != 1 {
			t.Fatalf("%s: list = %d, %v", name, len(all), err)
		}

		entry := got.Audit.Entry(nil, []string{"efs:fs/elsewhere"})
		finish := HomeOperationFinish{Audit: &entry, DeleteWorkspace: true}
		claimed, err := st.FinishHomeOperation(ctx, op.ID, finish)
		if err != nil || !claimed {
			t.Fatalf("%s: first finish = %v, %v; want claimed", name, claimed, err)
		}
		again := got.Audit.Entry(nil, nil)
		if claimed, err := st.FinishHomeOperation(ctx, op.ID, HomeOperationFinish{Audit: &again, DeleteWorkspace: true}); err != nil || claimed {
			t.Errorf("%s: second finish = %v, %v; want nothing claimed", name, claimed, err)
		}
		if _, found, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID); found {
			t.Errorf("%s: the destroyed workspace's row is still there", name)
		}
		rows, err := st.ListAuditByTenant(ctx, ws.TenantID, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rows {
			if r.Action == "workspace.destroy" {
				n++
				if r.Detail != "destroyed; NOT deleted: efs:fs/elsewhere" || r.HTTPStatus != 200 {
					t.Errorf("%s: outcome = %q (%d)", name, r.Detail, r.HTTPStatus)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: %d outcome rows, want exactly one", name, n)
		}
		if _, open, _ := st.GetHomeOperationByWorkspace(ctx, ws.ID); open {
			t.Errorf("%s: the finished record is still open", name)
		}
	}
}

// A Clean home that succeeded records the workspace as stopped with the claim; a refusal
// (no finish fields) only drops the record.
func TestHomeOperationFinishStopsOrDrops(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		ws := homeOpWorkspace(t, st)
		op := HomeOperation{ID: NewID(), WorkspaceID: ws.ID, MembershipID: ws.MembershipID, Kind: HomeOpAdminErase, Op: "clean"}
		if err := st.InsertHomeOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		if claimed, err := st.FinishHomeOperation(ctx, op.ID, HomeOperationFinish{StopWorkspace: true}); err != nil || !claimed {
			t.Fatalf("%s: finish = %v, %v", name, claimed, err)
		}
		got, _, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID)
		if got.State != "stopped" {
			t.Errorf("%s: state = %q, want stopped", name, got.State)
		}
		op.ID = NewID()
		if err := st.InsertHomeOperation(ctx, op); err != nil {
			t.Fatalf("%s: a new operation after the last one finished: %v", name, err)
		}
		if claimed, err := st.FinishHomeOperation(ctx, op.ID, HomeOperationFinish{}); err != nil || !claimed {
			t.Fatalf("%s: drop = %v, %v", name, claimed, err)
		}
		if _, found, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID); !found {
			t.Errorf("%s: dropping a record removed the workspace", name)
		}
	}
}
