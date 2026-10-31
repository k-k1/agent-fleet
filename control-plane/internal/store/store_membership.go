package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Membership joins an Identity to a Tenant with a tenant-scoped role
// (member | tenant_admin).
type Membership struct {
	ID, IdentityID, TenantID, Role, Status, CreatedAt string
}

// MembershipView is a membership enriched with its tenant's slug/name, for the
// /api/tenants picker and per-request tenant resolution.
type MembershipView struct {
	MembershipID, TenantID, TenantSlug, TenantName, Role string
}

// MemberInfo is a tenant member for the admin UI (docs/12 P3-6 / admin).
type MemberInfo struct {
	MembershipID, UserKey, Email, IdentityRole, MemberRole string
}

type MembershipStore interface {
	ListMemberships(ctx context.Context, identityID string) ([]MembershipView, error)
	ListMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error)
	// ListRemovedMembersByTenant returns the tenant's DEACTIVATED memberships.
	// Deliberately a second method rather than a flag on the one above: every other
	// caller of that one (share targets, the MCP admin tools, the session overview)
	// reads it as "who is a member of this tenant" and must never be handed somebody
	// who was taken off the roster. The admin roster still needs them, because
	// offboarding runs remove → stop workspace → clean home and the last two steps
	// happen when the person is already off the list (docs/log/61 §61.10.6).
	ListRemovedMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error)
	EnsureMembership(ctx context.Context, identityID, tenantID, role string) (Membership, error)
	GetMembership(ctx context.Context, identityID, tenantID string) (Membership, bool, error)
	// GetMembershipByID resolves a membership (with its tenant slug + live role) by
	// its id — used by the internal-git smart-HTTP handler to map a git token back
	// to (tenant, role) on every request. ok=false when it is missing/inactive.
	GetMembershipByID(ctx context.Context, membershipID string) (MembershipView, bool, error)
	// GitTokenEpoch is the epoch the membership's internal git token is minted under
	// (issue #1199). Only the current epoch verifies, so the git face reads it live on
	// every request. ok=false when the membership is missing or inactive.
	GitTokenEpoch(ctx context.Context, membershipID string) (epoch int64, ok bool, err error)
	// BumpGitTokenEpoch advances the membership's git token epoch by one and returns the
	// new value, killing every token minted under an earlier one. It works on an inactive
	// membership too: re-inviting a removed person reactivates the same id, so rotating
	// before the re-invite is how the old token is kept dead. ok=false when no such row.
	BumpGitTokenEpoch(ctx context.Context, membershipID string) (epoch int64, ok bool, err error)
	// IdentityIDForMembership maps a membership id back to its owning identity id —
	// used by the internal memo-bridge flush to resolve the workspace runtime from a
	// memo token (which carries only the membership). ok=false when it is missing.
	IdentityIDForMembership(ctx context.Context, membershipID string) (string, bool, error)
	// SetMembershipRole changes a membership's tenant-scoped role (member |
	// tenant_admin). EnsureMembership only inserts, so this is the update path.
	SetMembershipRole(ctx context.Context, membershipID, role string) error
	// SetMembershipStatus deactivates (or restores) a membership. Offboarding is a
	// LOGICAL delete: the workspace, its home and its secrets survive, but every
	// path that resolves a membership already requires status='active', so the
	// person is locked out on the next request (docs/log/61 §61.10.6 / decision 22).
	// This is what offboarding means, and it stays the default: DeleteMembership
	// below is a second, deliberate step taken later.
	SetMembershipStatus(ctx context.Context, membershipID, status string) error
	// DeleteMembership removes the row itself and everything keyed to it
	// (membershipCascade). It is the last step of the clean-up sequence — remove →
	// destroy the workspace → delete the row — and it is irreversible.
	//
	// ★ It used to be "deliberately not offered", on the grounds that schedules, audit
	// rows and shares reference the membership id. Two of those three turned out not to
	// be reasons: the schedules and shares ARE this person's and go with them, and
	// audit_log never referenced a membership at all (its actor is an identity). What
	// the reason really protected is the HISTORY — the audit ledger, and the cost and
	// occupancy rows an admin may still have to answer for — so those are what is kept
	// (docs/log/61 §61.18).
	//
	// Callers must have deactivated the membership and destroyed its workspace first;
	// this does not release a home or any cloud resource.
	DeleteMembership(ctx context.Context, membershipID string) error
	// EmailHasActiveMembership reports whether the person at this address is on any
	// tenant's roster. This is the term decision 16 adds to the entry gate: being
	// invited is itself permission to reach the login, so an invite-run deployment
	// need not also maintain AF_OAUTH_ALLOWED_*.
	// ★ Matched on identity.email, NOT on sanitizeUser(email): since P1 a person may
	// keep a user_key that no longer derives from their current address.
	EmailHasActiveMembership(ctx context.Context, email string) (bool, error)
	// EmailHasActiveMembershipInTenant is the same question asked of ONE tenant. A
	// tenant-defined provider (docs/log/61 §61.11) may only admit that tenant's own
	// people, so its entry gate cannot use the deployment-wide answer above: being
	// on some other subsidiary's roster is not permission to use this subsidiary's
	// IdP (decision 32-3).
	EmailHasActiveMembershipInTenant(ctx context.Context, email, tenantID string) (bool, error)
	// AnyActiveMembership reports whether the deployment has any roster at all —
	// used by the startup warning, which must no longer claim "every login is
	// denied" on a deployment that runs purely on invitations.
	AnyActiveMembership(ctx context.Context) (bool, error)
	// MembershipOwnerName resolves a membership to a human display label (email, or
	// the user key) — e.g. for stamping on an LFS lock. "" when it can't be resolved.
	MembershipOwnerName(ctx context.Context, membershipID string) (string, error)
}

func (s *SQL) ListMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error) {
	return s.listMembersByStatus(ctx, tenantID, "active")
}

// ListRemovedMembersByTenant is the offboarded half — see the interface comment.
func (s *SQL) ListRemovedMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error) {
	return s.listMembersByStatus(ctx, tenantID, "inactive")
}

func (s *SQL) listMembersByStatus(ctx context.Context, tenantID, status string) ([]MemberInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, i.user_key, i.email, i.role, m.role
		 FROM membership m JOIN identity i ON i.id = m.identity_id
		 WHERE m.tenant_id=? AND m.status=? ORDER BY i.user_key`, tenantID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemberInfo
	for rows.Next() {
		var mi MemberInfo
		if err := rows.Scan(&mi.MembershipID, &mi.UserKey, &mi.Email, &mi.IdentityRole, &mi.MemberRole); err != nil {
			return nil, err
		}
		out = append(out, mi)
	}
	return out, rows.Err()
}

func (s *SQL) ListMemberships(ctx context.Context, identityID string) ([]MembershipView, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, m.tenant_id, t.slug, t.name, m.role
		 FROM membership m JOIN tenant t ON t.id = m.tenant_id
		 WHERE m.identity_id=? AND m.status='active'
		 ORDER BY t.slug`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MembershipView
	for rows.Next() {
		var v MembershipView
		if err := rows.Scan(&v.MembershipID, &v.TenantID, &v.TenantSlug, &v.TenantName, &v.Role); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *SQL) GetMembershipByID(ctx context.Context, membershipID string) (MembershipView, bool, error) {
	var v MembershipView
	err := s.db.QueryRowContext(ctx,
		`SELECT m.id, m.tenant_id, t.slug, t.name, m.role
		 FROM membership m JOIN tenant t ON t.id = m.tenant_id
		 WHERE m.id=? AND m.status='active'`, membershipID).
		Scan(&v.MembershipID, &v.TenantID, &v.TenantSlug, &v.TenantName, &v.Role)
	if err == sql.ErrNoRows {
		return MembershipView{}, false, nil
	}
	if err != nil {
		return MembershipView{}, false, err
	}
	return v, true, nil
}

func (s *SQL) GitTokenEpoch(ctx context.Context, membershipID string) (int64, bool, error) {
	var epoch int64
	err := s.db.QueryRowContext(ctx,
		`SELECT git_token_epoch FROM membership WHERE id=? AND status='active'`, membershipID).Scan(&epoch)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return epoch, true, nil
}

func (s *SQL) BumpGitTokenEpoch(ctx context.Context, membershipID string) (int64, bool, error) {
	// One statement, so two concurrent rotations each move the epoch rather than both
	// writing the same next value.
	var epoch int64
	err := s.db.QueryRowContext(ctx,
		`UPDATE membership SET git_token_epoch = git_token_epoch + 1 WHERE id=? RETURNING git_token_epoch`,
		membershipID).Scan(&epoch)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return epoch, true, nil
}

func (s *SQL) IdentityIDForMembership(ctx context.Context, membershipID string) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT identity_id FROM membership WHERE id=? AND status='active'`, membershipID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// EnsureMembership inserts the membership and leaves an existing row untouched —
// including its status.
//
// ★ It must NOT reactivate. It is called from the auto-provisioning paths
// (auto_join_domains, AF_PROVISION=auto), which run on every login of a person who
// currently has no active membership; reactivating there would undo an
// administrator's removal the next time the person opened the page, which is
// precisely the offboarding docs/log/61 §61.10.6 exists to make work. Coming back onto
// a roster is an explicit act — the invite API reactivates deliberately
// (adminAPI.addMembership).
//
// A NEW default-tenant membership is refused when the identity's user key would share a
// directory under the data root (checkDefaultMemberKeyFree); an existing row is returned
// as it is, so a person who already has such a home keeps it.
func (s *SQL) EnsureMembership(ctx context.Context, identityID, tenantID, role string) (Membership, error) {
	if m, ok, err := s.GetMembership(ctx, identityID, tenantID); err != nil {
		return Membership{}, err
	} else if ok {
		return m, nil
	}
	if err := s.checkDefaultMemberKeyFree(ctx, identityID, tenantID); err != nil {
		return Membership{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO membership(id, identity_id, tenant_id, role, status, created_at)
		 VALUES(?, ?, ?, ?, 'active', ?) ON CONFLICT(identity_id, tenant_id) DO NOTHING`,
		NewID(), identityID, tenantID, role, NowTS()); err != nil {
		return Membership{}, err
	}
	var m Membership
	err := s.db.QueryRowContext(ctx,
		`SELECT id, identity_id, tenant_id, role, status, created_at FROM membership
		 WHERE identity_id=? AND tenant_id=?`, identityID, tenantID).
		Scan(&m.ID, &m.IdentityID, &m.TenantID, &m.Role, &m.Status, &m.CreatedAt)
	return m, err
}

func (s *SQL) GetMembership(ctx context.Context, identityID, tenantID string) (Membership, bool, error) {
	var m Membership
	err := s.db.QueryRowContext(ctx,
		`SELECT id, identity_id, tenant_id, role, status, created_at FROM membership
		 WHERE identity_id=? AND tenant_id=?`, identityID, tenantID).
		Scan(&m.ID, &m.IdentityID, &m.TenantID, &m.Role, &m.Status, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return Membership{}, false, nil
	}
	if err != nil {
		return Membership{}, false, err
	}
	return m, true, nil
}

func (s *SQL) SetMembershipRole(ctx context.Context, membershipID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE membership SET role=? WHERE id=?`, role, membershipID)
	return err
}

// SetMembershipStatus sets a membership's status. Deactivating also applies, in the same
// transaction, what leaving does to the member inbox (inboxMembershipInactiveTx), so the two
// can neither be separated by a crash nor undone by a re-invite.
func (s *SQL) SetMembershipStatus(ctx context.Context, membershipID, status string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE membership SET status=? WHERE id=?`, status, membershipID); err != nil {
		return err
	}
	if status != "active" {
		if err := inboxMembershipInactiveTx(ctx, tx, membershipID, NowTS()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// membershipCascade is every row keyed to one membership, in the order they must be
// deleted. It is a function rather than a constant because two statements need the id
// twice (a share names both ends).
//
// The dependents are listed EXPLICITLY rather than left to ON DELETE CASCADE, for the
// reason DeleteWorkspace gives: only a few of these tables declare one, and a delete that
// half-works because of a schema detail shows up as a foreign-key error in production and
// nowhere in tests.
//
// ★ What is NOT here is the point of the operation (docs/log/61 §61.18):
//
//   - audit_log — it has no membership_id at all; the actor is an identity
//     (0007_audit.sql). Offboarding must not be able to erase its own record.
//   - cloud_cost_daily / usage_daily — the money and the occupancy already happened.
//     memberCloudCost looks them up by membership id alone precisely so that spend
//     survives the workspace being destroyed (cloudcost.go); deleting them would change
//     last month's total after the fact.
//   - identity — the person may be on another tenant's roster, and even when they are
//     not, audit rows point at them. A membership is a seat, not a person.
//
// Nor is `workspace`: the caller must have destroyed it first (its home and cloud
// resources are not this function's to release), and that is checked in the handler.
//
// ⚠️ Every table named here must exist in BOTH dialects. A list of literal table names is
// exactly as correct as the assumption that the two migration series agree, and they did
// not: `memo_category` had been added to migrations/0020 and never mirrored on the
// Postgres side, which would have meant a 500 in the middle of an irreversible delete.
// The mirror is migrations-pg/0030 and `TestSchemaDialectParity` now fails if the two ever
// diverge again — that guard is what lets this stay one flat list.
func membershipCascade(membershipID string) []struct {
	sql  string
	args []any
} {
	id := []any{membershipID}
	both := []any{membershipID, membershipID}
	return []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM user_limit WHERE membership_id=?`, id},
		{`DELETE FROM engine_access_grant WHERE membership_id=?`, id},
		{`DELETE FROM pat WHERE membership_id=?`, id},
		{`DELETE FROM mcp_oauth_token WHERE membership_id=?`, id},
		{`DELETE FROM mcp_oauth_grant WHERE membership_id=?`, id},
		{`DELETE FROM mcp_client_oauth_state WHERE membership_id=?`, id},
		// sso_session was dropped by 0011 (ssm_profile replaced it). Deleting from a
		// table that does not exist only fails at run time in SQLite, so list tables
		// that really exist and nothing else.
		//
		// Profiles before hosts: a host write locks its profile and then the host
		// (lockSSMProfile), so the other order deadlocks with an in-flight host PUT
		// (measured on Postgres: SQLSTATE 40P01, cascade aborted). In this order a host
		// write either commits before the profile DELETE takes its lock, and the host
		// DELETE below sees the host, or waits and then finds its profile gone.
		{`DELETE FROM ssm_profile WHERE membership_id=?`, id},
		{`DELETE FROM ssm_sign_in WHERE membership_id=?`, id},
		{`DELETE FROM ssm_host WHERE membership_id=?`, id},
		{`DELETE FROM gcp_profiles WHERE membership_id=?`, id},
		{`DELETE FROM schedule_run WHERE membership_id=?`, id},
		{`DELETE FROM schedule WHERE membership_id=?`, id},
		{`DELETE FROM memo WHERE membership_id=?`, id},
		{`DELETE FROM memo_category WHERE membership_id=?`, id},
		{`DELETE FROM work_item_cache WHERE membership_id=?`, id},
		{`DELETE FROM work_item_query WHERE membership_id=?`, id},
		{`DELETE FROM work_item_session WHERE membership_id=?`, id},
		{`DELETE FROM work_item_comment_gen WHERE membership_id=?`, id},
		{`DELETE FROM notification WHERE membership_id=?`, id},
		{`DELETE FROM notification_usage_state WHERE membership_id=?`, id},
		{`DELETE FROM notification_usage_epoch WHERE membership_id=?`, id},
		// The limit board (ADR 0111 decision 6). An audience names other memberships, so a
		// member leaving must also leave every audience they were named in.
		{`DELETE FROM limit_publication_member WHERE membership_id=? OR viewer_membership_id=?`, both},
		{`DELETE FROM limit_publication WHERE membership_id=?`, id},
		{`DELETE FROM limit_observation WHERE membership_id=?`, id},
		{`DELETE FROM limit_epoch WHERE membership_id=?`, id},
		{`DELETE FROM limit_session_state WHERE membership_id=?`, id},
		// A share has two ends: one of them going away ends the share, however alive
		// the other one is.
		{`DELETE FROM session_share_proposal WHERE owner_membership_id=? OR proposer_membership_id=?`, both},
		// A handoff (docs/log/77) has two ends as well, and the catalog's CASCADE only
		// cleans the owner side — delete on both ends explicitly or a vanished
		// recipient leaves its row behind.
		{`DELETE FROM session_handoff_offer WHERE owner_membership_id=? OR recipient_membership_id=?`, both},
		{`DELETE FROM session_share WHERE owner_membership_id=? OR recipient_membership_id=?`, both},
		{`DELETE FROM shared_session_catalog WHERE owner_membership_id=?`, id},
		{`DELETE FROM session_share_owner_lease WHERE owner_membership_id=?`, id},
		{`DELETE FROM workspace_stop_intent WHERE owner_membership_id=?`, id},
		{`DELETE FROM home_operation WHERE membership_id=?`, id},
		{`DELETE FROM member_ui_state WHERE membership_id=?`, id},
		// The member inbox (ADR 0111 decision 9): the member's deliveries, read cursor and
		// receive rules go, and so do rules other members hold about them. An item stays
		// while anybody else still takes part in it, and the last statement removes the
		// items nobody is left in, body and all. The item has no foreign key to membership,
		// so the remaining participants keep their copy.
		// Mailbox before participant rows: the lock order of store_inbox.go.
		{`DELETE FROM inbox_mailbox WHERE membership_id=?`, id},
		{`DELETE FROM inbox_session_origin WHERE membership_id=?`, id},
		{`DELETE FROM inbox_participant WHERE membership_id=?`, id},
		{`DELETE FROM inbox_sender_rule WHERE membership_id=? OR sender_membership_id=?`, both},
		{`DELETE FROM inbox_item WHERE NOT EXISTS (SELECT 1 FROM inbox_participant p WHERE p.item_id=inbox_item.id)`, nil},
		{`DELETE FROM inbox_attachment WHERE NOT EXISTS (SELECT 1 FROM inbox_item i WHERE i.id=inbox_attachment.item_id)`, nil},
		// The parent goes last: with foreign keys on, deleting it before its children
		// fails.
		{`DELETE FROM membership WHERE id=?`, id},
	}
}

// DeleteMembership removes a membership row and everything keyed to it. Irreversible,
// and only ever reached from the explicit admin operation, which has already established
// that the membership is inactive and its workspace destroyed (docs/log/61 §61.18).
func (s *SQL) DeleteMembership(ctx context.Context, membershipID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.deleteMembershipTx(ctx, tx, membershipID); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteMembershipTx is the body above, inside somebody else's transaction — the tenant
// delete runs it once per remaining membership so the whole tenant goes in one commit.
func (s *SQL) deleteMembershipTx(ctx context.Context, tx *sqlTx, membershipID string) error {
	// The membership row and then the member's inbox items are locked before the cascade
	// touches any inbox mailbox or participant row: the lock order of store_inbox.go, which a
	// send in flight to this member holds from its membership lock on (measured on Postgres:
	// deleting the mailbox first deadlocked against such a send, 40P01). The row itself is
	// deleted last, for the foreign keys.
	if err := lockMemberships(ctx, tx, []string{membershipID}); err != nil {
		return err
	}
	items, err := queryIDs(ctx, tx, `SELECT item_id FROM inbox_participant WHERE membership_id=? ORDER BY item_id`, membershipID)
	if err != nil {
		return err
	}
	for _, id := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE inbox_item SET updated_at=updated_at WHERE id=?`, id); err != nil {
			return err
		}
	}
	for _, stmt := range membershipCascade(membershipID) {
		if _, err := tx.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			return fmt.Errorf("%s: %w", stmt.sql, err)
		}
	}
	return nil
}

// EmailHasActiveMembership answers the entry gate's membership term (decision 16).
// LOWER() is applied to the COLUMN, not to the placeholder: Postgres cannot infer a
// type for LOWER($1) and errors, which is the same reason identityByEmail lowercases
// in Go.
func (s *SQL) EmailHasActiveMembership(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM membership m JOIN identity i ON i.id = m.identity_id
		 WHERE m.status='active' AND i.email <> '' AND LOWER(i.email) = ?
		 LIMIT 1`, email).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// EmailHasActiveMembershipInTenant is EmailHasActiveMembership narrowed to one
// tenant — the entry-gate term a tenant-defined provider is allowed to use
// (docs/log/61 §61.11.3-3). Being on ANOTHER tenant's roster says nothing about this
// subsidiary's IdP.
func (s *SQL) EmailHasActiveMembershipInTenant(ctx context.Context, email, tenantID string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || tenantID == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM membership m JOIN identity i ON i.id = m.identity_id
		 WHERE m.status='active' AND m.tenant_id = ? AND i.email <> '' AND LOWER(i.email) = ?
		 LIMIT 1`, tenantID, email).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *SQL) AnyActiveMembership(ctx context.Context) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM membership WHERE status='active' LIMIT 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *SQL) MembershipOwnerName(ctx context.Context, membershipID string) (string, error) {
	var email, key string
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(i.email,''), i.user_key FROM membership m JOIN identity i ON i.id = m.identity_id
		 WHERE m.id=?`, membershipID).Scan(&email, &key)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if email != "" {
		return email, nil
	}
	return key, nil
}
