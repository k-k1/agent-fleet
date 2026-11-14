package store

import (
	"context"
	"database/sql"
	"errors"
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

// MemoQueue is the settings row that makes a memo category a queue. Only the member's Console
// writes it. Generation is the compare-and-set counter the reservation of a later step uses.
type MemoQueue struct {
	CategoryID, MembershipID string
	Instruction              string
	AgentKind, Model, Effort string
	Route                    string
	SpendCapUSD              float64
	Worktree                 bool
	MaxActive, Batch         int
	Enabled                  bool
	Generation               int
	CreatedAt, UpdatedAt     string
}

// MemoQueueStore is the queue half of the memo store.
type MemoQueueStore interface {
	ListMemoQueues(ctx context.Context, membershipID string) ([]MemoQueue, error)
	// MemoTx runs f in one transaction that holds the membership's lock first, so every
	// queue-relevant memo write of a member is serialized behind the others. f's error
	// rolls the transaction back and is returned as is.
	MemoTx(ctx context.Context, membershipID string, f func(MemoTxOps) error) error
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
	// CountQueueCreatesSince counts the messages a token or /mcp face created at or after
	// since that are still in a queue: the rate bound of a create into a queue.
	CountQueueCreatesSince(since string) (int, error)

	GetCategory(id string) (MemoCategory, bool, error)
	FindCategory(repo, name string) (MemoCategory, bool, error)
	UpdateCategory(c MemoCategory) error
	DeleteCategory(id string) error

	GetQueue(categoryID string) (MemoQueue, bool, error)
	// QueueOf is the queue a memo in (repo, category) belongs to: the settings of the
	// category row with that name. The unfiled bucket has no row and is never a queue.
	QueueOf(repo, category string) (MemoQueue, bool, error)
	PutQueue(q MemoQueue) error
	DeleteQueue(categoryID string) error
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
	if err := f(memoTx{ctx: ctx, q: tx, mid: membershipID}); err != nil {
		return err
	}
	return tx.Commit()
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
		`UPDATE memo SET state=?, release_hash=?, released_at=?
		   WHERE id=? AND membership_id=? AND state=?
		     AND repo=? AND category=? AND kind=? AND body=? AND ref_path=? AND attachments=?`,
		MemoStatePending, hash, at, seen.ID, t.mid, MemoStateHeld,
		seen.Repo, seen.Category, seen.Kind, seen.Body, seen.RefPath, seen.Attachments))
}

func (t memoTx) CountQueueCreatesSince(since string) (int, error) {
	var n int
	err := t.q.QueryRowContext(t.ctx, `SELECT COUNT(*) FROM memo
		WHERE membership_id=? AND provenance IN (?,?) AND state<>'' AND created_at>=?`,
		t.mid, MemoProvenanceWorkspace, MemoProvenanceMCP, since).Scan(&n)
	return n, err
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
	worktree, max_active, batch, enabled, generation, created_at, updated_at FROM memo_queue`

func scanMemoQueue(row scanner) (MemoQueue, error) {
	var q MemoQueue
	var worktree, enabled int
	err := row.Scan(&q.CategoryID, &q.MembershipID, &q.Instruction, &q.AgentKind, &q.Model, &q.Effort, &q.Route,
		&q.SpendCapUSD, &worktree, &q.MaxActive, &q.Batch, &enabled, &q.Generation, &q.CreatedAt, &q.UpdatedAt)
	q.Worktree, q.Enabled = worktree != 0, enabled != 0
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
		effort, route, spend_cap_usd, worktree, max_active, batch, enabled, generation, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(category_id) DO UPDATE SET instruction=excluded.instruction, agent_kind=excluded.agent_kind,
		  model=excluded.model, effort=excluded.effort, route=excluded.route, spend_cap_usd=excluded.spend_cap_usd,
		  worktree=excluded.worktree, max_active=excluded.max_active, batch=excluded.batch, enabled=excluded.enabled,
		  updated_at=excluded.updated_at
		WHERE memo_queue.membership_id=excluded.membership_id`,
		q.CategoryID, t.mid, q.Instruction, q.AgentKind, q.Model, q.Effort, q.Route, q.SpendCapUSD,
		b2i(q.Worktree), q.MaxActive, q.Batch, b2i(q.Enabled), q.Generation, q.CreatedAt, q.UpdatedAt)
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
