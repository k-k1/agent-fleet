package store

// Recipes, take-in and session attachments of the member inbox (ADR 0111 decisions 2-5,
// P1-A second half). The lock order of store_inbox.go holds here too: the take-in locks the
// item row before it touches any memo row, and a send that grants shares already holds its
// sender's membership row, which is the share owner lock.

import (
	"context"
	"database/sql"
	"errors"
)

// InboxAttachment is one typed attachment of an item. A recipe carries its sealed JSON in
// Ciphertext; a session attachment carries the shared session's catalog id, and Granted when
// its share was created with the item.
type InboxAttachment struct {
	ID, ItemID, Kind, Ciphertext, KeyRef, CatalogID, CreatedAt string
	Granted                                                    bool
}

// InboxSendExtras is what a send creates beside the item, in its transaction.
type InboxSendExtras struct {
	Attachments []InboxAttachment
	// Shares are the session-scope share rules a session attachment grants. A rule that
	// already exists for the same owner, recipient and scope is left as it is, so a
	// read-write share is never narrowed by attaching the session.
	Shares []SessionShare
}

// InboxTakeIn reports a take-in: Allowed=false when the caller may not take this item in
// (a request they are not the assignee of), Created=false when they already had.
type InboxTakeIn struct {
	Allowed, Created bool
}

func insertInboxExtras(ctx context.Context, tx *sqlTx, it InboxItem, extras InboxSendExtras) error {
	for _, a := range extras.Attachments {
		g := 0
		if a.Granted {
			g = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO inbox_attachment(id,item_id,kind,ciphertext,key_ref,catalog_id,granted,created_at)
			VALUES(?,?,?,?,?,?,?,?)`, a.ID, it.ID, a.Kind, a.Ciphertext, a.KeyRef, a.CatalogID, g, it.CreatedAt); err != nil {
			return err
		}
	}
	if len(extras.Shares) == 0 {
		return nil
	}
	// The share owner lock is the owner's membership row, which the send locked first; the
	// lease check is the one PutSessionShare makes: no share change while an approved Agent
	// operation of that owner is running.
	if err := ensureSessionShareOwnerIdle(ctx, tx, it.SenderMembershipID, ownerLeaseNowTS()); err != nil {
		return err
	}
	for _, r := range extras.Shares {
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_share
			(id,tenant_id,owner_membership_id,recipient_membership_id,scope_type,scope_key,permission,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_membership_id,recipient_membership_id,scope_type,scope_key) DO NOTHING`,
			r.ID, r.TenantID, r.OwnerMembershipID, r.RecipientMembershipID, r.ScopeType, r.ScopeKey,
			r.Permission, r.CreatedAt, r.UpdatedAt); err != nil {
			return err
		}
	}
	return nil
}

// FindMemoByOrigin returns the memo membershipID took itemID in as, if any.
func (s *SQL) FindMemoByOrigin(ctx context.Context, itemID, membershipID string) (Memo, bool, error) {
	m, err := scanMemo(s.db.QueryRowContext(ctx, memoCols+` WHERE origin_item_id=? AND membership_id=?`, itemID, membershipID))
	if errors.Is(err, sql.ErrNoRows) {
		return Memo{}, false, nil
	}
	return m, err == nil, err
}

// InboxSessionOrigin is the CP's record of where one of a member's sessions came from.
type InboxSessionOrigin struct {
	ItemID, SenderKey string
}

// RecordInboxSessionOrigin writes the provenance of a session the CP just created. A session
// name is unique within a workspace, so a later create under the same name replaces it.
func (s *SQL) RecordInboxSessionOrigin(ctx context.Context, membershipID, sessionName, itemID, senderKey string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO inbox_session_origin(membership_id,session_name,item_id,sender_key,created_at)
		VALUES(?,?,?,?,?) ON CONFLICT(membership_id,session_name) DO UPDATE SET item_id=excluded.item_id,
		sender_key=excluded.sender_key,created_at=excluded.created_at`, membershipID, sessionName, itemID, senderKey, NowTS())
	return err
}

// ListInboxSessionOrigins maps a member's session names to their recorded provenance.
func (s *SQL) ListInboxSessionOrigins(ctx context.Context, membershipID string) (map[string]InboxSessionOrigin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_name,item_id,sender_key FROM inbox_session_origin WHERE membership_id=?`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]InboxSessionOrigin{}
	for rows.Next() {
		var name string
		var o InboxSessionOrigin
		if err := rows.Scan(&name, &o.ItemID, &o.SenderKey); err != nil {
			return nil, err
		}
		out[name] = o
	}
	return out, rows.Err()
}

func (s *SQL) ListInboxAttachments(ctx context.Context, itemID string) ([]InboxAttachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,item_id,kind,ciphertext,key_ref,catalog_id,granted,created_at
		FROM inbox_attachment WHERE item_id=? ORDER BY created_at, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxAttachment{}
	for rows.Next() {
		var a InboxAttachment
		var g int
		if err := rows.Scan(&a.ID, &a.ItemID, &a.Kind, &a.Ciphertext, &a.KeyRef, &a.CatalogID, &g, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Granted = g != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// TakeInInboxRecipe copies a recipe into membershipID's memo queue as m, with the origin the
// caller set on m. Everything that decides whether the caller may is checked here, under the
// locks a removal and a release take, in the lock order of store_inbox.go: the caller's
// membership row first (it must still be active in the item's tenant), then the item row (the
// caller must be one of its recipients; for a request, its current assignee while claimed or
// done). A removal or a release that commits first is seen; one that comes later orders after
// the take-in. Taking in again returns the memo already made: one memo per (item, member).
func (s *SQL) TakeInInboxRecipe(ctx context.Context, itemID, membershipID string, m Memo) (Memo, InboxTakeIn, error) {
	var out InboxTakeIn
	var memo Memo
	_, err := s.inboxTx(ctx, func(tx *sqlTx) (bool, error) {
		if err := lockMemberships(ctx, tx, []string{membershipID}); err != nil {
			return false, err
		}
		ok, err := affected(tx.ExecContext(ctx, `UPDATE inbox_item SET updated_at=updated_at
			WHERE id=? AND (kind='dm' OR (kind='request' AND state IN ('claimed','done') AND assignee_membership_id=?))
			  AND EXISTS (SELECT 1 FROM membership m WHERE m.id=? AND m.status='active' AND m.tenant_id=inbox_item.tenant_id)
			  AND EXISTS (SELECT 1 FROM inbox_participant p WHERE p.item_id=inbox_item.id AND p.membership_id=?
			              AND p.role='recipient')`,
			itemID, membershipID, membershipID, membershipID))
		if err != nil || !ok {
			return false, err
		}
		out.Allowed = true
		existing, err := scanMemo(tx.QueryRowContext(ctx, memoCols+` WHERE origin_item_id=? AND membership_id=?`, itemID, membershipID))
		if err == nil {
			memo = existing
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM memo
			WHERE membership_id=? AND repo=? AND category=?`, membershipID, m.Repo, m.Category).Scan(&m.Position); err != nil {
			return false, err
		}
		m.MembershipID, m.OriginItemID = membershipID, itemID
		// A taken-in memo's provenance is inbox whatever the caller set, and it enters as a
		// plain note: ADR 0137 refuses an inbox memo in a queue.
		m.Provenance, m.State, m.Producer, m.ProducerOrigin, m.ReleaseHash, m.ReleasedAt = MemoProvenanceInbox, "", "", "", "", ""
		if _, err := tx.ExecContext(ctx, `INSERT INTO memo(id, membership_id, repo, category, kind, body, ref_path, attachments,
			position, created_at, sent_at, origin_item_id, origin_sender_membership_id, origin_sender_key, provenance)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			m.ID, m.MembershipID, m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.CreatedAt, m.SentAt,
			m.OriginItemID, m.OriginSenderMembershipID, m.OriginSenderKey, m.Provenance); err != nil {
			return false, err
		}
		memo = m
		out.Created = true
		return true, nil
	})
	return memo, out, err
}
