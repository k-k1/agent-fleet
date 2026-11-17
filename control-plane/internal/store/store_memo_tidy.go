package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Tidy runs (ADR 0137 section 8). A run is one row of memo_tidy_run, and what it changes in the
// memo table goes through the MemoTx writes below. The rules that decide what a run may do live in
// control-plane/internal/memotidy and control-plane/memo_tidy.go.

// Run states (memo_tidy_run.state).
const (
	// TidyRunRunning is a reserved run: the unique index allows one per member.
	TidyRunRunning = "running"
	// TidyRunApplied is a run that changed something, and the only one that can be undone.
	TidyRunApplied = "applied"
	// TidyRunKept is a valid reply that changed nothing.
	TidyRunKept = "kept"
	// TidyRunDropped is a reply refused whole, or an input that was no longer what the run read.
	TidyRunDropped = "dropped"
	// TidyRunSkipped is a run in which nothing reached a model.
	TidyRunSkipped = "skipped"
	// TidyRunFailed is a call that failed or timed out.
	TidyRunFailed = "failed"
	// TidyRunUndone is an applied run that was undone.
	TidyRunUndone = "undone"
)

// Run triggers (memo_tidy_run.trigger).
const (
	TidyTriggerAuto   = "auto"
	TidyTriggerManual = "manual"
)

// MemoTidyRun is one tidy run. Input and Ops are JSON that holds ids, hashes and counts, never
// the text of a message.
type MemoTidyRun struct {
	ID, MembershipID, QueueID string
	Trigger, State, Reason    string
	Input, Ops                string
	CreatedAt, FinishedAt     string
	UndoneAt, UpdatedAt       string
}

// MemoTidyStore is the part of the tidy run record that needs no transaction: the Console's run
// list and the daily count.
type MemoTidyStore interface {
	// ListMemoTidyRuns returns a queue's newest runs, at most limit.
	ListMemoTidyRuns(ctx context.Context, membershipID, queueID string, limit int) ([]MemoTidyRun, error)
	// CountMemoTidyRunsSince counts the runs of a queue created at or after since, whatever
	// their outcome: the daily cap counts every run that was reserved.
	CountMemoTidyRunsSince(ctx context.Context, queueID, since string) (int, error)
	// MemoTidyRead runs f with the reads that decide whether a run can be undone, outside a
	// transaction and without the membership's lock: the run list is polled and must not queue
	// behind the dispatcher.
	MemoTidyRead(ctx context.Context, membershipID string, f func(MemoTidyReader) error) error
}

// MemoTidyReader is the read side of an undo's check, which a MemoTx and MemoTidyRead both have.
type MemoTidyReader interface {
	GetMemo(id string) (Memo, bool, error)
	ListMemosIn(repo, category string) ([]Memo, error)
	GetCategory(id string) (MemoCategory, bool, error)
	GetQueue(categoryID string) (MemoQueue, bool, error)
	QueueOf(repo, category string) (MemoQueue, bool, error)
}

// MemoTidyTxOps are the writes of a run inside a MemoTx.
type MemoTidyTxOps interface {
	GetTidyRun(id string) (MemoTidyRun, bool, error)
	// InsertTidyRun reserves a run. A second running run of the member fails on the unique index.
	InsertTidyRun(r MemoTidyRun) error
	// FinishTidyRun writes the outcome of a run that is still running. False means it no longer
	// was (a takeover finished it), and nothing was written.
	FinishTidyRun(r MemoTidyRun) (bool, error)
	// UndoTidyRun marks an applied run undone. False means it was not applied.
	UndoTidyRun(id, at string) (bool, error)
	// WriteTidyMemo is WriteMemo that also writes tidy_from, tidy_run_id and tidy_seen_hash.
	// It is how a run supersedes and moves a row. It still refuses to make a superseded row held
	// or pending.
	WriteTidyMemo(m Memo) error
	// RestoreMemo is WriteTidyMemo for an undo, the one write that may make a superseded row held
	// or pending again.
	RestoreMemo(m Memo) error
	// MarkTidySeen sets tidy_seen_hash of a row to hash, which is how a run marks a message seen.
	MarkTidySeen(id, hash string) error
}

const memoTidyRunCols = `SELECT id, membership_id, queue_id, trigger, state, reason, input, ops, created_at, finished_at, undone_at, updated_at FROM memo_tidy_run`

func scanMemoTidyRun(row scanner) (MemoTidyRun, error) {
	var r MemoTidyRun
	err := row.Scan(&r.ID, &r.MembershipID, &r.QueueID, &r.Trigger, &r.State, &r.Reason, &r.Input, &r.Ops,
		&r.CreatedAt, &r.FinishedAt, &r.UndoneAt, &r.UpdatedAt)
	return r, err
}

func (s *SQL) ListMemoTidyRuns(ctx context.Context, membershipID, queueID string, limit int) ([]MemoTidyRun, error) {
	rows, err := s.db.QueryContext(ctx, memoTidyRunCols+` WHERE membership_id=? AND queue_id=? ORDER BY created_at DESC, id DESC LIMIT ?`,
		membershipID, queueID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoTidyRun{}
	for rows.Next() {
		r, err := scanMemoTidyRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQL) CountMemoTidyRunsSince(ctx context.Context, queueID, since string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memo_tidy_run WHERE queue_id=? AND created_at>=?`, queueID, since).Scan(&n)
	return n, err
}

func (s *SQL) MemoTidyRead(ctx context.Context, membershipID string, f func(MemoTidyReader) error) error {
	return f(memoTx{ctx: ctx, q: s.db, mid: membershipID})
}

func (t memoTx) GetTidyRun(id string) (MemoTidyRun, bool, error) {
	r, err := scanMemoTidyRun(t.q.QueryRowContext(t.ctx, memoTidyRunCols+` WHERE id=? AND membership_id=?`, id, t.mid))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoTidyRun{}, false, nil
	}
	return r, err == nil, err
}

func (t memoTx) InsertTidyRun(r MemoTidyRun) error {
	_, err := t.q.ExecContext(t.ctx, `INSERT INTO memo_tidy_run(id, membership_id, queue_id, trigger, state, reason, input, ops,
		created_at, finished_at, undone_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, t.mid, r.QueueID, r.Trigger, r.State, r.Reason, r.Input, r.Ops, r.CreatedAt, r.FinishedAt, r.UndoneAt, r.UpdatedAt)
	return err
}

func (t memoTx) FinishTidyRun(r MemoTidyRun) (bool, error) {
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo_tidy_run SET state=?, reason=?, ops=?, finished_at=?, updated_at=?
		WHERE id=? AND membership_id=? AND state=?`,
		r.State, r.Reason, r.Ops, r.FinishedAt, r.UpdatedAt, r.ID, t.mid, TidyRunRunning))
}

func (t memoTx) UndoTidyRun(id, at string) (bool, error) {
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo_tidy_run SET state=?, undone_at=?, updated_at=?
		WHERE id=? AND membership_id=? AND state=?`, TidyRunUndone, at, at, id, t.mid, TidyRunApplied))
}

func (t memoTx) WriteTidyMemo(m Memo) error {
	m.MembershipID = t.mid
	return writeMemoRow(t.ctx, t.q, m, true, false)
}

func (t memoTx) RestoreMemo(m Memo) error {
	m.MembershipID = t.mid
	return writeMemoRow(t.ctx, t.q, m, true, true)
}

func (t memoTx) MarkTidySeen(id, hash string) error {
	_, err := t.q.ExecContext(t.ctx, `UPDATE memo SET tidy_seen_hash=? WHERE id=? AND membership_id=? AND tidy_seen_hash<>?`, hash, id, t.mid, hash)
	return err
}

// decodeIDList reads a JSON array of ids. A malformed or empty column reads as none.
func decodeIDList(s string) []string {
	var out []string
	if s == "" || json.Unmarshal([]byte(s), &out) != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func encodeIDList(ids []string) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ids)
	return string(b)
}
