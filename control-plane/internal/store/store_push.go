package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// PushSubscription is one browser install a member enabled Web Push on (#4750).
type PushSubscription struct {
	ID, MembershipID, Endpoint, P256dh, Auth string
	UserAgent, CreatedAt, LastOKAt           string
}

// PushSubscriptionID is the id a subscription is listed and removed by: derived from the
// endpoint, so the endpoint itself (a bearer URL for that browser) never has to leave the server.
func PushSubscriptionID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:8])
}

type PushSubscriptionStore interface {
	// AddPushSubscription stores sub for its membership unless that would give the membership more
	// than max endpoints; it reports whether it stored. Re-registering an endpoint the membership
	// already holds is not a new device and always succeeds. An endpoint held by another membership
	// moves to this one.
	AddPushSubscription(ctx context.Context, sub PushSubscription, max int) (bool, error)
	ListPushSubscriptions(ctx context.Context, membershipID string) ([]PushSubscription, error)
	// GetPushSubscription answers the row for endpoint if membershipID still holds it. A send
	// falls back to it when the member's status cannot be read, so it must not touch membership.
	GetPushSubscription(ctx context.Context, membershipID, endpoint string) (PushSubscription, bool, error)
	// GetPushSendTarget is GetPushSubscription with the membership's delivery context (TenantSlug
	// left empty) read in the same statement, so a send checks the device's owner, the member's
	// status and the text setting at one point in time. withText false leaves the tenant setting
	// out of the statement and answers IncludeText false.
	GetPushSendTarget(ctx context.Context, membershipID, endpoint string, withText bool) (PushSubscription, PushDelivery, bool, error)
	// DeletePushSubscription reports whether a row of that membership was removed.
	DeletePushSubscription(ctx context.Context, membershipID, id string) (bool, error)
	// DeletePushSubscriptionByEndpoint drops a subscription the push service says is gone. It is
	// scoped to the membership the send was made for: a row that moved to another member while
	// the send was in flight is not that subscription any more and must survive a stale result.
	DeletePushSubscriptionByEndpoint(ctx context.Context, membershipID, endpoint string) error
	// ReleasePushEndpoint drops the row for endpoint whoever holds it, but only when the caller
	// presents the subscription's own keys: the endpoint alone is not proof of owning the browser.
	ReleasePushEndpoint(ctx context.Context, endpoint, p256dh, auth string) error
	TouchPushSubscription(ctx context.Context, membershipID, endpoint, at string) error
	// GetPushPrefs reads the kinds and the origins (pushsrv.PrefOrigins) the member turned off for
	// push (none by default) in one statement, so a send sees one saved state. A list that cannot
	// be read counts as none, without touching the other.
	GetPushPrefs(ctx context.Context, membershipID string) (kinds, origins []string, err error)
	// PutPushPrefs replaces the kinds, and the origins unless origins is nil (they are then left
	// as saved), in one statement, and answers what is stored afterwards.
	PutPushPrefs(ctx context.Context, membershipID string, kinds []string, origins *[]string) (savedKinds, savedOrigins []string, err error)
	// PushDeliveryContext answers, for one send: whether the membership is still active, whether
	// its tenant allows a question's text in the push (default false), and the tenant's slug (the
	// name the Console selects a tenant by). An inactive or unknown membership is the zero value.
	PushDeliveryContext(ctx context.Context, membershipID string) (PushDelivery, error)
	GetTenantPushIncludeText(ctx context.Context, tenantID string) (bool, error)
	PutTenantPushIncludeText(ctx context.Context, tenantID, tenantSlug string, on bool, actorID string) error
}

// PushSubscribedMembershipStore is kept apart from PushSubscriptionStore because only the
// notification drain needs it, not the push sender that takes the narrower interface.
type PushSubscribedMembershipStore interface {
	// ListPushSubscribedMemberships answers every membership holding at least one subscription,
	// in one read: a sweep over all workspaces asks this once instead of once per workspace.
	ListPushSubscribedMemberships(ctx context.Context) ([]string, error)
}

// PushDelivery is what one push send needs to know about the receiving membership.
type PushDelivery struct {
	Active, IncludeText bool
	TenantSlug          string
}

const pushCols = `SELECT id, membership_id, endpoint, p256dh, auth, user_agent, created_at, last_ok_at FROM push_subscription`

// UpsertPushSubscription stores sub for its membership with no device cap; an endpoint already
// held by another membership moves to this one. The API goes through AddPushSubscription.
func (s *SQL) UpsertPushSubscription(ctx context.Context, p PushSubscription) error {
	return upsertPushSubscription(ctx, s.db, p)
}

// AddPushSubscription counts and writes in one transaction under the membership's row lock, so
// two registrations of the same member run one after the other and cannot both pass the count:
// as separate statements, N concurrent registrations could overshoot the cap by N-1. SQLite serialises through its single connection; on Postgres the row lock
// does, and the count starts after it, so READ COMMITTED sees every row the other one committed.
func (s *SQL) AddPushSubscription(ctx context.Context, p PushSubscription, max int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE membership SET id=id WHERE id=?`, p.MembershipID); err != nil {
		return false, err
	}
	// held is checked apart from the count so that a member already past max (rows from before the
	// cap was atomic) can still refresh a device it holds.
	var total, held int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN endpoint=? THEN 1 ELSE 0 END), 0)
		FROM push_subscription WHERE membership_id=?`, p.Endpoint, p.MembershipID).Scan(&total, &held); err != nil {
		return false, err
	}
	if held == 0 && total >= max {
		return false, nil
	}
	if err := upsertPushSubscription(ctx, tx, p); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func upsertPushSubscription(ctx context.Context, q sqlExecQuery, p PushSubscription) error {
	if p.ID == "" {
		p.ID = PushSubscriptionID(p.Endpoint)
	}
	if p.CreatedAt == "" {
		p.CreatedAt = NowTS()
	}
	_, err := q.ExecContext(ctx, `INSERT INTO push_subscription(id, membership_id, endpoint, p256dh, auth, user_agent, created_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(endpoint) DO UPDATE SET membership_id=excluded.membership_id,
		p256dh=excluded.p256dh, auth=excluded.auth, user_agent=excluded.user_agent`,
		p.ID, p.MembershipID, p.Endpoint, p.P256dh, p.Auth, p.UserAgent, p.CreatedAt)
	return err
}

func (s *SQL) ListPushSubscriptions(ctx context.Context, membershipID string) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, pushCols+` WHERE membership_id=? ORDER BY created_at, id`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var p PushSubscription
		if err := rows.Scan(&p.ID, &p.MembershipID, &p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent, &p.CreatedAt, &p.LastOKAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQL) ListPushSubscribedMemberships(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT membership_id FROM push_subscription`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *SQL) GetPushSubscription(ctx context.Context, membershipID, endpoint string) (PushSubscription, bool, error) {
	var p PushSubscription
	err := s.db.QueryRowContext(ctx, pushCols+` WHERE membership_id=? AND endpoint=?`, membershipID, endpoint).
		Scan(&p.ID, &p.MembershipID, &p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent, &p.CreatedAt, &p.LastOKAt)
	if err == sql.ErrNoRows {
		return PushSubscription{}, false, nil
	}
	return p, err == nil, err
}

func (s *SQL) GetPushSendTarget(ctx context.Context, membershipID, endpoint string, withText bool) (PushSubscription, PushDelivery, bool, error) {
	var p PushSubscription
	var active, inc int64
	text, join := `0`, ``
	if withText {
		text, join = `COALESCE(t.include_text, 0)`, `LEFT JOIN tenant_push_setting t ON t.tenant_id = m.tenant_id`
	}
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.membership_id, s.endpoint, s.p256dh, s.auth, s.user_agent, s.created_at, s.last_ok_at,
		CASE WHEN m.status='active' THEN 1 ELSE 0 END, `+text+`
		FROM push_subscription s
		LEFT JOIN membership m ON m.id = s.membership_id
		`+join+`
		WHERE s.membership_id=? AND s.endpoint=?`, membershipID, endpoint).
		Scan(&p.ID, &p.MembershipID, &p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent, &p.CreatedAt, &p.LastOKAt, &active, &inc)
	if err == sql.ErrNoRows {
		return PushSubscription{}, PushDelivery{}, false, nil
	}
	if err != nil {
		return PushSubscription{}, PushDelivery{}, false, err
	}
	return p, PushDelivery{Active: active != 0, IncludeText: inc != 0}, true, nil
}

func (s *SQL) DeletePushSubscription(ctx context.Context, membershipID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM push_subscription WHERE membership_id=? AND id=?`, membershipID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *SQL) DeletePushSubscriptionByEndpoint(ctx context.Context, membershipID, endpoint string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscription WHERE membership_id=? AND endpoint=?`, membershipID, endpoint)
	return err
}

func (s *SQL) TouchPushSubscription(ctx context.Context, membershipID, endpoint, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_subscription SET last_ok_at=? WHERE membership_id=? AND endpoint=?`, at, membershipID, endpoint)
	return err
}

func (s *SQL) ReleasePushEndpoint(ctx context.Context, endpoint, p256dh, auth string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscription WHERE endpoint=? AND p256dh=? AND auth=?`, endpoint, p256dh, auth)
	return err
}

func (s *SQL) GetPushOffKinds(ctx context.Context, membershipID string) ([]string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT off_kinds FROM push_pref WHERE membership_id=?`, membershipID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	if json.Unmarshal([]byte(raw), &out) != nil {
		// An unreadable row must not silence the member's pushes for good, nor flood them: read it as
		// "nothing turned off" and let the next save repair it.
		return nil, nil
	}
	return out, nil
}

func (s *SQL) PutPushOffKinds(ctx context.Context, membershipID string, kinds []string) error {
	if kinds == nil {
		kinds = []string{}
	}
	b, _ := json.Marshal(kinds)
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_pref(membership_id, off_kinds, updated_at) VALUES(?,?,?)
		ON CONFLICT(membership_id) DO UPDATE SET off_kinds=excluded.off_kinds, updated_at=excluded.updated_at`,
		membershipID, string(b), NowTS())
	return err
}

func decodeOffList(raw string) []string {
	var out []string
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

func (s *SQL) GetPushPrefs(ctx context.Context, membershipID string) ([]string, []string, error) {
	var k, o string
	err := s.db.QueryRowContext(ctx, `SELECT off_kinds, off_origins FROM push_pref WHERE membership_id=?`, membershipID).Scan(&k, &o)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return decodeOffList(k), decodeOffList(o), nil
}

func (s *SQL) PutPushPrefs(ctx context.Context, membershipID string, kinds []string, origins *[]string) ([]string, []string, error) {
	if kinds == nil {
		kinds = []string{}
	}
	kb, _ := json.Marshal(kinds)
	ob := []byte("[]")
	set := `off_kinds=excluded.off_kinds, updated_at=excluded.updated_at`
	if origins != nil {
		if *origins != nil {
			ob, _ = json.Marshal(*origins)
		}
		set = `off_kinds=excluded.off_kinds, off_origins=excluded.off_origins, updated_at=excluded.updated_at`
	}
	var k, o string
	err := s.db.QueryRowContext(ctx, `INSERT INTO push_pref(membership_id, off_kinds, off_origins, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(membership_id) DO UPDATE SET `+set+` RETURNING off_kinds, off_origins`,
		membershipID, string(kb), string(ob), NowTS()).Scan(&k, &o)
	if err != nil {
		return nil, nil, err
	}
	return decodeOffList(k), decodeOffList(o), nil
}

func (s *SQL) PushDeliveryContext(ctx context.Context, membershipID string) (PushDelivery, error) {
	var inc sql.NullInt64
	var slug string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(t.include_text, 0), COALESCE(tn.slug, '') FROM membership m
		LEFT JOIN tenant tn ON tn.id = m.tenant_id
		LEFT JOIN tenant_push_setting t ON t.tenant_id = m.tenant_id
		WHERE m.id=? AND m.status='active'`, membershipID).Scan(&inc, &slug)
	if err == sql.ErrNoRows {
		return PushDelivery{}, nil
	}
	if err != nil {
		return PushDelivery{}, err
	}
	return PushDelivery{Active: true, IncludeText: inc.Int64 != 0, TenantSlug: slug}, nil
}

func (s *SQL) GetTenantPushIncludeText(ctx context.Context, tenantID string) (bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT include_text FROM tenant_push_setting WHERE tenant_id=?`, tenantID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v != 0, err
}

// PutTenantPushIncludeText stores the setting and its tenant.push_settings audit row in one
// transaction: an admin's change to what leaves through the push services must not stand
// without its audit row.
func (s *SQL) PutTenantPushIncludeText(ctx context.Context, tenantID, tenantSlug string, on bool, actorID string) error {
	v := 0
	if on {
		v = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tenant_push_setting(tenant_id, include_text, updated_by, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(tenant_id) DO UPDATE SET include_text=excluded.include_text, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		tenantID, v, actorID, NowTS()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at) VALUES(?, ?, 'user', ?, 'tenant.push_settings', ?, ?, ?)`,
		NewID(), tenantID, actorID, tenantSlug, fmt.Sprintf("include_text=%t", on), NowTS()); err != nil {
		return err
	}
	return tx.Commit()
}
