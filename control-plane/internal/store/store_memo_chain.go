package store

import (
	"database/sql"
	"errors"
)

// MemoChain is the count of one released message's continuations (ADR 0137, addendum
// 2026-11-20). ID is the root message's id. It lives in a table of its own because the sweep
// deletes a delivered message a week after it was sent, and a chain may outlive that.
type MemoChain struct {
	ID, MembershipID, CategoryID string
	RefProvider, RefKey          string
	// Admitted is the continuations let in so far, Stopped is the member's Stop chain.
	Admitted             int
	Stopped              bool
	CreatedAt, UpdatedAt string
}

// MemoChainTxOps are the chain statements of a MemoTx.
type MemoChainTxOps interface {
	// AdmitChain lets one more continuation into the chain of root, making the chain row first
	// when there is none, if the chain is not stopped and has admitted fewer than max. It is one
	// conditional UPDATE, so two creates racing at max-1 admit one. admitted is the chain's count
	// after the call, stopped says the chain is stopped, and ok is false when nothing was admitted.
	AdmitChain(c MemoChain, max int) (admitted int, stopped, ok bool, err error)
	GetChain(id string) (MemoChain, bool, error)
	// StopChain stops a chain, false when there is none.
	StopChain(id, at string) (bool, error)
}

func (t memoTx) AdmitChain(c MemoChain, max int) (int, bool, bool, error) {
	if _, err := t.q.ExecContext(t.ctx,
		`INSERT INTO memo_chain(id, membership_id, category_id, ref_provider, ref_key, admitted, stopped, created_at, updated_at)
		 VALUES(?,?,?,?,?,0,0,?,?) ON CONFLICT(id) DO NOTHING`,
		c.ID, t.mid, c.CategoryID, c.RefProvider, c.RefKey, c.CreatedAt, c.CreatedAt); err != nil {
		return 0, false, false, err
	}
	won, err := affected(t.q.ExecContext(t.ctx,
		`UPDATE memo_chain SET admitted=admitted+1, updated_at=? WHERE id=? AND membership_id=? AND stopped=0 AND admitted<?`,
		c.CreatedAt, c.ID, t.mid, max))
	if err != nil {
		return 0, false, false, err
	}
	cur, _, err := t.GetChain(c.ID)
	return cur.Admitted, cur.Stopped, won, err
}

func (t memoTx) GetChain(id string) (MemoChain, bool, error) {
	var c MemoChain
	var stopped int
	err := t.q.QueryRowContext(t.ctx,
		`SELECT id, membership_id, category_id, ref_provider, ref_key, admitted, stopped, created_at, updated_at
		 FROM memo_chain WHERE id=? AND membership_id=?`, id, t.mid).
		Scan(&c.ID, &c.MembershipID, &c.CategoryID, &c.RefProvider, &c.RefKey, &c.Admitted, &stopped, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MemoChain{}, false, nil
	}
	c.Stopped = stopped != 0
	return c, err == nil, err
}

func (t memoTx) StopChain(id, at string) (bool, error) {
	// A chain with no row yet (no continuation was ever added) is stopped by making the row, so
	// that the next admission finds it stopped.
	if _, err := t.q.ExecContext(t.ctx,
		`INSERT INTO memo_chain(id, membership_id, category_id, created_at, updated_at) VALUES(?,?,'',?,?) ON CONFLICT(id) DO NOTHING`,
		id, t.mid, at, at); err != nil {
		return false, err
	}
	return affected(t.q.ExecContext(t.ctx, `UPDATE memo_chain SET stopped=1, updated_at=? WHERE id=? AND membership_id=?`, at, id, t.mid))
}
