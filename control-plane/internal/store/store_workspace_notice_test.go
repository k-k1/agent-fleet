package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/pgtest"
)

type noticeEnv struct {
	st             *SQL
	ws             Workspace
	adminA, adminB string
}

func newNoticeEnv(t *testing.T, st *SQL) noticeEnv {
	t.Helper()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tn, _ := st.CreateTenant(ctx, "sales", "営業部")
	mk := func(email, key, role string) Membership {
		id, _ := st.UpsertIdentity(ctx, email, key, "")
		m, err := st.EnsureMembership(ctx, id.ID, tn.ID, role)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	mem := mk("w@acme.co.jp", "w-acme-co-jp", "member")
	a := mk("a@acme.co.jp", "a-acme-co-jp", "tenant_admin")
	b := mk("b@acme.co.jp", "b-acme-co-jp", "tenant_admin")
	ws := Workspace{ID: "W-1", TenantID: tn.ID, MembershipID: mem.ID, ContainerName: "af-ws-w",
		DataDir: "/d", AgentPort: "7731", AgentToken: "t", State: "running", CreatedAt: NowTS()}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	return noticeEnv{st: st, ws: ws, adminA: a.ID, adminB: b.ID}
}

func (e noticeEnv) note(id, to string) Notification {
	return Notification{EventID: id + "-" + to, MembershipID: to, Kind: "member-home-full",
		TargetType: "member", Payload: "{}", CreatedAt: NowTS()}
}

func (e noticeEnv) count(t *testing.T, to string) int {
	t.Helper()
	ns, err := e.st.ListNotifications(context.Background(), to, "0000", 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(ns)
}

func (e noticeEnv) raise(ctx context.Context, id string, to ...string) error {
	return e.st.SyncWorkspaceNotice(ctx, e.ws, WorkspaceNoticeHomeFull, func(_ NoticeReading, _ bool) NoticeDecision {
		return NoticeDecision{Want: true, Notes: func([]MemberInfo) []Notification {
			var out []Notification
			for _, m := range to {
				out = append(out, e.note(id, m))
			}
			return out
		}}
	})
}

func (e noticeEnv) release(ctx context.Context) error {
	return e.st.SyncWorkspaceNotice(ctx, e.ws, WorkspaceNoticeHomeFull, func(_ NoticeReading, _ bool) NoticeDecision {
		return NoticeDecision{Want: false}
	})
}

// The notice is the guard against duplicate admin notices (concurrent sweeps, replicas) and against
// a lost one (a mark left behind by a failed delivery), so it is proven on both dialects.
func TestWorkspaceNoticeSync(t *testing.T) {
	run := func(t *testing.T, st *SQL) {
		ctx := context.Background()
		e := newNoticeEnv(t, st)

		// Exactly one of many simultaneous evaluators delivers.
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := e.raise(ctx, "c1-"+string(rune('a'+i)), e.adminA); err != nil {
					t.Error(err)
				}
			}(i)
		}
		wg.Wait()
		if n := e.count(t, e.adminA); n != 1 {
			t.Fatalf("%d notifications after 16 simultaneous evaluations, want 1", n)
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, e.ws.ID, WorkspaceNoticeHomeFull); !on {
			t.Error("raised notice has no mark")
		}
		// Recovery clears it; the next crossing delivers again.
		if err := e.release(ctx); err != nil {
			t.Fatal(err)
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, e.ws.ID, WorkspaceNoticeHomeFull); on {
			t.Error("mark survived recovery")
		}
		if err := e.raise(ctx, "c2", e.adminA); err != nil || e.count(t, e.adminA) != 2 {
			t.Fatalf("second crossing: err=%v count=%d, want 2", err, e.count(t, e.adminA))
		}
		if err := e.release(ctx); err != nil {
			t.Fatal(err)
		}

		// A cancelled context between the decision and the commit leaves neither mark nor
		// notification, so the retry delivers.
		cctx, cancel := context.WithCancel(ctx)
		err := st.SyncWorkspaceNotice(cctx, e.ws, WorkspaceNoticeHomeFull, func(NoticeReading, bool) NoticeDecision {
			return NoticeDecision{Want: true, Notes: func([]MemberInfo) []Notification {
				cancel()
				return []Notification{e.note("c3", e.adminA)}
			}}
		})
		if err == nil {
			t.Fatal("a cancelled evaluation reported success")
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, e.ws.ID, WorkspaceNoticeHomeFull); on {
			t.Fatal("a cancelled evaluation left its mark behind")
		}
		if err := e.raise(ctx, "c3", e.adminA); err != nil || e.count(t, e.adminA) != 3 {
			t.Fatalf("retry after cancel: err=%v count=%d, want 3", err, e.count(t, e.adminA))
		}
		if err := e.release(ctx); err != nil {
			t.Fatal(err)
		}

		// One recipient failing (here: a membership that does not exist) rolls back the others
		// too, so the retry cannot double-deliver to the first.
		before := e.count(t, e.adminA)
		if err := e.raise(ctx, "c4", e.adminA, "no-such-membership"); err == nil {
			t.Fatal("a failing recipient reported success")
		}
		if n := e.count(t, e.adminA); n != before {
			t.Fatalf("partial delivery: admin A has %d, want %d", n, before)
		}
		if err := e.raise(ctx, "c4b", e.adminA, e.adminB); err != nil || e.count(t, e.adminA) != before+1 || e.count(t, e.adminB) != 1 {
			t.Fatalf("retry after partial failure: err=%v A=%d B=%d", err, e.count(t, e.adminA), e.count(t, e.adminB))
		}
		if err := e.release(ctx); err != nil {
			t.Fatal(err)
		}

		// A workspace that does not exist is never marked or notified.
		ghost := e.ws
		ghost.ID = "no-such"
		if err := st.SyncWorkspaceNotice(ctx, ghost, WorkspaceNoticeHomeFull, func(NoticeReading, bool) NoticeDecision {
			return NoticeDecision{Want: true, Notes: func([]MemberInfo) []Notification { return []Notification{e.note("g", e.adminA)} }}
		}); err != nil {
			t.Fatal(err)
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, "no-such", WorkspaceNoticeHomeFull); on {
			t.Error("a workspace that does not exist was marked")
		}

		// A wiped home starts over.
		if err := e.raise(ctx, "c5", e.adminA); err != nil {
			t.Fatal(err)
		}
		if err := st.ClearWorkspaceDisk(ctx, e.ws.ID); err != nil {
			t.Fatal(err)
		}
		if on, _ := st.WorkspaceNoticeRaised(ctx, e.ws.ID, WorkspaceNoticeHomeFull); on {
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
			t.Skip("set AF_TEST_DATABASE_URL to run the Postgres notice test")
		}
		st, err := OpenPostgres(url)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
}

// On Postgres a writer of the source reading waits for an evaluation that holds it, so a decision
// can never be taken on a value a concurrent write has already replaced (the evaluation then
// sees the new one next time round).
func TestWorkspaceNoticeEvaluationHoldsTheSourceRow(t *testing.T) {
	url, ok := pgtest.Schema(t)
	if !ok {
		t.Skip("set AF_TEST_DATABASE_URL to run the Postgres lock test")
	}
	st, err := OpenPostgres(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := newNoticeEnv(t, st)
	ctx := context.Background()
	if err := st.PutWorkspaceBootFailed(ctx, e.ws.ID, `{"claude":"x"}`, DiskObservedAt(time.Now())); err != nil {
		t.Fatal(err)
	}
	inDecide, proceed := make(chan struct{}), make(chan struct{})
	evalDone := make(chan error, 1)
	var saw string
	go func() {
		evalDone <- st.SyncWorkspaceNotice(ctx, e.ws, WorkspaceNoticeBootInstall, func(r NoticeReading, _ bool) NoticeDecision {
			saw = r.BootFailed
			close(inDecide)
			<-proceed
			return NoticeDecision{Want: false}
		})
	}()
	<-inDecide
	putDone := make(chan error, 1)
	go func() {
		putDone <- st.PutWorkspaceBootFailed(ctx, e.ws.ID, "", DiskObservedAt(time.Now().Add(time.Second)))
	}()
	select {
	case <-putDone:
		t.Fatal("the source row was written while an evaluation held it")
	case <-time.After(400 * time.Millisecond):
	}
	close(proceed)
	if err := <-evalDone; err != nil {
		t.Fatal(err)
	}
	if err := <-putDone; err != nil {
		t.Fatal(err)
	}
	if saw != `{"claude":"x"}` {
		t.Errorf("decision read %q", saw)
	}
}
