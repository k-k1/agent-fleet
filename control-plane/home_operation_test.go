package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// recordedHomeRuntime is backgroundHomeRuntime with the home task bound to the operation's
// record, as ecs is. Each wipe, erase or destroy reports its task through the binding and
// returns the next of outcomes (nil once they run out).
type recordedHomeRuntime struct {
	*backgroundHomeRuntime
	bmu      sync.Mutex
	bindings []runtime.HomeTaskBinding
	outcomes []error
	// startErr fails Start; onStart sees the world at the moment of the start.
	startErr error
	onStart  func()
}

func (r *recordedHomeRuntime) Start(ctx context.Context) error {
	if r.onStart != nil {
		r.onStart()
	}
	if r.startErr != nil {
		r.rec.add("start-failed")
		return r.startErr
	}
	return r.backgroundHomeRuntime.Start(ctx)
}

func newRecordedHomeRuntime(state string, outcomes ...error) *recordedHomeRuntime {
	return &recordedHomeRuntime{backgroundHomeRuntime: newBackgroundHomeRuntime(state), outcomes: outcomes}
}

func (r *recordedHomeRuntime) BindHomeTask(b runtime.HomeTaskBinding) bool {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	if b.Token != "" {
		r.bindings = append(r.bindings, b)
	}
	return true
}

func (r *recordedHomeRuntime) lastBinding() runtime.HomeTaskBinding {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	if len(r.bindings) == 0 {
		return runtime.HomeTaskBinding{}
	}
	return r.bindings[len(r.bindings)-1]
}

func (r *recordedHomeRuntime) run(what string) error {
	<-r.gate
	r.rec.add(what)
	if b := r.lastBinding(); b.Started != nil {
		b.Started("arn:task/" + b.Token)
	}
	r.bmu.Lock()
	defer r.bmu.Unlock()
	if len(r.outcomes) == 0 {
		return nil
	}
	err := r.outcomes[0]
	r.outcomes = r.outcomes[1:]
	return err
}

func (r *recordedHomeRuntime) WipeHome(_ context.Context, what runtime.HomeWipe) error {
	return r.run("wipe:" + string(what))
}
func (r *recordedHomeRuntime) EraseHome(context.Context) error { return r.run("erase") }
func (r *recordedHomeRuntime) Destroy(context.Context) ([]string, error) {
	return []string{"efs:fs-1/elsewhere/M"}, r.run("destroy")
}

var errStillRunning = fmt.Errorf("%w: the CP was restarted", runtime.ErrHomeTaskUnresolved)

// openRecord writes the record a CP leaves behind when it dies after RunTask.
func openRecord(t *testing.T, st *store.SQL, ws store.Workspace, kind, op string, audit *store.HomeOpAudit, arn string) store.HomeOperation {
	t.Helper()
	ctx := context.Background()
	rec := store.HomeOperation{ID: store.NewID(), WorkspaceID: ws.ID, MembershipID: ws.MembershipID, Kind: kind, Op: op, Audit: audit}
	if err := st.InsertHomeOperation(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if arn != "" {
		if err := st.SetHomeOperationTask(ctx, rec.ID, arn); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

func reconcileNow(mgr *manager) {
	mgr.reconcileHomeOps(context.Background())
	mgr.homeOpWG.Wait()
}

func recordOpen(t *testing.T, st *store.SQL, wsID string) bool {
	t.Helper()
	_, open, err := st.GetHomeOperationByWorkspace(context.Background(), wsID)
	if err != nil {
		t.Fatal(err)
	}
	return open
}

func countAudit(t *testing.T, st *store.SQL, tn store.Tenant, prefix string) int {
	t.Helper()
	n := 0
	for _, a := range auditActions(t, st, tn) {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func victimWorkspace(t *testing.T, st *store.SQL, victim store.Identity, tn store.Tenant) store.Workspace {
	t.Helper()
	ws, ok, err := st.GetWorkspaceByMembership(context.Background(), membershipIDOf(t, st, victim, tn))
	if err != nil || !ok {
		t.Fatalf("workspace: %v", err)
	}
	return ws
}

// A member's Recreate interrupted by a CP restart: the record is all that is left. The
// reconciler runs the wipe again bound to it — same token, the recorded task, Resume — and
// then starts the workspace, which a start of the member's own was refused until then.
func TestReconcilerFinishesAMemberWipeAfterARestart(t *testing.T) {
	rt := newRecordedHomeRuntime("stopped")
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	rec := openRecord(t, st, ws, store.HomeOpMemberWipe, "repos", nil, "arn:task/lost")

	res := &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(context.Background(), res); aerr == nil || aerr.code != errCodeHomeOperationInProgress {
		t.Fatalf("a start while the record is open = %+v, want %s", aerr, errCodeHomeOperationInProgress)
	}
	reconcileNow(mgr)
	b := rt.lastBinding()
	if b.Token != rec.ID || b.TaskARN != "arn:task/lost" || !b.Resume {
		t.Errorf("binding = %+v, want the record's token and task, resumed", b)
	}
	// Marked as clearing while the wipe runs, so the Console reads `starting` again.
	if got := rt.rec.log(); got != "mark,wipe:repos,unmark,start" {
		t.Errorf("drove %q, want mark,wipe:repos,unmark,start", got)
	}
	if recordOpen(t, st, ws.ID) {
		t.Error("the finished operation's record is still open")
	}
	reconcileNow(mgr)
	if got := rt.rec.log(); got != "mark,wipe:repos,unmark,start" {
		t.Errorf("a second sweep drove %q; the step after the task ran twice", got)
	}
}

// The member was removed while the operation was open: the wipe is finished, the start is
// not (the start is what the member asked for, and they no longer can).
func TestReconcilerDoesNotStartARemovedMembersWorkspace(t *testing.T) {
	ctx := context.Background()
	rt := newRecordedHomeRuntime("stopped")
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	openRecord(t, st, ws, store.HomeOpMemberWipe, "clean", nil, "")
	if err := st.SetMembershipStatus(ctx, ws.MembershipID, "inactive"); err != nil {
		t.Fatal(err)
	}
	reconcileNow(mgr)
	if got := rt.rec.log(); got != "mark,wipe:clean,unmark" {
		t.Errorf("drove %q, want the wipe and no start", got)
	}
	if recordOpen(t, st, ws.ID) {
		t.Error("the record stayed open")
	}
}

// An administrator's Clean home and Destroy interrupted by a restart: the reconciler writes
// the outcome the intent row is waiting for, once, with the row's deletion for Destroy.
func TestReconcilerFinishesAdminOperationsAfterARestart(t *testing.T) {
	for _, c := range []struct {
		kind, op, action, ok string
		leftovers            bool
	}{
		{store.HomeOpAdminErase, "clean", "workspace.clean_home", "home erased", false},
		{store.HomeOpDestroy, "destroy", "workspace.destroy", "workspace destroyed (home and runtime resources deleted)", true},
	} {
		ctx := context.Background()
		rt := newRecordedHomeRuntime("stopped")
		close(rt.gate)
		st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
		ws := victimWorkspace(t, st, victim, tn)
		audit := &store.HomeOpAudit{Base: store.AuditLog{TenantID: tn.ID, ActorKind: "user", ActorID: "boss",
			Action: c.action, Target: "leaver-acme-co-jp"}, OK: c.ok, Leftovers: c.leftovers, OKStatus: http.StatusOK,
			FailPrefix: "error: ", FailStatus: http.StatusInternalServerError}
		openRecord(t, st, ws, c.kind, c.op, audit, "")
		reconcileNow(mgr)
		reconcileNow(mgr)
		if n := countAudit(t, st, tn, c.action+":"+c.ok); n != 1 {
			t.Errorf("%s: %d outcome entries, want exactly one (%v)", c.kind, n, auditActions(t, st, tn))
		}
		_, found, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID)
		if c.kind == store.HomeOpDestroy {
			if found {
				t.Error("destroy: the row survived a finished Destroy")
			}
			if !auditHas(t, st, tn, c.action+":"+c.ok+"; NOT deleted: efs:fs-1/elsewhere/M") {
				t.Errorf("destroy: the leftovers are not in the outcome: %v", auditActions(t, st, tn))
			}
		} else if got, _, _ := st.GetWorkspaceByMembership(ctx, ws.MembershipID); got.State != "stopped" {
			t.Errorf("clean home: state = %q, want stopped", got.State)
		}
	}
}

// The process that started an operation and the reconciler never both apply the step after
// the task: while the starter holds the lease the reconciler leaves the record alone, and
// the starter's finish is the one outcome entry.
func TestReconcilerLeavesALiveOperationToItsStarter(t *testing.T) {
	rt := newRecordedHomeRuntime("running")
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	if w := httpCleanHome(mgr); w.Code != http.StatusAccepted {
		t.Fatalf("clean home = %d %s", w.Code, w.Body.String())
	}
	ws := victimWorkspace(t, st, victim, tn)
	if !recordOpen(t, st, ws.ID) {
		t.Fatal("no record was written before the task")
	}
	reconcileNow(mgr)
	if got := rt.rec.log(); got != "stop" {
		t.Errorf("the reconciler drove %q while the starter held the lease", got)
	}
	close(rt.gate)
	waitFor(t, "the outcome entry", func() bool { return auditHas(t, st, tn, "workspace.clean_home:home erased") })
	waitFor(t, "the lease to be released", func() bool { return leaseFree(t, mgr, ws.MembershipID) })
	reconcileNow(mgr)
	if n := countAudit(t, st, tn, "workspace.clean_home:"); n != 1 {
		t.Errorf("%d outcome entries, want one: %v", n, auditActions(t, st, tn))
	}
	if got := rt.rec.log(); got != "stop,erase" {
		t.Errorf("drove %q, want stop,erase", got)
	}
	if b := rt.lastBinding(); b.Token == "" || b.Resume {
		t.Errorf("the starter's binding = %+v, want its record's token, not resumed", b)
	}
}

// A member's background wipe whose outcome the starter could not read (the task may still
// run) is no failure: nothing is shown, the record stays, and a start is refused until the
// reconciler has finished it.
func TestMemberWipeWithAnUnresolvedOutcomeIsLeftToTheReconciler(t *testing.T) {
	rt := newRecordedHomeRuntime("running", errStillRunning)
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	res := &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	api := newWorkspaceAPI(mgr, false)
	if w := callMemberWipe(api, "recreate", res); w.Code != http.StatusAccepted {
		t.Fatalf("recreate = %d %s", w.Code, w.Body.String())
	}
	waitFor(t, "the lease to be released", func() bool {
		return strings.Contains(rt.rec.log(), "unmark") && leaseFree(t, mgr, ws.MembershipID)
	})
	if _, shown := api.workspacePayload(context.Background(), res, "stopped")["homeWipeFailed"]; shown {
		t.Error("an unresolved outcome was shown to the member as a failure")
	}
	if !recordOpen(t, st, ws.ID) {
		t.Fatal("the record of an unresolved operation was dropped")
	}
	if aerr := api.ensureWorkspaceStarted(context.Background(), res); aerr == nil || aerr.code != errCodeHomeOperationInProgress {
		t.Errorf("a start before the reconciler = %+v, want %s", aerr, errCodeHomeOperationInProgress)
	}
	reconcileNow(mgr)
	if got := rt.rec.log(); !strings.HasSuffix(got, "mark,wipe:repos,unmark,start") || strings.Count(got, "start") != 1 {
		t.Errorf("drove %q, want the reconciler's wipe and one start", got)
	}
	if recordOpen(t, st, ws.ID) {
		t.Error("the record stayed open after the reconciler finished it")
	}
}

// A resumed member wipe that fails leaves the workspace stopped and says why, as the
// starter would have.
func TestReconcilerReportsAFailedMemberWipe(t *testing.T) {
	rt := newRecordedHomeRuntime("stopped", errors.New("the home task (repos) failed with exit 1"))
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	openRecord(t, st, ws, store.HomeOpMemberWipe, "repos", nil, "")
	reconcileNow(mgr)
	if strings.Contains(rt.rec.log(), "start") {
		t.Errorf("a failed wipe went on to start: %q", rt.rec.log())
	}
	res := &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	got, _ := newWorkspaceAPI(mgr, false).workspacePayload(context.Background(), res, "stopped")["homeWipeFailed"].(string)
	if !strings.Contains(got, "exit 1") {
		t.Errorf("homeWipeFailed = %q, want the failure", got)
	}
	if recordOpen(t, st, ws.ID) {
		t.Error("a definite failure left the record open")
	}
}

// A wipe that succeeded is recorded as such before the start it owes: a CP lost between the
// two leaves a record in the start phase, and the reconciler then makes the start — and
// does not run the wipe again.
func TestReconcilerMakesTheStartAFinishedWipeOwes(t *testing.T) {
	rt := newRecordedHomeRuntime("stopped")
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	rec := openRecord(t, st, ws, store.HomeOpMemberWipe, "repos", nil, "arn:task/done")
	if claimed, err := st.AdvanceHomeOperation(context.Background(), rec.ID, store.HomeOpPhaseStart); err != nil || !claimed {
		t.Fatalf("advance: %v %v", claimed, err)
	}
	res := &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	if aerr := newWorkspaceAPI(mgr, false).ensureWorkspaceStarted(context.Background(), res); aerr == nil || aerr.code != errCodeHomeOperationInProgress {
		t.Errorf("somebody else's start while the start is owed = %+v, want %s", aerr, errCodeHomeOperationInProgress)
	}
	reconcileNow(mgr)
	if got := rt.rec.log(); got != "start" {
		t.Errorf("drove %q, want only the start", got)
	}
	if recordOpen(t, st, ws.ID) {
		t.Error("the record stayed open after its start")
	}
}

// The starter moves the record to the start phase before it starts, so the start is never
// only in its memory.
func TestMemberWipeRecordsTheFinishedWipeBeforeItStarts(t *testing.T) {
	rt := newRecordedHomeRuntime("running")
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	phase := make(chan string, 1)
	rt.onStart = func() {
		op, open, _ := st.GetHomeOperationByWorkspace(context.Background(), ws.ID)
		if !open {
			phase <- "closed"
			return
		}
		phase <- op.Phase
	}
	res := &resolved{rt: rt, ws: ws, mv: store.MembershipView{MembershipID: ws.MembershipID, TenantID: ws.TenantID}}
	if w := callMemberWipe(newWorkspaceAPI(mgr, false), "recreate", res); w.Code != http.StatusAccepted {
		t.Fatalf("recreate = %d %s", w.Code, w.Body.String())
	}
	if got := <-phase; got != store.HomeOpPhaseStart {
		t.Errorf("at the start the record was %q, want it open in the start phase", got)
	}
	waitFor(t, "the record to close", func() bool { return !recordOpen(t, st, ws.ID) })
}

// A start that fails after the wipe ends the record with the reason in the same write, so a
// CP lost right after cannot lose it.
func TestFailedStartAfterAWipeIsRecordedWithTheRecordsEnd(t *testing.T) {
	rt := newRecordedHomeRuntime("stopped")
	rt.startErr = errors.New("no capacity")
	close(rt.gate)
	st, mgr, victim, tn := destroyFixture(t, fixedRuntimeFactory{rt})
	ws := victimWorkspace(t, st, victim, tn)
	openRecord(t, st, ws, store.HomeOpMemberWipe, "clean", nil, "")
	reconcileNow(mgr)
	if recordOpen(t, st, ws.ID) {
		t.Fatal("the record stayed open after a failed start")
	}
	as, ok, err := st.GetWorkspaceAutoStopByMembership(context.Background(), ws.MembershipID)
	if err != nil || !ok || as.Kind != autoStopHomeWipe || !strings.Contains(as.Phase, "no capacity") {
		t.Errorf("auto-stop = %+v %v %v, want the failed start's reason", as, ok, err)
	}
	reconcileNow(mgr)
	if got := rt.rec.log(); strings.Count(got, "wipe:") != 1 {
		t.Errorf("drove %q; the wipe ran again", got)
	}
}
