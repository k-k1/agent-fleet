package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Shared IAM Identity Center sign-ins (ADR 0125). A sign-in is a member-named portal that SSO
// profiles opt into; the workspace renders one [sso-session af-<name>] for it. The rules here
// keep two things true for every member, on SQLite and on Postgres:
//   - the sso-session names are unique: no sign-in is named like a profile with a sign-in of
//     its own (decision 2). A sign-in may take the name of one of its own members;
//   - a sign-in exists exactly while a profile is in it (decision 1).
//
// Every write to a member's profiles or sign-ins takes the member's row lock first
// (lockSSMMember) and only then reads and checks. A transaction alone does not serialize the
// checks on Postgres (READ COMMITTED): two writes that each read "no other side" would both
// commit.

// SSMSignIn is one shared sign-in. No secret: the portal's start URL and SSO region.
type SSMSignIn struct {
	ID, MembershipID, Name string
	StartURL, SSORegion    string
	CreatedAt              string
}

// SignInNameRe is what a sign-in name may hold: af-<name> must fit the Agent's 64-character
// profile allowlist, and a name is never sanitized, so two names cannot collapse into one.
var SignInNameRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,61}$`)

// profileNameRe strips what an ~/.aws/config profile header cannot hold.
var profileNameRe = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)

// ProfileName is the ~/.aws profile name of a Settings label, and so the sso-session name
// (af-<name>) of a profile with a sign-in of its own.
func ProfileName(label string) string {
	p := profileNameRe.ReplaceAllString(strings.TrimSpace(label), "-")
	if p == "" {
		p = "ssm"
	}
	return p
}

// SignInNameTakenError refuses a write after which a sign-in and a profile with a sign-in of
// its own would render the same sso-session name.
type SignInNameTakenError struct{ Name string }

func (e *SignInNameTakenError) Error() string {
	return fmt.Sprintf("sign-in name %q is the name of a profile with a sign-in of its own", e.Name)
}

var (
	// ErrSignInMismatch: the write names a sign-in whose portal differs from the write's.
	ErrSignInMismatch = errors.New("the sign-in's start URL or SSO region differs from the profile's")
	// ErrSignInInvalid: a new sign-in's name or portal cannot be stored.
	ErrSignInInvalid = errors.New("invalid sign-in")
	// ErrSignInKind: only an SSO profile has a sign-in.
	ErrSignInKind = errors.New("only an sso profile joins a sign-in")
	// ErrSignInExists: a rename to a name another of the member's sign-ins has.
	ErrSignInExists = errors.New("the member has a sign-in of that name")
	// ErrSSMPortalRequired: an SSO profile with a sign-in of its own needs both its start URL
	// and its SSO region; the error names the missing one (startUrl or ssoRegion).
	ErrSSMPortalRequired = errors.New("an sso profile with a sign-in of its own needs its portal")
)

// requirePortal refuses an SSO profile that ends up with a sign-in of its own and lacks part
// of its portal. A profile in a shared sign-in has the sign-in's.
func requirePortal(p SSMProfile) error {
	if !p.CheckPortal || ssmKindOrDefault(p.Kind) != SSMKindSSO || p.SignInID != "" {
		return nil
	}
	switch {
	case p.StartURL == "":
		return fmt.Errorf("%w: startUrl", ErrSSMPortalRequired)
	case p.SSORegion == "":
		return fmt.Errorf("%w: ssoRegion", ErrSSMPortalRequired)
	}
	return nil
}

// lockSSMMember takes the member's row lock for the rest of tx: an UPDATE that changes nothing,
// which every profile and sign-in write of the member takes first, so their checks run one
// after another.
func lockSSMMember(ctx context.Context, q sqlExecQuery, membershipID string) error {
	_, err := q.ExecContext(ctx, `UPDATE membership SET id=id WHERE id=?`, membershipID)
	return err
}

const ssmSignInCols = `SELECT id, membership_id, name, start_url, sso_region, created_at FROM ssm_sign_in`

func scanSSMSignIn(row scanner) (SSMSignIn, error) {
	var si SSMSignIn
	err := row.Scan(&si.ID, &si.MembershipID, &si.Name, &si.StartURL, &si.SSORegion, &si.CreatedAt)
	return si, err
}

// joinSignIn points p at the sign-in p.SignIn.Name, creating it from p's portal when the
// member has none of that name, and copies the sign-in's portal into p. The caller holds the
// member's lock.
func joinSignIn(ctx context.Context, tx *sqlTx, p *SSMProfile) error {
	if ssmKindOrDefault(p.Kind) != SSMKindSSO {
		return ErrSignInKind
	}
	si, err := scanSSMSignIn(tx.QueryRowContext(ctx, ssmSignInCols+` WHERE membership_id=? AND name=?`, p.MembershipID, p.SignIn.Name))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if !SignInNameRe.MatchString(p.SignIn.Name) || p.StartURL == "" || p.SSORegion == "" {
			return ErrSignInInvalid
		}
		si = SSMSignIn{ID: NewID(), MembershipID: p.MembershipID, Name: p.SignIn.Name, StartURL: p.StartURL,
			SSORegion: p.SSORegion, CreatedAt: NowTS()}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ssm_sign_in(id, membership_id, name, start_url, sso_region, created_at)
			VALUES(?,?,?,?,?,?)`, si.ID, si.MembershipID, si.Name, si.StartURL, si.SSORegion, si.CreatedAt); err != nil {
			return err
		}
	case err != nil:
		return err
	case (p.StartURL != "" && p.StartURL != si.StartURL) || (p.SSORegion != "" && p.SSORegion != si.SSORegion):
		return ErrSignInMismatch
	}
	p.SignInID, p.SignInName, p.StartURL, p.SSORegion = si.ID, si.Name, si.StartURL, si.SSORegion
	return nil
}

// dropEmptySignIn deletes the sign-in id when no profile is in it any more.
func dropEmptySignIn(ctx context.Context, tx *sqlTx, id string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM ssm_sign_in WHERE id=? AND NOT EXISTS (SELECT 1 FROM ssm_profile WHERE sign_in_id=?)`, id, id)
	return err
}

// checkSignInNames refuses, with *SignInNameTakenError, a member state in which a sign-in has
// the name of an SSO profile with a sign-in of its own. The caller holds the member's lock.
func checkSignInNames(ctx context.Context, tx *sqlTx, membershipID string) error {
	names := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM ssm_sign_in WHERE membership_id=?`, membershipID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(names) == 0 {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT label FROM ssm_profile WHERE membership_id=? AND sign_in_id='' AND kind=?`,
		membershipID, SSMKindSSO)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return err
		}
		if n := ProfileName(label); names[n] {
			return &SignInNameTakenError{Name: n}
		}
	}
	return rows.Err()
}

// ListSSMSignIns lists the member's sign-ins by name.
func (s *SQL) ListSSMSignIns(ctx context.Context, membershipID string) ([]SSMSignIn, error) {
	rows, err := s.db.QueryContext(ctx, ssmSignInCols+` WHERE membership_id=? ORDER BY name`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSMSignIn
	for rows.Next() {
		si, err := scanSSMSignIn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// GetSSMSignIn reads one sign-in.
func (s *SQL) GetSSMSignIn(ctx context.Context, id string) (SSMSignIn, bool, error) {
	si, err := scanSSMSignIn(s.db.QueryRowContext(ctx, ssmSignInCols+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SSMSignIn{}, false, nil
	}
	return si, err == nil, err
}

// UpdateSSMSignIn renames the sign-in and/or changes its portal, and rewrites its members'
// copies of the portal in the same transaction (decision 3). found is false when the member
// has no such sign-in.
func (s *SQL) UpdateSSMSignIn(ctx context.Context, si SSMSignIn) (bool, error) {
	if !SignInNameRe.MatchString(si.Name) || si.StartURL == "" || si.SSORegion == "" {
		return false, ErrSignInInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := lockSSMMember(ctx, tx, si.MembershipID); err != nil {
		return false, err
	}
	cur, err := scanSSMSignIn(tx.QueryRowContext(ctx, ssmSignInCols+` WHERE id=? AND membership_id=?`, si.ID, si.MembershipID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if si.Name != cur.Name {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ssm_sign_in WHERE membership_id=? AND name=? AND id<>?`,
			si.MembershipID, si.Name, si.ID).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return false, ErrSignInExists
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ssm_sign_in SET name=?, start_url=?, sso_region=? WHERE id=? AND membership_id=?`,
		si.Name, si.StartURL, si.SSORegion, si.ID, si.MembershipID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ssm_profile SET start_url=?, sso_region=? WHERE sign_in_id=? AND membership_id=?`,
		si.StartURL, si.SSORegion, si.ID, si.MembershipID); err != nil {
		return false, err
	}
	if err := checkSignInNames(ctx, tx, si.MembershipID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// SignInReconcile is one change ReconcileSSMSignIns made.
type SignInReconcile struct {
	MembershipID, ProfileID, SignInID, What string
}

// ReconcileSSMSignIns repairs, once at start, what an older binary can leave behind after a
// rollback (decision 11): it writes a grouped profile's kind and portal without knowing the
// sign-in. A member that is no longer an SSO profile, or whose portal copy differs from its
// sign-in, leaves the sign-in and keeps its own values; a sign-in left without members (or
// whose member rows were deleted by an older binary) is deleted. Name collisions it cannot
// repair are left to the bridge, which exports neither side.
func (s *SQL) ReconcileSSMSignIns(ctx context.Context) ([]SignInReconcile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT membership_id FROM ssm_sign_in`)
	if err != nil {
		return nil, err
	}
	var members []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []SignInReconcile
	for _, m := range members {
		got, err := s.reconcileMember(ctx, m)
		if err != nil {
			return out, err
		}
		out = append(out, got...)
	}
	return out, nil
}

func (s *SQL) reconcileMember(ctx context.Context, membershipID string) ([]SignInReconcile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockSSMMember(ctx, tx, membershipID); err != nil {
		return nil, err
	}
	var out []SignInReconcile
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.sign_in_id, p.kind, p.start_url, p.sso_region, si.start_url, si.sso_region
		FROM ssm_profile p JOIN ssm_sign_in si ON si.id = p.sign_in_id WHERE p.membership_id=?`, membershipID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, sid, kind, url, region, siURL, siRegion string
		if err := rows.Scan(&id, &sid, &kind, &url, &region, &siURL, &siRegion); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case ssmKindOrDefault(kind) != SSMKindSSO:
			out = append(out, SignInReconcile{membershipID, id, sid, "left the sign-in: no longer an sso profile"})
		case url != siURL || region != siRegion:
			out = append(out, SignInReconcile{membershipID, id, sid, "left the sign-in: its portal was changed by an older binary"})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range out {
		if _, err := tx.ExecContext(ctx, `UPDATE ssm_profile SET sign_in_id='' WHERE id=?`, r.ProfileID); err != nil {
			return nil, err
		}
	}
	// Profiles that point at a sign-in that is gone have nothing to share.
	if _, err := tx.ExecContext(ctx, `UPDATE ssm_profile SET sign_in_id='' WHERE membership_id=? AND sign_in_id<>''
		AND NOT EXISTS (SELECT 1 FROM ssm_sign_in si WHERE si.id = ssm_profile.sign_in_id)`, membershipID); err != nil {
		return nil, err
	}
	empty, err := tx.QueryContext(ctx, `SELECT id FROM ssm_sign_in WHERE membership_id=?
		AND NOT EXISTS (SELECT 1 FROM ssm_profile p WHERE p.sign_in_id = ssm_sign_in.id)`, membershipID)
	if err != nil {
		return nil, err
	}
	var gone []string
	for empty.Next() {
		var id string
		if err := empty.Scan(&id); err != nil {
			empty.Close()
			return nil, err
		}
		gone = append(gone, id)
	}
	empty.Close()
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ssm_sign_in WHERE id=?`, id); err != nil {
			return nil, err
		}
		out = append(out, SignInReconcile{MembershipID: membershipID, SignInID: id, What: "deleted: no profile is in it"})
	}
	return out, tx.Commit()
}
