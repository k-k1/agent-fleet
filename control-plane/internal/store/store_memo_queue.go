package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Message queues, step 1a (ADR 0137). A memo category becomes a queue when a memo_queue row
// names it, and a memo in that category is a message with a state. The rules that decide a
// state live in the API layer (control-plane/memo_queue.go). This file gives them one
// transaction per write, serialized per membership, so that a rule computed from what was read
// is the rule applied to what is written.

// Message states (memo.state). "" is a plain note.
const (
	MemoStateHeld        = "held"
	MemoStatePending     = "pending"
	MemoStateClaimed     = "claimed"
	MemoStateDelivered   = "delivered"
	MemoStateUnconfirmed = "unconfirmed"
	MemoStateSuperseded  = "superseded"
)

// Provenances (memo.provenance). "" is an existing row with no recorded face, read as workspace.
const (
	MemoProvenanceMember    = "member"
	MemoProvenanceWorkspace = "workspace"
	MemoProvenanceMCP       = "mcp"
	MemoProvenanceInbox     = "inbox"
	MemoProvenanceCurator   = "curator"
)

// MemoReleasedByTidy is memo.released_by for a message a tidy run made pending: a derived message
// that took its pending sources' state, or a message it moved into a queue its source queue lists.
const MemoReleasedByTidy = "tidy"

// MaxTidyDestinations bounds a queue's tidy_destinations.
const MaxTidyDestinations = 10

// MemoReleasedBySetting is memo.released_by for a message that became pending by the queue's
// accept-from-own-sessions switch and not by a press.
const MemoReleasedBySetting = "setting"

// MemoReleasedByContinuation is memo.released_by for a continuation a run of the queue added and
// the Control Plane admitted as pending without a press (ADR 0137, addendum 2026-11-20).
const MemoReleasedByContinuation = "continuation"

// MemoOriginQueue is the producer origin of a session a queue's delivery started: the value
// the Agent records from the delivery's envelope and never from a client's launch field.
const MemoOriginQueue = "queue"

// MemoOriginUser is the producer origin of a session the member started themselves: the value
// the Agent's relay sends for session.OriginUser.
const MemoOriginUser = "user"

// MemoSendStaleAfter bounds a flush in flight: past it, the CP that reserved the rows is taken
// to have died between its reservation and the send's outcome, and the reservation is taken
// back (SweepStaleMemoSends). It is above the longest a send can take (two Agent calls of at
// most two minutes each), so a live flush is never swept.
const MemoSendStaleAfter = 10 * time.Minute

// MemoQueue is the settings row that makes a memo category a queue. Only the member's Console
// writes it. Generation is the compare-and-set counter the reservation of a later step uses.
type MemoQueue struct {
	CategoryID, MembershipID string
	Instruction              string
	AgentKind, Model, Effort string
	Route                    string
	SpendCapUSD              float64
	Worktree                 bool
	// AcceptOwnSessions drops the release press for a message from a session the member started
	// (ADR 0137 section 3). It is a guard rail: the Agent supplies the session's origin.
	AcceptOwnSessions bool
	// ContinuationMax is how many continuations of one released message a run of this queue may
	// add without a press (ADR 0137, addendum 2026-11-20). 0 is off. The column defaults to 4.
	ContinuationMax  int
	MaxActive, Batch int
	// MaxTaskHours is how long a delivery of the queue may stay open before it is overdue
	// (ADR 0137 section 6), 1 to 72.
	MaxTaskHours int
	// Fires by themselves (ADR 0137 section 6). OpenCron, OpenMinutes and TZ are a window, OpenAfter
	// a threshold, none of them always open. MaxFiresPerDay bounds the deliveries of 24 hours.
	OpenCron, TZ           string
	OpenMinutes, OpenAfter int
	MaxFiresPerDay         int
	// State, never written by the settings PUT: Draining is a threshold queue emptying, Paused
	// the circuit breaker (PausedAt, PausedReason), BreakerAfter the time from which the breaker
	// counts deliveries, NoReportAt when a fire found an Agent that sends no queue report.
	Draining, Paused                                 bool
	PausedAt, PausedReason, BreakerAfter, NoReportAt string
	// NoRefsAt is when a fire found an Agent that cannot show a reference. While it is set the
	// messages that carry one stay where they are (ADR 0137 section 4).
	NoRefsAt string
	// Tidy turns the tidy run before a fire on (ADR 0137 section 8). TidyDestinations are the
	// queues a run may move a message to as pending, at most MaxTidyDestinations category ids.
	// NoTidyAt is set by a fire that found an Agent that cannot tidy. Only the settings PUT
	// writes the first two, and nothing writes them in a run.
	Tidy                 bool
	TidyDestinations     []string
	NoTidyAt             string
	Enabled              bool
	Generation           int
	CreatedAt, UpdatedAt string
}

// MemoQueueStore is the queue half of the memo store.
type MemoQueueStore interface {
	ListMemoQueues(ctx context.Context, membershipID string) ([]MemoQueue, error)
	// ListEnabledMemoQueues returns every enabled queue of every member, and a queue that is not
	// enabled but still marked draining so that the mark is cleared: the dispatcher's work.
	ListEnabledMemoQueues(ctx context.Context) ([]MemoQueue, error)
	// ListQueuePendingStats counts the pending messages of every enabled queue, by category id.
	ListQueuePendingStats(ctx context.Context) (map[string]QueuePending, error)
	// ListMemberQueuePendingStats is the same for every queue of one member, enabled or not: the
	// read that says why a queue with notes waiting starts nothing.
	ListMemberQueuePendingStats(ctx context.Context, membershipID string) (map[string]QueuePending, error)
	// MemoRead runs f with the reads that decide whether a queue may fire, outside a transaction
	// and without the membership's lock: a display must not queue behind the dispatcher.
	MemoRead(ctx context.Context, membershipID string, f func(MemoWaitOps) error) error
	// SetMemoQueueDraining and SetMemoQueueNoReport write the dispatcher's state columns.
	SetMemoQueueDraining(ctx context.Context, queueID string, on bool) error
	SetMemoQueueNoReport(ctx context.Context, queueID, at string) error
	// SetMemoQueueNoRefs writes NoRefsAt, '' to clear it.
	SetMemoQueueNoRefs(ctx context.Context, queueID, at string) error
	// SetMemoQueueNoTidy writes NoTidyAt, '' to clear it.
	SetMemoQueueNoTidy(ctx context.Context, queueID, at string) error
	// MemoTx runs f in one transaction that holds the membership's lock first, so every
	// queue-relevant memo write of a member is serialized behind the others. Before f it
	// takes back the member's stale send reservations, so f never sees one. f's error
	// rolls the transaction back and is returned as is.
	MemoTx(ctx context.Context, membershipID string, f func(MemoTxOps) error) error
	MemoDeliveryStore
	MemoPRSnapshotStore
	MemoTidyStore
}

// MemoTxOps are the reads and writes a MemoTx may make, all scoped to its membership.
type MemoTxOps interface {
	GetMemo(id string) (Memo, bool, error)
	// ListMemosIn returns every row of one (repo, category) bucket, sent ones included.
	ListMemosIn(repo, category string) ([]Memo, error)
	InsertMemo(m Memo) error
	WriteMemo(m Memo) error
	DeleteMemo(id string) error
	// ReleaseMemo moves one message from held to pending with hash as its release, only if
	// the row still holds exactly the content of seen. It is one conditional write on the
	// row itself: a guard elsewhere (an EXISTS on the same table) lets concurrent writers
	// on Postgres both pass. False means nothing was written.
	ReleaseMemo(seen Memo, hash, at string) (bool, error)
	// CountQueueIntakeSince counts the creates into a queue from inside the workspace or
	// /mcp logged at or after since, dropping older entries. Moving or deleting a message
	// does not take its create off the log.
	CountQueueIntakeSince(since string) (int, error)
	RecordQueueIntake(at string) error

	GetCategory(id string) (MemoCategory, bool, error)
	FindCategory(repo, name string) (MemoCategory, bool, error)
	// EnsureCategory makes the category row (repo, name) when the member's repo has none, at
	// the end of the repo's order, and answers the row. A row that exists is left as it is.
	// Names match exactly, case included. The unique (membership, repo, name) index backs the
	// membership lock: of two creators the second reads the first's row, and never fails.
	EnsureCategory(repo, name string) (MemoCategory, error)
	UpdateCategory(c MemoCategory) error
	DeleteCategory(id string) error

	GetQueue(categoryID string) (MemoQueue, bool, error)
	// QueueOf is the queue a memo in (repo, category) belongs to: the settings of the
	// category row with that name. The unfiled bucket has no row and is never a queue.
	QueueOf(repo, category string) (MemoQueue, bool, error)
	PutQueue(q MemoQueue) error
	DeleteQueue(categoryID string) error
	// SetQueueDraining writes the draining state inside the transaction of a fire.
	SetQueueDraining(categoryID string, on bool) error
	// LockTenantSwitch takes the member's tenant row, so that turning memo queues off waits for
	// this transaction, and answers the switch as it is now.
	LockTenantSwitch() (bool, error)
	// CountLimitedDeliveries counts the member's open deliveries whose last kept report showed a
	// session of the tree waiting for a usage limit under agent kind kind.
	CountLimitedDeliveries(kind string) (int, error)
	// RecentQueueDeliveries returns a queue's newest deliveries, wakes left out.
	RecentQueueDeliveries(queueID string, limit int) ([]MemoDelivery, error)
	// LiveQueueDeliveries returns the deliveries of a queue that are not closed.
	LiveQueueDeliveries(queueID string) ([]MemoDelivery, error)
	// RecentTurnDeliveries returns a queue's deliveries created after the given time whose
	// first turn was recorded, newest first, at most limit. Deferred ones are left out.
	RecentTurnDeliveries(queueID, after string, limit int) ([]MemoDelivery, error)
	// PauseQueue pauses a queue that is not paused. False means it already was.
	PauseQueue(categoryID, at, reason string) (bool, error)
	// ResumeQueue lifts a pause and sets the time the breaker counts from.
	ResumeQueue(categoryID, at string) (bool, error)

	MemoDeliveryTxOps
	MemoTidyTxOps
	MemoWaitOps
	MemoPRWakeTxOps
	MemoDeferTxOps
	MemoChainTxOps
}

// MemoWaitOps are the reads behind "may this queue fire now", the ones the dispatcher makes
// behind the membership's lock and the Console's list makes without it.
type MemoWaitOps interface {
	CountLimitedDeliveries(kind string) (int, error)
	RecentQueueDeliveries(queueID string, limit int) ([]MemoDelivery, error)
	CountDeliveriesCreatedSince(queueID, since string) (int, error)
	CountLiveDeliveries(queueID string) (queue, member int, err error)
	// NthOldestDeliverySince is the creation time of the queue's skip-th oldest delivery (0 is
	// the oldest) created at or after since, counted as CountDeliveriesCreatedSince counts.
	NthOldestDeliverySince(queueID, since string, skip int) (string, bool, error)
	// RunningTidyRun is the member's tidy run that is in flight, at most one (a unique index).
	RunningTidyRun() (MemoTidyRun, bool, error)
}

// memoQuerier is what *sqlDB and *sqlTx have in common.
type memoQuerier interface {
	ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row
}

func (s *SQL) MemoTx(ctx context.Context, membershipID string, f func(MemoTxOps) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockMemberships(ctx, tx, []string{membershipID}); err != nil {
		return err
	}
	if err := sweepStaleMemoSends(ctx, tx, membershipID); err != nil {
		return err
	}
	if err := f(memoTx{ctx: ctx, q: tx, mid: membershipID}); err != nil {
		return err
	}
	if err := endEmptyDrains(ctx, tx, membershipID); err != nil {
		return err
	}
	return tx.Commit()
}

// endEmptyDrains ends the draining of a queue of the member that has no pending message left, in
// the transaction that left it so (a claim, a delete, a move to held): the moment of "nothing
// pending" is then never missed, and a message added afterwards starts a new batch that has to
// reach its threshold again. Every write of a pending message runs in a MemoTx.
func endEmptyDrains(ctx context.Context, q memoQuerier, membershipID string) error {
	_, err := q.ExecContext(ctx, `UPDATE memo_queue SET draining=0 WHERE membership_id=? AND draining=1 AND NOT EXISTS (
		SELECT 1 FROM memo m JOIN memo_category c ON c.membership_id=m.membership_id AND c.repo=m.repo AND c.name=m.category
		WHERE c.id=memo_queue.category_id AND m.state=?)`, membershipID, MemoStatePending)
	return err
}

// sweepStaleMemoSends takes back the send reservations older than MemoSendStaleAfter. Whether
// such a send reached the session is unknown, so a held or pending message becomes unconfirmed:
// shown to the person and never fired by itself, since a second send could do the work twice.
// A plain note is simply unsent again, as it was before any reservation existed.
func sweepStaleMemoSends(ctx context.Context, q memoQuerier, membershipID string) error {
	cutoff := time.Now().UTC().Add(-MemoSendStaleAfter).Format(time.RFC3339)
	_, err := q.ExecContext(ctx, `UPDATE memo SET
		  state=CASE WHEN state IN (?,?) THEN ? ELSE state END,
		  release_hash=CASE WHEN state IN (?,?) THEN '' ELSE release_hash END,
		  released_at=CASE WHEN state IN (?,?) THEN '' ELSE released_at END,
		  released_by=CASE WHEN state IN (?,?) THEN '' ELSE released_by END,
		  send_token='', send_started_at=''
		WHERE membership_id=? AND send_token<>'' AND send_started_at<?`,
		MemoStateHeld, MemoStatePending, MemoStateUnconfirmed,
		MemoStateHeld, MemoStatePending, MemoStateHeld, MemoStatePending, MemoStateHeld, MemoStatePending,
		membershipID, cutoff)
	return err
}

type memoTx struct {
	ctx context.Context
	q   memoQuerier
	mid string
}

func (t memoTx) GetMemo(id string) (Memo, bool, error) {
	m, err := scanMemo(t.q.QueryRowContext(t.ctx, memoCols+` WHERE id=? AND membership_id=?`, id, t.mid))
	if errors.Is(err, sql.ErrNoRows) {
		return Memo{}, false, nil
	}
	return m, err == nil, err
}

func (t memoTx) ListMemosIn(repo, category string) ([]Memo, error) {
	rows, err := t.q.QueryContext(t.ctx, memoCols+` WHERE membership_id=? AND repo=? AND category=?
		ORDER BY position, created_at, id`, t.mid, repo, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memo{}
	for rows.Next() {
		m, err := scanMemo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (t memoTx) InsertMemo(m Memo) error {
	m.MembershipID = t.mid
	return insertMemo(t.ctx, t.q, m)
}

func (t memoTx) WriteMemo(m Memo) error {
	m.MembershipID = t.mid
	return writeMemo(t.ctx, t.q, m)
}

func (t memoTx) DeleteMemo(id string) error {
	_, err := t.q.ExecContext(t.ctx, `DELETE FROM memo WHERE id=? AND membership_id=?`, id, t.mid)
	return err
}

func (t memoTx) ReleaseMemo(seen Memo, hash, at string) (bool, error) {
	return affected(t.q.ExecContext(t.ctx,
		`UPDATE memo SET state=?, release_hash=?, released_at=?, released_by='', held_reason='',
		   not_before='', defer_count=0, defer_reasons='',
		   release_repo=repo, release_category=category, release_kind=kind, release_body=body,
		   release_ref_path=ref_path, release_attachments=attachments,
		   release_ref_provider=ref_provider, release_ref_key=ref_key, release_ref_url=ref_url,
		   release_after_ids=after_ids, release_start_after=start_after, chain_id='', chain_seq=0, chain_delivery=''
		   WHERE id=? AND membership_id=? AND state=?
		     AND repo=? AND category=? AND kind=? AND body=? AND ref_path=? AND attachments=?
		     AND ref_provider=? AND ref_key=? AND ref_url=? AND after_ids=? AND start_after=?`,
		MemoStatePending, hash, at, seen.ID, t.mid, MemoStateHeld,
		seen.Repo, seen.Category, seen.Kind, seen.Body, seen.RefPath, seen.Attachments,
		seen.RefProvider, seen.RefKey, seen.RefURL, seen.AfterIDs, seen.StartAfter))
}

func (t memoTx) CountQueueIntakeSince(since string) (int, error) {
	if _, err := t.q.ExecContext(t.ctx, `DELETE FROM memo_queue_intake WHERE membership_id=? AND created_at<?`, t.mid, since); err != nil {
		return 0, err
	}
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_queue_intake WHERE membership_id=? AND created_at>=?`,
		t.mid, since).Scan(&n)
	return n, err
}

func (t memoTx) RecordQueueIntake(at string) error {
	_, err := t.q.ExecContext(t.ctx, `INSERT INTO memo_queue_intake(id, membership_id, created_at) VALUES(?,?,?)`,
		NewID(), t.mid, at)
	return err
}

func (t memoTx) GetCategory(id string) (MemoCategory, bool, error) {
	c, err := scanMemoCategory(t.q.QueryRowContext(t.ctx, memoCategoryCols+` WHERE id=? AND membership_id=?`, id, t.mid))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoCategory{}, false, nil
	}
	return c, err == nil, err
}

func (t memoTx) FindCategory(repo, name string) (MemoCategory, bool, error) {
	c, err := scanMemoCategory(t.q.QueryRowContext(t.ctx, memoCategoryCols+` WHERE membership_id=? AND repo=? AND name=?`, t.mid, repo, name))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoCategory{}, false, nil
	}
	return c, err == nil, err
}

func (t memoTx) EnsureCategory(repo, name string) (MemoCategory, error) {
	if c, ok, err := t.FindCategory(repo, name); err != nil || ok {
		return c, err
	}
	var maxPos sql.NullInt64
	if err := t.q.QueryRowContext(t.ctx, `SELECT MAX(position) FROM memo_category WHERE membership_id=? AND repo=?`,
		t.mid, repo).Scan(&maxPos); err != nil {
		return MemoCategory{}, err
	}
	next := 0
	if maxPos.Valid {
		next = int(maxPos.Int64) + 1
	}
	c := MemoCategory{ID: NewID(), MembershipID: t.mid, Repo: repo, Name: name, Position: next, CreatedAt: NowTS()}
	if _, err := t.q.ExecContext(t.ctx, `INSERT INTO memo_category(id, membership_id, repo, name, position, created_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(membership_id, repo, name) DO NOTHING`,
		c.ID, c.MembershipID, c.Repo, c.Name, c.Position, c.CreatedAt); err != nil {
		return MemoCategory{}, err
	}
	got, ok, err := t.FindCategory(repo, name)
	if err == nil && !ok {
		err = errors.New("store: category row missing right after its insert")
	}
	return got, err
}

func (t memoTx) UpdateCategory(c MemoCategory) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_category SET name=?, position=? WHERE id=? AND membership_id=?`,
		c.Name, c.Position, c.ID, t.mid)
	return err
}

func (t memoTx) DeleteCategory(id string) error {
	_, err := t.q.ExecContext(t.ctx, `DELETE FROM memo_category WHERE id=? AND membership_id=?`, id, t.mid)
	return err
}

const memoQueueCols = `SELECT category_id, membership_id, instruction, agent_kind, model, effort, route, spend_cap_usd,
	worktree, max_active, batch, enabled, generation, created_at, updated_at, max_task_hours,
	open_cron, open_minutes, tz, open_after, max_fires_per_day, draining, paused, paused_at, paused_reason, breaker_after,
	no_report_at, no_refs_at, accept_own_sessions, tidy, tidy_destinations, no_tidy_at, continuation_max FROM memo_queue`

func scanMemoQueue(row scanner) (MemoQueue, error) {
	var q MemoQueue
	var worktree, enabled, draining, paused, acceptOwn, tidy int
	var dests string
	err := row.Scan(&q.CategoryID, &q.MembershipID, &q.Instruction, &q.AgentKind, &q.Model, &q.Effort, &q.Route,
		&q.SpendCapUSD, &worktree, &q.MaxActive, &q.Batch, &enabled, &q.Generation, &q.CreatedAt, &q.UpdatedAt, &q.MaxTaskHours,
		&q.OpenCron, &q.OpenMinutes, &q.TZ, &q.OpenAfter, &q.MaxFiresPerDay, &draining, &paused, &q.PausedAt, &q.PausedReason,
		&q.BreakerAfter, &q.NoReportAt, &q.NoRefsAt, &acceptOwn, &tidy, &dests, &q.NoTidyAt, &q.ContinuationMax)
	q.Worktree, q.Enabled, q.Draining, q.Paused = worktree != 0, enabled != 0, draining != 0, paused != 0
	q.AcceptOwnSessions = acceptOwn != 0
	q.Tidy, q.TidyDestinations = tidy != 0, decodeIDList(dests)
	return q, err
}

func (t memoTx) GetQueue(categoryID string) (MemoQueue, bool, error) {
	q, err := scanMemoQueue(t.q.QueryRowContext(t.ctx, memoQueueCols+` WHERE category_id=? AND membership_id=?`, categoryID, t.mid))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoQueue{}, false, nil
	}
	return q, err == nil, err
}

func (t memoTx) QueueOf(repo, category string) (MemoQueue, bool, error) {
	if category == "" {
		return MemoQueue{}, false, nil
	}
	c, ok, err := t.FindCategory(repo, category)
	if err != nil || !ok {
		return MemoQueue{}, false, err
	}
	return t.GetQueue(c.ID)
}

// PutQueue inserts or replaces the settings of a category. created_at and generation of an
// existing row are kept: a settings change is not a reservation.
func (t memoTx) PutQueue(q MemoQueue) error {
	_, err := t.q.ExecContext(t.ctx, `INSERT INTO memo_queue(category_id, membership_id, instruction, agent_kind, model,
		effort, route, spend_cap_usd, worktree, max_active, batch, enabled, generation, created_at, updated_at,
		max_task_hours, open_cron, open_minutes, tz, open_after, max_fires_per_day, accept_own_sessions,
		tidy, tidy_destinations, continuation_max)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(category_id) DO UPDATE SET instruction=excluded.instruction, agent_kind=excluded.agent_kind,
		  model=excluded.model, effort=excluded.effort, route=excluded.route, spend_cap_usd=excluded.spend_cap_usd,
		  worktree=excluded.worktree, max_active=excluded.max_active, batch=excluded.batch, enabled=excluded.enabled,
		  max_task_hours=excluded.max_task_hours, open_cron=excluded.open_cron, open_minutes=excluded.open_minutes,
		  tz=excluded.tz, open_after=excluded.open_after, max_fires_per_day=excluded.max_fires_per_day,
		  accept_own_sessions=excluded.accept_own_sessions, tidy=excluded.tidy,
		  tidy_destinations=excluded.tidy_destinations, continuation_max=excluded.continuation_max,
		  updated_at=excluded.updated_at
		WHERE memo_queue.membership_id=excluded.membership_id`,
		q.CategoryID, t.mid, q.Instruction, q.AgentKind, q.Model, q.Effort, q.Route, q.SpendCapUSD,
		b2i(q.Worktree), q.MaxActive, q.Batch, b2i(q.Enabled), q.Generation, q.CreatedAt, q.UpdatedAt, q.MaxTaskHours,
		q.OpenCron, q.OpenMinutes, q.TZ, q.OpenAfter, q.MaxFiresPerDay, b2i(q.AcceptOwnSessions),
		b2i(q.Tidy), encodeIDList(q.TidyDestinations), q.ContinuationMax)
	return err
}

func (t memoTx) DeleteQueue(categoryID string) error {
	_, err := t.q.ExecContext(t.ctx, `DELETE FROM memo_queue WHERE category_id=? AND membership_id=?`, categoryID, t.mid)
	return err
}

// ListMemoQueues returns a membership's queue settings, oldest first.
func (s *SQL) ListMemoQueues(ctx context.Context, membershipID string) ([]MemoQueue, error) {
	rows, err := s.db.QueryContext(ctx, memoQueueCols+` WHERE membership_id=? ORDER BY created_at, category_id`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoQueue{}
	for rows.Next() {
		q, err := scanMemoQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// QueuePending is what a queue has waiting: how many pending messages, the creation time of the
// oldest, and the latest time any of them was created or released.
type QueuePending struct {
	Count int
	// Eligible is how many of them may be claimed now: no prerequisite, or every prerequisite
	// satisfied (MemoAfterSatisfiedSQL). Count, blocked ones included, keeps a drain going, and
	// Eligible opens a threshold and is what a claim takes.
	Eligible int
	// Refs is how many of the eligible ones carry a reference, Curators how many a tidy run
	// derived: compared with Eligible they say that what a claim could take is all held back.
	Refs, Curators int
	Oldest, Latest string
	// Deferred is how many of them wait for their not_before (a hand-back), Scheduled how many of
	// the rest wait for their start_after, and NextAt the earliest time one of them becomes
	// claimable by time ("" when none waits).
	Deferred, Scheduled int
	NextAt              string
}

func (s *SQL) ListEnabledMemoQueues(ctx context.Context) ([]MemoQueue, error) {
	rows, err := s.db.QueryContext(ctx, memoQueueCols+` WHERE enabled=1 OR draining=1 ORDER BY created_at, category_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoQueue{}
	for rows.Next() {
		q, err := scanMemoQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *SQL) ListQueuePendingStats(ctx context.Context) (map[string]QueuePending, error) {
	return s.queuePendingStats(ctx, `q.enabled=1`)
}

func (s *SQL) ListMemberQueuePendingStats(ctx context.Context, membershipID string) (map[string]QueuePending, error) {
	return s.queuePendingStats(ctx, `q.membership_id=?`, membershipID)
}

func (s *SQL) queuePendingStats(ctx context.Context, cond string, args ...any) (map[string]QueuePending, error) {
	now := DeliveryTS(time.Now())
	eligible := memoNotBeforeSQL + ` AND ` + memoEligibleSQL
	head := []any{now, now, now, now, now, now, now, now, now, now}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, COUNT(*), SUM(CASE WHEN `+eligible+` THEN 1 ELSE 0 END),
		  SUM(CASE WHEN m.ref_key<>'' AND `+eligible+` THEN 1 ELSE 0 END),
		  SUM(CASE WHEN m.provenance='curator' AND `+eligible+` THEN 1 ELSE 0 END), MIN(m.created_at), MAX(m.created_at), MAX(m.released_at),
		  SUM(CASE WHEN `+memoClaimableSQL+` AND m.not_before>? THEN 1 ELSE 0 END),
		  SUM(CASE WHEN `+memoClaimableSQL+` AND m.not_before<=? AND m.start_after>? THEN 1 ELSE 0 END),
		  COALESCE(MIN(CASE WHEN `+memoClaimableSQL+` AND `+memoWaitUntilSQL+`>? THEN `+memoWaitUntilSQL+` END), '')
		FROM memo m
		JOIN memo_category c ON c.membership_id=m.membership_id AND c.repo=m.repo AND c.name=m.category
		JOIN memo_queue q ON q.category_id=c.id
		WHERE `+cond+` AND m.state=? GROUP BY c.id`, append(append(head, args...), MemoStatePending)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]QueuePending{}
	for rows.Next() {
		var id string
		var p QueuePending
		var maxCreated, maxReleased string
		if err := rows.Scan(&id, &p.Count, &p.Eligible, &p.Refs, &p.Curators, &p.Oldest, &maxCreated, &maxReleased, &p.Deferred, &p.Scheduled, &p.NextAt); err != nil {
			return nil, err
		}
		p.Latest = maxCreated
		if maxReleased > p.Latest {
			p.Latest = maxReleased
		}
		out[id] = p
	}
	return out, rows.Err()
}

func (s *SQL) MemoRead(ctx context.Context, membershipID string, f func(MemoWaitOps) error) error {
	return f(memoTx{ctx: ctx, q: s.db, mid: membershipID})
}

func (s *SQL) SetMemoQueueDraining(ctx context.Context, queueID string, on bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memo_queue SET draining=? WHERE category_id=?`, b2i(on), queueID)
	return err
}

func (s *SQL) SetMemoQueueNoReport(ctx context.Context, queueID, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memo_queue SET no_report_at=? WHERE category_id=? AND no_report_at<>?`, at, queueID, at)
	return err
}

func (s *SQL) SetMemoQueueNoRefs(ctx context.Context, queueID, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memo_queue SET no_refs_at=? WHERE category_id=? AND no_refs_at<>?`, at, queueID, at)
	return err
}

func (s *SQL) SetMemoQueueNoTidy(ctx context.Context, queueID, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memo_queue SET no_tidy_at=? WHERE category_id=? AND no_tidy_at<>?`, at, queueID, at)
	return err
}

func (t memoTx) SetQueueDraining(categoryID string, on bool) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo_queue SET draining=? WHERE category_id=? AND membership_id=?`, b2i(on), categoryID, t.mid)
	return err
}

func (t memoTx) LockTenantSwitch() (bool, error) {
	if _, err := t.q.ExecContext(t.ctx, `UPDATE tenant SET memo_queues=memo_queues
		WHERE id=(SELECT tenant_id FROM membership WHERE id=?)`, t.mid); err != nil {
		return false, err
	}
	var v int
	err := t.q.QueryRowContext(t.ctx, `SELECT memo_queues FROM tenant WHERE id=(SELECT tenant_id FROM membership WHERE id=?)`, t.mid).Scan(&v)
	return v != 0, err
}

func (t memoTx) CountLimitedDeliveries(kind string) (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo_delivery WHERE membership_id=? AND state=? AND limit_kinds LIKE ?`,
		t.mid, DeliveryOpen, "%,"+kind+",%").Scan(&n)
	return n, err
}

func (t memoTx) PauseQueue(categoryID, at, reason string) (bool, error) {
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo_queue SET paused=1, paused_at=?, paused_reason=?, updated_at=?
		WHERE category_id=? AND membership_id=? AND paused=0`, at, reason, at, categoryID, t.mid))
}

func (t memoTx) ResumeQueue(categoryID, at string) (bool, error) {
	_, ok, err := t.GetQueue(categoryID)
	if err != nil || !ok {
		return false, err
	}
	_, err = t.q.ExecContext(t.ctx, `UPDATE memo_queue SET paused=0, paused_at='', paused_reason='', breaker_after=?, updated_at=?
		WHERE category_id=? AND membership_id=? AND paused=1`, at, at, categoryID, t.mid)
	return true, err
}
