package main

import (
	"context"
	"database/sql"
)

func (s *sqlStore) PutSessionShare(ctx context.Context, r SessionShare) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_share
		(id,tenant_id,owner_membership_id,recipient_membership_id,scope_type,scope_key,permission,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_membership_id,recipient_membership_id,scope_type,scope_key)
		DO UPDATE SET permission=excluded.permission,updated_at=excluded.updated_at`,
		r.ID, r.TenantID, r.OwnerMembershipID, r.RecipientMembershipID, r.ScopeType, r.ScopeKey,
		r.Permission, r.CreatedAt, r.UpdatedAt)
	return err
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_share WHERE id=? AND owner_membership_id=?`, id, owner)
	return err
}
func (s *sqlStore) DeleteSessionSharesByScope(ctx context.Context, owner, scopeType, scopeKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_share WHERE owner_membership_id=? AND scope_type=? AND scope_key=?`, owner, scopeType, scopeKey)
	return err
}

func (s *sqlStore) ReplaceSharedSessionCatalog(ctx context.Context, workspaceID, owner string, in []SharedSessionCatalog) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
		_, err = tx.ExecContext(ctx, `INSERT INTO shared_session_catalog
			(id,workspace_id,owner_membership_id,name,kind,dir,repo,working_copy_id,title,label,created_at,state,archived,last_seen)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,name) DO UPDATE SET
			kind=excluded.kind,dir=excluded.dir,repo=excluded.repo,working_copy_id=excluded.working_copy_id,
			title=excluded.title,label=excluded.label,state=excluded.state,archived=excluded.archived,last_seen=excluded.last_seen`,
			existing, workspaceID, owner, r.Name, r.Kind, r.Dir, r.Repo, r.WorkingCopyID, r.Title,
			r.Label, r.CreatedAt, r.State, archived, r.LastSeen)
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
	var archived int
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.OwnerMembershipID, &r.Name, &r.Kind, &r.Dir, &r.Repo,
		&r.WorkingCopyID, &r.Title, &r.Label, &r.CreatedAt, &r.State, &archived, &r.LastSeen)
	if err == sql.ErrNoRows {
		return SharedSessionCatalog{}, false, nil
	}
	r.Archived = archived != 0
	return r, err == nil, err
}
func (s *sqlStore) GetSharedSessionCatalog(ctx context.Context, id string) (SharedSessionCatalog, bool, error) {
	return scanCatalog(s.db.QueryRowContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen FROM shared_session_catalog WHERE id=?`, id))
}
func (s *sqlStore) ListSharedSessionCatalogByOwner(ctx context.Context, owner string) ([]SharedSessionCatalog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen FROM shared_session_catalog
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
	_, err := s.db.ExecContext(ctx, `UPDATE session_share_proposal
		SET status='expired',ciphertext='',decided_at=?
		WHERE owner_membership_id=? AND status='pending' AND expires_at<=?`, now, owner, now)
	return err
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
