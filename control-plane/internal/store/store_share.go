package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type SessionShare struct {
	ID, TenantID, OwnerMembershipID, RecipientMembershipID string
	ScopeType, ScopeKey, Permission, CreatedAt, UpdatedAt  string
}

type SharedSessionCatalog struct {
	ID, WorkspaceID, OwnerMembershipID, Name, Kind, Dir, Repo string
	WorkingCopyID, Title, Label, CreatedAt, State, LastSeen   string
	Archived                                                  bool
	// Worktree/Parent drive the recipient-side project/worktree tree (docs/log/59).
	// Parent is set only for a worktree: the folder name of the parent (base) working copy.
	Worktree bool
	Parent   string
	// ParentWorkingCopyID is the parent (base) working copy's workingCopyId, set only for
	// a worktree. Repo sharing covers a whole project (the base plus its worktrees), so the
	// ACL decision is made on this id and NOT on the folder name (Parent): a folder can be
	// renamed, while workingCopyId is fixed to the generation of the working copy.
	ParentWorkingCopyID string
	// Branch is the branch the working copy currently has checked out (display only). A
	// worktree's folder name is a random slug, so without this the recipient cannot tell
	// which piece of work a row is.
	Branch string
	// Activity is the Agent's live state (working | idle | question | plan | permission |
	// blocked | compacting). Held separately from State, which only says running/stopped
	// (alive or not). Empty while stopped.
	Activity string
}

type SessionShareProposal struct {
	ID, TenantID, CatalogID, OwnerMembershipID, ProposerMembershipID string
	Action, Ciphertext, KeyRef, Status                               string
	CreatedAt, ExpiresAt, DecidedAt, DecidedBy                       string
}

type SessionShareStore interface {
	PutSessionShare(ctx context.Context, row SessionShare) error
	GetSessionShare(ctx context.Context, id string) (SessionShare, bool, error)
	ListSessionSharesByOwner(ctx context.Context, membershipID string) ([]SessionShare, error)
	ListSessionSharesByRecipient(ctx context.Context, membershipID string) ([]SessionShare, error)
	DeleteSessionShare(ctx context.Context, id, ownerMembershipID string) error
	DeleteSessionSharesByScope(ctx context.Context, ownerMembershipID, scopeType, scopeKey string) error
	UpdateSessionSharePermission(ctx context.Context, id, ownerMembershipID, permission, updatedAt string) (bool, error)
	ReplaceSharedSessionCatalog(ctx context.Context, workspaceID, ownerMembershipID string, rows []SharedSessionCatalog) error
	ApplySharedSessionCatalogDelta(ctx context.Context, workspaceID, ownerMembershipID string, upserts []SharedSessionCatalog, deleted []string) error
	GetSharedSessionCatalog(ctx context.Context, id string) (SharedSessionCatalog, bool, error)
	ListSharedSessionCatalogByOwner(ctx context.Context, membershipID string) ([]SharedSessionCatalog, error)
	CreateSessionShareProposal(ctx context.Context, row SessionShareProposal) error
	CreateSessionShareProposalLimited(ctx context.Context, row SessionShareProposal, maxPending int) (bool, error)
	GetSessionShareProposal(ctx context.Context, id string) (SessionShareProposal, bool, error)
	ListSessionShareProposalsByOwner(ctx context.Context, membershipID string) ([]SessionShareProposal, error)
	CountPendingSessionShareProposals(ctx context.Context, catalogID string) (int, error)
	ExpireSessionShareProposals(ctx context.Context, ownerMembershipID, now string) error
	TransitionSessionShareProposal(ctx context.Context, id, from, to, decidedBy, decidedAt string, clearBody bool) (bool, error)
	ClaimSessionShareProposal(ctx context.Context, id, ownerMembershipID, decidedBy, now, leaseUntil string) (SessionShareProposal, SharedSessionCatalog, string, error)
	FinalizeSessionShareProposal(ctx context.Context, id, ownerMembershipID, decidedBy, decidedAt string) (bool, error)
	// Handing a session over to another member (docs/log/77). It derives from the share
	// ACL, so it lives on the same sub-interface.
	CreateSessionHandoffOffer(ctx context.Context, row SessionHandoffOffer) (bool, error)
	GetSessionHandoffOffer(ctx context.Context, id string) (SessionHandoffOffer, bool, error)
	ListSessionHandoffOffersByOwner(ctx context.Context, membershipID string) ([]SessionHandoffOffer, error)
	ListSessionHandoffOffersByRecipient(ctx context.Context, membershipID string) ([]SessionHandoffOffer, error)
	TransitionSessionHandoffOffer(ctx context.Context, id, from, to, decidedAt, acceptedSessionName string) (bool, error)
	TakeSessionHandoffOffer(ctx context.Context, id, recipientMembershipID, token, now string) (bool, error)
	ConfirmSessionHandoffTake(ctx context.Context, id, recipientMembershipID, token, now string) (bool, error)
	ReleaseSessionHandoffOffer(ctx context.Context, id, recipientMembershipID, token string) (bool, error)
	ReleaseUnconfirmedSessionHandoffTake(ctx context.Context, id, recipientMembershipID, token string) (bool, error)
	ConfirmedSessionHandoffTake(ctx context.Context, id, recipientMembershipID string) (string, bool, error)
	GiveBackUnknownSessionHandoffTake(ctx context.Context, id, recipientMembershipID, token string, now time.Time) (bool, error)
	ListReceivedSessionHandoffOffers(ctx context.Context, membershipID string) ([]ReceivedHandoffOffer, error)
	AcceptSessionHandoffOffer(ctx context.Context, id, recipientMembershipID, token, decidedAt, acceptedSessionName string) (bool, error)
	ExpireSessionHandoffOffers(ctx context.Context, now string) ([]SessionHandoffOffer, error)
	AcquireSessionShareOwnerLease(ctx context.Context, ownerMembershipID, operationID, now, leaseUntil string) (bool, error)
	RenewSessionShareOwnerLease(ctx context.Context, ownerMembershipID, operationID, now, leaseUntil string) (bool, error)
	ReleaseSessionShareOwnerLease(ctx context.Context, ownerMembershipID, operationID string) error
}

var ErrSessionShareOwnerBusy = errors.New("session share owner has an Agent operation in progress")

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
		return ErrSessionShareOwnerBusy
	}
	return nil
}

func (s *SQL) AcquireSessionShareOwnerLease(ctx context.Context, owner, operationID, now, leaseUntil string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockSessionShareOwner(ctx, tx, owner); err != nil {
		return false, err
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, now); errors.Is(err, ErrSessionShareOwnerBusy) {
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

func (s *SQL) ReleaseSessionShareOwnerLease(ctx context.Context, owner, operationID string) error {
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

func (s *SQL) RenewSessionShareOwnerLease(ctx context.Context, owner, operationID, now, leaseUntil string) (bool, error) {
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

func (s *SQL) PutSessionShare(ctx context.Context, r SessionShare) error {
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
	if err = invalidateUnauthorizedShareDerivatives(ctx, tx, r.OwnerMembershipID, r.UpdatedAt); err != nil {
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

func (s *SQL) GetSessionShare(ctx context.Context, id string) (SessionShare, bool, error) {
	return scanSessionShare(s.db.QueryRowContext(ctx, `SELECT id,tenant_id,owner_membership_id,
		recipient_membership_id,scope_type,scope_key,permission,created_at,updated_at
		FROM session_share WHERE id=?`, id))
}

func (s *SQL) listSessionShares(ctx context.Context, col, id string) ([]SessionShare, error) {
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

func (s *SQL) ListSessionSharesByOwner(ctx context.Context, id string) ([]SessionShare, error) {
	return s.listSessionShares(ctx, "owner_membership_id", id)
}
func (s *SQL) ListSessionSharesByRecipient(ctx context.Context, id string) ([]SessionShare, error) {
	return s.listSessionShares(ctx, "recipient_membership_id", id)
}
func (s *SQL) DeleteSessionShare(ctx context.Context, id, owner string) error {
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
	if err = invalidateUnauthorizedShareDerivatives(ctx, tx, owner, NowTS()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQL) DeleteSessionSharesByScope(ctx context.Context, owner, scopeType, scopeKey string) error {
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
	if err = invalidateUnauthorizedShareDerivatives(ctx, tx, owner, NowTS()); err != nil {
		return err
	}
	return tx.Commit()
}

type sqlExecQuery interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// invalidateUnauthorizedShareDerivatives expires every outstanding promise derived from a
// share and wipes its body, immediately after the sharing rules changed. It runs in the
// same transaction as the owner's share change, which is what makes the ordering
// unambiguous (docs/log/59 §2): the ACL change first means the proposal/handoff expires,
// the approval first means the ACL change lands only after that side effect completed.
//
// There are two kinds of derivative, and they need DIFFERENT permissions: an RW proposal
// (recipient → owner) requires rw, while a handoff offer (owner → recipient) only needs
// the conversation to be readable, so ro is enough. They share one function because there
// are four call sites and adding only one of them breaks silently — the share is revoked
// yet the handoff still sits in the other person's inbox. A third kind goes here too.
//
// A handoff whose take is confirmed (its session create has reached the recipient's Agent,
// issue #2020) loses its body at once but keeps its status and take: expiring it would let
// the sender's own launch skip the withdraw while the recipient's session starts, the same
// work twice (ADR 0057 decision 6). The holder's accept settles it; its release expires it
// (ReleaseSessionHandoffOffer), and the inbox no longer lists it.
func invalidateUnauthorizedShareDerivatives(ctx context.Context, q sqlExecQuery, owner, now string) error {
	if _, err := q.ExecContext(ctx, `UPDATE session_handoff_offer
		SET status=CASE WHEN take_confirmed_at='' THEN 'expired' ELSE status END,
		    decided_at=CASE WHEN take_confirmed_at='' THEN ? ELSE decided_at END,
		    ciphertext=''
		WHERE owner_membership_id=? AND status='pending' AND NOT EXISTS (
			SELECT 1 FROM shared_session_catalog c JOIN session_share s
			  ON s.owner_membership_id=c.owner_membership_id
			 AND s.recipient_membership_id=session_handoff_offer.recipient_membership_id
			 AND ((s.scope_type='session' AND s.scope_key=c.name)
			   OR (s.scope_type IN ('repo','worktree') AND s.scope_key=c.working_copy_id)
			   OR (s.scope_type='repo' AND s.scope_key=c.parent_working_copy_id AND c.parent_working_copy_id<>''))
			WHERE c.id=session_handoff_offer.catalog_id
		)`, now, owner); err != nil {
		return err
	}
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

func (s *SQL) UpdateSessionSharePermission(ctx context.Context, id, owner, permission, updatedAt string) (bool, error) {
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
	if err = invalidateUnauthorizedShareDerivatives(ctx, tx, owner, updatedAt); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// catalogWriteBatch is how many rows one multi-row statement carries. 19 columns per row
// keeps a batch far below both SQLite's and Postgres' bind-parameter limits.
const catalogWriteBatch = 200

// catalogRowChanged reports whether the Agent's view of a session differs from its stored
// row in any column the sync maintains. id, created_at and owner are fixed at insert, and
// last_seen is the sync's own clock: comparing it would rewrite every row on every sync.
func catalogRowChanged(old, cur SharedSessionCatalog) bool {
	return old.Kind != cur.Kind || old.Dir != cur.Dir || old.Repo != cur.Repo ||
		old.WorkingCopyID != cur.WorkingCopyID || old.Title != cur.Title || old.Label != cur.Label ||
		old.State != cur.State || old.Archived != cur.Archived || old.Worktree != cur.Worktree ||
		old.Parent != cur.Parent || old.ParentWorkingCopyID != cur.ParentWorkingCopyID ||
		old.Branch != cur.Branch || old.Activity != cur.Activity
}

// ReplaceSharedSessionCatalog makes the stored catalog of one workspace equal to `in`.
//
// It runs under the owner's membership row lock, which every share change also takes, so its
// cost is latency on the owner's share dialogs. It therefore reads the stored rows once and
// writes only the difference, batched: an unchanged inventory is a handful of statements no
// matter how many sessions the member has (measured for 1,200 rows: 2,403 statements before,
// see TestReplaceSharedSessionCatalogWritesOnlyTheDiff). last_seen is stamped only on rows
// that are inserted or changed.
func (s *SQL) ReplaceSharedSessionCatalog(ctx context.Context, workspaceID, owner string, in []SharedSessionCatalog) error {
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
	rows, err := tx.QueryContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id,branch,activity
		FROM shared_session_catalog WHERE workspace_id=?`, workspaceID)
	if err != nil {
		return err
	}
	stored := map[string]SharedSessionCatalog{}
	for rows.Next() {
		r, _, err := scanCatalog(rows)
		if err != nil {
			rows.Close()
			return err
		}
		stored[r.Name] = r
	}
	// A read cut short must not pass for the full catalog: every row it missed would be
	// swept below as stale, its session-scoped share rules with it.
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	keep := make(map[string]bool, len(in))
	var write []SharedSessionCatalog
	for _, r := range in {
		if keep[r.Name] {
			continue // the first occurrence wins, as the per-row upsert did
		}
		keep[r.Name] = true
		if old, ok := stored[r.Name]; !ok || catalogRowChanged(old, r) {
			write = append(write, r)
		}
	}
	for len(write) > 0 {
		n := min(len(write), catalogWriteBatch)
		if err = upsertCatalogRows(ctx, tx, workspaceID, owner, write[:n]); err != nil {
			return err
		}
		write = write[n:]
	}
	var stale []string
	for name := range stored {
		if !keep[name] {
			stale = append(stale, name)
		}
	}
	if err = deleteCatalogNames(ctx, tx, workspaceID, owner, stale); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteCatalogNames removes catalog rows and the session-scoped share rules that point at
// them, batched.
func deleteCatalogNames(ctx context.Context, tx *sqlTx, workspaceID, owner string, stale []string) error {
	for len(stale) > 0 {
		n := min(len(stale), catalogWriteBatch)
		names, marks := make([]any, 0, n), make([]string, 0, n)
		for _, name := range stale[:n] {
			names = append(names, name)
			marks = append(marks, "?")
		}
		list := strings.Join(marks, ",")
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_share WHERE owner_membership_id=? AND scope_type='session' AND scope_key IN (`+list+`)`,
			append([]any{owner}, names...)...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shared_session_catalog WHERE workspace_id=? AND name IN (`+list+`)`,
			append([]any{workspaceID}, names...)...); err != nil {
			return err
		}
		stale = stale[n:]
	}
	return nil
}

// ApplySharedSessionCatalogDelta applies what changed in an owner's catalog since the last
// sync the caller applied: `upserts` are written (an existing row keeps its id, created_at and
// owner) and `deleted` names are removed with their session-scoped share rules. Everything not
// named is left as it is, which is the difference from ReplaceSharedSessionCatalog; the caller
// is responsible for knowing that the stored catalog already matched the Agent's before the
// delta (a full Replace first), and for falling back to Replace whenever it does not.
// It takes the same owner lock and idle check as Replace.
func (s *SQL) ApplySharedSessionCatalogDelta(ctx context.Context, workspaceID, owner string, upserts []SharedSessionCatalog, deleted []string) error {
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
	seen := make(map[string]bool, len(upserts))
	var write []SharedSessionCatalog
	for _, r := range upserts {
		if !seen[r.Name] {
			seen[r.Name] = true
			write = append(write, r)
		}
	}
	for len(write) > 0 {
		n := min(len(write), catalogWriteBatch)
		if err = upsertCatalogRows(ctx, tx, workspaceID, owner, write[:n]); err != nil {
			return err
		}
		write = write[n:]
	}
	var gone []string
	for _, name := range deleted {
		if !seen[name] { // a name both changed and deleted in one answer is not deleted
			gone = append(gone, name)
		}
	}
	if err = deleteCatalogNames(ctx, tx, workspaceID, owner, gone); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertCatalogRows writes one batch. A row that already exists keeps its id (proposals and
// handoff offers point at it), created_at and owner; the update list is the same column set
// catalogRowChanged compares, plus last_seen.
func upsertCatalogRows(ctx context.Context, tx *sqlTx, workspaceID, owner string, batch []SharedSessionCatalog) error {
	var q strings.Builder
	q.WriteString(`INSERT INTO shared_session_catalog
		(id,workspace_id,owner_membership_id,name,kind,dir,repo,working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id,branch,activity)
		VALUES `)
	args := make([]any, 0, len(batch)*19)
	for i, r := range batch {
		if i > 0 {
			q.WriteString(",")
		}
		q.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		archived, worktree := 0, 0
		if r.Archived {
			archived = 1
		}
		if r.Worktree {
			worktree = 1
		}
		args = append(args, r.ID, workspaceID, owner, r.Name, r.Kind, r.Dir, r.Repo, r.WorkingCopyID, r.Title,
			r.Label, r.CreatedAt, r.State, archived, r.LastSeen, worktree, r.Parent, r.ParentWorkingCopyID, r.Branch, r.Activity)
	}
	q.WriteString(` ON CONFLICT(workspace_id,name) DO UPDATE SET
		kind=excluded.kind,dir=excluded.dir,repo=excluded.repo,working_copy_id=excluded.working_copy_id,
		title=excluded.title,label=excluded.label,state=excluded.state,archived=excluded.archived,last_seen=excluded.last_seen,
		worktree=excluded.worktree,parent=excluded.parent,parent_working_copy_id=excluded.parent_working_copy_id,
		branch=excluded.branch,activity=excluded.activity`)
	_, err := tx.ExecContext(ctx, q.String(), args...)
	return err
}

func scanCatalog(row interface{ Scan(...any) error }) (SharedSessionCatalog, bool, error) {
	var r SharedSessionCatalog
	var archived, worktree int
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.OwnerMembershipID, &r.Name, &r.Kind, &r.Dir, &r.Repo,
		&r.WorkingCopyID, &r.Title, &r.Label, &r.CreatedAt, &r.State, &archived, &r.LastSeen, &worktree,
		&r.Parent, &r.ParentWorkingCopyID, &r.Branch, &r.Activity)
	if err == sql.ErrNoRows {
		return SharedSessionCatalog{}, false, nil
	}
	r.Archived = archived != 0
	r.Worktree = worktree != 0
	return r, err == nil, err
}
func (s *SQL) GetSharedSessionCatalog(ctx context.Context, id string) (SharedSessionCatalog, bool, error) {
	return scanCatalog(s.db.QueryRowContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id,branch,activity FROM shared_session_catalog WHERE id=?`, id))
}
func (s *SQL) ListSharedSessionCatalogByOwner(ctx context.Context, owner string) ([]SharedSessionCatalog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,owner_membership_id,name,kind,dir,repo,
		working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id,branch,activity FROM shared_session_catalog
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

func (s *SQL) CreateSessionShareProposal(ctx context.Context, r SessionShareProposal) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_share_proposal
		(id,tenant_id,catalog_id,owner_membership_id,proposer_membership_id,action,ciphertext,key_ref,status,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.TenantID, r.CatalogID, r.OwnerMembershipID, r.ProposerMembershipID,
		r.Action, r.Ciphertext, r.KeyRef, r.Status, r.CreatedAt, r.ExpiresAt)
	return err
}
func (s *SQL) CreateSessionShareProposalLimited(ctx context.Context, r SessionShareProposal, maxPending int) (bool, error) {
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
func (s *SQL) GetSessionShareProposal(ctx context.Context, id string) (SessionShareProposal, bool, error) {
	return scanProposal(s.db.QueryRowContext(ctx, `SELECT id,tenant_id,catalog_id,owner_membership_id,proposer_membership_id,
		action,ciphertext,key_ref,status,created_at,expires_at,decided_at,decided_by FROM session_share_proposal WHERE id=?`, id))
}
func (s *SQL) ListSessionShareProposalsByOwner(ctx context.Context, owner string) ([]SessionShareProposal, error) {
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
func (s *SQL) CountPendingSessionShareProposals(ctx context.Context, catalogID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_share_proposal WHERE catalog_id=? AND status='pending'`, catalogID).Scan(&n)
	return n, err
}
func (s *SQL) ExpireSessionShareProposals(ctx context.Context, owner, now string) error {
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
func (s *SQL) ClaimSessionShareProposal(ctx context.Context, id, owner, by, now, leaseUntil string) (SessionShareProposal, SharedSessionCatalog, string, error) {
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
		working_copy_id,title,label,created_at,state,archived,last_seen,worktree,parent,parent_working_copy_id,branch,activity FROM shared_session_catalog WHERE id=?`, p.CatalogID))
	if err != nil {
		return p, c, "", err
	}
	if !ok {
		return p, c, "not_found", nil
	}
	if err = ensureSessionShareOwnerIdle(ctx, tx, owner, ownerLeaseNowTS()); errors.Is(err, ErrSessionShareOwnerBusy) {
		return p, c, "busy", nil
	} else if err != nil {
		return p, c, "", err
	}
	// Portable row locks make an ACL/catalog mutation and this claim commit in a
	// deterministic order, but are released before the external Agent request.
	if _, err = tx.ExecContext(ctx, `UPDATE shared_session_catalog SET last_seen=last_seen WHERE id=?`, c.ID); err != nil {
		return p, c, "", err
	}
	// put rejects scope_key='', so a session directly under the base
	// (parent_working_copy_id='') can never match a repo rule by accident — the SQL
	// keeps the same test as effectivePermission.
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

func (s *SQL) FinalizeSessionShareProposal(ctx context.Context, id, owner, by, at string) (bool, error) {
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
func (s *SQL) TransitionSessionShareProposal(ctx context.Context, id, from, to, by, at string, clearBody bool) (bool, error) {
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
