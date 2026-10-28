package store

import (
	"context"
	"database/sql"
	"strings"
)

// SettingsStore is a small kv for deployment-wide toggles such as the egress
// mode (docs/log/20 M3). GetSetting returns "" when unset. ListSettingKeys /
// DeleteSetting exist for prefix-scoped cursors (claude audit) so stale keys
// don't accumulate forever.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	ListSettingKeys(ctx context.Context, prefix string) ([]string, error)
	DeleteSetting(ctx context.Context, key string) error
}

func (s *SQL) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM deployment_setting WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *SQL) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployment_setting(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ListSettingKeys returns keys starting with prefix. LIKE narrows the scan but
// `_` in the prefix is a LIKE wildcard, so the literal-prefix check in Go is
// what actually decides membership.
func (s *SQL) ListSettingKeys(ctx context.Context, prefix string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key FROM deployment_setting WHERE key LIKE ? ORDER BY key`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, rows.Err()
}

func (s *SQL) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_setting WHERE key=?`, key)
	return err
}
