package store

import (
	"context"
	"time"
)

// CPLeaseStore keeps the leases that make a loop single-runner across Control Plane tasks
// (cp_lease, issue #1603).
type CPLeaseStore interface {
	// AcquireCPLease takes name for holder until until, or extends it when holder has it
	// already. false, with nothing written, while another holder's lease has not expired.
	AcquireCPLease(ctx context.Context, name, holder string, now, until time.Time) (bool, error)
	// ReleaseCPLease gives name up if holder has it, so the next CP need not wait out the
	// expiry.
	ReleaseCPLease(ctx context.Context, name, holder string) error
}

// cpLeaseTS is fixed width in UTC, so the text comparison in AcquireCPLease is a time
// comparison on both dialects.
func cpLeaseTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }

// AcquireCPLease is one statement, so two CPs racing for an expired lease cannot both win:
// the conflict's WHERE decides, and the loser's upsert writes nothing.
func (s *SQL) AcquireCPLease(ctx context.Context, name, holder string, now, until time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO cp_lease (name, holder, expires_at, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET holder=excluded.holder, expires_at=excluded.expires_at,
		updated_at=excluded.updated_at
		WHERE cp_lease.holder=excluded.holder OR cp_lease.expires_at<=excluded.updated_at`,
		name, holder, cpLeaseTS(until), cpLeaseTS(now))
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
