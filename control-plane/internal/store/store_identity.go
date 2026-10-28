package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Identity is a person, unique by email within the deployment. role is the
// deployment-scoped role (user | super_admin).
type Identity struct {
	ID, Email, UserKey, Role, Status, LastLoginAt string
}

// IdentityLink is one proven login, on its way to LinkIdentity. It is a struct
// rather than eight positional parameters because every field but one is a string
// and this is the identity path: a swapped pair here hands somebody another
// person's workspace, and `Realm: x, Subject: y` says which is which at the call
// site.
type IdentityLink struct {
	Provider string // the button that was pressed (env id, or t:<slug>:<name>)
	Subject  string // the IdP's stable id for this account
	// Realm is WHERE Subject was proven: the issuer URL for OIDC, and
	// https://github.com for the GitHub adapter. Two providers sharing a realm are
	// two buttons onto the same IdP, so the same subject there is the same person —
	// that is rule 1.5, and it is the one join a tenant-defined provider is allowed
	// to make, because the realm is asserted by the adapter and verified against
	// that IdP, never taken from the tenant's row (docs/log/61 §61.15).
	Realm string
	// RealmClaim / RealmSubject are rule 1.5's SECOND key (docs/log/61 §61.15.10 + decision
	// 38). Some IdPs make `sub` pairwise — Entra's is a function of (app registration,
	// user) — so the same person through two app registrations on one issuer is two
	// subjects and rule 1.5 never fires. A provider may therefore also name a stable
	// claim (`oid`): RealmClaim is WHICH claim was read, RealmSubject is what it
	// carried. Both must match for the rule to join, so two providers reading
	// different claims never join on a coincidental value collision.
	//
	// ★ Subject keeps meaning `sub`. Replacing it would change the key of every row
	// already written, and a tenant-defined provider — which has no rule 2 to fall
	// back on — would refuse its own existing people as email_taken.
	RealmClaim   string
	RealmSubject string
	Email        string
	FallbackKey  string // sanitizeUser(email), used only when a new identity is created
	RoleHint     string
	EmailJoin    bool
}

// LinkedProvider is one sign-in method bound to a person, as shown on their own
// account panel (docs/log/61 §61.16). Subject is included because it is the only
// stable handle on the row; the Console shows the label and the address instead.
type LinkedProvider struct {
	Provider    string
	Subject     string
	Realm       string
	Email       string
	CreatedAt   string
	LastLoginAt string
}

type IdentityStore interface {
	// UpsertIdentity creates/updates the person. roleHint, when non-empty,
	// upgrades the deployment role (e.g. "super_admin" from SUPER_ADMIN_EMAILS);
	// it never downgrades.
	UpsertIdentity(ctx context.Context, email, key, roleHint string) (Identity, error)
	// LinkIdentity is UpsertIdentity for a login that proved an IdP subject
	// (AUTH=oauth). The (provider, subject) pair decides who this is, so an email
	// change keeps the same identity — and therefore the same user_key, workspace
	// and secrets (docs/log/61 §61.5). isNew reports that no existing identity matched
	// and a new one was created, which is the only signal the caller has that this
	// person landed in a different workspace than the one they may expect.
	// fallbackKey is used only on that path (it is sanitizeUser(email)).
	//
	// ★ EmailJoin selects rule 2 ("this address already belongs to someone — join
	// that identity"). It is FALSE for a tenant-defined provider (docs/log/61 §61.11 +
	// decision 32): its issuer belongs to the subsidiary, not to the operator, so an
	// admin there could otherwise mint a token carrying a colleague's address and
	// land in that colleague's identity — home, secrets and all. The caller decides,
	// because the naming rule that distinguishes the two kinds of provider
	// (tenantProviderID) belongs to the login layer, not to SQL.
	LinkIdentity(ctx context.Context, link IdentityLink) (ident Identity, isNew bool, err error)
	// FillProviderRealm stamps the realm on the rows a provider wrote before the
	// column existed (or through a path that had no provider object). Startup-only
	// and idempotent — see the implementation for why it cannot be a migration.
	FillProviderRealm(ctx context.Context, provider, realm string) error
	// ListLinkedProviders returns the sign-in methods bound to one person, newest
	// login first. It is the read half of the "link a second sign-in method" flow
	// (docs/log/61 §61.16) and carries nothing secret — the pairs it lists are exactly
	// what the person proved by signing in.
	ListLinkedProviders(ctx context.Context, identityID string) ([]LinkedProvider, error)
	// AttachProvider binds an ALREADY-PROVEN (provider, subject) to an EXISTING
	// identity, at that person's own request (docs/log/61 §61.16 + decision 37). It differs
	// from LinkIdentity in three ways that are the whole point:
	//
	//   - it never creates an identity and never resolves one — the caller passes the
	//     identity that is already signed in;
	//   - it never touches the identity row: not the email, and above all not the
	//     role (decision 31 — a linked method must not be able to hand out a deployment
	//     role, the same reason roleHint is suppressed for tenant-defined providers);
	//   - it REFUSES rather than re-points when the pair (or the same IdP account
	//     reached through another button — rule 1.5) already belongs to somebody:
	//     ErrLinkTaken. Joining two accounts that both have a login history is a
	//     merge, and a merge cannot be undone (§61.5).
	AttachProvider(ctx context.Context, identityID string, link IdentityLink) error
	// DetachProvider removes one sign-in method from a person's account (docs/log/61
	// §61.16.4, the deferred half of decision 37). identityID is always in the WHERE, so
	// no caller can reach somebody else's row even with a (provider, subject) it
	// guessed.
	//
	// ★ It REFUSES to remove the last one (ErrLastLoginMethod). An account with no
	// method left cannot be signed into at all, and there is no recovery path from
	// the Console — this deployment has no password and no SMTP (decision 28). The count
	// is taken in SQL, in the same statement, because the caller's check and the
	// delete are otherwise two moments with a browser tab in between.
	// ErrNoSuchLoginMethod means the pair is not this person's (or not there at all);
	// the two are separate so the API can answer 409 and 404 rather than one 400.
	DetachProvider(ctx context.Context, identityID, provider, subject string) error
	GetIdentityByID(ctx context.Context, id string) (Identity, bool, error)
	// GetIdentityByUserKey is the READ-ONLY lookup for view paths (admin stats,
	// admin MCP list tools): unlike UpsertIdentity it neither inserts a row for a
	// mistyped key nor touches last_login_at.
	GetIdentityByUserKey(ctx context.Context, key string) (Identity, bool, error)
	// DemoteSuperAdmins drops every identity whose deployment role is super_admin
	// and whose email is NOT in keep back to "user", and returns the demoted
	// addresses. SUPER_ADMIN_EMAILS is the single source of truth and CP runs this
	// once at STARTUP (ADR0043 decision 24): a login-time sync would never reach the
	// person who left, because the person who left never logs in again. Identities
	// with an empty email are left alone — they cannot be named in the env, so
	// demoting them would be unrecoverable by the documented procedure.
	DemoteSuperAdmins(ctx context.Context, keep []string) ([]string, error)
}

func (s *SQL) UpsertIdentity(ctx context.Context, email, key, roleHint string) (Identity, error) {
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
		NewID(), email, key, NowTS()); err != nil {
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
// startup (ADR0043 decision 24). UpsertIdentity's roleHint stays upgrade-only on
// purpose: addMembership / cleanHome / stopWorkspace all call it with roleHint="",
// so putting the demotion there would strip an operator's role the moment somebody
// added a member. The revocation therefore lives here, in a pass CP runs once at
// boot — which is also the only moment that reaches a person who has left and will
// never sign in again.
//
// The candidate set is read first rather than expressed as a NOT IN (…) so the
// caller can log exactly whose role was revoked; the set is a handful of rows.
func (s *SQL) DemoteSuperAdmins(ctx context.Context, keep []string) ([]string, error) {
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
// the three rules of docs/log/61 §61.5 in order:
//
//  1. (provider, subject) already recorded => that identity, whatever its email is
//     now. user_key does not move, so home and secrets stay put across a rename.
//     1.5. not recorded, but the SAME REALM has this subject on another provider =>
//     that identity (docs/log/61 §61.15 + decision 35). Two buttons can be two doors onto
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
// emailJoin=false is a TENANT-DEFINED provider (docs/log/61 §61.11): the operator did
// not configure that issuer, a subsidiary's administrator did, so an asserted
// address is not proof of being that person. Rule 2 becomes rule 2' — CLAIM an
// identity nobody has ever signed in as (the placeholder an invite leaves behind,
// which is how a subsidiary's first login is supposed to work), and refuse with
// ErrIdentityClaimed when the address belongs to an account that has a login
// history. Falling through to rule 3 instead would NOT be fail-closed: user_key is
// sanitizeUser(email), so UpsertIdentity's ON CONFLICT(user_key) would land right
// back on the very identity rule 2 was refusing to join (and identity.email is
// UNIQUE, so a genuinely separate row cannot exist either). Refusing is the only
// answer that is actually a refusal.
func (s *SQL) LinkIdentity(ctx context.Context, link IdentityLink) (Identity, bool, error) {
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
		// …and its second key, for an issuer whose `sub` is pairwise (docs/log/61
		// §61.15.10 + decision 38). Tried AFTER the subject match, so an IdP where both
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
				return Identity{}, false, ErrIdentityClaimed
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
	now := NowTS()
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

// ErrIdentityClaimed is returned when a tenant-defined provider asserts an address
// that already belongs to somebody who has signed in here. The login is refused
// rather than joined or duplicated — see LinkIdentity.
var ErrIdentityClaimed = errors.New("this email address already belongs to an account on this deployment")

// identityHasProvider reports whether anybody has ever completed an IdP login as
// this identity. An identity with no such row is either an invite placeholder or a
// pre-P1 account; that is the line rule 2' draws.
func (s *SQL) identityHasProvider(ctx context.Context, identityID string) (bool, error) {
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
func (s *SQL) identityIDForProvider(ctx context.Context, provider, subject string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT identity_id FROM identity_provider WHERE provider=? AND subject=?`, provider, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// identityIDForRealm implements rule 1.5: the same IdP account reached through a
// DIFFERENT button (docs/log/61 §61.15). The realm identifies the authority that
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
func (s *SQL) identityIDForRealm(ctx context.Context, realm, subject string) (string, error) {
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
// by a STABLE CLAIM rather than by `sub` (docs/log/61 §61.15.10 + decision 38). Entra's
// `sub` is pairwise — a function of (app registration, user) — so one person coming
// through the head office's app and through a subsidiary's carries two subjects on one
// issuer, and identityIDForRealm cannot see that they are the same account. `oid` is
// the same value in both tokens, and this is where that is read.
//
// The CLAIM NAME is part of the key, not just the value. Two providers reading
// DIFFERENT claims must never join because their values happened to collide — the
// name records which question was asked. And an empty claim or value matches
// nothing, so every row written before the column, and every provider that names no
// claim, simply does not take part.
func (s *SQL) identityIDForRealmClaim(ctx context.Context, realm, claim, subject string) (string, error) {
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
// wrote, and is called once per provider at STARTUP (docs/log/61 §61.15).
//
// It has to happen in Go rather than in the migration: which realm a provider id
// belongs to is only known from the provider set CP just built, and a migration that
// guessed "provider='github' means the GitHub adapter" would be wrong on a
// deployment that named an OIDC provider `github` (oauth_oidc.go warns about that
// but still builds it). Only empty realms are written, so a person whose row was
// filled by a login keeps that value, and re-running is a no-op.
func (s *SQL) FillProviderRealm(ctx context.Context, provider, realm string) error {
	if provider == "" || realm == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE identity_provider SET realm=? WHERE provider=? AND realm=''`, realm, provider)
	return err
}

// ErrLinkTaken is returned by AttachProvider when the IdP account being linked
// already belongs to somebody on this deployment — the pair itself is recorded, or
// rule 1.5 finds the same account under another button. Linking it would either
// re-point an existing mapping or merge two accounts, and both are irreversible.
var ErrLinkTaken = errors.New("that sign-in method already belongs to an account on this deployment")

// ListLinkedProviders — see the Store interface (docs/log/61 §61.16).
func (s *SQL) ListLinkedProviders(ctx context.Context, identityID string) ([]LinkedProvider, error) {
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

// AttachProvider — see the Store interface (docs/log/61 §61.16 + decision 37).
//
// The three refusals below are STRUCTURAL: they hold whoever calls this and
// whatever the login layer checked first. The caller adds the policy half (the
// address must be the person's own — decision 37), which this layer cannot state
// because it does not know which of several rules the deployment settled on; what
// it does know is that a row must never move to another identity and that two
// accounts must never become one.
func (s *SQL) AttachProvider(ctx context.Context, identityID string, link IdentityLink) error {
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
		return ErrLinkTaken
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
			return ErrLinkTaken
		}
		// ★ …and through rule 1.5's second key (docs/log/61 §61.15.10). Without this the
		// pairwise-`sub` case slips past: two subjects, one Entra account, so the pair
		// looks free while signing in with it would land on somebody else.
		byClaim, err := s.identityIDForRealmClaim(ctx, link.Realm, link.RealmClaim, link.RealmSubject)
		if err != nil {
			return err
		}
		if byClaim != "" && byClaim != identityID {
			return ErrLinkTaken
		}
	}
	// 3. the address the IdP asserted belongs to a different person. Even with the
	//    caller's same-address rule in front, this stays: identity.email is UNIQUE
	//    and case-sensitive there, so a case-differing row is reachable, and it is
	//    somebody else's account.
	if ident, ok, err := s.identityByEmail(ctx, link.Email); err != nil {
		return err
	} else if ok && ident.ID != identityID {
		return ErrLinkTaken
	}
	now := NowTS()
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

// ErrLastLoginMethod / ErrNoSuchLoginMethod are DetachProvider's two refusals.
//
// The first one is a lockout guard, not a formality: with the last method gone
// there is no way back into the account — no password, no SMTP to mail a link from
// (decision 28) — and the person doing it is the account's own owner, mid-cleanup, who
// is exactly the one who cannot ask anybody to undo it.
var (
	ErrLastLoginMethod   = errors.New("this is the only sign-in method left on the account, and removing it would lock you out")
	ErrNoSuchLoginMethod = errors.New("that sign-in method is not linked to this account")
)

// DetachProvider — see the Store interface (docs/log/61 §61.16.4).
func (s *SQL) DetachProvider(ctx context.Context, identityID, provider, subject string) error {
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
		return ErrNoSuchLoginMethod
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
		return ErrLastLoginMethod
	}
	return nil
}

// identityByEmail finds the person an address already belongs to. Non-empty
// emails are UNIQUE (idx_identity_email), so at most one row matches; the empty
// email never does (those rows are invite-by-user_key placeholders, claimed through
// UpsertIdentity's key path instead).
func (s *SQL) identityByEmail(ctx context.Context, email string) (Identity, bool, error) {
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
func (s *SQL) createIdentityForLogin(ctx context.Context, email, key, roleHint string) (Identity, bool, error) {
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
func (s *SQL) touchIdentity(ctx context.Context, identityID, email, roleHint string) error {
	// Two shapes rather than one with NULLIF/CASE: both dialects parse these
	// without having to infer a placeholder's type from a bare '' literal.
	var err error
	if email == "" {
		_, err = s.db.ExecContext(ctx, `UPDATE identity SET last_login_at=? WHERE id=?`, NowTS(), identityID)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE identity SET last_login_at=?, email=? WHERE id=?`, NowTS(), email, identityID)
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
func (s *SQL) disambiguateUserKey(ctx context.Context, email, key string) (string, error) {
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
	return key + disambiguatedKeySep + hex.EncodeToString(sum[:4]), nil
}

// disambiguatedKeySep and disambiguatedKeyHex are the suffix disambiguateUserKey appends;
// SplitDisambiguatedUserKey parses the same shape, so change them together.
const (
	disambiguatedKeySep = "-"
	disambiguatedKeyHex = 8
)

// SplitDisambiguatedUserKey splits a key of disambiguateUserKey's shape,
// "<key>-<8 lowercase hex>", into its prefix. The admin API uses it to accept a stored key
// that runs past sanitizeUser's 40 characters; whether the prefix is itself a sanitized
// key is the caller's check, since sanitizeUser lives outside this package.
func SplitDisambiguatedUserKey(key string) (prefix string, ok bool) {
	i := len(key) - disambiguatedKeyHex - len(disambiguatedKeySep)
	if i < 1 || key[i:i+len(disambiguatedKeySep)] != disambiguatedKeySep {
		return "", false
	}
	for _, c := range key[i+len(disambiguatedKeySep):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return key[:i], true
}

func (s *SQL) GetIdentityByUserKey(ctx context.Context, key string) (Identity, bool, error) {
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

func (s *SQL) GetIdentityByID(ctx context.Context, id string) (Identity, bool, error) {
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
