package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// sqlStore is the shared database/sql MetadataStore adapter. Both the SQLite
// (default self-host) and Postgres (P3-7 段3a / RDS) backends use it: the SQL is
// dialect-neutral except placeholders, so the only per-dialect state is the db
// wrapper's rebind (?→$n for Postgres), the embedded migrations, and a SQLite-only
// legacy data hook. See openSQLite / openPostgres.
type sqlStore struct {
	db         *sqlDB
	dialect    string
	mfs        embed.FS                    // embedded numbered migrations
	mdir       string                      // dir within mfs
	legacyHook func(context.Context) error // sqlite-only post-migration data move; nil for pg
}

// openSQLite opens the DB with the server-app pragmas (WAL + busy_timeout +
// foreign keys). MaxOpenConns(1) serializes access — simplest correct choice at
// this scale and avoids "database is locked".
func openSQLite(path string) (*sqlStore, error) {
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
	s := &sqlStore{db: &sqlDB{DB: db, rb: rebindNoop}, dialect: "sqlite", mfs: migrationFS, mdir: "migrations"}
	s.legacyHook = s.migrateMemberships
	return s, nil
}

func (s *sqlStore) Close() error { return s.db.Close() }

// migrate applies embedded numbered migrations idempotently, then runs the
// identity/membership data migration (docs/14, P3-2).
func (s *sqlStore) migrate(ctx context.Context) error {
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
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: bad version prefix", name)
		}
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
		// string literals or TRIGGER bodies (use one statement per `;`). 改善が
		// 必要になったら分割器を導入すること。
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
	if s.legacyHook != nil {
		return s.legacyHook(ctx)
	}
	return nil
}

// migrateMemberships moves app_user(tenant_id) + workspace(user_id) into the
// identity/membership/workspace(membership_id) shape (docs/14). It runs once,
// gated on the presence of the transient `workspace_new` table created by 0002.
// Idempotent and safe on a fresh DB (no app_user rows to copy).
func (s *sqlStore) migrateMemberships(ctx context.Context) error {
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

func (s *sqlStore) EnsureDefaultTenant(ctx context.Context) (Tenant, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at)
		 VALUES('default','default','Default','active','{}','shared',?)
		 ON CONFLICT(id) DO NOTHING`, nowTS()); err != nil {
		return Tenant{}, err
	}
	return s.getTenant(ctx, "default")
}

func (s *sqlStore) CreateTenant(ctx context.Context, slug, name string) (Tenant, error) {
	id := newID()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant(id, slug, name, status, limits, isolation, created_at)
		 VALUES(?, ?, ?, 'active', '{}', 'shared', ?)`, id, slug, name, nowTS()); err != nil {
		return Tenant{}, err
	}
	return s.getTenant(ctx, id)
}

func (s *sqlStore) GetTenantBySlug(ctx context.Context, slug string) (Tenant, bool, error) {
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

func (s *sqlStore) GetTenant(ctx context.Context, id string) (Tenant, error) {
	return s.getTenant(ctx, id)
}

func (s *sqlStore) SetTenantLimits(ctx context.Context, tenantID, limitsJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tenant SET limits=? WHERE id=?`, limitsJSON, tenantID)
	return err
}

func (s *sqlStore) getTenant(ctx context.Context, id string) (Tenant, error) {
	var t Tenant
	var keyRef sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, status, limits, isolation, COALESCE(key_ref,''), created_at,
		        allowed_providers, auto_join_domains, allowed_domains, hidden_providers
		 FROM tenant WHERE id=?`, id).
		Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Limits, &t.Isolation, &keyRef, &t.CreatedAt,
			&t.AllowedProviders, &t.AutoJoinDomains, &t.AllowedDomains, &t.HiddenProviders)
	t.KeyRef = keyRef.String
	return t, err
}

// SetTenantLogin writes the per-tenant login rules (docs/61 §61.9.7).
func (s *sqlStore) SetTenantLogin(ctx context.Context, tenantID, allowedProviders, autoJoinDomains, allowedDomains, hiddenProviders string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenant SET allowed_providers=?, auto_join_domains=?, allowed_domains=?, hidden_providers=? WHERE id=?`,
		allowedProviders, autoJoinDomains, allowedDomains, hiddenProviders, tenantID)
	return err
}

// SetTenantAllowedCIDRs writes the tenant's source-network restriction (docs/66).
// Separate from SetTenantLogin on purpose: that one is super_admin-only because its
// three fields reach outside the tenant, while this one is the tenant_admin's
// (ADR 0047 決定 6). Empty = no restriction.
func (s *sqlStore) SetTenantAllowedCIDRs(ctx context.Context, tenantID, cidrs string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tenant SET allowed_cidrs=? WHERE id=?`, cidrs, tenantID)
	return err
}

// GetTenantAllowedCIDRs reads it back for the editing screen (the request path reads
// it through the cache instead).
func (s *sqlStore) GetTenantAllowedCIDRs(ctx context.Context, tenantID string) (string, error) {
	var cidrs string
	err := s.db.QueryRowContext(ctx, `SELECT allowed_cidrs FROM tenant WHERE id=?`, tenantID).Scan(&cidrs)
	return cidrs, err
}

// ListTenantLoginRules loads every tenant's rules in one query, already split into
// slices — the shape the entry-gate cache and the auto-join resolution want.
func (s *sqlStore) ListTenantLoginRules(ctx context.Context) ([]TenantLoginRules, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, allowed_providers, auto_join_domains, allowed_domains, hidden_providers, allowed_cidrs
		 FROM tenant WHERE status='active' ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantLoginRules
	for rows.Next() {
		var r TenantLoginRules
		var provs, autoJoin, allowed, hidden, cidrs string
		if err := rows.Scan(&r.ID, &r.Slug, &r.Name, &provs, &autoJoin, &allowed, &hidden, &cidrs); err != nil {
			return nil, err
		}
		r.AllowedProviders = splitCSVLower(provs)
		r.AutoJoinDomains = splitDomainCSV(autoJoin)
		r.AllowedDomains = splitDomainCSV(allowed)
		r.HiddenProviders = splitCSVLower(hidden)
		r.AllowedCIDRs = splitCSV(cidrs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqlStore) UpsertIdentity(ctx context.Context, email, key, roleHint string) (Identity, error) {
	key, derr := s.disambiguateUserKey(ctx, email, key)
	if derr != nil {
		return Identity{}, derr
	}
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

// DemoteSuperAdmins enforces "SUPER_ADMIN_EMAILS is the only source of truth" at
// startup (ADR0043 決定 24). UpsertIdentity's roleHint stays upgrade-only on
// purpose: addMembership / cleanHome / stopWorkspace all call it with roleHint="",
// so putting the demotion there would strip an operator's role the moment somebody
// added a member. The revocation therefore lives here, in a pass CP runs once at
// boot — which is also the only moment that reaches a person who has left and will
// never sign in again.
//
// The candidate set is read first rather than expressed as a NOT IN (…) so the
// caller can log exactly whose role was revoked; the set is a handful of rows.
func (s *sqlStore) DemoteSuperAdmins(ctx context.Context, keep []string) ([]string, error) {
	keepSet := make(map[string]bool, len(keep))
	for _, e := range keep {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			keepSet[e] = true
		}
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email FROM identity WHERE role='super_admin' AND email <> ''`)
	if err != nil {
		return nil, err
	}
	type row struct{ id, email string }
	var cands []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.email); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var demoted []string
	for _, c := range cands {
		if keepSet[strings.ToLower(c.email)] {
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE identity SET role='user' WHERE id=? AND role='super_admin'`, c.id); err != nil {
			return demoted, err
		}
		demoted = append(demoted, c.email)
	}
	return demoted, nil
}

// LinkIdentity resolves a login that carries an IdP subject to a person, applying
// the three rules of docs/61 §61.5 in order:
//
//  1. (provider, subject) already recorded => that identity, whatever its email is
//     now. user_key does not move, so home and secrets stay put across a rename.
//     1.5. not recorded, but the SAME REALM has this subject on another provider =>
//     that identity (docs/61 §61.15 + 決定 35). Two buttons can be two doors onto
//     one IdP — the deployment's GitHub and a tenant's own GitHub, or an env Entra
//     and a tenant row pointing at the same issuer — and the same account behind
//     both is the same person. The realm is the adapter's own answer to "where did
//     I verify this" (issuerURL), never a value the tenant typed, which is why this
//     join is open to tenant-defined providers while rule 2 is not.
//
//  2. not recorded, but the email matches an identity => join it. This is what
//     makes "sign in with the company address and you land in the same workspace
//     whichever button you pressed" true, and it is how an existing Google-only
//     deployment migrates: the first login after the upgrade takes this path.
//
//  3. neither => a NEW identity (isNew), created through UpsertIdentity so the
//     user_key collision guard still applies.
//
// Rule 2 matches on the email the IdP asserted, which is safe only because every
// enabled provider is one the operator configured and each declares how it
// justifies that email (§61.4) — the allowlist is what keeps a foreign address
// from reaching this code at all. Two DIFFERENT emails are never merged: proving
// you can sign in to both accounts shows control of both, not that they are one
// person, and the merge would be irreversible.
//
// ★ emailJoin=false is a TENANT-DEFINED provider (docs/61 §61.11): the operator did
// not configure that issuer, a subsidiary's administrator did, so an asserted
// address is not proof of being that person. Rule 2 becomes rule 2' — CLAIM an
// identity nobody has ever signed in as (the placeholder an invite leaves behind,
// which is how a subsidiary's first login is supposed to work), and refuse with
// errIdentityClaimed when the address belongs to an account that has a login
// history. Falling through to rule 3 instead would NOT be fail-closed: user_key is
// sanitizeUser(email), so UpsertIdentity's ON CONFLICT(user_key) would land right
// back on the very identity rule 2 was refusing to join (and identity.email is
// UNIQUE, so a genuinely separate row cannot exist either). Refusing is the only
// answer that is actually a refusal.
func (s *sqlStore) LinkIdentity(ctx context.Context, link IdentityLink) (Identity, bool, error) {
	provider, subject, email := link.Provider, link.Subject, link.Email
	if provider == "" || subject == "" { // no subject to key on: legacy/pre-P0 session
		ident, err := s.UpsertIdentity(ctx, email, link.FallbackKey, link.RoleHint)
		return ident, false, err
	}
	identityID, err := s.identityIDForProvider(ctx, provider, subject)
	if err != nil {
		return Identity{}, false, err
	}
	if identityID == "" && link.Realm != "" { // rule 1.5
		if identityID, err = s.identityIDForRealm(ctx, link.Realm, subject); err != nil {
			return Identity{}, false, err
		}
		// ★ …and its second key, for an issuer whose `sub` is pairwise (docs/61
		// §61.15.10 + 決定 38). Tried AFTER the subject match, so an IdP where both
		// work behaves exactly as it did before this column existed.
		if identityID == "" {
			if identityID, err = s.identityIDForRealmClaim(ctx, link.Realm, link.RealmClaim, link.RealmSubject); err != nil {
				return Identity{}, false, err
			}
		}
	}
	isNew := false
	if identityID == "" {
		ident, ok, err := s.identityByEmail(ctx, email)
		switch {
		case err != nil:
			return Identity{}, false, err
		case ok && link.EmailJoin: // rule 2
			identityID = ident.ID
		case ok: // rule 2', a tenant-defined provider: claim, never join
			claimed, err := s.identityHasProvider(ctx, ident.ID)
			if err != nil {
				return Identity{}, false, err
			}
			if claimed {
				return Identity{}, false, errIdentityClaimed
			}
			identityID = ident.ID
		default: // rule 3
			ident, created, err := s.createIdentityForLogin(ctx, email, link.FallbackKey, link.RoleHint)
			if err != nil {
				return Identity{}, false, err
			}
			identityID, isNew = ident.ID, created
		}
	}
	// Record the pair. ON CONFLICT only refreshes the display columns: an existing
	// mapping is never re-pointed at another identity, so a subject cannot be moved
	// onto someone else's workspace by a later login.
	// ★ realm is refreshed on every login, not only on insert: rows written before
	// 0041 (and by the resolver path, which has no provider object to ask) carry an
	// empty one, and rule 1.5 can only see what has been recorded.
	now := nowTS()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO identity_provider(provider, subject, identity_id, email, realm,
		                               realm_claim, realm_subject, created_at, last_login_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(provider, subject) DO UPDATE SET
		   email = excluded.email,
		   realm = CASE WHEN excluded.realm = '' THEN identity_provider.realm ELSE excluded.realm END,
		   realm_claim = CASE WHEN excluded.realm_subject = '' THEN identity_provider.realm_claim ELSE excluded.realm_claim END,
		   realm_subject = CASE WHEN excluded.realm_subject = '' THEN identity_provider.realm_subject ELSE excluded.realm_subject END,
		   last_login_at = excluded.last_login_at`,
		provider, subject, identityID, email, link.Realm,
		link.RealmClaim, link.RealmSubject, now, now); err != nil {
		return Identity{}, false, err
	}
	if err := s.touchIdentity(ctx, identityID, email, link.RoleHint); err != nil {
		return Identity{}, false, err
	}
	ident, ok, err := s.GetIdentityByID(ctx, identityID)
	if err != nil {
		return Identity{}, false, err
	}
	if !ok {
		return Identity{}, false, fmt.Errorf("identity %s vanished mid-login", identityID)
	}
	return ident, isNew, nil
}

// errIdentityClaimed is returned when a tenant-defined provider asserts an address
// that already belongs to somebody who has signed in here. The login is refused
// rather than joined or duplicated — see LinkIdentity.
var errIdentityClaimed = errors.New("this email address already belongs to an account on this deployment")

// identityHasProvider reports whether anybody has ever completed an IdP login as
// this identity. An identity with no such row is either an invite placeholder or a
// pre-P1 account; that is the line rule 2' draws.
func (s *sqlStore) identityHasProvider(ctx context.Context, identityID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM identity_provider WHERE identity_id=? LIMIT 1`, identityID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// identityIDForProvider returns the identity a (provider, subject) is bound to,
// or "" when the pair has never signed in here.
func (s *sqlStore) identityIDForProvider(ctx context.Context, provider, subject string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT identity_id FROM identity_provider WHERE provider=? AND subject=?`, provider, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// identityIDForRealm implements rule 1.5: the same IdP account reached through a
// DIFFERENT button (docs/61 §61.15). The realm identifies the authority that
// verified the subject — https://github.com for the GitHub adapter, the issuer URL
// for OIDC — so (realm, subject) is the same person no matter which provider row
// carried them here.
//
// ★ Deliberately not keyed on the email as well: an email change must not break the
// link, and the address is precisely the thing a tenant-defined provider is not
// trusted to assert (rule 2”). An empty realm never matches — pre-0041 rows have
// one, and matching them would join everyone whose subject happens to collide
// across two unrelated IdPs. ORDER BY provider only makes the answer deterministic
// when several rows exist, which by construction all point at one identity.
func (s *sqlStore) identityIDForRealm(ctx context.Context, realm, subject string) (string, error) {
	if realm == "" || subject == "" {
		return "", nil
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT identity_id FROM identity_provider WHERE realm=? AND subject=? ORDER BY provider LIMIT 1`,
		realm, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// identityIDForRealmClaim is rule 1.5's second key: the same IdP account recognised
// by a STABLE CLAIM rather than by `sub` (docs/61 §61.15.10 + 決定 38). Entra's `sub`
// is pairwise — a function of (app registration, user) — so one person coming through
// the head office's app and through a subsidiary's carries two subjects on one
// issuer, and identityIDForRealm cannot see that they are the same account. `oid` is
// the same value in both tokens, and this is where that is read.
//
// ★ The CLAIM NAME is part of the key, not just the value. Two providers reading
// DIFFERENT claims must never join because their values happened to collide — the
// name records which question was asked. And an empty claim or value matches
// nothing, so every row written before the column, and every provider that names no
// claim, simply does not take part.
func (s *sqlStore) identityIDForRealmClaim(ctx context.Context, realm, claim, subject string) (string, error) {
	if realm == "" || claim == "" || subject == "" {
		return "", nil
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT identity_id FROM identity_provider
		 WHERE realm=? AND realm_claim=? AND realm_subject=? ORDER BY provider LIMIT 1`,
		realm, claim, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// FillProviderRealm records the realm of the rows an env-defined provider already
// wrote, and is called once per provider at STARTUP (docs/61 §61.15).
//
// ★ It has to happen in Go rather than in the migration: which realm a provider id
// belongs to is only known from the provider set CP just built, and a migration that
// guessed "provider='github' means the GitHub adapter" would be wrong on a
// deployment that named an OIDC provider `github` (oauth_oidc.go warns about that
// but still builds it). Only empty realms are written, so a person whose row was
// filled by a login keeps that value, and re-running is a no-op.
func (s *sqlStore) FillProviderRealm(ctx context.Context, provider, realm string) error {
	if provider == "" || realm == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE identity_provider SET realm=? WHERE provider=? AND realm=''`, realm, provider)
	return err
}

// errLinkTaken is returned by AttachProvider when the IdP account being linked
// already belongs to somebody on this deployment — the pair itself is recorded, or
// rule 1.5 finds the same account under another button. Linking it would either
// re-point an existing mapping or merge two accounts, and both are irreversible.
var errLinkTaken = errors.New("that sign-in method already belongs to an account on this deployment")

// ListLinkedProviders — see the Store interface (docs/61 §61.16).
func (s *sqlStore) ListLinkedProviders(ctx context.Context, identityID string) ([]LinkedProvider, error) {
	if identityID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, subject, COALESCE(realm,''), COALESCE(email,''),
		        COALESCE(created_at,''), COALESCE(last_login_at,'')
		 FROM identity_provider WHERE identity_id=? ORDER BY last_login_at DESC, provider`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkedProvider
	for rows.Next() {
		var lp LinkedProvider
		if err := rows.Scan(&lp.Provider, &lp.Subject, &lp.Realm, &lp.Email,
			&lp.CreatedAt, &lp.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, lp)
	}
	return out, rows.Err()
}

// AttachProvider — see the Store interface (docs/61 §61.16 + 決定 37).
//
// ★ The three refusals below are STRUCTURAL: they hold whoever calls this and
// whatever the login layer checked first. The caller adds the policy half (the
// address must be the person's own — 決定 37), which this layer cannot state
// because it does not know which of several rules the deployment settled on; what
// it does know is that a row must never move to another identity and that two
// accounts must never become one.
func (s *sqlStore) AttachProvider(ctx context.Context, identityID string, link IdentityLink) error {
	if identityID == "" || link.Provider == "" || link.Subject == "" {
		return fmt.Errorf("attach provider: identity, provider and subject are required")
	}
	// 1. the pair is already recorded — the same identity is a no-op (pressing the
	//    button twice), another identity is a refusal (never re-point).
	owner, err := s.identityIDForProvider(ctx, link.Provider, link.Subject)
	if err != nil {
		return err
	}
	if owner != "" && owner != identityID {
		return errLinkTaken
	}
	// 2. rule 1.5: the same IdP account reached through a different button already
	//    belongs to somebody else. Signing in with it would land there, so linking it
	//    here would create two identities claiming one account.
	if owner == "" && link.Realm != "" {
		byRealm, err := s.identityIDForRealm(ctx, link.Realm, link.Subject)
		if err != nil {
			return err
		}
		if byRealm != "" && byRealm != identityID {
			return errLinkTaken
		}
		// ★ …and through rule 1.5's second key (docs/61 §61.15.10). Without this the
		// pairwise-`sub` case slips past: two subjects, one Entra account, so the pair
		// looks free while signing in with it would land on somebody else.
		byClaim, err := s.identityIDForRealmClaim(ctx, link.Realm, link.RealmClaim, link.RealmSubject)
		if err != nil {
			return err
		}
		if byClaim != "" && byClaim != identityID {
			return errLinkTaken
		}
	}
	// 3. the address the IdP asserted belongs to a different person. Even with the
	//    caller's same-address rule in front, this stays: identity.email is UNIQUE
	//    and case-sensitive there, so a case-differing row is reachable, and it is
	//    somebody else's account.
	if ident, ok, err := s.identityByEmail(ctx, link.Email); err != nil {
		return err
	} else if ok && ident.ID != identityID {
		return errLinkTaken
	}
	now := nowTS()
	// The insert deliberately does NOT set last_login_at to "now": linking is not a
	// login with this method, and the account panel orders by it. ON CONFLICT keeps
	// the same shape as LinkIdentity — identity_id is never in the update list.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO identity_provider(provider, subject, identity_id, email, realm,
		                               realm_claim, realm_subject, created_at, last_login_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, '')
		 ON CONFLICT(provider, subject) DO UPDATE SET
		   email = excluded.email,
		   realm = CASE WHEN excluded.realm = '' THEN identity_provider.realm ELSE excluded.realm END,
		   realm_claim = CASE WHEN excluded.realm_subject = '' THEN identity_provider.realm_claim ELSE excluded.realm_claim END,
		   realm_subject = CASE WHEN excluded.realm_subject = '' THEN identity_provider.realm_subject ELSE excluded.realm_subject END`,
		link.Provider, link.Subject, identityID, link.Email, link.Realm,
		link.RealmClaim, link.RealmSubject, now)
	return err
}

// errLastLoginMethod / errNoSuchLoginMethod are DetachProvider's two refusals.
//
// ★ The first one is a lockout guard, not a formality: with the last method gone
// there is no way back into the account — no password, no SMTP to mail a link from
// (決定 28) — and the person doing it is the account's own owner, mid-cleanup, who
// is exactly the one who cannot ask anybody to undo it.
var (
	errLastLoginMethod   = errors.New("this is the only sign-in method left on the account, and removing it would lock you out")
	errNoSuchLoginMethod = errors.New("that sign-in method is not linked to this account")
)

// DetachProvider — see the Store interface (docs/61 §61.16.4).
func (s *sqlStore) DetachProvider(ctx context.Context, identityID, provider, subject string) error {
	if identityID == "" || provider == "" || subject == "" {
		return fmt.Errorf("detach provider: identity, provider and subject are required")
	}
	// The row must be this person's own. Checked separately from the delete so
	// "not yours / not there" is distinguishable from "it is the last one".
	owner, err := s.identityIDForProvider(ctx, provider, subject)
	if err != nil {
		return err
	}
	if owner != identityID {
		return errNoSuchLoginMethod
	}
	// ★ The count lives INSIDE the delete. Reading it first and deleting after would
	// let two tabs each see "2 left" and remove one, ending at zero.
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM identity_provider
		 WHERE identity_id=? AND provider=? AND subject=?
		   AND (SELECT COUNT(*) FROM identity_provider WHERE identity_id=?) > 1`,
		identityID, provider, subject, identityID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// The ownership check above already passed, so the only remaining reason the
		// statement matched nothing is the count guard.
		return errLastLoginMethod
	}
	return nil
}

// identityByEmail finds the person an address already belongs to. Non-empty
// emails are UNIQUE (idx_identity_email), so at most one row matches; the empty
// email never does (those rows are invite-by-user_key placeholders, claimed through
// UpsertIdentity's key path instead).
func (s *sqlStore) identityByEmail(ctx context.Context, email string) (Identity, bool, error) {
	if strings.TrimSpace(email) == "" {
		return Identity{}, false, nil
	}
	var idn Identity
	var last sql.NullString
	// Lowercased in Go rather than with LOWER(?) so the placeholder's type is never
	// in question on Postgres, and ordered so a case-differing pair (possible: the
	// unique index is on the exact string) resolves the same way every time.
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, user_key, role, status, COALESCE(last_login_at,'')
		 FROM identity WHERE LOWER(email)=? ORDER BY id LIMIT 1`,
		strings.ToLower(strings.TrimSpace(email))).
		Scan(&idn.ID, &idn.Email, &idn.UserKey, &idn.Role, &idn.Status, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, err
	}
	idn.LastLoginAt = last.String
	return idn, true, nil
}

// createIdentityForLogin runs rule 3 through UpsertIdentity (so the user_key
// collision guard applies) and reports whether a row was actually created. An
// admin who pre-created the person by user_key (invite) leaves a row with an
// empty email waiting to be claimed — claiming it is not a new account, so it
// must not raise the "this is a new account" notice.
func (s *sqlStore) createIdentityForLogin(ctx context.Context, email, key, roleHint string) (Identity, bool, error) {
	finalKey, err := s.disambiguateUserKey(ctx, email, key)
	if err != nil {
		return Identity{}, false, err
	}
	_, existed, err := s.GetIdentityByUserKey(ctx, finalKey)
	if err != nil {
		return Identity{}, false, err
	}
	ident, err := s.UpsertIdentity(ctx, email, key, roleHint)
	return ident, !existed, err
}

// touchIdentity updates the display email and last login of an identity resolved
// by (provider, subject). It deliberately does NOT go through UpsertIdentity:
// that one re-derives the key from the email and would divert a renamed person to
// a fresh user_key (disambiguateUserKey), which is exactly what §61.5 forbids.
// Role still upgrades only (never downgrades), same as UpsertIdentity.
func (s *sqlStore) touchIdentity(ctx context.Context, identityID, email, roleHint string) error {
	// Two shapes rather than one with NULLIF/CASE: both dialects parse these
	// without having to infer a placeholder's type from a bare '' literal.
	var err error
	if email == "" {
		_, err = s.db.ExecContext(ctx, `UPDATE identity SET last_login_at=? WHERE id=?`, nowTS(), identityID)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE identity SET last_login_at=?, email=? WHERE id=?`, nowTS(), email, identityID)
	}
	if err != nil {
		return err
	}
	if roleHint != "super_admin" {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE identity SET role='super_admin' WHERE id=?`, identityID)
	return err
}

// disambiguateUserKey guards against sanitizeUser collisions: user_key is UNIQUE
// and identity-defining, so two different emails that sanitize to the same key
// ("a.b@x" vs "a-b@x", or long addresses truncated at 40 chars) would otherwise
// silently merge into ONE identity — same workspace, same encrypted secrets. When
// the key's row already belongs to a DIFFERENT email, the newcomer is diverted to
// a deterministic distinct key (sanitized + short hash of the full email).
// Existing keys never change, so no data migration is needed.
func (s *sqlStore) disambiguateUserKey(ctx context.Context, email, key string) (string, error) {
	if email == "" || key == "" {
		return key, nil
	}
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM identity WHERE user_key=?`, key).Scan(&existing)
	if err == sql.ErrNoRows {
		return key, nil
	}
	if err != nil {
		return "", err
	}
	if existing == "" || strings.EqualFold(existing, email) {
		return key, nil // same person (or an invite-by-key row being claimed)
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return key + "-" + hex.EncodeToString(sum[:4]), nil
}

func (s *sqlStore) GetIdentityByUserKey(ctx context.Context, key string) (Identity, bool, error) {
	var idn Identity
	var last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, user_key, role, status, COALESCE(last_login_at,'') FROM identity WHERE user_key=?`, key).
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

func (s *sqlStore) GetIdentityByID(ctx context.Context, id string) (Identity, bool, error) {
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

func (s *sqlStore) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, slug, name, status, limits, isolation, COALESCE(key_ref,''), created_at,
		        allowed_providers, auto_join_domains, allowed_domains, hidden_providers
		 FROM tenant ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Limits, &t.Isolation, &t.KeyRef, &t.CreatedAt,
			&t.AllowedProviders, &t.AutoJoinDomains, &t.AllowedDomains, &t.HiddenProviders); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *sqlStore) ListMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error) {
	return s.listMembersByStatus(ctx, tenantID, "active")
}

// ListRemovedMembersByTenant is the offboarded half — see the interface comment.
func (s *sqlStore) ListRemovedMembersByTenant(ctx context.Context, tenantID string) ([]MemberInfo, error) {
	return s.listMembersByStatus(ctx, tenantID, "inactive")
}

func (s *sqlStore) listMembersByStatus(ctx context.Context, tenantID, status string) ([]MemberInfo, error) {
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

func (s *sqlStore) ListMemberships(ctx context.Context, identityID string) ([]MembershipView, error) {
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

func (s *sqlStore) GetMembershipByID(ctx context.Context, membershipID string) (MembershipView, bool, error) {
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

func (s *sqlStore) IdentityIDForMembership(ctx context.Context, membershipID string) (string, bool, error) {
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
// precisely the offboarding docs/61 §61.10.6 exists to make work. Coming back onto
// a roster is an explicit act — the invite API reactivates deliberately
// (adminAPI.addMembership).
func (s *sqlStore) EnsureMembership(ctx context.Context, identityID, tenantID, role string) (Membership, error) {
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

func (s *sqlStore) GetMembership(ctx context.Context, identityID, tenantID string) (Membership, bool, error) {
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

func (s *sqlStore) SetMembershipRole(ctx context.Context, membershipID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE membership SET role=? WHERE id=?`, role, membershipID)
	return err
}

func (s *sqlStore) SetMembershipStatus(ctx context.Context, membershipID, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE membership SET status=? WHERE id=?`, status, membershipID)
	return err
}

// EmailHasActiveMembership answers the entry gate's membership term (決定 16).
// LOWER() is applied to the COLUMN, not to the placeholder: Postgres cannot infer a
// type for LOWER($1) and errors, which is the same reason identityByEmail lowercases
// in Go.
func (s *sqlStore) EmailHasActiveMembership(ctx context.Context, email string) (bool, error) {
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
// (docs/61 §61.11.3-3). Being on ANOTHER tenant's roster says nothing about this
// subsidiary's IdP.
func (s *sqlStore) EmailHasActiveMembershipInTenant(ctx context.Context, email, tenantID string) (bool, error) {
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

func (s *sqlStore) AnyActiveMembership(ctx context.Context) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM membership WHERE status='active' LIMIT 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *sqlStore) GetUserLimit(ctx context.Context, membershipID string) (UserLimit, bool, error) {
	var u UserLimit
	err := s.db.QueryRowContext(ctx,
		`SELECT membership_id, max_sessions, disk_gb, mem_limit, cpu_limit, slot_class, created_at FROM user_limit WHERE membership_id=?`, membershipID).
		Scan(&u.MembershipID, &u.MaxSessions, &u.DiskGB, &u.MemLimit, &u.CPULimit, &u.SlotClass, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return UserLimit{}, false, nil
	}
	if err != nil {
		return UserLimit{}, false, err
	}
	return u, true, nil
}

func (s *sqlStore) PutUserLimit(ctx context.Context, membershipID string, q UserQuota) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_limit(membership_id, max_sessions, disk_gb, mem_limit, cpu_limit, slot_class, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(membership_id) DO UPDATE SET max_sessions=excluded.max_sessions, disk_gb=excluded.disk_gb,
		   mem_limit=excluded.mem_limit, cpu_limit=excluded.cpu_limit, slot_class=excluded.slot_class`,
		membershipID, q.MaxSessions, q.DiskGB, q.MemLimit, q.CPULimit, q.SlotClass, nowTS())
	return err
}

func (s *sqlStore) SetWorkspaceState(ctx context.Context, workspaceID, state string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspace SET state=?, last_active_at=? WHERE id=?`, state, nowTS(), workspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM workspace_stop_intent WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordWorkspaceActivity merges a monotonic activity watermark and connection
// lease from any CP replica. CASE is portable across SQLite/Postgres and prevents
// an inactive replica from shortening another replica's live lease.
func lockWorkspace(ctx context.Context, q sqlExecQuery, workspaceID string) error {
	_, err := q.ExecContext(ctx, `UPDATE workspace SET id=id WHERE id=?`, workspaceID)
	return err
}

func (s *sqlStore) RecordWorkspaceActivity(ctx context.Context, workspaceID, lastSeenAt, connectedUntil, now string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return false, err
	}
	var stopping int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_stop_intent WHERE workspace_id=?`, workspaceID).Scan(&stopping); err != nil {
		return false, err
	}
	if stopping != 0 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_activity
		(workspace_id, last_seen_at, connected_until, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET
		 last_seen_at=CASE WHEN workspace_activity.last_seen_at > excluded.last_seen_at
		                   THEN workspace_activity.last_seen_at ELSE excluded.last_seen_at END,
		 connected_until=CASE WHEN workspace_activity.connected_until > excluded.connected_until
		                      THEN workspace_activity.connected_until ELSE excluded.connected_until END,
		 updated_at=excluded.updated_at`, workspaceID, lastSeenAt, connectedUntil, now); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *sqlStore) WorkspaceHasRecentActivity(ctx context.Context, workspaceID, cutoff, now string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workspace_activity
		WHERE workspace_id=? AND (last_seen_at > ? OR connected_until > ?)`, workspaceID, cutoff, now).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *sqlStore) ClaimWorkspaceIdleStop(ctx context.Context, workspaceID, owner, operationID, cutoff, now string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return false, err
	}
	var leaseActive int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_share_owner_lease
		WHERE owner_membership_id=? AND operation_id=? AND expires_at>?`, owner, operationID, now).Scan(&leaseActive); err != nil {
		return false, err
	}
	if leaseActive == 0 {
		return false, nil
	}
	var recent int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_activity
		WHERE workspace_id=? AND (last_seen_at>? OR connected_until>?)`, workspaceID, cutoff, now).Scan(&recent); err != nil {
		return false, err
	}
	if recent != 0 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_stop_intent
		(workspace_id,owner_membership_id,operation_id,created_at) VALUES(?,?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET owner_membership_id=excluded.owner_membership_id,
		operation_id=excluded.operation_id,created_at=excluded.created_at`, workspaceID, owner, operationID, now); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *sqlStore) ReleaseWorkspaceIdleStop(ctx context.Context, workspaceID, operationID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_stop_intent WHERE workspace_id=? AND operation_id=?`, workspaceID, operationID)
	return err
}

// ClearWorkspaceIdleStop is the explicit lifecycle reconciliation path. Callers
// must already own the distributed lifecycle lease (and adapter host fence), so
// an intent orphaned by a crashed reaper can be safely superseded.
func (s *sqlStore) ClearWorkspaceIdleStop(ctx context.Context, workspaceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM workspace_stop_intent WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteWorkspace removes the workspace row and everything keyed to it. It is the DB
// half of the irreversible destroy (ADR 0045 決定 13); the runtime half (home, cloud
// resources) has already run by the time this is called.
//
// The dependents are deleted EXPLICITLY rather than left to ON DELETE CASCADE: only two
// of the five tables declare one, and a delete that half-works because of a schema
// detail is the kind of thing that shows up as a foreign-key error in production and
// nowhere in tests. session_share_proposal does cascade off shared_session_catalog on
// both dialects, so it is the one dependent left implicit.
func (s *sqlStore) DeleteWorkspace(ctx context.Context, workspaceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	for _, stmt := range []string{
		`DELETE FROM workspace_stop_intent WHERE workspace_id=?`,
		`DELETE FROM workspace_activity WHERE workspace_id=?`,
		`DELETE FROM shared_session_catalog WHERE workspace_id=?`,
		`DELETE FROM session WHERE workspace_id=?`,
		`DELETE FROM wrapped_dek WHERE workspace_id=?`,
		`DELETE FROM workspace WHERE id=?`,
	} {
		if _, err = tx.ExecContext(ctx, stmt, workspaceID); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return tx.Commit()
}

// AcquireWorkspaceOperationFence holds a Postgres session advisory lock across
// external Runtime I/O. Unlike a time-based lease it remains held while a CP is
// paused and is released automatically if the process/connection dies. SQLite is
// the single-CP local profile; native additionally supplies its kernel flock.
func (s *sqlStore) AcquireWorkspaceOperationFence(ctx context.Context, workspaceID string) (func(), error) {
	if s.dialect != "postgres" {
		return func() {}, nil
	}
	var conn *sql.Conn
	for {
		var err error
		conn, err = s.db.DB.Conn(ctx)
		if err != nil {
			return nil, err
		}
		var acquired bool
		err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, workspaceID).Scan(&acquired)
		if err != nil {
			discardSQLConn(conn) // server may have acquired immediately before the error
			return nil, err
		}
		if acquired {
			break
		}
		_ = conn.Close() // return the waiter connection before polling again
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			var unlocked bool
			err := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, workspaceID).Scan(&unlocked)
			cancel()
			if err != nil || !unlocked {
				discardSQLConn(conn)
				return
			}
			_ = conn.Close()
		})
	}, nil
}

// database/sql Conn.Close returns a physical PG session to the pool, which is
// unsafe when advisory-lock ownership is uncertain. ErrBadConn tells the pool to
// destroy that session; PostgreSQL then releases all of its session locks.
func discardSQLConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func (s *sqlStore) GetWorkspaceByMembership(ctx context.Context, membershipID string) (Workspace, bool, error) {
	ws, err := scanWorkspace(s.db.QueryRowContext(ctx, workspaceCols+` WHERE w.membership_id=?`, membershipID))
	if err == sql.ErrNoRows {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, err
	}
	return ws, true, nil
}

func (s *sqlStore) CreateWorkspace(ctx context.Context, ws Workspace) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace(id, tenant_id, membership_id, container_name, network, data_dir,
		   agent_port, agent_token, state, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		ws.ID, ws.TenantID, ws.MembershipID, ws.ContainerName, ws.Network, ws.DataDir,
		ws.AgentPort, ws.AgentToken, ws.State, ws.CreatedAt)
	return err
}

func (s *sqlStore) GetWorkspaceSettings(ctx context.Context, workspaceID string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(settings,'') FROM workspace WHERE id=?`, workspaceID).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *sqlStore) SetWorkspaceSettings(ctx context.Context, workspaceID, settingsJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workspace SET settings=? WHERE id=?`, settingsJSON, workspaceID)
	return err
}

func (s *sqlStore) MaxAgentPort(ctx context.Context) (int, error) {
	var max int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(CAST(agent_port AS INTEGER)), 0) FROM workspace`).Scan(&max)
	return max, err
}

func (s *sqlStore) ListWorkspaces(ctx context.Context, tenantID string) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, workspaceCols+` WHERE w.tenant_id=? ORDER BY w.created_at`, tenantID)
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

func (s *sqlStore) GetWrappedDEK(ctx context.Context, workspaceID string) (string, string, bool, error) {
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

func (s *sqlStore) PutWrappedDEK(ctx context.Context, workspaceID, ciphertext, keyRef string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wrapped_dek(workspace_id, ciphertext, key_ref, key_version, created_at)
		 VALUES(?, ?, ?, 1, ?)
		 ON CONFLICT(workspace_id) DO UPDATE SET ciphertext=excluded.ciphertext, key_ref=excluded.key_ref`,
		workspaceID, ciphertext, keyRef, nowTS())
	return err
}

func (s *sqlStore) ReplaceSessions(ctx context.Context, workspaceID string, rows []SessionRow) error {
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

func (s *sqlStore) ListSessions(ctx context.Context, workspaceID string) ([]SessionRow, error) {
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

func (s *sqlStore) CreatePAT(ctx context.Context, p PAT, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pat(id, identity_id, membership_id, token_hash, scope, name, created_at, expires_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.IdentityID, nullable(p.MembershipID), tokenHash, p.Scope, p.Name, p.CreatedAt, nullable(p.ExpiresAt))
	return err
}

func (s *sqlStore) GetPATByHash(ctx context.Context, tokenHash string) (PAT, bool, error) {
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

func (s *sqlStore) ListPATsByIdentity(ctx context.Context, identityID string) ([]PAT, error) {
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
func (s *sqlStore) RevokePAT(ctx context.Context, id, identityID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pat SET revoked_at=? WHERE id=? AND identity_id=? AND revoked_at IS NULL`,
		nowTS(), id, identityID)
	return err
}

func (s *sqlStore) TouchPAT(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pat SET last_used_at=? WHERE id=?`, nowTS(), id)
	return err
}

// --- Internal git repositories (docs/reference/internal-git-provider) ---

func (s *sqlStore) CreateGitRepo(ctx context.Context, g GitRepo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO git_repo(id, tenant_id, name, default_branch, created_by, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		g.ID, g.TenantID, g.Name, g.DefaultBranch, nullable(g.CreatedBy), g.CreatedAt)
	return err
}

func (s *sqlStore) ListGitReposByTenant(ctx context.Context, tenantID string) ([]GitRepo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, default_branch, COALESCE(created_by,''), created_at
		 FROM git_repo WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitRepo
	for rows.Next() {
		var g GitRepo
		if err := rows.Scan(&g.ID, &g.TenantID, &g.Name, &g.DefaultBranch, &g.CreatedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetGitRepo(ctx context.Context, tenantID, name string) (GitRepo, bool, error) {
	var g GitRepo
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, default_branch, COALESCE(created_by,''), created_at
		 FROM git_repo WHERE tenant_id=? AND name=?`, tenantID, name).
		Scan(&g.ID, &g.TenantID, &g.Name, &g.DefaultBranch, &g.CreatedBy, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return GitRepo{}, false, nil
	}
	if err != nil {
		return GitRepo{}, false, err
	}
	return g, true, nil
}

func (s *sqlStore) CountGitReposByTenant(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM git_repo WHERE tenant_id=?`, tenantID).Scan(&n)
	return n, err
}

// RenameGitRepo renames one repo within a tenant. The (tenant_id, name) UNIQUE
// constraint makes a collision with an existing name an error (surfaced as a 409
// by the caller after a pre-check).
func (s *sqlStore) RenameGitRepo(ctx context.Context, tenantID, oldName, newName string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE git_repo SET name=? WHERE tenant_id=? AND name=?`, newName, tenantID, oldName)
	return err
}

func (s *sqlStore) DeleteGitRepo(ctx context.Context, tenantID, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM git_repo WHERE tenant_id=? AND name=?`, tenantID, name)
	return err
}

// --- Git LFS object ledger (docs/reference/internal-git-provider, P3) ---

func (s *sqlStore) PutLFSObject(ctx context.Context, tenantID, repo, oid string, size int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO lfs_object(tenant_id, repo_name, oid, size, created_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, repo_name, oid) DO NOTHING`,
		tenantID, repo, oid, size, nowTS())
	return err
}

func (s *sqlStore) TenantLFSBytes(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(size), 0) FROM lfs_object WHERE tenant_id=?`, tenantID).Scan(&n)
	return n, err
}

func (s *sqlStore) DeleteLFSObjectsByRepo(ctx context.Context, tenantID, repo string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM lfs_object WHERE tenant_id=? AND repo_name=?`, tenantID, repo)
	return err
}

func (s *sqlStore) RenameLFSObjectsRepo(ctx context.Context, tenantID, oldRepo, newRepo string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE lfs_object SET repo_name=? WHERE tenant_id=? AND repo_name=?`, newRepo, tenantID, oldRepo)
	return err
}

func (s *sqlStore) DeleteLFSObject(ctx context.Context, tenantID, repo, oid string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM lfs_object WHERE tenant_id=? AND repo_name=? AND oid=?`, tenantID, repo, oid)
	return err
}

// --- Git LFS locks (docs/reference/internal-git-provider, P3) ---

const lfsLockCols = `SELECT id, tenant_id, repo_name, path, ref_name, owner_id, owner_name, locked_at FROM lfs_lock`

func scanLFSLock(row interface{ Scan(...any) error }) (LFSLock, error) {
	var l LFSLock
	err := row.Scan(&l.ID, &l.TenantID, &l.RepoName, &l.Path, &l.RefName, &l.OwnerID, &l.OwnerName, &l.LockedAt)
	return l, err
}

func (s *sqlStore) CreateLFSLock(ctx context.Context, l LFSLock) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO lfs_lock(id, tenant_id, repo_name, path, ref_name, owner_id, owner_name, locked_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.TenantID, l.RepoName, l.Path, l.RefName, l.OwnerID, l.OwnerName, l.LockedAt)
	return err
}

func (s *sqlStore) GetLFSLockByPath(ctx context.Context, tenantID, repo, path string) (LFSLock, bool, error) {
	l, err := scanLFSLock(s.db.QueryRowContext(ctx,
		lfsLockCols+` WHERE tenant_id=? AND repo_name=? AND path=?`, tenantID, repo, path))
	if err == sql.ErrNoRows {
		return LFSLock{}, false, nil
	}
	if err != nil {
		return LFSLock{}, false, err
	}
	return l, true, nil
}

func (s *sqlStore) GetLFSLock(ctx context.Context, tenantID, repo, id string) (LFSLock, bool, error) {
	l, err := scanLFSLock(s.db.QueryRowContext(ctx,
		lfsLockCols+` WHERE tenant_id=? AND repo_name=? AND id=?`, tenantID, repo, id))
	if err == sql.ErrNoRows {
		return LFSLock{}, false, nil
	}
	if err != nil {
		return LFSLock{}, false, err
	}
	return l, true, nil
}

// ListLFSLocks returns locks ordered oldest-first, paginated by an opaque cursor
// (a row offset). It fetches limit+1 to know whether a next page exists; the
// returned cursor is "" when the page is the last.
func (s *sqlStore) ListLFSLocks(ctx context.Context, tenantID, repo, filterPath, filterID string, limit int, cursor string) ([]LFSLock, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	offset := 0
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			offset = n
		}
	}
	q := lfsLockCols + ` WHERE tenant_id=? AND repo_name=?`
	args := []any{tenantID, repo}
	if filterPath != "" {
		q += ` AND path=?`
		args = append(args, filterPath)
	}
	if filterID != "" {
		q += ` AND id=?`
		args = append(args, filterID)
	}
	q += ` ORDER BY locked_at, id LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []LFSLock
	for rows.Next() {
		l, err := scanLFSLock(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.Itoa(offset + limit)
	}
	return out, next, nil
}

func (s *sqlStore) DeleteLFSLock(ctx context.Context, tenantID, repo, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM lfs_lock WHERE tenant_id=? AND repo_name=? AND id=?`, tenantID, repo, id)
	return err
}

func (s *sqlStore) DeleteLFSLocksByRepo(ctx context.Context, tenantID, repo string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM lfs_lock WHERE tenant_id=? AND repo_name=?`, tenantID, repo)
	return err
}

func (s *sqlStore) RenameLFSLocksRepo(ctx context.Context, tenantID, oldRepo, newRepo string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE lfs_lock SET repo_name=? WHERE tenant_id=? AND repo_name=?`, newRepo, tenantID, oldRepo)
	return err
}

func (s *sqlStore) MembershipOwnerName(ctx context.Context, membershipID string) (string, error) {
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

func (s *sqlStore) ListLFSObjectOIDs(ctx context.Context, tenantID, repo string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT oid FROM lfs_object WHERE tenant_id=? AND repo_name=?`, tenantID, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			return nil, err
		}
		out = append(out, oid)
	}
	return out, rows.Err()
}

func (s *sqlStore) InsertAudit(ctx context.Context, a AuditLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log(id, tenant_id, actor_kind, actor_id, action, target, detail, at, http_status)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.TenantID, a.ActorKind, a.ActorID, a.Action, a.Target, a.Detail, a.At, a.HTTPStatus)
	return err
}

func (s *sqlStore) ListAuditByTenant(ctx context.Context, tenantID string, limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	// tenantID=="" spans every tenant (super_admin, deployment-wide view); a set
	// tenantID scopes to that tenant only.
	q := `SELECT id, tenant_id, actor_kind, actor_id, action, target, detail, at, http_status FROM audit_log`
	args := []any{}
	if tenantID != "" {
		q += ` WHERE tenant_id=?`
		args = append(args, tenantID)
	}
	q += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		var a AuditLog
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ActorKind, &a.ActorID,
			&a.Action, &a.Target, &a.Detail, &a.At, &a.HTTPStatus); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecordEgress accumulates egress hits into the (day, host, allowed) bucket
// (docs/20 M2). Upsert += so repeated batches add up.
func (s *sqlStore) RecordEgress(ctx context.Context, day, host string, allowed bool, count int) error {
	a := 0
	if allowed {
		a = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO egress_daily(day, host, allowed, count)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(day, host, allowed) DO UPDATE SET count = egress_daily.count + excluded.count`,
		day, host, a, count)
	return err
}

// ListEgress returns the busiest destination hosts on/after sinceDay, each with its
// would-allow / would-block totals, most-hit first (docs/20 M2).
func (s *sqlStore) ListEgress(ctx context.Context, sinceDay string, limit int) ([]EgressStat, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT host,
		        SUM(CASE WHEN allowed=1 THEN count ELSE 0 END) AS allowed,
		        SUM(CASE WHEN allowed=0 THEN count ELSE 0 END) AS blocked
		 FROM egress_daily WHERE day >= ?
		 GROUP BY host ORDER BY (allowed+blocked) DESC, host ASC LIMIT ?`, sinceDay, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EgressStat
	for rows.Next() {
		var e EgressStat
		if err := rows.Scan(&e.Host, &e.Allowed, &e.Blocked); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- egress allowlist + deployment settings (docs/20 M3) --------------------

func (s *sqlStore) ListAllowlist(ctx context.Context, state string, limit int) ([]AllowlistEntry, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT id, tenant_id, entry, state, reason, added_by, added_at FROM egress_allowlist`
	args := []any{}
	if state != "" {
		q += ` WHERE state=?`
		args = append(args, state)
	}
	q += ` ORDER BY added_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AllowlistEntry
	for rows.Next() {
		var e AllowlistEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Entry, &e.State, &e.Reason, &e.AddedBy, &e.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqlStore) AddAllowlist(ctx context.Context, e AllowlistEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO egress_allowlist(id, tenant_id, entry, state, reason, added_by, added_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TenantID, e.Entry, e.State, e.Reason, e.AddedBy, e.AddedAt)
	return err
}

func (s *sqlStore) SetAllowlistState(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE egress_allowlist SET state=? WHERE id=?`, state, id)
	return err
}

func (s *sqlStore) EffectiveAllowlist(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT entry FROM egress_allowlist WHERE state='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM deployment_setting WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *sqlStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployment_setting(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ListSettingKeys returns keys starting with prefix. LIKE narrows the scan but
// `_` in the prefix is a LIKE wildcard, so the literal-prefix check in Go is
// what actually decides membership.
func (s *sqlStore) ListSettingKeys(ctx context.Context, prefix string) ([]string, error) {
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

func (s *sqlStore) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_setting WHERE key=?`, key)
	return err
}

// AddUsage accumulates workspace running-seconds into the (membership, day)
// showback bucket (docs/roadmap.md P3-9). Upsert += so repeated samples add up.
func (s *sqlStore) AddUsage(ctx context.Context, membershipID, tenantID, day string, secs int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_daily(membership_id, tenant_id, day, running_secs)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(membership_id, day) DO UPDATE SET running_secs = usage_daily.running_secs + excluded.running_secs`,
		membershipID, tenantID, day, secs)
	return err
}

// ListUsage returns per-day showback rows in [fromDay, toDay] (inclusive),
// enriched with tenant slug + member key/email via LEFT JOINs (a row survives
// even if its membership/identity was later removed). tenantID=="" spans all
// tenants (super_admin); otherwise it is scoped to that tenant.
func (s *sqlStore) ListUsage(ctx context.Context, tenantID, fromDay, toDay string) ([]UsageRow, error) {
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

// workspaceCols joins the tenant only for its slug (Workspace.TenantSlug). A LEFT
// JOIN on purpose: a workspace whose tenant row is missing is already broken, but it
// must still be readable — the alternative is that the sweeper and the reaper stop
// seeing it, which turns a bad row into leaked AWS resources.
const workspaceCols = `SELECT w.id, w.tenant_id, COALESCE(t.slug,''), w.membership_id,
	w.container_name, w.network, w.data_dir, w.agent_port, w.agent_token, w.state,
	w.created_at, COALESCE(w.last_active_at,'')
	FROM workspace w LEFT JOIN tenant t ON t.id = w.tenant_id`

type scanner interface{ Scan(dest ...any) error }

func scanWorkspace(row scanner) (Workspace, error) {
	var ws Workspace
	err := row.Scan(&ws.ID, &ws.TenantID, &ws.TenantSlug, &ws.MembershipID, &ws.ContainerName,
		&ws.Network, &ws.DataDir, &ws.AgentPort, &ws.AgentToken, &ws.State, &ws.CreatedAt,
		&ws.LastActiveAt)
	return ws, err
}

// --- SSM login config (docs/history/p3-ssm-session.md) --------------------------

const ssmProfileCols = `SELECT id, membership_id, label, start_url, sso_region, account_id, role_name, region, created_at FROM ssm_profile`

func scanSSMProfile(row scanner) (SSMProfile, error) {
	var p SSMProfile
	err := row.Scan(&p.ID, &p.MembershipID, &p.Label, &p.StartURL, &p.SSORegion,
		&p.AccountID, &p.RoleName, &p.Region, &p.CreatedAt)
	return p, err
}

func (s *sqlStore) ListSSMProfiles(ctx context.Context, membershipID string) ([]SSMProfile, error) {
	rows, err := s.db.QueryContext(ctx, ssmProfileCols+` WHERE membership_id=? ORDER BY label`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSMProfile
	for rows.Next() {
		p, err := scanSSMProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetSSMProfile(ctx context.Context, id string) (SSMProfile, bool, error) {
	p, err := scanSSMProfile(s.db.QueryRowContext(ctx, ssmProfileCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return SSMProfile{}, false, nil
	}
	return p, err == nil, err
}

func (s *sqlStore) CreateSSMProfile(ctx context.Context, p SSMProfile) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ssm_profile(id, membership_id, label, start_url, sso_region, account_id, role_name, region, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		p.ID, p.MembershipID, p.Label, p.StartURL, p.SSORegion, p.AccountID, p.RoleName, p.Region, p.CreatedAt)
	return err
}

func (s *sqlStore) UpdateSSMProfile(ctx context.Context, p SSMProfile) error {
	// membership_id in the WHERE so a member can only update their own row.
	_, err := s.db.ExecContext(ctx,
		`UPDATE ssm_profile SET label=?, start_url=?, sso_region=?, account_id=?, role_name=?, region=?
		   WHERE id=? AND membership_id=?`,
		p.Label, p.StartURL, p.SSORegion, p.AccountID, p.RoleName, p.Region, p.ID, p.MembershipID)
	return err
}

func (s *sqlStore) DeleteSSMProfile(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ssm_profile WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

const ssmHostCols = `SELECT id, membership_id, alias, profile_id, region, instance_id, document_name, created_at FROM ssm_host`

func scanSSMHost(row scanner) (SSMHost, error) {
	var h SSMHost
	err := row.Scan(&h.ID, &h.MembershipID, &h.Alias, &h.ProfileID, &h.Region,
		&h.InstanceID, &h.DocumentName, &h.CreatedAt)
	return h, err
}

func (s *sqlStore) ListSSMHosts(ctx context.Context, membershipID string) ([]SSMHost, error) {
	rows, err := s.db.QueryContext(ctx, ssmHostCols+` WHERE membership_id=? ORDER BY alias`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSMHost
	for rows.Next() {
		h, err := scanSSMHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetSSMHost(ctx context.Context, id string) (SSMHost, bool, error) {
	h, err := scanSSMHost(s.db.QueryRowContext(ctx, ssmHostCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return SSMHost{}, false, nil
	}
	return h, err == nil, err
}

func (s *sqlStore) CreateSSMHost(ctx context.Context, h SSMHost) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ssm_host(id, membership_id, alias, profile_id, region, instance_id, document_name, created_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		h.ID, h.MembershipID, h.Alias, h.ProfileID, h.Region, h.InstanceID, h.DocumentName, h.CreatedAt)
	return err
}

func (s *sqlStore) UpdateSSMHost(ctx context.Context, h SSMHost) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE ssm_host SET alias=?, profile_id=?, region=?, instance_id=?, document_name=?
		   WHERE id=? AND membership_id=?`,
		h.Alias, h.ProfileID, h.Region, h.InstanceID, h.DocumentName, h.ID, h.MembershipID)
	return err
}

func (s *sqlStore) DeleteSSMHost(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ssm_host WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

// --- Memo queue (docs/21) --------------------------------------------------------

const memoCols = `SELECT id, membership_id, repo, category, kind, body, ref_path, attachments, position, created_at, sent_at FROM memo`

func scanMemo(row scanner) (Memo, error) {
	var m Memo
	err := row.Scan(&m.ID, &m.MembershipID, &m.Repo, &m.Category, &m.Kind,
		&m.Body, &m.RefPath, &m.Attachments, &m.Position, &m.CreatedAt, &m.SentAt)
	return m, err
}

// ListMemos returns unsent memos plus sent ones still inside the retention window
// (sent_at empty, or sent_at at/after retainBefore). retainBefore is an RFC3339
// cutoff; RFC3339 strings sort chronologically so the lexical compare is correct.
func (s *sqlStore) ListMemos(ctx context.Context, membershipID, retainBefore string) ([]Memo, error) {
	rows, err := s.db.QueryContext(ctx,
		memoCols+` WHERE membership_id=? AND (sent_at='' OR sent_at>=?)
		           ORDER BY repo, category, position, created_at`, membershipID, retainBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memo
	for rows.Next() {
		m, err := scanMemo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetMemo(ctx context.Context, id string) (Memo, bool, error) {
	m, err := scanMemo(s.db.QueryRowContext(ctx, memoCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return Memo{}, false, nil
	}
	return m, err == nil, err
}

func (s *sqlStore) CreateMemo(ctx context.Context, m Memo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memo(id, membership_id, repo, category, kind, body, ref_path, attachments, position, created_at, sent_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.MembershipID, m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.CreatedAt, m.SentAt)
	return err
}

func (s *sqlStore) UpdateMemo(ctx context.Context, m Memo) error {
	// membership_id in the WHERE so a member can only update their own row.
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo SET repo=?, category=?, kind=?, body=?, ref_path=?, attachments=?, position=?
		   WHERE id=? AND membership_id=?`,
		m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.ID, m.MembershipID)
	return err
}

func (s *sqlStore) DeleteMemo(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

// MarkMemosSent stamps sent_at on the caller's memos in ids (ownership enforced by
// membership_id in the WHERE, so foreign ids are silently ignored).
func (s *sqlStore) MarkMemosSent(ctx context.Context, membershipID string, ids []string, sentAt string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, sentAt)
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	args = append(args, membershipID)
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo SET sent_at=? WHERE id IN (`+strings.Join(ph, ",")+`) AND membership_id=?`, args...)
	return err
}

// SweepSentMemos deletes sent memos whose sent_at is before the retention cutoff.
func (s *sqlStore) SweepSentMemos(ctx context.Context, retainBefore string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo WHERE sent_at!='' AND sent_at<?`, retainBefore)
	return err
}

// --- Memo categories (docs/21 UI刷新) -------------------------------------------

const memoCategoryCols = `SELECT id, membership_id, repo, name, position, created_at FROM memo_category`

func scanMemoCategory(row scanner) (MemoCategory, error) {
	var c MemoCategory
	err := row.Scan(&c.ID, &c.MembershipID, &c.Repo, &c.Name, &c.Position, &c.CreatedAt)
	return c, err
}

// ListCategories returns a membership's categories ordered by repo then explicit
// position (created_at breaks ties so the order is stable).
func (s *sqlStore) ListCategories(ctx context.Context, membershipID string) ([]MemoCategory, error) {
	rows, err := s.db.QueryContext(ctx,
		memoCategoryCols+` WHERE membership_id=? ORDER BY repo, position, created_at`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoCategory
	for rows.Next() {
		c, err := scanMemoCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetCategory(ctx context.Context, id string) (MemoCategory, bool, error) {
	c, err := scanMemoCategory(s.db.QueryRowContext(ctx, memoCategoryCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return MemoCategory{}, false, nil
	}
	return c, err == nil, err
}

func (s *sqlStore) CreateCategory(ctx context.Context, c MemoCategory) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memo_category(id, membership_id, repo, name, position, created_at)
		 VALUES(?,?,?,?,?,?)`,
		c.ID, c.MembershipID, c.Repo, c.Name, c.Position, c.CreatedAt)
	return err
}

// UpdateCategory sets name + position on an owned category (membership_id in the WHERE).
func (s *sqlStore) UpdateCategory(ctx context.Context, c MemoCategory) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo_category SET name=?, position=? WHERE id=? AND membership_id=?`,
		c.Name, c.Position, c.ID, c.MembershipID)
	return err
}

func (s *sqlStore) DeleteCategory(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM memo_category WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

// ReassignMemoCategory rewrites memo.category from `from` to `to` for the caller's memos
// in a repo bucket (ownership by membership_id in the WHERE).
func (s *sqlStore) ReassignMemoCategory(ctx context.Context, membershipID, repo, from, to string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memo SET category=? WHERE membership_id=? AND repo=? AND category=?`,
		to, membershipID, repo, from)
	return err
}

// --- Notification center -------------------------------------------------------

const notificationCols = `SELECT seq, event_id, membership_id, kind, target_type, target_id, target_kind, display_name, payload, created_at, seen_at FROM notification`

func scanNotification(row scanner) (Notification, error) {
	var n Notification
	err := row.Scan(&n.Seq, &n.EventID, &n.MembershipID, &n.Kind, &n.TargetType,
		&n.TargetID, &n.TargetKind, &n.DisplayName, &n.Payload, &n.CreatedAt, &n.SeenAt)
	return n, err
}

func (s *sqlStore) InsertNotification(ctx context.Context, n Notification) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification(event_id, membership_id, kind, target_type, target_id, target_kind, display_name, payload, created_at, seen_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(event_id) DO NOTHING`, n.EventID, n.MembershipID,
		n.Kind, n.TargetType, n.TargetID, n.TargetKind, n.DisplayName, n.Payload, n.CreatedAt, n.SeenAt)
	return err
}

func (s *sqlStore) ListNotifications(ctx context.Context, membershipID, retainAfter string, limit int) ([]Notification, error) {
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

func (s *sqlStore) CountUnseenNotifications(ctx context.Context, membershipID, retainAfter string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification WHERE membership_id=? AND created_at>=? AND seen_at=''`, membershipID, retainAfter).Scan(&n)
	return n, err
}

func (s *sqlStore) MarkNotificationsSeenThrough(ctx context.Context, membershipID string, seq int64, seenAt string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notification SET seen_at=? WHERE membership_id=? AND seq<=? AND seen_at=''`, seenAt, membershipID, seq)
	return err
}

func (s *sqlStore) MarkNotificationsSeen(ctx context.Context, membershipID string, eventIDs []string, seenAt string) error {
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

func (s *sqlStore) SweepNotifications(ctx context.Context, retainBefore string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notification WHERE created_at<?`, retainBefore)
	return err
}

func (s *sqlStore) GetUsageNotificationState(ctx context.Context, membershipID, source, windowKey string) (UsageNotificationState, bool, error) {
	var st UsageNotificationState
	err := s.db.QueryRowContext(ctx, `SELECT membership_id, source, window_key, resets_at, armed FROM notification_usage_state WHERE membership_id=? AND source=? AND window_key=?`, membershipID, source, windowKey).
		Scan(&st.MembershipID, &st.Source, &st.WindowKey, &st.ResetsAt, &st.Armed)
	if err == sql.ErrNoRows {
		return UsageNotificationState{}, false, nil
	}
	return st, err == nil, err
}

func (s *sqlStore) PutUsageNotificationState(ctx context.Context, st UsageNotificationState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_usage_state(membership_id, source, window_key, resets_at, armed) VALUES(?,?,?,?,?)
		ON CONFLICT(membership_id, source, window_key) DO UPDATE SET resets_at=excluded.resets_at, armed=excluded.armed`,
		st.MembershipID, st.Source, st.WindowKey, st.ResetsAt, st.Armed)
	return err
}

// --- Schedules (docs/38 + ADR0021) ----------------------------------------------

// b2i/i2b bridge the 0/1 INTEGER columns (enabled, new_branch) and Go bools; the
// database/sql layer does not coerce int<->bool on its own.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

const scheduleCols = `SELECT id, membership_id, tenant_id, owner_conv, spec_kind, spec, spec_label, tz,
	wake_policy, session_mode, reuse_target, agent_kind, model, repo, worktree, new_branch, prompt,
	overlap_policy, enabled, next_run, last_run, last_status, created_at, updated_at,
	reuse_session, reuse_started_at, reuse_run_count, rotation, missing_target_policy,
	manual_fire_pending, report FROM schedule`

func scanSchedule(row scanner) (Schedule, error) {
	var s Schedule
	var newBranch, enabled, manualFire, report int
	err := row.Scan(&s.ID, &s.MembershipID, &s.TenantID, &s.OwnerConv, &s.SpecKind, &s.Spec, &s.SpecLabel, &s.TZ,
		&s.WakePolicy, &s.SessionMode, &s.ReuseTarget, &s.AgentKind, &s.Model, &s.Repo, &s.Worktree, &newBranch, &s.Prompt,
		&s.OverlapPolicy, &enabled, &s.NextRun, &s.LastRun, &s.LastStatus, &s.CreatedAt, &s.UpdatedAt,
		&s.ReuseSession, &s.ReuseStartedAt, &s.ReuseRunCount, &s.Rotation, &s.MissingTargetPolicy,
		&manualFire, &report)
	s.NewBranch = newBranch != 0
	s.Enabled = enabled != 0
	s.ManualFirePending = manualFire != 0
	s.Report = report != 0
	return s, err
}

func (s *sqlStore) CreateSchedule(ctx context.Context, sc Schedule) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO schedule(id, membership_id, tenant_id, owner_conv, spec_kind, spec, spec_label, tz,
		   wake_policy, session_mode, reuse_target, agent_kind, model, repo, worktree, new_branch, prompt,
		   overlap_policy, enabled, next_run, last_run, last_status, created_at, updated_at,
		   reuse_session, reuse_started_at, reuse_run_count, rotation, missing_target_policy, report)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sc.ID, sc.MembershipID, sc.TenantID, sc.OwnerConv, sc.SpecKind, sc.Spec, sc.SpecLabel, sc.TZ,
		sc.WakePolicy, sc.SessionMode, sc.ReuseTarget, sc.AgentKind, sc.Model, sc.Repo, sc.Worktree, b2i(sc.NewBranch), sc.Prompt,
		sc.OverlapPolicy, b2i(sc.Enabled), sc.NextRun, sc.LastRun, sc.LastStatus, sc.CreatedAt, sc.UpdatedAt,
		sc.ReuseSession, sc.ReuseStartedAt, sc.ReuseRunCount, sc.Rotation, sc.MissingTargetPolicy, b2i(sc.Report))
	return err
}

func (s *sqlStore) GetSchedule(ctx context.Context, id string) (Schedule, bool, error) {
	sc, err := scanSchedule(s.db.QueryRowContext(ctx, scheduleCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return Schedule{}, false, nil
	}
	return sc, err == nil, err
}

func (s *sqlStore) ListSchedules(ctx context.Context, membershipID string) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, scheduleCols+` WHERE membership_id=? ORDER BY created_at`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ListDueSchedules returns enabled rows with a non-empty next_run at or before
// nowRFC. The empty-string guard matters: next_run=” (a paused/spent schedule)
// must never look due, and ” sorts before any real RFC3339 stamp.
func (s *sqlStore) ListDueSchedules(ctx context.Context, nowRFC string) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		scheduleCols+` WHERE enabled=1 AND next_run!='' AND next_run<=? ORDER BY next_run`, nowRFC)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *sqlStore) UpdateSchedule(ctx context.Context, sc Schedule) error {
	// membership_id in the WHERE so a member can only update their own row.
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET owner_conv=?, spec_kind=?, spec=?, spec_label=?, tz=?, wake_policy=?,
		   session_mode=?, reuse_target=?, agent_kind=?, model=?, repo=?, worktree=?, new_branch=?, prompt=?,
		   overlap_policy=?, enabled=?, next_run=?, updated_at=?, rotation=?, missing_target_policy=?, report=?
		 WHERE id=? AND membership_id=?`,
		sc.OwnerConv, sc.SpecKind, sc.Spec, sc.SpecLabel, sc.TZ, sc.WakePolicy,
		sc.SessionMode, sc.ReuseTarget, sc.AgentKind, sc.Model, sc.Repo, sc.Worktree, b2i(sc.NewBranch), sc.Prompt,
		sc.OverlapPolicy, b2i(sc.Enabled), sc.NextRun, sc.UpdatedAt, sc.Rotation, sc.MissingTargetPolicy, b2i(sc.Report),
		sc.ID, sc.MembershipID)
	return err
}

// SetScheduleReuse persists the reuse ledger (P6) after a reuse fire: which long-lived
// session is now current, when it started, and the fire count since the last rotation.
// Kept separate from RecordScheduleFire (which advances the cron ledger) because the
// firer computes these before the fire ledger is stamped, and only reuse schedules touch
// them.
func (s *sqlStore) SetScheduleReuse(ctx context.Context, id, reuseSession, reuseStartedAt string, runCount int, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET reuse_session=?, reuse_started_at=?, reuse_run_count=?, updated_at=? WHERE id=?`,
		reuseSession, reuseStartedAt, runCount, updatedAt, id)
	return err
}

func (s *sqlStore) SetScheduleEnabled(ctx context.Context, id, membershipID string, enabled bool, nextRun, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET enabled=?, next_run=?, updated_at=? WHERE id=? AND membership_id=?`,
		b2i(enabled), nextRun, updatedAt, id, membershipID)
	return err
}

func (s *sqlStore) DeleteSchedule(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedule WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}

func (s *sqlStore) RecordScheduleFire(ctx context.Context, id, lastRun, lastStatus, nextRun string, enabled bool, updatedAt string) error {
	// Clear manual_fire_pending on every fire: the run-now signal is consumed once the
	// fire it requested has happened (the scheduler already read it to tag the run).
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET last_run=?, last_status=?, next_run=?, enabled=?, updated_at=?, manual_fire_pending=0 WHERE id=?`,
		lastRun, lastStatus, nextRun, b2i(enabled), updatedAt, id)
	return err
}

// MarkManualFirePending flags a run-now request: it sets next_run so the ticker fires the
// schedule immediately AND records that this next fire was manually triggered, so the run
// history can distinguish it from an automatic scheduled fire (docs/38). enabled is forced
// true (run-now on a paused schedule is rejected earlier). membership_id scopes the write.
func (s *sqlStore) MarkManualFirePending(ctx context.Context, id, membershipID, nextRun, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedule SET enabled=1, next_run=?, manual_fire_pending=1, updated_at=? WHERE id=? AND membership_id=?`,
		nextRun, updatedAt, id, membershipID)
	return err
}

func (s *sqlStore) AppendScheduleRun(ctx context.Context, run ScheduleRun, keepN int) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO schedule_run(id, schedule_id, membership_id, fired_at, status, detail, session, trigger_kind) VALUES(?,?,?,?,?,?,?,?)`,
		run.ID, run.ScheduleID, run.MembershipID, run.FiredAt, run.Status, run.Detail, run.Session, run.Trigger); err != nil {
		return err
	}
	if keepN <= 0 {
		return nil
	}
	// Trim to the keepN most recent rows for this schedule. The NOT IN (…LIMIT…)
	// subquery is dialect-neutral across SQLite and Postgres.
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM schedule_run WHERE schedule_id=? AND id NOT IN (
		   SELECT id FROM schedule_run WHERE schedule_id=? ORDER BY fired_at DESC LIMIT ?)`,
		run.ScheduleID, run.ScheduleID, keepN)
	return err
}

func (s *sqlStore) ListScheduleRuns(ctx context.Context, scheduleID, membershipID string, limit int) ([]ScheduleRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, schedule_id, membership_id, fired_at, status, detail, session, trigger_kind FROM schedule_run
		 WHERE schedule_id=? AND membership_id=? ORDER BY fired_at DESC LIMIT ?`,
		scheduleID, membershipID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleRun
	for rows.Next() {
		var r ScheduleRun
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.MembershipID, &r.FiredAt, &r.Status, &r.Detail, &r.Session, &r.Trigger); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- Tenant-distributed MCP servers (docs/48 P4 + ADR0031) ----------------------
//
// Every statement carries tenant_id, including the ones that already have the primary
// key in the WHERE. An id is opaque but not secret (it travels to the Console and into
// each member's cache), so scoping by tenant is what actually stops a tenant_admin of
// one tenant from reaching another's row by guessing or replaying an id.

const mcpServerCols = `SELECT id, tenant_id, name, label, transport, url, headers_enc, key_ref,
	targets, kinds, timeout_ms, enabled, user_secret, created_by, created_at, updated_at FROM mcp_server`

func scanMCPServer(row scanner) (MCPServerRow, error) {
	var m MCPServerRow
	var enabled, userSecret int
	err := row.Scan(&m.ID, &m.TenantID, &m.Name, &m.Label, &m.Transport, &m.URL, &m.HeadersEnc, &m.KeyRef,
		&m.Targets, &m.Kinds, &m.TimeoutMS, &enabled, &userSecret, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
	m.Enabled = enabled != 0
	m.UserSecret = userSecret != 0
	return m, err
}

func (s *sqlStore) ListMCPServers(ctx context.Context, tenantID string) ([]MCPServerRow, error) {
	rows, err := s.db.QueryContext(ctx, mcpServerCols+` WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPServerRow
	for rows.Next() {
		m, err := scanMCPServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *sqlStore) GetMCPServer(ctx context.Context, tenantID, id string) (MCPServerRow, bool, error) {
	m, err := scanMCPServer(s.db.QueryRowContext(ctx, mcpServerCols+` WHERE tenant_id=? AND id=?`, tenantID, id))
	if err == sql.ErrNoRows {
		return MCPServerRow{}, false, nil
	}
	return m, err == nil, err
}

func (s *sqlStore) CreateMCPServer(ctx context.Context, m MCPServerRow) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_server(id, tenant_id, name, label, transport, url, headers_enc, key_ref,
		   targets, kinds, timeout_ms, enabled, user_secret, created_by, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.TenantID, m.Name, m.Label, m.Transport, m.URL, m.HeadersEnc, m.KeyRef,
		m.Targets, m.Kinds, m.TimeoutMS, b2i(m.Enabled), b2i(m.UserSecret), m.CreatedBy, m.CreatedAt, m.UpdatedAt)
	return err
}

func (s *sqlStore) UpdateMCPServer(ctx context.Context, m MCPServerRow) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE mcp_server SET name=?, label=?, transport=?, url=?, headers_enc=?, key_ref=?,
		   targets=?, kinds=?, timeout_ms=?, enabled=?, user_secret=?, updated_at=?
		 WHERE tenant_id=? AND id=?`,
		m.Name, m.Label, m.Transport, m.URL, m.HeadersEnc, m.KeyRef,
		m.Targets, m.Kinds, m.TimeoutMS, b2i(m.Enabled), b2i(m.UserSecret), m.UpdatedAt,
		m.TenantID, m.ID)
	return err
}

func (s *sqlStore) DeleteMCPServer(ctx context.Context, tenantID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_server WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}

// --- tenant-defined login providers (docs/61 §61.11 + ADR0043 決定 29-33) -------

const tenantIdPCols = `SELECT id, tenant_id, name, label_ja, label_en, kind, issuer, client_id,
       secret_enc, key_ref, trust, allowed_tids, allowed_domains, allowed_orgs, link_claim, status,
       approved_by, approved_at, created_by, created_at, updated_at FROM tenant_idp`

func scanTenantIdP(sc scanner) (TenantIdP, error) {
	var t TenantIdP
	err := sc.Scan(&t.ID, &t.TenantID, &t.Name, &t.LabelJA, &t.LabelEN, &t.Kind, &t.Issuer, &t.ClientID,
		&t.SecretEnc, &t.KeyRef, &t.Trust, &t.AllowedTIDs, &t.AllowedDomains, &t.AllowedOrgs,
		&t.LinkClaim, &t.Status,
		&t.ApprovedBy, &t.ApprovedAt, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *sqlStore) ListTenantIdPs(ctx context.Context, tenantID string) ([]TenantIdP, error) {
	rows, err := s.db.QueryContext(ctx, tenantIdPCols+` WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantIdP
	for rows.Next() {
		t, err := scanTenantIdP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *sqlStore) ListAllTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error) {
	return s.listTenantIdPs(ctx, "")
}

func (s *sqlStore) ListActiveTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error) {
	return s.listTenantIdPs(ctx, "active")
}

// listTenantIdPs is the deployment-wide read. The tenant slug travels with each row
// because the provider id the rest of CP sees is built from it (t:<slug>:<name>),
// and joining here saves the caller a lookup per row. The display name comes along
// for the default button label (docs/61 §61.15.10).
//
// ★ Rows of a non-active tenant are left out: a suspended tenant's IdP must not keep
// minting sessions, the same way ListTenantLoginRules only loads active tenants.
func (s *sqlStore) listTenantIdPs(ctx context.Context, status string) ([]TenantIdP, map[string]TenantRef, error) {
	q := `SELECT p.id, p.tenant_id, p.name, p.label_ja, p.label_en, p.kind, p.issuer, p.client_id,
	             p.secret_enc, p.key_ref, p.trust, p.allowed_tids, p.allowed_domains, p.allowed_orgs,
	             p.link_claim, p.status,
	             p.approved_by, p.approved_at, p.created_by, p.created_at, p.updated_at, t.slug, t.name
	      FROM tenant_idp p JOIN tenant t ON t.id = p.tenant_id
	      WHERE t.status='active'`
	args := []any{}
	if status != "" {
		q += ` AND p.status=?`
		args = append(args, status)
	}
	q += ` ORDER BY t.slug, p.name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []TenantIdP
	tenants := map[string]TenantRef{}
	for rows.Next() {
		var t TenantIdP
		var ref TenantRef
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.LabelJA, &t.LabelEN, &t.Kind, &t.Issuer, &t.ClientID,
			&t.SecretEnc, &t.KeyRef, &t.Trust, &t.AllowedTIDs, &t.AllowedDomains, &t.AllowedOrgs,
			&t.LinkClaim, &t.Status,
			&t.ApprovedBy, &t.ApprovedAt, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &ref.Slug, &ref.Name); err != nil {
			return nil, nil, err
		}
		out = append(out, t)
		tenants[t.TenantID] = ref
	}
	return out, tenants, rows.Err()
}

func (s *sqlStore) GetTenantIdP(ctx context.Context, tenantID, id string) (TenantIdP, bool, error) {
	t, err := scanTenantIdP(s.db.QueryRowContext(ctx, tenantIdPCols+` WHERE tenant_id=? AND id=?`, tenantID, id))
	if err == sql.ErrNoRows {
		return TenantIdP{}, false, nil
	}
	return t, err == nil, err
}

func (s *sqlStore) CreateTenantIdP(ctx context.Context, t TenantIdP) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant_idp(id, tenant_id, name, label_ja, label_en, kind, issuer, client_id,
		   secret_enc, key_ref, trust, allowed_tids, allowed_domains, allowed_orgs, link_claim, status,
		   approved_by, approved_at, created_by, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.TenantID, t.Name, t.LabelJA, t.LabelEN, t.Kind, t.Issuer, t.ClientID,
		t.SecretEnc, t.KeyRef, t.Trust, t.AllowedTIDs, t.AllowedDomains, t.AllowedOrgs, t.LinkClaim, t.Status,
		t.ApprovedBy, t.ApprovedAt, t.CreatedBy, t.CreatedAt, t.UpdatedAt)
	return err
}

// UpdateTenantIdP replaces the editable content of a row. status / approved_* are
// written here too, because an edit that changes the issuer, the client_id or the
// trust rule sends the row back to pending (決定 30) — the caller computes that and
// this is where it lands.
func (s *sqlStore) UpdateTenantIdP(ctx context.Context, t TenantIdP) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenant_idp SET name=?, label_ja=?, label_en=?, kind=?, issuer=?, client_id=?,
		   secret_enc=?, key_ref=?, trust=?, allowed_tids=?, allowed_domains=?, allowed_orgs=?,
		   link_claim=?, status=?, approved_by=?, approved_at=?, updated_at=?
		 WHERE tenant_id=? AND id=?`,
		t.Name, t.LabelJA, t.LabelEN, t.Kind, t.Issuer, t.ClientID,
		t.SecretEnc, t.KeyRef, t.Trust, t.AllowedTIDs, t.AllowedDomains, t.AllowedOrgs,
		t.LinkClaim, t.Status, t.ApprovedBy, t.ApprovedAt, t.UpdatedAt,
		t.TenantID, t.ID)
	return err
}

func (s *sqlStore) SetTenantIdPStatus(ctx context.Context, tenantID, id, status, approvedBy, approvedAt, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenant_idp SET status=?, approved_by=?, approved_at=?, updated_at=? WHERE tenant_id=? AND id=?`,
		status, approvedBy, approvedAt, updatedAt, tenantID, id)
	return err
}

func (s *sqlStore) DeleteTenantIdP(ctx context.Context, tenantID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tenant_idp WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}

// TenantIdPIssuerInUse — see the interface. Rows of every tenant, and every status:
// a PENDING second registration is exactly the one worth catching, since the whole
// point is to say something before it is approved and people start signing in.
func (s *sqlStore) TenantIdPIssuerInUse(ctx context.Context, issuer, excludeID string) (bool, error) {
	if issuer == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM tenant_idp WHERE issuer=? AND id<>? LIMIT 1`, issuer, excludeID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CountMembersOnlyOnProvider — see the interface.
//
// ★ "Only" is over identity_provider, not over this tenant: the row records a PROVEN
// login, so a person with a second one can get back in through it even if that other
// method belongs to another tenant. Somebody with no identity_provider row at all is
// not counted either — they have never signed in (an invite placeholder), so this
// provider is not what they would lose.
func (s *sqlStore) CountMembersOnlyOnProvider(ctx context.Context, tenantID, providerID string) (int, error) {
	if tenantID == "" || providerID == "" {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM membership m
		 WHERE m.tenant_id=? AND m.status='active'
		   AND EXISTS (SELECT 1 FROM identity_provider ip
		               WHERE ip.identity_id=m.identity_id AND ip.provider=?)
		   AND NOT EXISTS (SELECT 1 FROM identity_provider ip2
		                   WHERE ip2.identity_id=m.identity_id AND ip2.provider<>?)`,
		tenantID, providerID, providerID).Scan(&n)
	return n, err
}

// --- cloud cost (docs/67, ADR 0048) --------------------------------------------

// PutCloudCost replaces the given days wholesale. Cost Explorer restates recent days
// (they arrive `Estimated` and keep moving for about a day), so the poller re-fetches a
// trailing window every run and the latest answer must WIN, not accumulate. Deleting the
// days first is also what makes a day that came back empty actually become empty —
// otherwise a resource that lost its tag would leave its last attributed row frozen in
// place forever, and the invoice would never stop blaming that person.
func (s *sqlStore) PutCloudCost(ctx context.Context, days []string, rows []CloudCostRow) error {
	if len(days) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range days {
		if _, err = tx.ExecContext(ctx, `DELETE FROM cloud_cost_daily WHERE day=?`, d); err != nil {
			return err
		}
	}
	now := nowTS()
	for _, r := range rows {
		est := 0
		if r.Estimated {
			est = 1
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO cloud_cost_daily(day, membership_id, tenant_id, service,
			   unblended, amortized, currency, estimated, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?)
			 ON CONFLICT(day, membership_id, service) DO UPDATE SET
			   tenant_id=excluded.tenant_id, unblended=excluded.unblended,
			   amortized=excluded.amortized, currency=excluded.currency,
			   estimated=excluded.estimated, updated_at=excluded.updated_at`,
			r.Day, r.MembershipID, r.TenantID, r.Service,
			r.Unblended, r.Amortized, r.Currency, est, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListCloudCost returns the raw per-(day, member, service) rows in the window.
// membershipID != "" is the member's own view; tenantID != "" scopes to one tenant.
//
// ⚠️ The shared bucket (membership_id=”) has no tenant, so a tenant-scoped query must
// NOT return it — a tenant_admin seeing the deployment's ALB/RDS bill would be reading
// outside their tenant (ADR 0048 決定 4). It falls out of the WHERE naturally, and that
// is deliberate rather than accidental.
func (s *sqlStore) ListCloudCost(ctx context.Context, tenantID, membershipID, fromDay, toDay string) ([]CloudCostRow, error) {
	q := `SELECT day, membership_id, tenant_id, service, unblended, amortized, currency, estimated
	      FROM cloud_cost_daily WHERE day BETWEEN ? AND ?`
	args := []any{fromDay, toDay}
	if tenantID != "" {
		q += ` AND tenant_id=?`
		args = append(args, tenantID)
	}
	if membershipID != "" {
		q += ` AND membership_id=?`
		args = append(args, membershipID)
	}
	q += ` ORDER BY day, membership_id, service`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudCostRow
	for rows.Next() {
		var r CloudCostRow
		var est int
		if err := rows.Scan(&r.Day, &r.MembershipID, &r.TenantID, &r.Service,
			&r.Unblended, &r.Amortized, &r.Currency, &est); err != nil {
			return nil, err
		}
		r.Estimated = est != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// CloudCostTotals sums the window per member, joining the labels in at read time (a
// deleted membership keeps its spend and surfaces with empty key/email, like ListUsage).
// The shared bucket is included as a row with an empty MembershipID; the API decides
// who is allowed to see it.
func (s *sqlStore) CloudCostTotals(ctx context.Context, tenantID, fromDay, toDay string) ([]CloudCostTotal, error) {
	q := `SELECT COALESCE(t.slug,''), c.membership_id, COALESCE(i.user_key,''), COALESCE(i.email,''),
	             SUM(c.unblended), SUM(c.amortized), COALESCE(MAX(c.currency),'')
	      FROM cloud_cost_daily c
	      LEFT JOIN tenant t ON t.id = c.tenant_id
	      LEFT JOIN membership m ON m.id = c.membership_id
	      LEFT JOIN identity i ON i.id = m.identity_id
	      WHERE c.day BETWEEN ? AND ?`
	args := []any{fromDay, toDay}
	if tenantID != "" {
		q += ` AND c.tenant_id=?`
		args = append(args, tenantID)
	}
	q += ` GROUP BY t.slug, c.membership_id, i.user_key, i.email ORDER BY SUM(c.unblended) DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudCostTotal
	for rows.Next() {
		var c CloudCostTotal
		if err := rows.Scan(&c.TenantSlug, &c.MembershipID, &c.UserKey, &c.Email,
			&c.Unblended, &c.Amortized, &c.Currency); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CloudCostDays is the coverage window that actually exists. It is what lets the API say
// "cost allocation was not switched on before this date" instead of drawing an honest-
// looking zero — and that distinction is permanent, because activation is not
// retroactive (docs/67 §67.5).
func (s *sqlStore) CloudCostDays(ctx context.Context) (string, string, error) {
	var first, last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(day), MAX(day) FROM cloud_cost_daily`).Scan(&first, &last)
	if err != nil {
		return "", "", err
	}
	return first.String, last.String, nil
}
