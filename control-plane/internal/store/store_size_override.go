package store

import (
	"context"
	"database/sql"
)

// Override sources, in the order they are resolved: boost > auto > the stored choice (ADR 0139
// decisions 4 and 5). Only the boost has a writer today; the auto source is reserved so the
// recommender can store its result beside a live boost without touching it.
const (
	SizeOverrideBoost = "boost"
	SizeOverrideAuto  = "auto"
)

// SizeOverride is one stored override: the target box (class and the rung's declared MiB) and
// when it ends. It never replaces user_limit's stored choice.
type SizeOverride struct {
	MembershipID string
	Source       string
	SlotClass    string
	MemMiB       int64
	// ExpiresAt is RFC3339 UTC; "" means no end.
	ExpiresAt string
	CreatedAt string
}

// SizeOverrideStore is the per-membership override table (ADR 0139).
type SizeOverrideStore interface {
	GetSizeOverride(ctx context.Context, membershipID, source string) (SizeOverride, bool, error)
	PutSizeOverride(ctx context.Context, o SizeOverride) error
	DeleteSizeOverride(ctx context.Context, membershipID, source string) error
}

func (s *SQL) GetSizeOverride(ctx context.Context, membershipID, source string) (SizeOverride, bool, error) {
	var o SizeOverride
	err := s.db.QueryRowContext(ctx,
		`SELECT membership_id, source, slot_class, mem_mib, expires_at, created_at
		   FROM workspace_size_override WHERE membership_id=? AND source=?`, membershipID, source).
		Scan(&o.MembershipID, &o.Source, &o.SlotClass, &o.MemMiB, &o.ExpiresAt, &o.CreatedAt)
	if err == sql.ErrNoRows {
		return SizeOverride{}, false, nil
	}
	if err != nil {
		return SizeOverride{}, false, err
	}
	return o, true, nil
}

// PutSizeOverride upserts the (membership, source) row.
func (s *SQL) PutSizeOverride(ctx context.Context, o SizeOverride) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_size_override(membership_id, source, slot_class, mem_mib, expires_at, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(membership_id, source) DO UPDATE SET slot_class=excluded.slot_class,
		   mem_mib=excluded.mem_mib, expires_at=excluded.expires_at`,
		o.MembershipID, o.Source, o.SlotClass, o.MemMiB, o.ExpiresAt, NowTS())
	return err
}

func (s *SQL) DeleteSizeOverride(ctx context.Context, membershipID, source string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_size_override WHERE membership_id=? AND source=?`, membershipID, source)
	return err
}
