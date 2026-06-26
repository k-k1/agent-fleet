package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// sqliteStore is the default MetadataStore adapter (docs/12 P3-1). One embedded
// DB file per deployment fits the self-host model (CP = 1 process / 1 host).
type sqliteStore struct {
	db *sql.DB
}

// openSQLite opens the DB with the server-app pragmas (WAL + busy_timeout +
// foreign keys). MaxOpenConns(1) serializes access — simplest correct choice at
// this scale and avoids "database is locked".
func openSQLite(path string) (*sqliteStore, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

// migrate applies embedded numbered migrations idempotently via schema_migrations.
func (s *sqliteStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: bad version prefix", name)
		}
		var one int
		if s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version=?`, version).Scan(&one) == nil {
			continue // already applied
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range strings.Split(string(body), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, nowTS()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteStore) EnsureDefaultTenant(ctx context.Context) (Tenant, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at)
		 VALUES('default','default','Default','active','{}','shared',?)
		 ON CONFLICT(id) DO NOTHING`, nowTS()); err != nil {
		return Tenant{}, err
	}
	var t Tenant
	var keyRef sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, status, limits, isolation, COALESCE(key_ref,''), created_at
		 FROM tenant WHERE id='default'`).
		Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Limits, &t.Isolation, &keyRef, &t.CreatedAt)
	t.KeyRef = keyRef.String
	return t, err
}

func (s *sqliteStore) UpsertUser(ctx context.Context, tenantID, email, key string) (User, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO app_user(id, tenant_id, email, user_key, role, status, last_login_at)
		 VALUES(?, ?, ?, ?, 'member', 'active', ?)
		 ON CONFLICT(tenant_id, user_key) DO UPDATE SET
		   last_login_at = excluded.last_login_at,
		   email = CASE WHEN excluded.email <> '' THEN excluded.email ELSE app_user.email END`,
		newID(), tenantID, email, key, nowTS())
	if err != nil {
		return User{}, err
	}
	var u User
	var last sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, email, user_key, role, status, COALESCE(last_login_at,'')
		 FROM app_user WHERE tenant_id=? AND user_key=?`, tenantID, key).
		Scan(&u.ID, &u.TenantID, &u.Email, &u.UserKey, &u.Role, &u.Status, &last)
	u.LastLoginAt = last.String
	return u, err
}

func (s *sqliteStore) GetWorkspaceByUser(ctx context.Context, userID string) (Workspace, bool, error) {
	ws, err := s.scanWorkspace(s.db.QueryRowContext(ctx, workspaceCols+` WHERE user_id=?`, userID))
	if err == sql.ErrNoRows {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, err
	}
	return ws, true, nil
}

func (s *sqliteStore) CreateWorkspace(ctx context.Context, ws Workspace) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace(id, tenant_id, user_id, container_name, network, data_dir,
		   agent_port, agent_token, state, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		ws.ID, ws.TenantID, ws.UserID, ws.ContainerName, ws.Network, ws.DataDir,
		ws.AgentPort, ws.AgentToken, ws.State, ws.CreatedAt)
	return err
}

func (s *sqliteStore) MaxAgentPort(ctx context.Context) (int, error) {
	var max int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(CAST(agent_port AS INTEGER)), 0) FROM workspace`).Scan(&max)
	return max, err
}

func (s *sqliteStore) ListWorkspaces(ctx context.Context, tenantID string) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, workspaceCols+` WHERE tenant_id=? ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		ws, err := s.scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

const workspaceCols = `SELECT id, tenant_id, user_id, container_name, network, data_dir,
	agent_port, agent_token, state, created_at, COALESCE(last_active_at,'') FROM workspace`

type scanner interface{ Scan(dest ...any) error }

func (s *sqliteStore) scanWorkspace(row scanner) (Workspace, error) {
	var ws Workspace
	err := row.Scan(&ws.ID, &ws.TenantID, &ws.UserID, &ws.ContainerName, &ws.Network,
		&ws.DataDir, &ws.AgentPort, &ws.AgentToken, &ws.State, &ws.CreatedAt, &ws.LastActiveAt)
	return ws, err
}
