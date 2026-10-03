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
// an administrator's Clean home, Destroy and purge) is kept as a store.HomeOperation from
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
	runtime.BindHomeTask(rt, runtime.HomeTaskBinding{Token: op.ID, Started: m.recordHomeTask(op.ID)})
	return &op, nil
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
	}
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

// noteHomeWipeFailure keeps why a member's background Recreate or Clean home left the
// workspace stopped, for the workspace payload (homeWipeFailed).
func (m *manager) noteHomeWipeFailure(workspaceID, why string) {
	m.homeWipeFailures.Store(workspaceID, why)
}

// homeOperationOpenErr refuses a start while ws has an unfinished home operation: its task
// may be running, or the operation has yet to apply its own step after it. Asked only where
// the runtime runs home tasks, so no other runtime's start gains a query.
func (m *manager) homeOperationOpenErr(ctx context.Context, ws store.Workspace, rt runtime.Runtime) *apiError {
	if m.store == nil || !runtime.RunsHomeTask(rt) {
		return nil
	}
	_, open, err := m.store.GetHomeOperationByWorkspace(ctx, ws.ID)
	if err != nil {
		return internalErr(err)
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
	rt := m.runtimeFor(ws, "")
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
	log.Printf("home operation %s (%s, ws %s): resuming (task %q)", op.ID, op.Kind, ws.ID, op.TaskARN)
	runtime.BindHomeTask(rt, runtime.HomeTaskBinding{Token: op.ID, TaskARN: op.TaskARN, Resume: true,
		Started: m.recordHomeTask(op.ID)})
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
	if !m.finishHomeOperation(op, err, leftovers) || op.Kind != store.HomeOpMemberWipe {
		return
	}
	if err != nil {
		m.noteHomeWipeFailure(ws.ID, err.Error())
		return
	}
	if aerr := m.startAfterHomeWipe(lease, op); aerr != nil {
		log.Printf("home operation %s: start after the wipe: %s", op.ID, aerr.message)
		m.noteHomeWipeFailure(ws.ID, aerr.message)
	}
}

// startAfterHomeWipe is the member's start that follows a resumed Recreate or Clean home.
// It is what the member asked for, but only while it still can be: a membership removed in
// the meantime is not started, nor is a workspace row that has been replaced.
func (m *manager) startAfterHomeWipe(lease *workspaceLifecycleLeaseGuard, op store.HomeOperation) *apiError {
	ctx := lease.Context()
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
