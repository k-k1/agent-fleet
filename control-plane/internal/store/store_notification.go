package store

import (
	"context"
	"database/sql"
	"strings"
)

// Notification is a membership-scoped, content-free activity record shared by
// every browser the member uses. Payload contains only structured metadata; chat
// answer/question text is deliberately never persisted here.
type Notification struct {
	Seq                                                           int64
	EventID, MembershipID, Kind, TargetType, TargetID, TargetKind string
	DisplayName, Payload, CreatedAt, SeenAt                       string
}

type UsageNotificationState struct {
	MembershipID, Source, WindowKey, ResetsAt string
	Armed                                     int
}

type NotificationStore interface {
	InsertNotification(ctx context.Context, n Notification) error
	ListNotifications(ctx context.Context, membershipID, retainAfter string, limit int) ([]Notification, error)
	CountUnseenNotifications(ctx context.Context, membershipID, retainAfter string) (int, error)
	MarkNotificationsSeenThrough(ctx context.Context, membershipID string, seq int64, seenAt string) error
	MarkNotificationsSeen(ctx context.Context, membershipID string, eventIDs []string, seenAt string) error
	SweepNotifications(ctx context.Context, retainBefore string) error
	GetUsageNotificationState(ctx context.Context, membershipID, source, windowKey string) (UsageNotificationState, bool, error)
	PutUsageNotificationState(ctx context.Context, state UsageNotificationState) error
	// ApplyUsageObservation is the reset notification's whole read-decide-write for one post,
	// in one transaction fenced by the source's login generation (store_limit_board.go).
	ApplyUsageObservation(ctx context.Context, membershipID, source, epoch, epochSince string, windowKeys []string,
		decide func(windowKey string, prev UsageNotificationState, found bool) (UsageNotificationState, *Notification)) (accepted bool, err error)
}

// --- Notification center -------------------------------------------------------

const notificationCols = `SELECT seq, event_id, membership_id, kind, target_type, target_id, target_kind, display_name, payload, created_at, seen_at FROM notification`

func scanNotification(row scanner) (Notification, error) {
	var n Notification
	err := row.Scan(&n.Seq, &n.EventID, &n.MembershipID, &n.Kind, &n.TargetType,
		&n.TargetID, &n.TargetKind, &n.DisplayName, &n.Payload, &n.CreatedAt, &n.SeenAt)
	return n, err
}

func (s *SQL) InsertNotification(ctx context.Context, n Notification) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification(event_id, membership_id, kind, target_type, target_id, target_kind, display_name, payload, created_at, seen_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(event_id) DO NOTHING`, n.EventID, n.MembershipID,
		n.Kind, n.TargetType, n.TargetID, n.TargetKind, n.DisplayName, n.Payload, n.CreatedAt, n.SeenAt)
	return err
}

func (s *SQL) ListNotifications(ctx context.Context, membershipID, retainAfter string, limit int) ([]Notification, error) {
	rows, err := s.db.QueryContext(ctx, notificationCols+` WHERE membership_id=? AND created_at>=? ORDER BY seq DESC LIMIT ?`, membershipID, retainAfter, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SQL) CountUnseenNotifications(ctx context.Context, membershipID, retainAfter string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification WHERE membership_id=? AND created_at>=? AND seen_at=''`, membershipID, retainAfter).Scan(&n)
	return n, err
}

func (s *SQL) MarkNotificationsSeenThrough(ctx context.Context, membershipID string, seq int64, seenAt string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notification SET seen_at=? WHERE membership_id=? AND seq<=? AND seen_at=''`, seenAt, membershipID, seq)
	return err
}

func (s *SQL) MarkNotificationsSeen(ctx context.Context, membershipID string, eventIDs []string, seenAt string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	ph := make([]string, len(eventIDs))
	args := []any{seenAt, membershipID}
	for i, id := range eventIDs {
		ph[i] = "?"
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE notification SET seen_at=? WHERE membership_id=? AND event_id IN (`+strings.Join(ph, ",")+`)`, args...)
	return err
}

func (s *SQL) SweepNotifications(ctx context.Context, retainBefore string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notification WHERE created_at<?`, retainBefore)
	return err
}

func (s *SQL) GetUsageNotificationState(ctx context.Context, membershipID, source, windowKey string) (UsageNotificationState, bool, error) {
	var st UsageNotificationState
	err := s.db.QueryRowContext(ctx, `SELECT membership_id, source, window_key, resets_at, armed FROM notification_usage_state WHERE membership_id=? AND source=? AND window_key=?`, membershipID, source, windowKey).
		Scan(&st.MembershipID, &st.Source, &st.WindowKey, &st.ResetsAt, &st.Armed)
	if err == sql.ErrNoRows {
		return UsageNotificationState{}, false, nil
	}
	return st, err == nil, err
}

func (s *SQL) PutUsageNotificationState(ctx context.Context, st UsageNotificationState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_usage_state(membership_id, source, window_key, resets_at, armed) VALUES(?,?,?,?,?)
		ON CONFLICT(membership_id, source, window_key) DO UPDATE SET resets_at=excluded.resets_at, armed=excluded.armed`,
		st.MembershipID, st.Source, st.WindowKey, st.ResetsAt, st.Armed)
	return err
}
