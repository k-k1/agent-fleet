package store

import (
	"context"
	"database/sql"
)

// DEKStore holds the envelope-encrypted per-workspace DEK (docs/15 P3-3).
type DEKStore interface {
	GetWrappedDEK(ctx context.Context, workspaceID string) (ciphertext, keyRef string, ok bool, err error)
	PutWrappedDEK(ctx context.Context, workspaceID, ciphertext, keyRef string) error
	// GetHomeDEK reads the random key of a member's home (store_home_dek.go).
	GetHomeDEK(ctx context.Context, membershipID string) (HomeDEK, bool, error)
	// InsertHomeDEK stores d unless the home already has a key, and returns the key the home
	// has afterwards, which is the other writer's when two raced.
	InsertHomeDEK(ctx context.Context, d HomeDEK) (HomeDEK, error)
	// ConfirmHomeDEK marks a migrating home 'random' once its Agent reported the store sealed
	// under its key; false when the row is not the one read (key, scheme, confirm epoch).
	ConfirmHomeDEK(ctx context.Context, read HomeDEK) (bool, error)
	// RemigrateHomeDEK puts a confirmed home back to 'migrating', so the derived key is
	// handed out beside its key again (a home restored from before its store moved).
	RemigrateHomeDEK(ctx context.Context, membershipID string) (bool, error)
	// CountHomeDEKs is the home_dek tally an operator checks the migration against.
	CountHomeDEKs(ctx context.Context) (HomeDEKCounts, error)
	// DeleteHomeDEK drops the key of a home proven gone for the holder of the membership's
	// lifecycle lease; conditional on the lease, the row read, the 'random' scheme and no
	// workspace row for the membership (store_home_dek.go).
	DeleteHomeDEK(ctx context.Context, read HomeDEK, leaseToken string) (bool, error)
	// ListOrphanHomeDEKs lists the keys no workspace row names, DeleteOrphanHomeDEK is the
	// operator's removal of one (any scheme, still only while no workspace names it).
	ListOrphanHomeDEKs(ctx context.Context) ([]OrphanHomeDEK, error)
	DeleteOrphanHomeDEK(ctx context.Context, membershipID string) (bool, error)
}

func (s *SQL) GetWrappedDEK(ctx context.Context, workspaceID string) (string, string, bool, error) {
	var ct, kr string
	err := s.db.QueryRowContext(ctx,
		`SELECT ciphertext, key_ref FROM wrapped_dek WHERE workspace_id=?`, workspaceID).Scan(&ct, &kr)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return ct, kr, true, nil
}

func (s *SQL) PutWrappedDEK(ctx context.Context, workspaceID, ciphertext, keyRef string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wrapped_dek(workspace_id, ciphertext, key_ref, key_version, created_at)
		 VALUES(?, ?, ?, 1, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET ciphertext=excluded.ciphertext, key_ref=excluded.key_ref`,
		workspaceID, ciphertext, keyRef, NowTS())
	return err
}
