package main

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var errSessionShareOwnerBusy = errors.New("session share owner has an Agent operation in progress")

func ownerLeaseNowTS() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func lockSessionShareOwner(ctx context.Context, q sqlExecQuery, owner string) error {
	_, err := q.ExecContext(ctx, `UPDATE membership SET id=id WHERE id=?`, owner)
	return err
}

func ensureSessionShareOwnerIdle(ctx context.Context, q sqlExecQuery, owner, now string) error {
	var active int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_share_owner_lease
		WHERE owner_membership_id=? AND expires_at>?`, owner, now).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errSessionShareOwnerBusy
	}
	return nil
}

func (s *sqlStore) AcquireSessionShareOwnerLease(ctx context.Context, owner, operationID, now, leaseUntil string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return false, err
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, now); errors.Is(err, errSessionShareOwnerBusy) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO session_share_owner_lease
		(owner_membership_id,operation_id,expires_at,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(owner_membership_id) DO UPDATE SET operation_id=excluded.operation_id,
		expires_at=excluded.expires_at,updated_at=excluded.updated_at`, owner, operationID, leaseUntil, now); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *sqlStore) ReleaseSessionShareOwnerLease(ctx context.Context, owner, operationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM session_share_owner_lease
		WHERE owner_membership_id=? AND operation_id=?`, owner, operationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlStore) RenewSessionShareOwnerLease(ctx context.Context, owner, operationID, now, leaseUntil string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE session_share_owner_lease
		SET expires_at=?,updated_at=? WHERE owner_membership_id=? AND operation_id=? AND expires_at>?`,
		leaseUntil, now, owner, operationID, now)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *sqlStore) PutSessionShare(ctx context.Context, r SessionShare) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, r.OwnerMembershipID); err != nil {
		return err
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, r.OwnerMembershipID, ownerLeaseNowTS()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_share
		(id,tenant_id,owner_membership_id,recipient_membership_id,scope_type,scope_key,permission,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_membership_id,recipient_membership_id,scope_type,scope_key)
		DO UPDATE SET permission=excluded.permission,updated_at=excluded.updated_at`,
		r.ID, r.TenantID, r.OwnerMembershipID, r.RecipientMembershipID, r.ScopeType, r.ScopeKey,
		r.Permission, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return err
	}
	if err = invalidateUnauthorizedProposals(ctx, tx, r.OwnerMembershipID, r.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func scanSessionShare(row interface{ Scan(...any) error }) (SessionShare, bool, error) {
	var r SessionShare
	err := row.Scan(&r.ID, &r.TenantID, &r.OwnerMembershipID, &r.RecipientMembershipID,
		&r.ScopeType, &r.ScopeKey, &r.Permission, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return SessionShare{}, false, nil
	}
	return r, err == nil, err
}

func (s *sqlStore) GetSessionShare(ctx context.Context, id string) (SessionShare, bool, error) {
	return scanSessionShare(s.db.QueryRowContext(ctx, `SELECT id,tenant_id,owner_membership_id,
		recipient_membership_id,scope_type,scope_key,permission,created_at,updated_at
		FROM session_share WHERE id=?`, id))
}

func (s *sqlStore) listSessionShares(ctx context.Context, col, id string) ([]SessionShare, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,tenant_id,owner_membership_id,
		recipient_membership_id,scope_type,scope_key,permission,created_at,updated_at
		FROM session_share WHERE `+col+`=? ORDER BY created_at DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionShare{}
	for rows.Next() {
		r, _, err := scanSessionShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqlStore) ListSessionSharesByOwner(ctx context.Context, id string) ([]SessionShare, error) {
	return s.listSessionShares(ctx, "owner_membership_id", id)
}
func (s *sqlStore) ListSessionSharesByRecipient(ctx context.Context, id string) ([]SessionShare, error) {
	return s.listSessionShares(ctx, "recipient_membership_id", id)
}
func (s *sqlStore) DeleteSessionShare(ctx context.Context, id, owner string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return err
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, ownerLeaseNowTS()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM session_share WHERE id=? AND owner_membership_id=?`, id, owner); err != nil {
		return err
	}
	if err = invalidateUnauthorizedProposals(ctx, tx, owner, nowTS()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *sqlStore) DeleteSessionSharesByScope(ctx context.Context, owner, scopeType, scopeKey string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return err
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, ownerLeaseNowTS()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM session_share WHERE owner_membership_id=? AND scope_type=? AND scope_key=?`, owner, scopeType, scopeKey); err != nil {
		return err
	}
	if err = invalidateUnauthorizedProposals(ctx, tx, owner, nowTS()); err != nil {
		return err
	}
	return tx.Commit()
}

type sqlExecQuery interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func invalidateUnauthorizedProposals(ctx context.Context, q sqlExecQuery, owner, now string) error {
	_, err := q.ExecContext(ctx, `UPDATE session_share_proposal SET status='expired',ciphertext='',decided_at=?
		WHERE owner_membership_id=? AND status IN ('pending','processing') AND NOT EXISTS (
			SELECT 1 FROM shared_session_catalog c JOIN session_share s
			  ON s.owner_membership_id=c.owner_membership_id
			 AND s.recipient_membership_id=session_share_proposal.proposer_membership_id
			 AND s.permission='rw'
			 AND ((s.scope_type='session' AND s.scope_key=c.name)
			   OR (s.scope_type IN ('repo','worktree') AND s.scope_key=c.working_copy_id)
			   OR (s.scope_type='repo' AND s.scope_key=c.parent_working_copy_id AND c.parent_working_copy_id<>''))
			WHERE c.id=session_share_proposal.catalog_id
		)`, now, owner)
	return err
}

func (s *sqlStore) UpdateSessionSharePermission(ctx context.Context, id, owner, permission, updatedAt string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return false, err
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, ownerLeaseNowTS()); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE session_share SET permission=?,updated_at=? WHERE id=? AND owner_membership_id=?`, permission, updatedAt, id, owner)
	if err != nil {
		return false, err
	}
	if err = invalidateUnauthorizedProposals(ctx, tx, owner, updatedAt); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *sqlStore) ReplaceSharedSessionCatalog(ctx context.Context, workspaceID, owner string, in []SharedSessionCatalog) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return err
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, ownerLeaseNowTS()); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, r := range in {
		keep[r.Name] = true
		var existing string
		_ = tx.QueryRowContext(ctx, `SELECT id FROM shared_session_catalog WHERE workspace_id=? AND name=?`, workspaceID, r.Name).Scan(&existing)
		if existing == "" {
			existing = r.ID
		}
		archived := 0
		if r.Archived {
			archived = 1
		}
		worktree := 0
		if r.Worktree {
			worktree = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO shared_session_catalog
			(id,workspace_id,owner_membership_id,name,kind,dir,repo,working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,name) DO UPDATE SET
			kind=excluded.kind,dir=excluded.dir,repo=excluded.repo,working_copy_id=excluded.working_copy_id,
			title=excluded.title,label=excluded.label,state=excluded.state,archived=excluded.archived,last_seen=excluded.last_seen,
			worktree=excluded.worktree,parent=excluded.parent,parent_working_copy_id=excluded.parent_working_copy_id`,
			existing, workspaceID, owner, r.Name, r.Kind, r.Dir, r.Repo, r.WorkingCopyID, r.Title,
			r.Label, r.CreatedAt, r.State, archived, r.LastSeen, worktree, r.Parent, r.ParentWorkingCopyID)
		if err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM shared_session_catalog WHERE workspace_id=?`, workspaceID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !keep[name] {
			stale = append(stale, name)
		}
	}
	rows.Close()
	for _, name := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_share WHERE owner_membership_id=? AND scope_type='session' AND scope_key=?`, owner, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shared_session_catalog WHERE workspace_id=? AND name=?`, workspaceID, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanCatalog(row interface{ Scan(...any) error }) (SharedSessionCatalog, bool, error) {
	var r SharedSessionCatalog
	var archived, worktree int
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.OwnerMembershipID, &r.Name, &r.Kind, &r.Dir, &r.Repo,
		&r.WorkingCopyID, &r.Title, &r.Label, &r.CreatedAt, &r.State, &archived, &r.LastSeen, &worktree,
		&r.Parent, &r.ParentWorkingCopyID)
	if err == sql.ErrNoRows {
		return SharedSessionCatalog{}, false, nil
	}
	r.Archived = archived != 0
	r.Worktree = worktree != 0
	return r, err == nil, err
}
func (s *sqlStore) GetSharedSessionCatalog(ctx context.Context, id string) (SharedSessionCatalog, bool, error) {
	return scanCatalog(s.db.QueryRowContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id FROM shared_session_catalog WHERE id=?`, id))
}
func (s *sqlStore) ListSharedSessionCatalogByOwner(ctx context.Context, owner string) ([]SharedSessionCatalog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id FROM shared_session_catalog
		WHERE owner_membership_id=? ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SharedSessionCatalog{}
	for rows.Next() {
		r, _, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqlStore) CreateSessionShareProposal(ctx context.Context, r SessionShareProposal) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_share_proposal
		(id,tenant_id,catalog_id,owner_membership_id,proposer_membership_id,action,ciphertext,key_ref,status,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.TenantID, r.CatalogID, r.OwnerMembershipID, r.ProposerMembershipID,
		r.Action, r.Ciphertext, r.KeyRef, r.Status, r.CreatedAt, r.ExpiresAt)
	return err
}
func (s *sqlStore) CreateSessionShareProposalLimited(ctx context.Context, r SessionShareProposal, maxPending int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Serialize creators per catalog on both backends. SQLite's first write takes
	// the database write lock; Postgres locks this catalog row until commit.
	if _, err = tx.ExecContext(ctx, `UPDATE shared_session_catalog SET last_seen=last_seen WHERE id=?`, r.CatalogID); err != nil {
		return false, err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_share_proposal WHERE catalog_id=? AND status='pending'`, r.CatalogID).Scan(&n); err != nil {
		return false, err
	}
	if n >= maxPending {
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO session_share_proposal
		(id,tenant_id,catalog_id,owner_membership_id,proposer_membership_id,action,ciphertext,key_ref,status,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.TenantID, r.CatalogID, r.OwnerMembershipID, r.ProposerMembershipID,
		r.Action, r.Ciphertext, r.KeyRef, r.Status, r.CreatedAt, r.ExpiresAt); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
func scanProposal(row interface{ Scan(...any) error }) (SessionShareProposal, bool, error) {
	var r SessionShareProposal
	err := row.Scan(&r.ID, &r.TenantID, &r.CatalogID, &r.OwnerMembershipID, &r.ProposerMembershipID, &r.Action,
		&r.Ciphertext, &r.KeyRef, &r.Status, &r.CreatedAt, &r.ExpiresAt, &r.DecidedAt, &r.DecidedBy)
	if err == sql.ErrNoRows {
		return SessionShareProposal{}, false, nil
	}
	return r, err == nil, err
}
func (s *sqlStore) GetSessionShareProposal(ctx context.Context, id string) (SessionShareProposal, bool, error) {
	return scanProposal(s.db.QueryRowContext(ctx, `SELECT id,tenant_id,catalog_id,owner_membership_id,proposer_membership_id,
		action,ciphertext,key_ref,status,created_at,expires_at,decided_at,decided_by FROM session_share_proposal WHERE id=?`, id))
}
func (s *sqlStore) ListSessionShareProposalsByOwner(ctx context.Context, owner string) ([]SessionShareProposal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,tenant_id,catalog_id,owner_membership_id,proposer_membership_id,
		action,ciphertext,key_ref,status,created_at,expires_at,decided_at,decided_by FROM session_share_proposal
		WHERE owner_membership_id=? ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionShareProposal{}
	for rows.Next() {
		r, _, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *sqlStore) CountPendingSessionShareProposals(ctx context.Context, catalogID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_share_proposal WHERE catalog_id=? AND status='pending'`, catalogID).Scan(&n)
	return n, err
}
func (s *sqlStore) ExpireSessionShareProposals(ctx context.Context, owner, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_share_proposal
		SET status='expired',ciphertext='',decided_at=?
		WHERE owner_membership_id=? AND expires_at<=? AND
		 (status='pending' OR (status='processing' AND NOT EXISTS (
			SELECT 1 FROM session_share_owner_lease l
			WHERE l.owner_membership_id=session_share_proposal.owner_membership_id AND l.expires_at>?
		 )))`, now, owner, now, ownerLeaseNowTS())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimSessionShareProposal uses a short transaction to linearize approval with
// ACL/catalog mutations, then commits processing before any Agent HTTP I/O. The
// proposal id is the durable Agent operation id, so a lost response is reconciled
// without repeating the side effect.
func (s *sqlStore) ClaimSessionShareProposal(ctx context.Context, id, owner, by, now, leaseUntil string) (SessionShareProposal, SharedSessionCatalog, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionShareProposal{}, SharedSessionCatalog{}, "", err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return SessionShareProposal{}, SharedSessionCatalog{}, "", err
	}
	p, ok, err := scanProposal(tx.QueryRowContext(ctx, `SELECT id,tenant_id,catalog_id,owner_membership_id,proposer_membership_id,
		action,ciphertext,key_ref,status,created_at,expires_at,decided_at,decided_by FROM session_share_proposal WHERE id=?`, id))
	if err != nil {
		return p, SharedSessionCatalog{}, "", err
	}
	if !ok || p.OwnerMembershipID != owner {
		return p, SharedSessionCatalog{}, "not_found", nil
	}
	if p.Status != "pending" {
		return p, SharedSessionCatalog{}, p.Status, nil
	}
	if p.ExpiresAt <= now {
		if _, err = tx.ExecContext(ctx, `UPDATE session_share_proposal SET status='expired',ciphertext='',decided_at=? WHERE id=? AND status='pending'`, now, id); err != nil {
			return p, SharedSessionCatalog{}, "", err
		}
		if err = tx.Commit(); err != nil {
			return p, SharedSessionCatalog{}, "", err
		}
		return p, SharedSessionCatalog{}, "expired", nil
	}
	c, ok, err := scanCatalog(tx.QueryRowContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id FROM shared_session_catalog WHERE id=?`, p.CatalogID))
	if err != nil {
		return p, c, "", err
	}
	if !ok {
		return p, c, "not_found", nil
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, ownerLeaseNowTS()); errors.Is(err, errSessionShareOwnerBusy) {
		return p, c, "busy", nil
	} else if err != nil {
		return p, c, "", err
	}
	// Portable row locks make an ACL/catalog mutation and this claim commit in a
	// deterministic order, but are released before the external Agent request.
	if _, err = tx.ExecContext(ctx, `UPDATE shared_session_catalog SET last_seen=last_seen WHERE id=?`, c.ID); err != nil {
		return p, c, "", err
	}
	// scope_key='' は put が拒否するので、ベース直下のセッション(parent_working_copy_id='')が
	// repo 規則へ誤って一致することはない — effectivePermission と同じ判定を SQL 側でも保つ。
	if _, err = tx.ExecContext(ctx, `UPDATE session_share SET updated_at=updated_at
		WHERE owner_membership_id=? AND recipient_membership_id=? AND permission='rw'
		  AND ((scope_type='session' AND scope_key=?) OR (scope_type IN ('repo','worktree') AND scope_key=?)
		    OR (scope_type='repo' AND scope_key=? AND scope_key<>''))`,
		owner, p.ProposerMembershipID, c.Name, c.WorkingCopyID, c.ParentWorkingCopyID); err != nil {
		return p, c, "", err
	}
	var authorized int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_share WHERE owner_membership_id=? AND recipient_membership_id=? AND permission='rw'
		AND ((scope_type='session' AND scope_key=?) OR (scope_type IN ('repo','worktree') AND scope_key=?)
		  OR (scope_type='repo' AND scope_key=? AND scope_key<>''))`,
		owner, p.ProposerMembershipID, c.Name, c.WorkingCopyID, c.ParentWorkingCopyID).Scan(&authorized)
	if err != nil {
		return p, c, "", err
	}
	if authorized == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE session_share_proposal SET status='expired',ciphertext='',decided_at=? WHERE id=? AND status='pending'`, now, id); err != nil {
			return p, c, "", err
		}
		if err = tx.Commit(); err != nil {
			return p, c, "", err
		}
		return p, c, "expired", nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO session_share_owner_lease
		(owner_membership_id,operation_id,expires_at,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(owner_membership_id) DO UPDATE SET operation_id=excluded.operation_id,
		expires_at=excluded.expires_at,updated_at=excluded.updated_at`, owner, id, leaseUntil, now); err != nil {
		return p, c, "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE session_share_proposal SET status='processing',decided_by=?,decided_at=? WHERE id=? AND status='pending'`, by, now, id)
	if err != nil {
		return p, c, "", err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return p, c, "processing", err
	}
	if err = tx.Commit(); err != nil {
		return p, c, "", err
	}
	p.Status = "processing"
	p.DecidedBy = by
	p.DecidedAt = now
	return p, c, "claimed", nil
}

func (s *sqlStore) FinalizeSessionShareProposal(ctx context.Context, id, owner, by, at string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE session_share_proposal
		SET status='approved',ciphertext='',decided_by=?,decided_at=?
		WHERE id=? AND owner_membership_id=? AND status='processing'`, by, at, id, owner)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM session_share_owner_lease
		WHERE owner_membership_id=? AND operation_id=?`, owner, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
func (s *sqlStore) TransitionSessionShareProposal(ctx context.Context, id, from, to, by, at string, clearBody bool) (bool, error) {
	body := "ciphertext"
	if clearBody {
		body = "''"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE session_share_proposal SET status=?,decided_by=?,decided_at=?,ciphertext=`+body+` WHERE id=? AND status=?`, to, by, at, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
