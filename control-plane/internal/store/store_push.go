package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
