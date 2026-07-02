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

// migrate applies embedded numbered migrations idempotently, then runs the
// identity/membership data migration (docs/14, P3-2).
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
	return s.migrateMemberships(ctx)
}

// migrateMemberships moves app_user(tenant_id) + workspace(user_id) into the
// identity/membership/workspace(membership_id) shape (docs/14). It runs once,
// gated on the presence of the transient `workspace_new` table created by 0002.
// Idempotent and safe on a fresh DB (no app_user rows to copy).
func (s *sqliteStore) migrateMemberships(ctx context.Context) error {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='workspace_new'`).Scan(&name)
	if err == sql.ErrNoRows {
		return nil // already migrated
	}
	if err != nil {
		return err
	}

	// Buffer source rows before writing (single connection can't interleave an
	// open Rows with writes).
	type auRow struct{ id, tenant, email, key, role, last string }
	var aus []auRow
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, email, user_key, role, COALESCE(last_login_at,'') FROM app_user`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a auRow
		if err := rows.Scan(&a.id, &a.tenant, &a.email, &a.key, &a.role, &a.last); err != nil {
			rows.Close()
			return err
		}
		aus = append(aus, a)
	}
	rows.Close()

	type wsRow struct {
		id, tenant, name, network, dir, port, token, state, created, last, key string
	}
	var wss []wsRow
	wrows, err := s.db.QueryContext(ctx,
		`SELECT w.id, w.tenant_id, w.container_name, w.network, w.data_dir, w.agent_port,
		        w.agent_token, w.state, w.created_at, COALESCE(w.last_active_at,''), a.user_key
		 FROM workspace w JOIN app_user a ON a.id = w.user_id`)
	if err != nil {
		return err
	}
	for wrows.Next() {
		var w wsRow
		if err := wrows.Scan(&w.id, &w.tenant, &w.name, &w.network, &w.dir, &w.port,
			&w.token, &w.state, &w.created, &w.last, &w.key); err != nil {
			wrows.Close()
			return err
		}
		wss = append(wss, w)
	}
	wrows.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	identityByKey := map[string]string{}
	for _, a := range aus {
		if identityByKey[a.key] == "" {
			id := newID()
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO identity(id, email, user_key, role, status, last_login_at)
				 VALUES(?, ?, ?, 'user', 'active', ?) ON CONFLICT(user_key) DO NOTHING`,
				id, a.email, a.key, a.last); err != nil {
				tx.Rollback()
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT id FROM identity WHERE user_key=?`, a.key).Scan(&id); err != nil {
				tx.Rollback()
				return err
			}
			identityByKey[a.key] = id
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO membership(id, identity_id, tenant_id, role, status, created_at)
			 VALUES(?, ?, ?, ?, 'active', ?) ON CONFLICT(identity_id, tenant_id) DO NOTHING`,
			newID(), identityByKey[a.key], a.tenant, a.role, nowTS()); err != nil {
			tx.Rollback()
			return err
		}
	}
	for _, w := range wss {
		var mid string
		if err := tx.QueryRowContext(ctx,
			`SELECT m.id FROM membership m JOIN identity i ON i.id = m.identity_id
			 WHERE i.user_key=? AND m.tenant_id=?`, w.key, w.tenant).Scan(&mid); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate workspace %s: membership lookup: %w", w.id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workspace_new(id, tenant_id, membership_id, container_name, network,
			   data_dir, agent_port, agent_token, state, created_at, last_active_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			w.id, w.tenant, mid, w.name, w.network, w.dir, w.port, w.token, w.state, w.created, w.last); err != nil {
			tx.Rollback()
			return err
		}
	}
	for _, stmt := range []string{
		`DROP TABLE workspace`,
		`ALTER TABLE workspace_new RENAME TO workspace`,
		`DROP TABLE app_user`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback()
			return fmt.Errorf("swap (%s): %w", stmt, err)
		}
	}
	return tx.Commit()
}

func (s *sqliteStore) EnsureDefaultTenant(ctx context.Context) (Tenant, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at)
		 VALUES('default','default','Default','active','{}','shared',?)
		 ON CONFLICT(id) DO NOTHING`, nowTS()); err != nil {
		return Tenant{}, err
	}
	return s.getTenant(ctx, "default")
}

func (s *sqliteStore) CreateTenant(ctx context.Context, slug, name string) (Tenant, error) {
	id := newID()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at)
		 VALUES(?, ?, ?, 'active', '{}', 'shared', ?)`, id, slug, name, nowTS()); err != nil {
		return Tenant{}, err
	}
	return s.getTenant(ctx, id)
}

func (s *sqliteStore) GetTenantBySlug(ctx context.Context, slug string) (Tenant, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM tenant WHERE slug=?`, slug).Scan(&id)
	if err == sql.ErrNoRows {
		return Tenant{}, false, nil
	}
	if err != nil {
		return Tenant{}, false, err
	}
	t, err := s.getTenant(ctx, id)
	return t, err == nil, err
}

func (s *sqliteStore) GetTenant(ctx context.Context, id string) (Tenant, error) {
	return s.getTenant(ctx, id)
}

func (s *sqliteStore) SetTenantLimits(ctx context.Context, tenantID, limitsJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tenant SET limits=? WHERE id=?`, limitsJSON, tenantID)
	return err
}

func (s *sqliteStore) getTenant(ctx context.Context, id string) (Tenant, error) {
	var t Tenant
	var keyRef sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, status, limits, isolation, COALESCE(key_ref,''), created_at
		 FROM tenant WHERE id=?`, id).
		Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Limits, &t.Isolation, &keyRef, &t.CreatedAt)
	t.KeyRef = keyRef.String
	return t, err
}

func (s *sqliteStore) UpsertIdentity(ctx context.Context, email, key, roleHint string) (Identity, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO identity(id, email, user_key, role, status, last_login_at)
		 VALUES(?, ?, ?, 'user', 'active', ?)
		 ON CONFLICT(user_key) DO UPDATE SET
		   last_login_at = excluded.last_login_at,
		   email = CASE WHEN excluded.email <> '' THEN excluded.email ELSE identity.email END`,
		newID(), email, key, nowTS()); err != nil {
		return Identity{}, err
	}
	// Upgrade (never downgrade) the deployment role.
	if roleHint == "super_admin" {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE identity SET role='super_admin' WHERE user_key=?`, key); err != nil {
			return Identity{}, err
		}
	}
	var id Identity
	var last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, user_key, role, status, COALESCE(last_login_at,'') FROM identity WHERE user_key=?`, key).
		Scan(&id.ID, &id.Email, &id.UserKey, &id.Role, &id.Status, &last)
	id.LastLoginAt = last.String
	return id, err
}

func (s *sqliteStore) GetIdentityByID(ctx context.Context, id string) (Identity, bool, error) {
	var idn Identity
	var last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, user_key, role, status, COALESCE(last_login_at,'') FROM identity WHERE id=?`, id).
		Scan(&idn.ID, &idn.Email, &idn.UserKey, &idn.Role, &idn.Status, &last)
	if err == sql.ErrNoRows {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, err
	}
	idn.LastLoginAt = last.String
	return idn, true, nil
}

func (s *sqliteStore) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, status, limits, isolation, COALESCE(key_ref,''), created_at
		 FROM tenant ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Limits, &t.Isolation, &t.KeyRef, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ListMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, i.user_key, i.email, i.role, m.role
		 FROM membership m JOIN identity i ON i.id = m.identity_id
		 WHERE m.tenant_id=? AND m.status='active' ORDER BY i.user_key`, tenantID)
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

func (s *sqliteStore) ListMemberships(ctx context.Context, identityID string) ([]MembershipView, error) {
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

func (s *sqliteStore) EnsureMembership(ctx context.Context, identityID, tenantID, role string) (Membership, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO membership(id, identity_id, tenant_id, role, status, created_at)
		 VALUES(?, ?, ?, ?, 'active', ?) ON CONFLICT(identity_id, tenant_id) DO NOTHING`,
		newID(), identityID, tenantID, role, nowTS()); err != nil {
		return Membership{}, err
	}
	var m Membership
	err := s.db.QueryRowContext(ctx,
		`SELECT id, identity_id, tenant_id, role, status, created_at FROM membership
		 WHERE identity_id=? AND tenant_id=?`, identityID, tenantID).
		Scan(&m.ID, &m.IdentityID, &m.TenantID, &m.Role, &m.Status, &m.CreatedAt)
	return m, err
}

func (s *sqliteStore) GetMembership(ctx context.Context, identityID, tenantID string) (Membership, bool, error) {
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

func (s *sqliteStore) SetMembershipRole(ctx context.Context, membershipID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE membership SET role=? WHERE id=?`, role, membershipID)
	return err
}

func (s *sqliteStore) GetUserLimit(ctx context.Context, membershipID string) (UserLimit, bool, error) {
	var u UserLimit
	err := s.db.QueryRowContext(ctx,
		`SELECT membership_id, max_sessions, disk_gb, created_at FROM user_limit WHERE membership_id=?`, membershipID).
		Scan(&u.MembershipID, &u.MaxSessions, &u.DiskGB, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return UserLimit{}, false, nil
	}
	if err != nil {
		return UserLimit{}, false, err
	}
	return u, true, nil
}

func (s *sqliteStore) PutUserLimit(ctx context.Context, membershipID string, maxSessions, diskGB int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_limit(membership_id, max_sessions, disk_gb, created_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(membership_id) DO UPDATE SET max_sessions=excluded.max_sessions, disk_gb=excluded.disk_gb`,
		membershipID, maxSessions, diskGB, nowTS())
	return err
}

func (s *sqliteStore) SetWorkspaceState(ctx context.Context, workspaceID, state string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workspace SET state=?, last_active_at=? WHERE id=?`, state, nowTS(), workspaceID)
	return err
}

func (s *sqliteStore) GetWorkspaceByMembership(ctx context.Context, membershipID string) (Workspace, bool, error) {
	ws, err := scanWorkspace(s.db.QueryRowContext(ctx, workspaceCols+` WHERE membership_id=?`, membershipID))
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
		`INSERT INTO workspace(id, tenant_id, membership_id, container_name, network, data_dir,
		   agent_port, agent_token, state, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		ws.ID, ws.TenantID, ws.MembershipID, ws.ContainerName, ws.Network, ws.DataDir,
		ws.AgentPort, ws.AgentToken, ws.State, ws.CreatedAt)
	return err
}

func (s *sqliteStore) GetWorkspaceSettings(ctx context.Context, workspaceID string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(settings,'') FROM workspace WHERE id=?`, workspaceID).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *sqliteStore) SetWorkspaceSettings(ctx context.Context, workspaceID, settingsJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workspace SET settings=? WHERE id=?`, settingsJSON, workspaceID)
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
		ws, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GetWrappedDEK(ctx context.Context, workspaceID string) (string, string, bool, error) {
	var ct, kr string
	err := s.db.QueryRowContext(ctx,
		`SELECT ciphertext, key_ref FROM wrapped_dek WHERE workspace_id=?`, workspaceID).Scan(&ct, &kr)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return ct, kr, true, nil
}

func (s *sqliteStore) PutWrappedDEK(ctx context.Context, workspaceID, ciphertext, keyRef string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wrapped_dek(workspace_id, ciphertext, key_ref, key_version, created_at)
		 VALUES(?, ?, ?, 1, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET ciphertext=excluded.ciphertext, key_ref=excluded.key_ref`,
		workspaceID, ciphertext, keyRef, nowTS())
	return err
}

func (s *sqliteStore) ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session(workspace_id, name, kind, dir, repo, label, created_at, state, last_seen)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			workspaceID, r.Name, r.Kind, r.Dir, r.Repo, r.Label, r.CreatedAt, r.State, nowTS()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqliteStore) ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, kind, dir, repo, label, created_at, state, last_seen
		 FROM session WHERE workspace_id=? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		r := SessionRow{WorkspaceID: workspaceID}
		if err := rows.Scan(&r.Name, &r.Kind, &r.Dir, &r.Repo, &r.Label, &r.CreatedAt, &r.State, &r.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- Personal Access Tokens (docs/decisions/0006, P3-6) ---

func (s *sqliteStore) CreatePAT(ctx context.Context, p PAT, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pat(id, identity_id, membership_id, token_hash, scope, name, created_at, expires_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.IdentityID, nullable(p.MembershipID), tokenHash, p.Scope, p.Name, p.CreatedAt, nullable(p.ExpiresAt))
	return err
}

func (s *sqliteStore) GetPATByHash(ctx context.Context, tokenHash string) (PAT, bool, error) {
	var p PAT
	var mid, exp, rev, last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, identity_id, COALESCE(membership_id,''), scope, name, created_at,
		        expires_at, revoked_at, last_used_at FROM pat WHERE token_hash=?`, tokenHash).
		Scan(&p.ID, &p.IdentityID, &mid, &p.Scope, &p.Name, &p.CreatedAt, &exp, &rev, &last)
	if err == sql.ErrNoRows {
		return PAT{}, false, nil
	}
	if err != nil {
		return PAT{}, false, err
	}
	p.MembershipID, p.ExpiresAt, p.RevokedAt, p.LastUsedAt = mid.String, exp.String, rev.String, last.String
	return p, true, nil
}

func (s *sqliteStore) ListPATsByIdentity(ctx context.Context, identityID string) ([]PAT, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, identity_id, COALESCE(membership_id,''), scope, name, created_at,
		        COALESCE(expires_at,''), COALESCE(revoked_at,''), COALESCE(last_used_at,'')
		 FROM pat WHERE identity_id=? ORDER BY created_at DESC`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PAT
	for rows.Next() {
		var p PAT
		if err := rows.Scan(&p.ID, &p.IdentityID, &p.MembershipID, &p.Scope, &p.Name,
			&p.CreatedAt, &p.ExpiresAt, &p.RevokedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RevokePAT marks a token revoked, scoped to its owner so a user can only revoke
// their own tokens.
func (s *sqliteStore) RevokePAT(ctx context.Context, id, identityID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pat SET revoked_at=? WHERE id=? AND identity_id=? AND revoked_at IS NULL`,
		nowTS(), id, identityID)
	return err
}

func (s *sqliteStore) TouchPAT(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pat SET last_used_at=? WHERE id=?`, nowTS(), id)
	return err
}

func (s *sqliteStore) InsertAudit(ctx context.Context, a AuditLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.TenantID, a.ActorKind, a.ActorID, a.Action, a.Target, a.Detail, a.At)
	return err
}

func (s *sqliteStore) ListAuditByTenant(ctx context.Context, tenantID string, limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, actor_kind, actor_id, action, target, detail, at
		 FROM audit_log WHERE tenant_id=? ORDER BY at DESC, id DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		var a AuditLog
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ActorKind, &a.ActorID,
			&a.Action, &a.Target, &a.Detail, &a.At); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddUsage accumulates workspace running-seconds into the (membership, day)
// showback bucket (docs/roadmap.md P3-9). Upsert += so repeated samples add up.
func (s *sqliteStore) AddUsage(ctx context.Context, membershipID, tenantID, day string, secs int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_daily(membership_id, tenant_id, day, running_secs)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(membership_id, day) DO UPDATE SET running_secs = running_secs + excluded.running_secs`,
		membershipID, tenantID, day, secs)
	return err
}

// ListUsage returns per-day showback rows in [fromDay, toDay] (inclusive),
// enriched with tenant slug + member key/email via LEFT JOINs (a row survives
// even if its membership/identity was later removed). tenantID=="" spans all
// tenants (super_admin); otherwise it is scoped to that tenant.
func (s *sqliteStore) ListUsage(ctx context.Context, tenantID, fromDay, toDay string) ([]UsageRow, error) {
	q := `SELECT u.tenant_id, COALESCE(t.slug,''), u.membership_id,
	             COALESCE(i.user_key,''), COALESCE(i.email,''), u.day, u.running_secs
	      FROM usage_daily u
	      LEFT JOIN tenant t ON t.id = u.tenant_id
	      LEFT JOIN membership m ON m.id = u.membership_id
	      LEFT JOIN identity i ON i.id = m.identity_id
	      WHERE u.day BETWEEN ? AND ?`
	args := []any{fromDay, toDay}
	if tenantID != "" {
		q += ` AND u.tenant_id=?`
		args = append(args, tenantID)
	}
	q += ` ORDER BY u.tenant_id, i.user_key, u.day`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.TenantID, &u.TenantSlug, &u.MembershipID,
			&u.UserKey, &u.Email, &u.Day, &u.RunningSecs); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// nullable maps "" to a SQL NULL so empty optional columns stay NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const workspaceCols = `SELECT id, tenant_id, membership_id, container_name, network, data_dir,
	agent_port, agent_token, state, created_at, COALESCE(last_active_at,'') FROM workspace`

type scanner interface{ Scan(dest ...any) error }

func scanWorkspace(row scanner) (Workspace, error) {
	var ws Workspace
	err := row.Scan(&ws.ID, &ws.TenantID, &ws.MembershipID, &ws.ContainerName, &ws.Network,
		&ws.DataDir, &ws.AgentPort, &ws.AgentToken, &ws.State, &ws.CreatedAt, &ws.LastActiveAt)
	return ws, err
}
