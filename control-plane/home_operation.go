package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A home operation that runs the stack's home task (ecs: a member's Recreate or Clean home,
// an administrator's Clean home; ecs and ecs-ec2: every Destroy — an administrator's, a
// purge, the golden pipeline's seed and probe) is kept as a store.HomeOperation from
// before RunTask until the step after the task — the member's start, the audit outcome,
// Destroy's row deletion — has been applied (#1544, ADR 0045).
//
// Two processes can try to apply that step: the one that started the operation, and the
// reconciler of any replica once the first one is gone. Neither runs it without the
// member's lifecycle lease, which the starter holds for the whole operation and loses
// within workspaceLifecycleLease of dying; and the step itself starts with the record's
// deletion (store.FinishHomeOperation), in the transaction that writes the audit entry and
// deletes the workspace row, so it is applied once even if both get that far.
//
// The record's id is the task's RunTask clientToken. A reconciler that finds no task ARN
// on the record asks RunTask again with it and gets the task the lost answer named, or
// starts the removal if none was ever placed (runtime.HomeTaskBinding).

// homeOpReconcileEvery is how often each CP looks for operations nobody is finishing. A
// starter that died holds the lease for at most workspaceLifecycleLease more, so a minute
// is the scale on which an interrupted operation resumes.
const homeOpReconcileEvery = time.Minute

// homeOpFinishTimeout bounds the finishing transaction. Its own context: the lease's may
// already be cancelled, and the outcome is owed either way.
const homeOpFinishTimeout = 10 * time.Second

// openHomeOperation writes the record of an operation on ws's home before anything is
// stopped, and binds rt's home task to it. nil, with no record written, where rt runs no
// home task. A record already open refuses the operation: its task may still be running.
func (m *manager) openHomeOperation(ctx context.Context, ws store.Workspace, rt runtime.Runtime, kind string, what runtime.HomeWipe, audit *store.HomeOpAudit) (*store.HomeOperation, error) {
	if !runtime.RunsHomeTask(rt) {
		return nil, nil
	}
	op := store.HomeOperation{ID: store.NewID(), WorkspaceID: ws.ID, MembershipID: ws.MembershipID,
		Kind: kind, Op: string(what), Audit: audit}
	if err := m.store.InsertHomeOperation(ctx, op); err != nil {
		if errors.Is(err, store.ErrHomeOperationOpen) {
			return nil, runtime.ErrHomeTaskInFlight
		}
		return nil, err
	}
	runtime.BindHomeTask(rt, runtime.HomeTaskBinding{Token: op.ID, Started: m.recordHomeTask(op.ID),
		Sending: m.recordHomeTaskSent(op.ID)})
	return &op, nil
}

// recordHomeTaskSent stores when the operation's first RunTask goes out, before it does: a
// later attempt asks again under the same token only while ECS surely still keeps it.
func (m *manager) recordHomeTaskSent(id string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), homeOpFinishTimeout)
		defer cancel()
		return m.store.SetHomeOperationSent(ctx, id, store.NowTS())
	}
}

// homeOpSentAt parses TaskSentAt; zero when no RunTask went out.
func homeOpSentAt(op store.HomeOperation) time.Time {
	t, err := time.Parse(time.RFC3339, op.TaskSentAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// recordHomeTask stores the task's ARN on the record. A failed write only costs the
// reconciler a RunTask under the same token, which names the same task.
func (m *manager) recordHomeTask(id string) func(string) {
	return func(arn string) {
		ctx, cancel := context.WithTimeout(context.Background(), homeOpFinishTimeout)
		defer cancel()
		if err := m.store.SetHomeOperationTask(ctx, id, arn); err != nil {
			log.Printf("home operation %s: record task %s: %v", id, arn, err)
		}
	}
}

// dropHomeOperation removes the record of an operation that was refused before anything
// ran. The refusal is the request's own answer, so nothing else is written.
func (m *manager) dropHomeOperation(op *store.HomeOperation) {
	if op == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), homeOpFinishTimeout)
	defer cancel()
	if _, err := m.store.FinishHomeOperation(ctx, op.ID, store.HomeOperationFinish{}); err != nil {
		log.Printf("home operation %s: drop after a refusal: %v", op.ID, err)
	}
}

// finishHomeOperation applies the database half of the step after op's task — the audit
// outcome, Destroy's row deletion, Clean home's stopped state — with the record's
// deletion, and reports whether this caller claimed it. Whatever else follows (the
// member's start, the failure shown to the member) only the claimant does.
//
// An unresolved outcome (runtime.ErrHomeTaskUnresolved) is no outcome: the record stays
// for the reconciler, and nothing is claimed.
func (m *manager) finishHomeOperation(op store.HomeOperation, err error, leftovers []string) bool {
	if errors.Is(err, runtime.ErrHomeTaskUnresolved) {
		log.Printf("home operation %s (%s, ws %s) is not finished: %v; the reconciler takes it over",
			op.ID, op.Kind, op.WorkspaceID, err)
		return false
	}
	var f store.HomeOperationFinish
	if op.Audit != nil {
		e := op.Audit.Entry(err, leftovers)
		f.Audit = &e
	}
	if err == nil {
		f.DeleteWorkspace = op.Kind == store.HomeOpDestroy
		f.StopWorkspace = op.Kind == store.HomeOpAdminErase
	} else if op.Kind == store.HomeOpMemberWipe {
		f.AutoStop = homeWipeFailure(err.Error())
	}
	return m.closeHomeOperation(op, err, f)
}

// homeWipeFailure is the workspace_auto_stop row that tells the member why their
// Recreate or Clean home left the workspace stopped (homeWipeFailed).
func homeWipeFailure(why string) *store.WorkspaceAutoStop {
	return &store.WorkspaceAutoStop{Kind: autoStopHomeWipe, Phase: why, StoppedAt: store.NowTS()}
}

// closeHomeOperation deletes op's record with f's writes and reports whether this caller
// claimed it.
func (m *manager) closeHomeOperation(op store.HomeOperation, err error, f store.HomeOperationFinish) bool {
	ctx, cancel := context.WithTimeout(context.Background(), homeOpFinishTimeout)
	defer cancel()
	claimed, ferr := m.store.FinishHomeOperation(ctx, op.ID, f)
	if ferr != nil {
		// The record stays; the reconciler runs the operation again (every one of them is
		// idempotent) and writes the outcome then.
		log.Printf("home operation %s (%s, ws %s): finish: %v", op.ID, op.Kind, op.WorkspaceID, ferr)
		return false
	}
	if !claimed {
		return false
	}
	if err != nil {
		log.Printf("home operation %s (%s, ws %s) failed: %v", op.ID, op.Kind, op.WorkspaceID, err)
	}
	if f.DeleteWorkspace {
		m.evictMembershipCache(op.MembershipID)
	}
	return true
}

// homeOpStartKey carries, on the context of the start a member's wipe owes, the id of
// that operation: its own record is the one open record that does not refuse it.
type homeOpStartKey struct{}

// homeOperationOpenErr refuses a start while ws has an unfinished home operation: its task
// may be running, or the operation has yet to apply its own step after it. Asked only where
// the runtime runs home tasks, so no other runtime's start gains a query.
func (m *manager) homeOperationOpenErr(ctx context.Context, ws store.Workspace, rt runtime.Runtime) *apiError {
	if m.store == nil || !runtime.RunsHomeTask(rt) {
		return nil
	}
	cur, open, err := m.store.GetHomeOperationByWorkspace(ctx, ws.ID)
	if err != nil {
		return internalErr(err)
	}
	if own, _ := ctx.Value(homeOpStartKey{}).(string); open && own == cur.ID && cur.Phase == store.HomeOpPhaseStart {
		return nil // the start the operation itself still owes
	}
	if open {
		return &apiError{http.StatusConflict, errCodeHomeOperationInProgress, runtime.ErrHomeTaskInFlight.Error()}
	}
	return nil
}

// runHomeOpReconciler finishes, on every replica, the home operations whose starter is
// gone: once at boot, then every interval.
func (m *manager) runHomeOpReconciler(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		m.reconcileHomeOps(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reconcileHomeOps starts a resume for every open record. Each runs on its own: one takes
// as long as its task, and a lease another process holds makes it return at once.
func (m *manager) reconcileHomeOps(ctx context.Context) {
	ops, err := m.store.ListHomeOperations(ctx)
	if err != nil {
		log.Printf("home operations: list: %v", err)
		return
	}
	for _, op := range ops {
		m.homeOpWG.Add(1)
		go func() {
			defer m.homeOpWG.Done()
			m.resumeHomeOperation(ctx, op)
		}()
	}
}

// resumeHomeOperation finishes op when nobody else is: it takes the member's lifecycle lease
// (held → somebody is on it), runs the operation again bound to the record, and applies the
// step after it. Running it again is safe because each operation is: the drain wait and
// RunTask under the same token find the task already there, and every removal is
// idempotent.
func (m *manager) resumeHomeOperation(ctx context.Context, op store.HomeOperation) {
	ctx, cancel := context.WithTimeout(ctx, homeTaskBudget)
	defer cancel()
	ws, found, err := m.store.GetWorkspaceByMembership(ctx, op.MembershipID)
	if err != nil {
		log.Printf("home operation %s: read workspace: %v", op.ID, err)
		return
	}
	if !found || ws.ID != op.WorkspaceID {
		m.finishHomeOperation(op, errors.New("the workspace no longer exists; whether its home task ran is unknown"), nil)
		return
	}
	lock := m.startLockFor(ws.ID)
	lock.Lock()
	lease, err := acquireWorkspaceLifecycleLease(ctx, m.store, op.MembershipID)
	lock.Unlock()
	if err != nil {
		if !errors.Is(err, store.ErrSessionShareOwnerBusy) {
			log.Printf("home operation %s: lease: %v", op.ID, err)
		}
		return
	}
	defer lease.Close()
	rt := m.runtimeFor(ws, noSecretKeys)
	releaseFence, err := m.acquireWorkspaceOperationFence(lease.Context(), ws.ID, rt)
	if err != nil {
		log.Printf("home operation %s: fence: %v", op.ID, err)
		return
	}
	defer releaseFence()
	// Re-read under the lease: the starter may have finished it between the listing and now.
	cur, open, err := m.store.GetHomeOperationByWorkspace(ctx, ws.ID)
	if err != nil || !open || cur.ID != op.ID {
		return
	}
	op = cur
	log.Printf("home operation %s (%s, %s, ws %s): resuming (task %q)", op.ID, op.Kind, op.Phase, ws.ID, op.TaskARN)
	if op.Phase == store.HomeOpPhaseStart {
		// The wipe is done and recorded as done; only the start it owes is left.
		m.startAfterMemberWipe(lease, op)
		return
	}
	runtime.BindHomeTask(rt, runtime.HomeTaskBinding{Token: op.ID, TaskARN: op.TaskARN, Resume: true,
		SentAt: homeOpSentAt(op), Started: m.recordHomeTask(op.ID), Sending: m.recordHomeTaskSent(op.ID)})
	var leftovers []string
	switch op.Kind {
	case store.HomeOpMemberWipe:
		unqueue := runtime.QueueHomeWipe(rt)
		err = runtime.WipeHome(lease.Context(), rt, runtime.HomeWipe(op.Op))
		unqueue()
	case store.HomeOpAdminErase:
		err = runtime.EraseHome(lease.Context(), rt)
	case store.HomeOpDestroy:
		leftovers, err = runtime.DestroyRuntime(lease.Context(), rt)
	default:
		err = errors.New("unknown home operation kind " + op.Kind)
	}
	if op.Kind == store.HomeOpMemberWipe {
		m.finishMemberWipe(lease, op, err)
		return
	}
	m.finishHomeOperation(op, err, leftovers)
}

// finishMemberWipe applies what follows a member's wipe task, for its starter and for the
// reconciler alike. A failure ends the record with the reason in one write. A success
// first moves the record to HomeOpPhaseStart — durably, so a CP lost before the start leaves
// the reconciler a start to make, never the wipe to run again — and only then starts.
func (m *manager) finishMemberWipe(lease *workspaceLifecycleLeaseGuard, op store.HomeOperation, err error) {
	if err != nil {
		m.finishHomeOperation(op, err, nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), homeOpFinishTimeout)
	claimed, aerr := m.store.AdvanceHomeOperation(ctx, op.ID, store.HomeOpPhaseStart)
	cancel()
	if aerr != nil {
		log.Printf("home operation %s: record the finished wipe: %v; the reconciler takes it over", op.ID, aerr)
		return
	}
	if !claimed {
		return
	}
	op.Phase = store.HomeOpPhaseStart
	m.startAfterMemberWipe(lease, op)
}

// startAfterMemberWipe makes the start a finished member wipe owes and then ends the
// record: with no more to write when it started (or must not: the member is gone), with
// the reason when it failed. A start cut off by losing the lease leaves the record in
// HomeOpPhaseStart for the reconciler.
func (m *manager) startAfterMemberWipe(lease *workspaceLifecycleLeaseGuard, op store.HomeOperation) {
	aerr := m.startAfterHomeWipe(lease, op)
	var f store.HomeOperationFinish
	if aerr != nil {
		if aerr.code == "workspace_operation_in_progress" {
			log.Printf("home operation %s: the start after the wipe lost its lease; the reconciler retries it", op.ID)
			return
		}
		log.Printf("home operation %s: start after the wipe: %s", op.ID, aerr.message)
		f.AutoStop = homeWipeFailure("the home was cleared, but the workspace did not start: " + aerr.message)
	}
	m.closeHomeOperation(op, nil, f)
}

// startAfterHomeWipe is the member's start that follows a resumed Recreate or Clean home.
// It is what the member asked for, but only while it still can be: a membership removed in
// the meantime is not started, nor is a workspace row that has been replaced.
func (m *manager) startAfterHomeWipe(lease *workspaceLifecycleLeaseGuard, op store.HomeOperation) *apiError {
	ctx := context.WithValue(lease.Context(), homeOpStartKey{}, op.ID)
	identityID, active, err := m.store.IdentityIDForMembership(ctx, op.MembershipID)
	if err != nil {
		return internalErr(err)
	}
	if !active {
		log.Printf("home operation %s: membership %s is no longer active; not starting", op.ID, op.MembershipID)
		return nil
	}
	res, aerr := m.resolveByMembership(ctx, identityID, op.MembershipID)
	if aerr != nil {
		if aerr.code == "forbidden_tenant" {
			return nil
		}
		return aerr
	}
	if res.ws.ID != op.WorkspaceID {
		return nil
	}
	lock := m.startLockFor(res.ws.ID)
	lock.Lock()
	defer lock.Unlock()
	return newWorkspaceAPI(m, true).ensureWorkspaceStartedRTLocked(ctx, res, res.rt, lease)
}
