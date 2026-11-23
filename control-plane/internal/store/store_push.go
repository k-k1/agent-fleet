package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	// UpsertPushSubscription stores sub for its membership. An endpoint already held by another
	// membership moves to this one.
	UpsertPushSubscription(ctx context.Context, sub PushSubscription) error
	ListPushSubscriptions(ctx context.Context, membershipID string) ([]PushSubscription, error)
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
	// GetPushOffKinds returns the notification kinds the member turned off for push (none by default).
	GetPushOffKinds(ctx context.Context, membershipID string) ([]string, error)
	PutPushOffKinds(ctx context.Context, membershipID string, kinds []string) error
	// PushDeliveryContext answers, for one send: whether the membership is still active, whether
	// its tenant allows a question's text in the push (default false), and the tenant's slug (the
	// name the Console selects a tenant by). An inactive or unknown membership is the zero value.
	PushDeliveryContext(ctx context.Context, membershipID string) (PushDelivery, error)
	GetTenantPushIncludeText(ctx context.Context, tenantID string) (bool, error)
	PutTenantPushIncludeText(ctx context.Context, tenantID string, on bool, updatedBy string) error
}

// PushDelivery is what one push send needs to know about the receiving membership.
type PushDelivery struct {
	Active, IncludeText bool
	TenantSlug          string
}

const pushCols = `SELECT id, membership_id, endpoint, p256dh, auth, user_agent, created_at, last_ok_at FROM push_subscription`

func (s *SQL) UpsertPushSubscription(ctx context.Context, p PushSubscription) error {
	if p.ID == "" {
		p.ID = PushSubscriptionID(p.Endpoint)
	}
	if p.CreatedAt == "" {
		p.CreatedAt = NowTS()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_subscription(id, membership_id, endpoint, p256dh, auth, user_agent, created_at)
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

func (s *SQL) PutTenantPushIncludeText(ctx context.Context, tenantID string, on bool, updatedBy string) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_push_setting(tenant_id, include_text, updated_by, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(tenant_id) DO UPDATE SET include_text=excluded.include_text, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		tenantID, v, updatedBy, NowTS())
	return err
}
