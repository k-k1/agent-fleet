package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// SQL is the shared database/sql MetadataStore adapter. Both the SQLite
// (default self-host) and Postgres (P3-7 stage 3a / RDS) backends use it: the SQL is
// dialect-neutral except placeholders, so the only per-dialect state is the db
// wrapper's rebind (?→$n for Postgres), the embedded migrations, and a SQLite-only
// legacy data hook. See OpenSQLite / OpenPostgres.
type SQL struct {
	db         *sqlDB
	dialect    string
	mfs        embed.FS                    // embedded numbered migrations
	mdir       string                      // dir within mfs
	legacyHook func(context.Context) error // sqlite-only post-migration data move; nil for pg
	// afterMCPOAuthSpend, when set by a test, runs right after ConsumeMCPOAuthToken's spending
	// statement, so a test can land a rival caller in that window. Nil in production.
	afterMCPOAuthSpend func()
}

// OpenSQLite opens the DB with the server-app pragmas (WAL + busy_timeout +
// foreign keys). MaxOpenConns(1) serializes access — simplest correct choice at
// this scale and avoids "database is locked".
func OpenSQLite(path string) (*SQL, error) {
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
	s := &SQL{db: &sqlDB{DB: db, rb: rebindNoop}, dialect: "sqlite", mfs: migrationFS, mdir: "migrations"}
	s.legacyHook = s.migrateMemberships
	return s, nil
}

func (s *SQL) Close() error { return s.db.Close() }

// Ping opens (or reuses) a connection and asks the database if it is there. It
// backs GET /readyz — the endpoint that exists because /healthz answers "ok"
// whether or not this store works, which is how the 2026-09-01 outage stayed
// invisible for fifteen minutes (store_postgres_secret.go).
//
// Deliberately not a query: a stale password fails at CONNECT, so PingContext —
// which has to hand out a connection — is exactly the check that catches it.
func (s *SQL) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// migrate applies embedded numbered migrations idempotently, then runs the
// identity/membership data migration (docs/14, P3-2).
func (s *SQL) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(s.mfs, s.mdir)
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
	// ⚠️ Two files with the same numeric prefix is a silent data loss, not a style
	// problem: schema_migrations is keyed by the INTEGER below, so the first of the
	// pair records that version and the second is skipped forever as "already
	// applied" — the table it was supposed to create never exists, and the failure
	// surfaces as a query error in production long after the merge.
	//
	// It happens whenever two branches add a migration in parallel, which is the
	// normal way this repository works. Measured once (2026-08-22: develop's
	// 0030_memo_category and a branch's 0030_user_limit_slot_class collided in
	// migrations-pg). Refusing to start is the correct response — a CP that boots on
	// a half-applied schema is worse than one that does not boot.
	seen := map[int]string{}
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: bad version prefix", name)
		}
		if prev, dup := seen[version]; dup {
			return fmt.Errorf("migrations %s and %s share version %d: one of them would be silently skipped; renumber the newer one", prev, name, version)
		}
		seen[version] = name
		var one int
		switch err := s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version=?`, version).Scan(&one); {
		case err == nil:
			continue // already applied
		case !errors.Is(err, sql.ErrNoRows):
			// transient/query errors must not be mistaken for "not applied"
			return fmt.Errorf("migration %s: check applied: %w", name, err)
		}
		body, err := s.mfs.ReadFile(s.mdir + "/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// NOTE: naive `;` split — migration files must not contain `;` inside
		// string literals or TRIGGER bodies (use one statement per `;`). Introduce a
		// real statement splitter if that ever has to change.
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
			`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, NowTS()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if s.legacyHook != nil {
		if err := s.legacyHook(ctx); err != nil {
			return err
		}
	}
	if err := s.repairWorkspaceColumns(ctx); err != nil {
		return err
	}
	return s.repairHandoffTakeColumns(ctx)
}

// repairHandoffTakeColumns adds the session_handoff_offer columns of 0095_handoff_take
// (pg 0080) to a database where that version is recorded but the file never ran.
//
// Two files once shared that version: 0095_member_ui_state (pg 0080, since renumbered to 0097 /
// 0082) reached develop two minutes before 0095_handoff_take. A database that ran the version as
// member_ui_state records it as applied and skips handoff_take forever. Migrate still succeeds,
// and the missing columns surface later as query errors in the handoff paths (measured on both
// dialects: "no such column: take_token" / SQLSTATE 42703). Checking the columns first keeps
// this idempotent, so every other database is left as it is.
func (s *SQL) repairHandoffTakeColumns(ctx context.Context) error {
	cols := []string{"take_token", "taken_at", "take_confirmed_at"}
	if s.dialect == "postgres" {
		for _, c := range cols {
			if _, err := s.db.ExecContext(ctx,
				`ALTER TABLE session_handoff_offer ADD COLUMN IF NOT EXISTS `+c+` TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("repair session_handoff_offer.%s: %w", c, err)
			}
		}
		return nil
	}
	have := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(session_handoff_offer)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(have) == 0 {
		return nil // no handoff table (nothing migrated yet)
	}
	for _, c := range cols {
		if have[c] {
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE session_handoff_offer ADD COLUMN `+c+` TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("repair session_handoff_offer.%s: %w", c, err)
		}
	}
	return nil
}

// repairWorkspaceColumns re-adds the workspace columns that the identity/membership
// swap throws away, and is why it exists at all.
//
// Measured on a FRESH SQLite DB: migration 0002 creates `workspace_new`, and the legacy
// hook swaps it in at the end with `DROP TABLE workspace` +
// `ALTER TABLE workspace_new RENAME TO workspace`. That swap runs inside the same
// migrate(), after the 0009-and-later ALTERs, so every column added to workspace after
// 0002 (`settings`, and now `preview_slug`) is thrown away moments after it is created —
// and schema_migrations already records those versions as applied, so no later boot ever
// adds them back.
//
// The symptom is silent, which is what makes it expensive: `settings` was missing on
// fresh SQLite deployments only, where reads swallow it (`raw, _ :=`) and only the save
// path 500s. Postgres has no legacy hook and is unaffected.
//
// The fix is to re-add the missing columns AFTER the swap. PRAGMA is consulted first, so
// this is idempotent and an already-broken DB heals itself on the next boot.
func (s *SQL) repairWorkspaceColumns(ctx context.Context) error {
	if s.dialect != "sqlite" {
		return nil
	}
	have := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(workspace)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(have) == 0 {
		return nil // no workspace table yet (nothing migrated); nothing to repair
	}
	// Every column added to workspace after 0002 has to be listed here as well.
	for _, c := range []struct{ name, ddl string }{
		{"settings", `ALTER TABLE workspace ADD COLUMN settings TEXT NOT NULL DEFAULT ''`},
		{"preview_slug", `ALTER TABLE workspace ADD COLUMN preview_slug TEXT NOT NULL DEFAULT ''`},
	} {
		if have[c.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, c.ddl); err != nil {
			return fmt.Errorf("repair workspace.%s: %w", c.name, err)
		}
	}
	// Indexes go away with the table, so recreate them too (IF NOT EXISTS keeps it
	// idempotent).
	if _, err := s.db.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_preview_slug ON workspace(preview_slug) WHERE preview_slug <> ''`); err != nil {
		return fmt.Errorf("repair idx_workspace_preview_slug: %w", err)
	}
	return nil
}

// migrateMemberships moves app_user(tenant_id) + workspace(user_id) into the
// identity/membership/workspace(membership_id) shape (docs/14). It runs once,
// gated on the presence of the transient `workspace_new` table created by 0002.
// Idempotent and safe on a fresh DB (no app_user rows to copy).
func (s *SQL) migrateMemberships(ctx context.Context) error {
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
	if err := rows.Err(); err != nil {
		return err // a partial read must not reach the DROP TABLE below
	}

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
	if err := wrows.Err(); err != nil {
		return err // a partial read must not reach the DROP TABLE below
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	identityByKey := map[string]string{}
	for _, a := range aus {
		if identityByKey[a.key] == "" {
			id := NewID()
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
			NewID(), identityByKey[a.key], a.tenant, a.role, NowTS()); err != nil {
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

// nullable maps "" to a SQL NULL so empty optional columns stay NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type scanner interface{ Scan(dest ...any) error }

// b2i/i2b bridge the 0/1 INTEGER columns (enabled, new_branch) and Go bools; the
// database/sql layer does not coerce int<->bool on its own.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
