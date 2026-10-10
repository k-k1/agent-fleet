package store

import (
	"context"
	"database/sql"
	"errors"
)

// Home DEK schemes (migrations/0090_home_dek.sql).
const (
	HomeDEKMigrating = "migrating"
	HomeDEKRandom    = "random"
)

// HomeDEK is the random credential-store key of one member's home, sealed by the key
// custodian. Ciphertext is never logged.
type HomeDEK struct {
	MembershipID, Ciphertext, KeyRef, Scheme, CreatedAt, MigratedAt string
	// ConfirmEpoch is bumped by every RemigrateHomeDEK; a confirm must name the one it read.
	ConfirmEpoch int64
}

// HomeDEKCounts tallies home_dek against the workspaces: Migrating and Random are rows by
// scheme, WithoutKey the workspaces whose home has no row yet (never started since the
// deployment turned the random key on, or it is off).
type HomeDEKCounts struct {
	Migrating, Random, WithoutKey int
}

func (s *SQL) GetHomeDEK(ctx context.Context, membershipID string) (HomeDEK, bool, error) {
	d := HomeDEK{MembershipID: membershipID}
	err := s.db.QueryRowContext(ctx,
		`SELECT ciphertext, key_ref, scheme, created_at, migrated_at, confirm_epoch FROM home_dek WHERE membership_id=?`,
		membershipID).Scan(&d.Ciphertext, &d.KeyRef, &d.Scheme, &d.CreatedAt, &d.MigratedAt, &d.ConfirmEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return HomeDEK{}, false, nil
	}
	if err != nil {
		return HomeDEK{}, false, err
	}
	return d, true, nil
}

// InsertHomeDEK never replaces a stored key: the home's store may already be sealed under it,
// and overwriting it would make that store unreadable.
func (s *SQL) InsertHomeDEK(ctx context.Context, d HomeDEK) (HomeDEK, error) {
	if d.Scheme == "" {
		d.Scheme = HomeDEKMigrating
	}
	if d.CreatedAt == "" {
		d.CreatedAt = NowTS()
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO home_dek(membership_id, ciphertext, key_ref, scheme, created_at)
		 VALUES(?, ?, ?, ?, ?) ON CONFLICT(membership_id) DO NOTHING`,
		d.MembershipID, d.Ciphertext, d.KeyRef, d.Scheme, d.CreatedAt); err != nil {
		return HomeDEK{}, err
	}
	got, ok, err := s.GetHomeDEK(ctx, d.MembershipID)
	if err != nil {
		return HomeDEK{}, err
	}
	if !ok {
		return HomeDEK{}, errors.New("store: home_dek row vanished right after its insert")
	}
	return got, nil
}

func (s *SQL) CountHomeDEKs(ctx context.Context) (HomeDEKCounts, error) {
	var c HomeDEKCounts
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM home_dek WHERE scheme='migrating'),
		(SELECT COUNT(*) FROM home_dek WHERE scheme='random'),
		(SELECT COUNT(*) FROM workspace w WHERE NOT EXISTS
			(SELECT 1 FROM home_dek h WHERE h.membership_id = w.membership_id))`).
		Scan(&c.Migrating, &c.Random, &c.WithoutKey)
	return c, err
}

// ConfirmHomeDEK is conditioned on the row the start read: the sealed key, the scheme and the
// confirm epoch. A report about a key that is not the stored one, or one taken before a
// remigrate, can never confirm it.
func (s *SQL) ConfirmHomeDEK(ctx context.Context, read HomeDEK) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE home_dek SET scheme='random', migrated_at=?
		 WHERE membership_id=? AND ciphertext=? AND scheme='migrating' AND confirm_epoch=?`,
		NowTS(), read.MembershipID, read.Ciphertext, read.ConfirmEpoch)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *SQL) RemigrateHomeDEK(ctx context.Context, membershipID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE home_dek SET scheme='migrating', migrated_at='', confirm_epoch=confirm_epoch+1
		 WHERE membership_id=? AND scheme='random'`, membershipID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
