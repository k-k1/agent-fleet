package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// CPLeaseStore keeps the leases that make a loop single-runner across Control Plane tasks
// (cp_lease, issue #1603). Expiry is computed and compared by the database's clock alone:
// two CPs whose clocks disagree would otherwise both see the lease as theirs.
type CPLeaseStore interface {
	// AcquireCPLease takes name for holder for ttl, when it is free, expired, or already
	// holder's. false, with nothing written, while another holder's lease is live.
	AcquireCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	// RenewCPLease extends holder's lease by ttl from now, only while it is still live: a
	// holder whose lease expired has to acquire it again, and loses to whoever took it.
	RenewCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	// ReleaseCPLease gives name up when holder still has it, so the next holder need not
	// wait out the expiry. Releasing a lease somebody else holds now changes nothing.
	ReleaseCPLease(ctx context.Context, name, holder string) error
	// BumpCPCounter adds one to the counter name (created at 1) and returns the new value.
	BumpCPCounter(ctx context.Context, name string) (int64, error)
	// CPCounter reads the counter name, 0 when it has never been bumped.
	CPCounter(ctx context.Context, name string) (int64, error)
}

// dbNowMs is the database's current time in Unix milliseconds.
func (s *SQL) dbNowMs() string {
	if s.dialect == "postgres" {
		return "(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT"
	}
	return "CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)"
}

// AcquireCPLease is one statement, so two CPs racing for an expired lease cannot both win:
// the conflict's WHERE decides, and the loser's upsert writes nothing.
func (s *SQL) AcquireCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	q := strings.ReplaceAll(`INSERT INTO cp_lease (name, holder, expires_ms) VALUES(?, ?, NOW + ?)
		ON CONFLICT(name) DO UPDATE SET holder=excluded.holder, expires_ms=excluded.expires_ms
		WHERE cp_lease.holder=excluded.holder OR cp_lease.expires_ms <= NOW`, "NOW", s.dbNowMs())
	res, err := s.db.ExecContext(ctx, q, name, holder, ttl.Milliseconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *SQL) RenewCPLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	q := strings.ReplaceAll(`UPDATE cp_lease SET expires_ms = NOW + ?
		WHERE name=? AND holder=? AND expires_ms > NOW`, "NOW", s.dbNowMs())
	res, err := s.db.ExecContext(ctx, q, ttl.Milliseconds(), name, holder)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *SQL) ReleaseCPLease(ctx context.Context, name, holder string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cp_lease WHERE name=? AND holder=?`, name, holder)
	return err
}

// BumpCPCounter is one statement, so two CPs bumping at once get two different values.
func (s *SQL) BumpCPCounter(ctx context.Context, name string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO cp_counter (name, value) VALUES(?, 1)
		ON CONFLICT(name) DO UPDATE SET value = cp_counter.value + 1 RETURNING value`, name).Scan(&v)
	return v, err
}

func (s *SQL) CPCounter(ctx context.Context, name string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT value FROM cp_counter WHERE name=?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}
