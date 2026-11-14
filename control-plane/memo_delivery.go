package main

// Message queues, step 1c (ADR 0137 section 6, issue #4163): a delivery is one fire of a queue.
// "Fire now" reserves a slot and claims the messages in one transaction, the scheduler's tick
// sends the claimed deliveries, reconciles the ones a restart or a lost answer left behind, and
// closes the open ones whose session is done.
//
// Two rules hold every step here together:
//   - A slot is reserved inside store.MemoTx, which takes the membership row's lock first. A
//     queue belongs to one member, so the queue's slots and the member-wide limit are both
//     counted and taken behind that one lock, on SQLite and on Postgres, by every Control Plane.
//   - Every move of a delivery's state is a conditional write on the delivery's own row
//     (store MoveDelivery). Only the writer whose write succeeded acts on the move. Elapsed time
//     alone never decides that an old worker will not send: it only decides who tries the write.
//
// Nothing is ever fired twice. A create that got no answer keeps its slot until the Agent's
// abandon answer settles it, and a timeout or a stopped workspace closes nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Error codes of a fire. Kept out of errcodes.go like the codes of memo_queue.go, until the
// Console slice adds their catalog entries.
const (
	errCodeMemoQueueNothingPending = "memo_queue_nothing_pending"
	errCodeMemoQueueNoSlot         = "memo_queue_no_slot"
	errCodeMemoQueueMemberLimit    = "memo_queue_member_limit"
	errCodeMemoQueueBadRepo        = "memo_queue_bad_repo"
	errCodeMemoQueueItemInvalid    = "memo_queue_item_invalid"
)

const (
	// memoQueueMemberLimit bounds the deliveries that are not closed across all of a member's
	// queues (ADR 0137 section 7, provisional).
	memoQueueMemberLimit = 4
	// memoDeliveryListMax is how many deliveries GET .../deliveries answers.
	memoDeliveryListMax = 50
	// memoQueueSendsPerTick bounds the queue sends one tick starts. They share the tick's
	// schedulerMaxConcurrentFires with the schedules, and the tick waits for all of them, so
	// this keeps half the fires for schedules whatever the queues hold.
	memoQueueSendsPerTick = schedulerMaxConcurrentFires / 2
	// memoSettleBudget bounds the reconciliation and closing pass of one tick, and
	// memoAgentCallWait each Agent call in it. Both run before the tick's fires start.
	memoSettleBudget  = 60 * time.Second
	memoAgentCallWait = 15 * time.Second
	// memoDeliveryWriteWait bounds a delivery write made where no caller deadline applies (a
	// send after its Agent call). A write that times out decides nothing: the next tick
	// reconciles the delivery from its state.
	memoDeliveryWriteWait = 30 * time.Second
	// memoDeliverySendMargin is the part of a fire's time budget beyond the wait for the
	// Agent: the reservation, the create (agentHTTPClient's two minutes) and slack.
	memoDeliverySendMargin = 10 * time.Minute
)

// The Agent's bounds on one item of a queue delivery (workspace/agent sessionx
// checkQueueDelivery). A message past them is refused by the Agent whole, so it is refused at
// the press instead, where the member can still edit it.
const (
	memoDeliveryItemBodyMax    = 64 << 10
	memoDeliveryAttachmentsMax = 20
	memoDeliveryPathMax        = 4096
)

// memoDeliveryItemProblem says why the Agent would refuse m as an item, "" when it would not.
func memoDeliveryItemProblem(m store.Memo) string {
	if len(m.Body) > memoDeliveryItemBodyMax {
		return fmt.Sprintf("its text is over %d bytes", memoDeliveryItemBodyMax)
	}
	if len(m.RefPath) > memoDeliveryPathMax {
		return fmt.Sprintf("its file path is over %d bytes", memoDeliveryPathMax)
	}
	switch memoEffectiveProvenance(m) {
	case store.MemoProvenanceMember, store.MemoProvenanceWorkspace, store.MemoProvenanceMCP:
	default:
		return "its origin cannot be sent to a session"
	}
	atts := parseMemoAttachments(m.Attachments)
	if strings.TrimSpace(m.Attachments) != "" && atts == nil {
		return "its attachments cannot be read"
	}
	if len(atts) > memoDeliveryAttachmentsMax {
		return fmt.Sprintf("it has more than %d attachments", memoDeliveryAttachmentsMax)
	}
	for _, a := range atts {
		if len(a.Path) > memoDeliveryPathMax || len(a.Name) > memoDeliveryPathMax || !filepath.IsAbs(a.Path) {
			return "an attachment's path or name cannot be sent"
		}
	}
	return ""
}

// memoDeliveryKey is the durable Agent idempotency key of a delivery's create.
func memoDeliveryKey(deliveryID string) string { return "q_" + deliveryID }

// memoDeliveryDTO is a delivery on the wire. MessageIDs is never null.
type memoDeliveryDTO struct {
	ID         string   `json:"id"`
	State      string   `json:"state"`
	Outcome    string   `json:"outcome"`
	Session    string   `json:"session"`
	MessageIDs []string `json:"messageIds"`
	CreatedAt  string   `json:"createdAt"`
	SentAt     string   `json:"sentAt"`
	ClosedAt   string   `json:"closedAt"`
}

func memoDeliveryToDTO(d store.MemoDelivery) memoDeliveryDTO {
	ids := d.MessageIDs
	if ids == nil {
		ids = []string{}
	}
	return memoDeliveryDTO{ID: d.ID, State: d.State, Outcome: d.Outcome, Session: d.Session, MessageIDs: ids,
		CreatedAt: d.CreatedAt, SentAt: d.SentAt, ClosedAt: d.ClosedAt}
}

// memoQueueRepoDir is the working copy a queue's sessions start from, relative to the
// workspace's home (the Agent joins a relative dir onto it). The repo of a queue is a folder
// name under ~/repos, and "" (the common bucket) has none. ok is false for a name that is not
// one folder.
func memoQueueRepoDir(repo string) (string, bool) {
	if repo == "" {
		return "", true
	}
	if repo == "." || repo == ".." || strings.ContainsAny(repo, `/\`) || filepath.Base(repo) != repo {
		return "", false
	}
	return "repos/" + repo, true
}

// --- Fire now: Console only ------------------------------------------------------------------

// fire (POST /api/memo-queues/{categoryId}/fire) reserves a slot of the queue and claims its
// oldest pending messages in one transaction, and answers 202 with the delivery. The send
// happens on the scheduler's next tick: a cold wake does not fit in a request. enabled gates
// the automatic fires of a later step, not this press. An instruction is required.
func (a memoAPI) fire(w http.ResponseWriter, r *http.Request, res *resolved) {
	d, aerr := memoFireNow(r.Context(), a.mgr, res, r.PathValue("categoryId"))
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusAccepted, memoDeliveryToDTO(d))
}

// memoFireNow is the reservation and the claim. The running-session count of the quota is read
// before the transaction (an Agent call does not belong under the membership lock), and the
// deliveries not yet sent are added to it inside, where they cannot change.
func memoFireNow(ctx context.Context, mgr *manager, res *resolved, categoryID string) (store.MemoDelivery, *apiError) {
	on, err := mgr.store.GetTenantMemoQueues(ctx, res.mv.TenantID)
	if err != nil {
		return store.MemoDelivery{}, internalErr(err)
	}
	if !on {
		return store.MemoDelivery{}, &apiError{http.StatusConflict, errCodeMemoQueuesDisabled, "your tenant has not turned memo queues on"}
	}
	running, limit := -1, 0
	if !mgr.nativeRuntime {
		limit = mgr.sessionLimit(ctx, res)
		if limit > 0 && res.rt.State(ctx) == "running" {
			if n, err := mgr.countSessions(ctx, res.rt); err == nil {
				running = n
			}
		}
	}
	var out store.MemoDelivery
	var refusal *apiError
	err = mgr.store.MemoTx(ctx, res.mv.MembershipID, func(tx store.MemoTxOps) error {
		d, aerr, err := memoReserveAndClaim(tx, categoryID, running, limit)
		if err != nil {
			return err
		}
		// A refusal still commits: the messages it put back to held stay held.
		out, refusal = d, aerr
		return nil
	})
	if err != nil {
		return store.MemoDelivery{}, memoTxResult(err)
	}
	return out, refusal
}

// memoReserveAndClaim runs inside a MemoTx. running is the member's running metered sessions
// (-1 when unknown, then the quota is not checked, as the Console's create does), limit the
// max_sessions cap (0 = none).
func memoReserveAndClaim(tx store.MemoTxOps, categoryID string, running, limit int) (store.MemoDelivery, *apiError, error) {
	none := store.MemoDelivery{}
	c, ok, err := tx.GetCategory(categoryID)
	if err != nil {
		return none, nil, err
	}
	var q store.MemoQueue
	if ok {
		q, ok, err = tx.GetQueue(c.ID)
		if err != nil {
			return none, nil, err
		}
	}
	if !ok {
		return none, &apiError{http.StatusNotFound, "not_found", "queue not found"}, nil
	}
	if strings.TrimSpace(q.Instruction) == "" {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueNoInstruction, "a queue without an instruction cannot fire"}, nil
	}
	if _, ok := memoQueueRepoDir(c.Repo); !ok {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueBadRepo, "the queue's repository is not a folder under repos"}, nil
	}
	inQueue, inMember, err := tx.CountLiveDeliveries(c.ID)
	if err != nil {
		return none, nil, err
	}
	if inQueue >= q.MaxActive {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueNoSlot,
			fmt.Sprintf("every slot of this queue is taken (%d of %d)", inQueue, q.MaxActive)}, nil
	}
	if inMember >= memoQueueMemberLimit {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueMemberLimit,
			fmt.Sprintf("your queues already run %d deliveries, the most at once", inMember)}, nil
	}
	if limit > 0 {
		// The deliveries not yet sent have no session to count yet. With the running count
		// unknown (the workspace is not running) they are still a lower bound.
		unsent, err := tx.CountUnsentDeliveries()
		if err != nil {
			return none, nil, err
		}
		if running < 0 {
			running = 0
		}
		if running+unsent >= limit {
			return none, &apiError{http.StatusTooManyRequests, errCodeQuotaSessions,
				fmt.Sprintf("concurrent session limit reached (%d running, %d starting, max %d)", running, unsent, limit)}, nil
		}
	}
	rows, err := tx.ListMemosIn(c.Repo, c.Name)
	if err != nil {
		return none, nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		// created_at has a second's precision: two messages added in one second are told
		// apart by the position each was appended at.
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt < rows[j].CreatedAt
		}
		if rows[i].Position != rows[j].Position {
			return rows[i].Position < rows[j].Position
		}
		return rows[i].ID < rows[j].ID
	})
	now := store.DeliveryTS(time.Now())
	d := store.MemoDelivery{ID: store.NewID(), QueueID: c.ID, State: store.DeliveryClaimed, MessageIDs: []string{},
		CreatedAt: now, UpdatedAt: now}
	for _, m := range rows {
		if len(d.MessageIDs) >= q.Batch {
			break
		}
		if m.State != store.MemoStatePending || m.SentAt != "" || m.SendToken != "" || m.DeliveryID != "" {
			continue
		}
		// A release is bound to the content it showed. One that no longer matches goes back
		// for a new press instead of being fired.
		if m.ReleaseHash != memoContentHash(m) {
			m.State, m.ReleaseHash, m.ReleasedAt = store.MemoStateHeld, "", ""
			if err := tx.WriteMemo(m); err != nil {
				return none, nil, err
			}
			continue
		}
		if why := memoDeliveryItemProblem(m); why != "" {
			return none, &apiError{http.StatusConflict, errCodeMemoQueueItemInvalid,
				fmt.Sprintf("message %s cannot be sent to a session: %s", m.ID, why)}, nil
		}
		won, err := tx.ClaimMemo(m, d.ID)
		if err != nil {
			return none, nil, err
		}
		if won {
			d.MessageIDs = append(d.MessageIDs, m.ID)
		}
	}
	if len(d.MessageIDs) == 0 {
		return none, &apiError{http.StatusConflict, errCodeMemoQueueNothingPending, "nothing in this queue is waiting to be sent"}, nil
	}
	if err := tx.InsertDelivery(d); err != nil {
		return none, nil, err
	}
	return d, nil, nil
}

// listDeliveries (GET /api/memo-queues/{categoryId}/deliveries): a queue's deliveries, newest
// first, at most memoDeliveryListMax.
func (a memoAPI) listDeliveries(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	ds, err := a.store.ListMemoDeliveries(r.Context(), mv.MembershipID, r.PathValue("categoryId"), memoDeliveryListMax)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]memoDeliveryDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, memoDeliveryToDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// --- The tick: send, reconcile, close ------------------------------------------------------

// memoQueueTick is the queue work of a scheduler tick. settle runs first: it reconciles and
// closes, and answers the claimed deliveries this tick should send. send runs each of them in
// one of the tick's fire slots.
type memoQueueTick interface {
	settle(ctx context.Context, now time.Time) []store.MemoDelivery
	send(ctx context.Context, d store.MemoDelivery)
}

// withQueue arms the scheduler's queue work.
func (sc *scheduler) withQueue(q memoQueueTick) *scheduler {
	sc.queue = q
	return sc
}

// memoQueueRunner sends, reconciles and closes deliveries. It wakes and waits the way a
// schedule's fire does, through the wakeFirer.
type memoQueueRunner struct {
	mgr *manager
	f   *wakeFirer
	// budget is a fire's own time budget: a claimed or sending delivery older than it is
	// reconciled. It is above everything a live send waits for.
	budget time.Duration
	// settleBudget bounds one tick's settle pass (memoSettleBudget).
	settleBudget time.Duration
}

func newMemoQueueRunner(mgr *manager, f *wakeFirer) *memoQueueRunner {
	ready := f.readyTimeout
	if ready <= 0 {
		ready = 5 * time.Minute
	}
	return &memoQueueRunner{mgr: mgr, f: f, budget: ready + memoDeliverySendMargin, settleBudget: memoSettleBudget}
}

// memoMemberView is what settle knows of one member's workspace in this tick.
type memoMemberView struct {
	res *resolved
	// live is the Agent's session list, and liveOK whether it answered. mirrorOK is set
	// instead when the workspace is stopped, and mirror then holds the DB mirror's rows. With
	// neither, nothing is known of the sessions this tick: a workspace that runs but does not
	// answer, or one that is starting or stopping, proves no session done.
	live     []sessionWire
	liveOK   bool
	mirror   []sessionWire
	mirrorOK bool
}

func (r *memoQueueRunner) settle(ctx context.Context, now time.Time) []store.MemoDelivery {
	ctx, cancel := context.WithTimeout(ctx, r.settleBudget)
	defer cancel()
	ds, err := r.mgr.store.ListLiveMemoDeliveries(ctx)
	if err != nil {
		log.Printf("memo queue: list deliveries: %v", err)
		return nil
	}
	stale := store.DeliveryTS(now.Add(-r.budget))
	views := map[string]*memoMemberView{}
	var sends []store.MemoDelivery
	for _, d := range ds {
		if ctx.Err() != nil {
			break
		}
		switch d.State {
		case store.DeliveryClaimed:
			if d.CreatedAt < stale {
				// The worker that claimed it is gone or past its budget. If it still moves the
				// delivery to sending, it wins and this write loses: nothing is returned twice.
				r.closeNotSent(ctx, d, store.DeliveryClaimed)
				continue
			}
			if len(sends) < memoQueueSendsPerTick {
				sends = append(sends, d)
			}
		case store.DeliverySending:
			if d.SentAt >= stale {
				continue
			}
			if v := r.view(ctx, views, d.MembershipID); v != nil && v.liveOK {
				r.reconcileSending(ctx, v, d)
			}
			// While the workspace is stopped, or the Agent does not answer, the delivery keeps
			// its slot: its session may exist.
		case store.DeliveryOpen:
			if v := r.view(ctx, views, d.MembershipID); v != nil {
				r.closeIfDone(ctx, v, d, now)
			}
		}
	}
	return sends
}

// view resolves a member's workspace once per tick, without waking it.
func (r *memoQueueRunner) view(ctx context.Context, views map[string]*memoMemberView, membershipID string) *memoMemberView {
	if v, ok := views[membershipID]; ok {
		return v
	}
	views[membershipID] = nil
	identityID, ok, err := r.mgr.store.IdentityIDForMembership(ctx, membershipID)
	if err != nil || !ok {
		return nil
	}
	res, aerr := r.mgr.resolveByMembership(ctx, identityID, membershipID)
	if aerr != nil {
		return nil
	}
	v := &memoMemberView{res: res}
	switch res.rt.State(ctx) {
	case "running":
		cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
		v.live, err = r.mgr.agentSessions(cctx, res.rt)
		cancel()
		v.liveOK = err == nil
	case "stopped":
		rows, err := r.mgr.store.ListSessions(ctx, res.ws.ID)
		if err != nil {
			break
		}
		v.mirrorOK = true
		for _, s := range rows {
			// A row the mirror still records as running says nothing about an end.
			v.mirror = append(v.mirror, sessionWire{Name: s.Name, Kind: s.Kind, Carried: s.Carried, Alive: s.State == "running"})
		}
	}
	views[membershipID] = v
	return v
}

// reconcileSending settles a sending delivery past its budget from the Agent's abandon answer.
// A lookup would not do: it answers not_found both for a key only reserved and for one it has
// no record of, and a create under a reserved key may still begin. Abandon settles the key
// under the lock a create begins under.
func (r *memoQueueRunner) reconcileSending(ctx context.Context, v *memoMemberView, d store.MemoDelivery) {
	status, body, ok := r.abandon(ctx, v.res, d.ID)
	if !ok {
		return // no answer, or not the Agent's: nothing is decided
	}
	switch {
	case status == http.StatusOK:
		if name := sessionNameOf(body); name != "" {
			r.open(ctx, d, name)
		}
	case status == http.StatusAccepted:
		// A create is in flight: the slot stays, and the next tick asks again.
	case status == http.StatusNotFound && agentErrCode(body) == "not_created":
		r.closeNotSent(ctx, d, store.DeliverySending)
	case status == http.StatusServiceUnavailable && agentErrCode(body) == "create_outcome_unknown":
		// No record, an unreadable one, or a create cut off by a restart: the session may
		// exist. Look for it by the delivery it carries, in a list read after the answer.
		cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
		list, err := r.mgr.agentSessions(cctx, v.res.rt)
		cancel()
		if err != nil {
			return
		}
		for _, s := range list {
			if s.QueueDelivery == d.ID && s.Name != "" {
				r.open(ctx, d, s.Name)
				return
			}
		}
		r.closeUnknown(ctx, d)
	}
}

// abandon asks the Agent to settle the delivery's key. ok is false when there is no answer, or
// one that does not name the key: a gateway's or proxy's own error proves nothing.
func (r *memoQueueRunner) abandon(ctx context.Context, res *resolved, deliveryID string) (int, []byte, bool) {
	key := memoDeliveryKey(deliveryID)
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		res.rt.Endpoint()+"/sessions-idempotency/"+url.PathEscape(key)+"/abandon", nil)
	if err != nil {
		return 0, nil, false
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		return 0, nil, false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.Header.Get(createKeyHeader) != key {
		return 0, nil, false
	}
	return resp.StatusCode, b, true
}

func sessionNameOf(body []byte) string {
	var s struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &s)
	return s.Name
}

// closeIfDone closes an open delivery whose session is done (ADR 0137 section 6, the part that
// needs no report from the Agent): archived, deleted, stopped with no interaction carried, or
// idle on two consecutive ticks. A session where a machine is working, or a person has to act,
// keeps it open. The rows of other states (limited, unknown) keep it open too.
func (r *memoQueueRunner) closeIfDone(ctx context.Context, v *memoMemberView, d store.MemoDelivery, now time.Time) {
	if !v.liveOK && !v.mirrorOK {
		// No observation this tick: the idle ticks are no longer consecutive.
		r.markIdle(ctx, d, 0)
		return
	}
	rows := v.live
	if !v.liveOK {
		rows = v.mirror
	}
	var row *sessionWire
	for i := range rows {
		if rows[i].Name == d.Session {
			row = &rows[i]
			break
		}
	}
	switch {
	case row == nil:
		// Only the Agent's own list proves a deletion. The mirror may predate the session.
		if v.liveOK {
			r.closeOpen(ctx, d, store.DeliveryAborted)
		}
	case row.Archived:
		r.closeOpen(ctx, d, store.DeliveryAborted)
	case !row.Alive:
		if row.Carried == "" {
			r.closeOpen(ctx, d, store.DeliveryClean)
		} else {
			r.markIdle(ctx, d, 0)
		}
	case row.State == stateIdle && sessionActivityAt(*row, now, 0) == activityIdleWait:
		if d.IdleTicks >= 1 {
			r.closeOpen(ctx, d, store.DeliveryClean)
		} else {
			r.markIdle(ctx, d, 1)
		}
	default:
		r.markIdle(ctx, d, 0)
	}
}

func (r *memoQueueRunner) markIdle(ctx context.Context, d store.MemoDelivery, ticks int) {
	if d.IdleTicks == ticks {
		return
	}
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	if _, err := r.mgr.store.MarkMemoDeliveryIdle(wctx, d.ID, ticks); err != nil {
		log.Printf("memo queue: delivery %s: idle mark: %v", d.ID, err)
	}
}

// --- The settles: each a conditional write on the delivery's state ---------------------------

// memoWriteCtx is the context of a delivery write. A caller's deadline (the settle pass's) is
// kept: a write that waits on another Control Plane's membership lock must not hold the tick,
// and every schedule behind it, past its budget. A caller without one (a send, whose outcome
// has to be recorded even if the tick is cancelled) gets memoDeliveryWriteWait. Either way a
// write that does not finish decides nothing, and the next tick reconciles from the state.
func memoWriteCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), memoDeliveryWriteWait)
}

// memoDeliveryMove moves a delivery and, only when that write won, applies then to its
// messages, in one MemoTx. It reports whether the move won.
func memoDeliveryMove(ctx context.Context, st store.MemoStore, d store.MemoDelivery, from, to, session, outcome string,
	then func(tx store.MemoTxOps, at string) error) (bool, error) {
	won := false
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	err := st.MemoTx(wctx, d.MembershipID, func(tx store.MemoTxOps) error {
		at := store.DeliveryTS(time.Now())
		ok, err := tx.MoveDelivery(d.ID, from, to, session, outcome, at)
		if err != nil || !ok {
			return err
		}
		won = true
		if then != nil {
			return then(tx, at)
		}
		return nil
	})
	return won && err == nil, err
}

// closeNotSent closes a delivery that provably sent nothing: its messages return to pending.
func (r *memoQueueRunner) closeNotSent(ctx context.Context, d store.MemoDelivery, from string) {
	if _, err := memoDeliveryMove(ctx, r.mgr.store, d, from, store.DeliveryClosed, "", store.DeliveryNotSent,
		func(tx store.MemoTxOps, _ string) error { return tx.ReturnDeliveryMessages(d.ID) }); err != nil {
		log.Printf("memo queue: delivery %s: close as not sent: %v", d.ID, err)
	}
}

// closeUnknown closes a delivery whose create may or may not have happened and whose session
// cannot be found: its messages become unconfirmed, shown to the person and never fired again
// by themselves. A duplicate would do the work twice.
func (r *memoQueueRunner) closeUnknown(ctx context.Context, d store.MemoDelivery) {
	if _, err := memoDeliveryMove(ctx, r.mgr.store, d, store.DeliverySending, store.DeliveryClosed, "", store.DeliveryUnknown,
		func(tx store.MemoTxOps, _ string) error { return tx.UnconfirmDeliveryMessages(d.ID) }); err != nil {
		log.Printf("memo queue: delivery %s: close as unknown: %v", d.ID, err)
	}
}

// open records the session a sending delivery created: its messages are delivered.
func (r *memoQueueRunner) open(ctx context.Context, d store.MemoDelivery, session string) {
	if _, err := memoDeliveryMove(ctx, r.mgr.store, d, store.DeliverySending, store.DeliveryOpen, session, "",
		func(tx store.MemoTxOps, at string) error { return tx.DeliverDeliveryMessages(d.ID, at) }); err != nil {
		log.Printf("memo queue: delivery %s: open with %s: %v", d.ID, session, err)
	}
}

func (r *memoQueueRunner) closeOpen(ctx context.Context, d store.MemoDelivery, outcome string) {
	if _, err := memoDeliveryMove(ctx, r.mgr.store, d, store.DeliveryOpen, store.DeliveryClosed, "", outcome, nil); err != nil {
		log.Printf("memo queue: delivery %s: close: %v", d.ID, err)
	}
}

// --- Send -------------------------------------------------------------------------------------

// memoSendPlan is what a claimed delivery sends: the queue, its category and the messages.
type memoSendPlan struct {
	q        store.MemoQueue
	c        store.MemoCategory
	messages []store.Memo
}

// send runs one claimed delivery: wake and wait as a schedule does, reserve the durable key,
// move the delivery to sending, create the session. Every failure before the move to sending is
// provably not sent, and closes the delivery as such. After it, only the Agent's own refusal
// does: anything else leaves it sending for reconciliation.
func (r *memoQueueRunner) send(ctx context.Context, d store.MemoDelivery) {
	notSent := func(why string) {
		log.Printf("memo queue: delivery %s not sent: %s", d.ID, why)
		r.closeNotSent(ctx, d, store.DeliveryClaimed)
	}
	plan, ok, err := r.plan(ctx, d)
	if err != nil {
		log.Printf("memo queue: delivery %s: %v", d.ID, err)
		return
	}
	if !ok {
		return // no longer claimed: another worker or reconciliation has it
	}
	if len(plan.messages) == 0 {
		notSent("no message is claimed by it")
		return
	}
	kind := scheduleInjectKind(plan.q.AgentKind)
	if err := agentkinds.CheckCreateKind(kind); err != nil {
		notSent("agent kind: " + err.Error())
		return
	}
	if aerr := providedRouteCreateRefusal(ctx, d.MembershipID, plan.q.Route); aerr != nil {
		notSent("route: " + aerr.message)
		return
	}
	identityID, ok, err := r.mgr.store.IdentityIDForMembership(ctx, d.MembershipID)
	if err != nil || !ok {
		notSent("the member is gone")
		return
	}
	res, aerr := r.mgr.resolveByMembership(ctx, identityID, d.MembershipID)
	if aerr != nil {
		notSent("resolve membership: " + aerr.message)
		return
	}
	if on, err := r.mgr.store.GetTenantMemoQueues(ctx, res.mv.TenantID); err != nil || !on {
		notSent("memo queues are off for the tenant")
		return
	}
	if !r.wake(ctx, res, d) {
		return
	}
	// The quota: the running sessions as the Agent counts them now, and, in the move to
	// sending below, every delivery whose session the count may not show yet.
	running, lim, countedAt := 0, 0, store.DeliveryTS(time.Now())
	if !r.mgr.nativeRuntime {
		lim = r.mgr.sessionLimit(ctx, res)
	}
	if lim > 0 {
		n, err := r.mgr.countSessions(ctx, res.rt)
		if err != nil {
			notSent("count sessions: " + err.Error())
			return
		}
		running = n
	}
	if err := reserveDeliveryCreate(ctx, res, d.ID); err != nil {
		notSent("reserve the key: " + err.Error())
		return
	}
	won, over, err := r.startSending(ctx, d, running, lim, countedAt)
	if err != nil || !won {
		// Lost to reconciliation (or another Control Plane's send): that writer owns it now.
		return
	}
	if over != "" {
		log.Printf("memo queue: delivery %s not sent: %s", d.ID, over)
		return
	}
	status, h, body, err := r.create(ctx, res, memoDeliveryCreateBody(d, plan))
	if err != nil {
		log.Printf("memo queue: delivery %s: create got no answer (%v); reconciliation settles it", d.ID, err)
		return
	}
	key := memoDeliveryKey(d.ID)
	outcome := ""
	if h.Get(createKeyHeader) == key {
		outcome = h.Get(createOutcomeHeader)
	}
	switch {
	case outcome == "created" && status >= 200 && status < 300 && sessionNameOf(body) != "":
		r.open(ctx, d, sessionNameOf(body))
	case outcome == "refused" && (status < 200 || status >= 300):
		log.Printf("memo queue: delivery %s: the Agent refused the create (%d %s)", d.ID, status, strings.TrimSpace(string(body)))
		r.closeNotSent(ctx, d, store.DeliverySending)
	default:
		// An answer that does not carry the create's outcome: the Agent's own refusal of the
		// body before it took the key, or a proxy's error. Neither proves anything, but the
		// abandon answer does, now: not_created closes it as not sent, a create that began is
		// reported, and anything else is left to reconciliation.
		log.Printf("memo queue: delivery %s: create answered %d (outcome %q); asking the Agent", d.ID, status, outcome)
		cur := d
		cur.State = store.DeliverySending
		r.reconcileSending(ctx, &memoMemberView{res: res, liveOK: true}, cur)
	}
}

// startSending moves a claimed delivery to sending, with the quota check in the same
// transaction: running sessions counted at countedAt, plus every other delivery of the member
// whose session that count may miss (sending, or sent or opened since). The membership lock
// orders two sends of one member, on any Control Plane, so the second sees the first. won is
// whether this send owns the delivery; over is set when the quota closed it as not sent.
func (r *memoQueueRunner) startSending(ctx context.Context, d store.MemoDelivery, running, lim int, countedAt string) (won bool, over string, err error) {
	wctx, cancel := memoWriteCtx(ctx)
	defer cancel()
	err = r.mgr.store.MemoTx(wctx, d.MembershipID, func(tx store.MemoTxOps) error {
		at := store.DeliveryTS(time.Now())
		to, outcome := store.DeliverySending, ""
		if lim > 0 {
			starting, err := tx.CountDeliveriesStartedSince(countedAt, d.ID)
			if err != nil {
				return err
			}
			if running+starting >= lim {
				to, outcome = store.DeliveryClosed, store.DeliveryNotSent
				over = fmt.Sprintf("concurrent session limit reached (%d running, %d starting, max %d)", running, starting, lim)
			}
		}
		ok, err := tx.MoveDelivery(d.ID, store.DeliveryClaimed, to, "", outcome, at)
		if err != nil || !ok {
			return err
		}
		won = true
		if to == store.DeliveryClosed {
			return tx.ReturnDeliveryMessages(d.ID)
		}
		return nil
	})
	return won && err == nil, over, err
}

// plan reads what a claimed delivery sends. ok is false when it is no longer claimed.
func (r *memoQueueRunner) plan(ctx context.Context, d store.MemoDelivery) (memoSendPlan, bool, error) {
	var p memoSendPlan
	claimed := false
	err := r.mgr.store.MemoTx(ctx, d.MembershipID, func(tx store.MemoTxOps) error {
		cur, ok, err := tx.GetDelivery(d.ID)
		if err != nil || !ok || cur.State != store.DeliveryClaimed {
			return err
		}
		claimed = true
		q, ok, err := tx.GetQueue(cur.QueueID)
		if err != nil || !ok {
			return err
		}
		c, ok, err := tx.GetCategory(cur.QueueID)
		if err != nil || !ok {
			return err
		}
		p.q, p.c = q, c
		rows, err := tx.ListMemosIn(c.Repo, c.Name)
		if err != nil {
			return err
		}
		byID := map[string]store.Memo{}
		for _, m := range rows {
			if m.DeliveryID == d.ID && m.State == store.MemoStateClaimed {
				byID[m.ID] = m
			}
		}
		for _, id := range cur.MessageIDs {
			if m, ok := byID[id]; ok {
				p.messages = append(p.messages, m)
			}
		}
		return nil
	})
	return p, claimed, err
}

// wake brings the workspace up and waits for the Agent, the schedule's path: the reaper
// keep-alive is taken before the wake, so a start that overruns cannot have its fresh
// container reclaimed under it. A failure here sent nothing, and closes the delivery as not
// sent. It reports whether the Agent is ready.
func (r *memoQueueRunner) wake(ctx context.Context, res *resolved, d store.MemoDelivery) bool {
	notSent := func(why string) bool {
		log.Printf("memo queue: delivery %s not sent: %s", d.ID, why)
		r.closeNotSent(ctx, d, store.DeliveryClaimed)
		return false
	}
	release, err := r.mgr.trackWorkspaceConnection(ctx, res.ws.ID, "")
	if err != nil {
		if errors.Is(err, errWorkspaceStopping) {
			return notSent("the workspace is stopping")
		}
		return notSent("keep-alive: " + err.Error())
	}
	r.f.scheduleRelease(release)
	var wakeErr error
	if res.rt.State(ctx) != "running" {
		if aerr := r.f.wsAPI.ensureWorkspaceStartedUnattended(ctx, res); aerr != nil {
			if aerr.code == "quota_workspaces" {
				return notSent("the tenant is at its running-workspace cap")
			}
			wakeErr = fmt.Errorf("wake: %s", aerr.message)
		}
	}
	if err := r.f.awaitAgentReady(ctx, res.rt); err != nil {
		if wakeErr != nil {
			err = fmt.Errorf("%w (after %v)", err, wakeErr)
		}
		return notSent("agent not ready: " + err.Error())
	}
	if wakeErr != nil {
		_ = r.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
		_ = r.mgr.touchWorkspace(ctx, res.ws.ID)
	}
	return true
}

// reserveDeliveryCreate records the delivery's key on the Agent before its create is sent, so
// that abandon can later answer "not created" for it. Only an answer that names the key counts:
// an Agent without the route could never settle the key, and a create there could not be
// reconciled, so nothing is sent to it.
func reserveDeliveryCreate(ctx context.Context, res *resolved, deliveryID string) error {
	key := memoDeliveryKey(deliveryID)
	cctx, cancel := context.WithTimeout(ctx, memoAgentCallWait)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		res.rt.Endpoint()+"/sessions-idempotency/"+url.PathEscape(key)+"/reserve", nil)
	if err != nil {
		return err
	}
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK || resp.Header.Get(createKeyHeader) != key {
		return fmt.Errorf("the Agent answered %s without the key", resp.Status)
	}
	return nil
}

// create posts the session create. An error is no answer at all.
func (r *memoQueueRunner) create(ctx context.Context, res *resolved, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, res.rt.Endpoint()+"/sessions", bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if res.rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+res.rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, b, nil
}

// memoDeliveryCreate is the Agent's POST /sessions body for a queue fire (the Agent slice's
// contract, issue #4162). The Agent composes the first prompt from QueueDelivery.
type memoDeliveryCreate struct {
	Dir                string               `json:"dir"`
	Worktree           bool                 `json:"worktree"`
	Kind               string               `json:"kind"`
	Driver             string               `json:"driver"`
	Model              string               `json:"model"`
	Effort             string               `json:"effort"`
	Route              *memoDeliveryRoute   `json:"route,omitempty"`
	SpendCapUSD        float64              `json:"spend_cap_usd"`
	IdempotencyKey     string               `json:"idempotency_key"`
	IdempotencyDurable bool                 `json:"idempotency_durable"`
	Source             string               `json:"source"`
	QueueDelivery      memoDeliveryEnvelope `json:"queue_delivery"`
}

type memoDeliveryRoute struct {
	Route string `json:"route"`
}

type memoDeliveryEnvelope struct {
	ID          string             `json:"id"`
	Queue       string             `json:"queue"`
	Label       string             `json:"label"`
	Instruction string             `json:"instruction"`
	Items       []memoDeliveryItem `json:"items"`
}

type memoDeliveryItem struct {
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Body           string           `json:"body"`
	RefPath        string           `json:"refPath"`
	Attachments    []memoAttachment `json:"attachments"`
	Provenance     string           `json:"provenance"`
	Producer       string           `json:"producer"`
	ProducerOrigin string           `json:"producerOrigin"`
}

// memoDeliveryCreateBody builds the create. A queue with a repository gets a new worktree off
// it unless its settings say otherwise: two sessions in one working copy corrupt each other.
// The common bucket has no repository, so its sessions start where the Agent starts them.
func memoDeliveryCreateBody(d store.MemoDelivery, p memoSendPlan) []byte {
	dir, _ := memoQueueRepoDir(p.c.Repo)
	kind := scheduleInjectKind(p.q.AgentKind)
	body := memoDeliveryCreate{
		Dir: dir, Worktree: p.q.Worktree && dir != "", Kind: kind, Driver: injectDriver(kind),
		Model: p.q.Model, Effort: p.q.Effort, SpendCapUSD: p.q.SpendCapUSD,
		IdempotencyKey: memoDeliveryKey(d.ID), IdempotencyDurable: true, Source: "queue",
		QueueDelivery: memoDeliveryEnvelope{ID: d.ID, Queue: p.c.ID, Label: p.c.Repo + "/" + p.c.Name,
			Instruction: p.q.Instruction, Items: make([]memoDeliveryItem, 0, len(p.messages))},
	}
	if p.q.Route != "" {
		body.Route = &memoDeliveryRoute{Route: p.q.Route}
	}
	for _, m := range p.messages {
		atts := parseMemoAttachments(m.Attachments)
		if atts == nil {
			atts = []memoAttachment{}
		}
		body.QueueDelivery.Items = append(body.QueueDelivery.Items, memoDeliveryItem{ID: m.ID, Kind: m.Kind, Body: m.Body,
			RefPath: m.RefPath, Attachments: atts, Provenance: memoEffectiveProvenance(m), Producer: m.Producer,
			ProducerOrigin: m.ProducerOrigin})
	}
	b, _ := json.Marshal(body)
	return b
}
