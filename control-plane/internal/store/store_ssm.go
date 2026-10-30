package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SSMProfile is the COMMON auth bundle shared by many hosts (docs/log/p3-ssm-
// session.md): the AWS IAM Identity Center (SSO) portal + account/role/default region.
// It maps to one ~/.aws named profile; `aws sso login` authenticates it. Personal
// scope. NON-SECRET: the CP never sees AWS credentials — the in-container aws CLI logs
// in directly against StartURL and caches the token in the workspace home.
//
// Kind is SSMKindSSO (the default; "" reads as it) or SSMKindAssumeRole: a profile with no
// portal of its own that assumes RoleARN from the login of the sso profile SourceProfileID
// (issue #1109). ExternalID, SessionName and DurationSeconds are its optional assume-role
// parameters. Also non-secret. An assume-role profile's AccountID is derived from RoleARN
// by the CP, never entered apart from it.
type SSMProfile struct {
	ID, MembershipID, Label                  string
	StartURL, SSORegion, AccountID, RoleName string
	Region, CreatedAt                        string
	Kind, SourceProfileID, RoleARN           string
	ExternalID, SessionName                  string
	DurationSeconds                          int
	// ConfirmUse makes af-aws-exec ask the member in the Console before the profile's first
	// use in a session (ADR 0126 decision 4).
	ConfirmUse bool
	// KeepConfirmUse tells UpdateSSMProfile to leave confirm_use as the row has it at the
	// UPDATE, ignoring ConfirmUse: a write that did not name the field must not put back a
	// value read before another write changed it (ADR 0126 decision 8). Never stored.
	KeepConfirmUse bool
	// SignInID is the shared sign-in the profile is in, "" for a sign-in of its own (ADR
	// 0125). SignInName is that sign-in's name, read with the row.
	SignInID, SignInName string
	// SignIn is what a create or an update does with the profile's sign-in. Never stored.
	SignIn SignInWrite
}

// SignInWrite is a write's instruction for the profile's sign-in. The zero value keeps the
// sign-in an update finds on the row (a create gets none): a write that did not name it must
// not move the profile (ADR 0125 decision 11).
type SignInWrite struct {
	// Set is true when the write names the sign-in; Name "" then leaves any sign-in.
	Set  bool
	Name string
}

// The values of SSMProfile.Kind.
const (
	SSMKindSSO        = "sso"
	SSMKindAssumeRole = "assume_role"
)

// SSMHost is a per-member bookmark for one SSM Session Manager target: which instance,
// which run-as SSM document, an optional region override, and which profile to
// authenticate with. No secrets. Mirrors the operator's hand-rolled bash map (host
// alias -> "document instance") as first-class WebUI records.
type SSMHost struct {
	ID, MembershipID, Alias, ProfileID  string
	Region                              string // optional per-host override ("" = profile default)
	InstanceID, DocumentName, CreatedAt string
}

// ErrSSMProfileNotFound is CreateSSMHost / UpdateSSMHost refusing a host whose profile the
// member does not have.
var ErrSSMProfileNotFound = errors.New("ssm profile not found")

// ErrSSMSourceInvalid is Create/UpdateSSMProfile refusing an assume-role profile whose source is
// not an sso profile of the same member: missing, someone else's, or itself an assume-role
// profile, a chain the exported AWS config does not express.
var ErrSSMSourceInvalid = errors.New("ssm profile source must be an sso profile of the same member")

// SSMProfileInUseError is DeleteSSMProfile refusing a profile that hosts still reference.
// Hosts lists them, ordered by alias, so the caller can name them.
type SSMProfileInUseError struct {
	Hosts []SSMHost
	// Dependents are the assume-role profiles that take this one as their source (issue #1109).
	Dependents []SSMProfile
}

func (e *SSMProfileInUseError) Error() string {
	if len(e.Dependents) > 0 {
		return fmt.Sprintf("ssm profile is used by %d host(s) and %d assume-role profile(s)", len(e.Hosts), len(e.Dependents))
	}
	return fmt.Sprintf("ssm profile is used by %d host(s)", len(e.Hosts))
}

// SSMStore is the SSM login config (docs/log/p3-ssm-session.md), personal
// scope. No AWS secrets stored. Mutations are scoped by membership so a member
// only touches their own rows. A profile is the common auth bundle; a host
// references one.
type SSMStore interface {
	ListSSMProfiles(ctx context.Context, membershipID string) ([]SSMProfile, error)
	GetSSMProfile(ctx context.Context, id string) (SSMProfile, bool, error)
	CreateSSMProfile(ctx context.Context, p SSMProfile) error
	UpdateSSMProfile(ctx context.Context, p SSMProfile) error
	DeleteSSMProfile(ctx context.Context, id, membershipID string) error
	ListSSMSignIns(ctx context.Context, membershipID string) ([]SSMSignIn, error)
	GetSSMSignIn(ctx context.Context, id string) (SSMSignIn, bool, error)
	UpdateSSMSignIn(ctx context.Context, si SSMSignIn) (bool, error)
	ListSSMHosts(ctx context.Context, membershipID string) ([]SSMHost, error)
	GetSSMHost(ctx context.Context, id string) (SSMHost, bool, error)
	CreateSSMHost(ctx context.Context, h SSMHost) error
	UpdateSSMHost(ctx context.Context, h SSMHost) error
	DeleteSSMHost(ctx context.Context, id, membershipID string) error
}

// --- SSM login config (docs/log/p3-ssm-session.md) --------------------------

const ssmProfileCols = `SELECT id, membership_id, label, start_url, sso_region, account_id, role_name, region, created_at,
	kind, source_profile_id, role_arn, external_id, session_name, duration_seconds, confirm_use, sign_in_id,
	COALESCE((SELECT si.name FROM ssm_sign_in si WHERE si.id = ssm_profile.sign_in_id), '') FROM ssm_profile`

func scanSSMProfile(row scanner) (SSMProfile, error) {
	var p SSMProfile
	var confirm int
	err := row.Scan(&p.ID, &p.MembershipID, &p.Label, &p.StartURL, &p.SSORegion,
		&p.AccountID, &p.RoleName, &p.Region, &p.CreatedAt,
		&p.Kind, &p.SourceProfileID, &p.RoleARN, &p.ExternalID, &p.SessionName, &p.DurationSeconds, &confirm,
		&p.SignInID, &p.SignInName)
	p.ConfirmUse = confirm != 0
	return p, err
}

func (s *SQL) ListSSMProfiles(ctx context.Context, membershipID string) ([]SSMProfile, error) {
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

func (s *SQL) GetSSMProfile(ctx context.Context, id string) (SSMProfile, bool, error) {
	p, err := scanSSMProfile(s.db.QueryRowContext(ctx, ssmProfileCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return SSMProfile{}, false, nil
	}
	return p, err == nil, err
}

// checkSSMSource, for an assume-role profile, takes the write lock of its source (lockSSMProfile)
// and refuses with ErrSSMSourceInvalid unless that is an sso profile of the member's own and
// not the profile itself. Under the lock the source cannot be deleted or turned into an
// assume-role profile before this write commits.
func checkSSMSource(ctx context.Context, tx *sqlTx, p SSMProfile) error {
	if p.Kind != SSMKindAssumeRole {
		return nil
	}
	if p.SourceProfileID == "" || p.SourceProfileID == p.ID {
		return ErrSSMSourceInvalid
	}
	found, err := lockSSMProfile(ctx, tx, p.SourceProfileID, p.MembershipID)
	if err != nil {
		return err
	}
	if !found {
		return ErrSSMSourceInvalid
	}
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM ssm_profile WHERE id=?`, p.SourceProfileID).Scan(&kind); err != nil {
		return err
	}
	if kind != SSMKindSSO {
		return ErrSSMSourceInvalid
	}
	return nil
}

func (s *SQL) CreateSSMProfile(ctx context.Context, p SSMProfile) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockSSMMember(ctx, tx, p.MembershipID); err != nil {
		return err
	}
	if err := checkSSMSource(ctx, tx, p); err != nil {
		return err
	}
	if p.SignIn.Set && p.SignIn.Name != "" {
		if err := joinSignIn(ctx, tx, &p); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ssm_profile(id, membership_id, label, start_url, sso_region, account_id, role_name, region, created_at,
			kind, source_profile_id, role_arn, external_id, session_name, duration_seconds, confirm_use, sign_in_id)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.MembershipID, p.Label, p.StartURL, p.SSORegion, p.AccountID, p.RoleName, p.Region, p.CreatedAt,
		ssmKindOrDefault(p.Kind), p.SourceProfileID, p.RoleARN, p.ExternalID, p.SessionName, p.DurationSeconds, boolInt(p.ConfirmUse),
		p.SignInID); err != nil {
		return err
	}
	if err := checkSignInNames(ctx, tx, p.MembershipID); err != nil {
		return err
	}
	return tx.Commit()
}

func ssmKindOrDefault(k string) string {
	if k == "" {
		return SSMKindSSO
	}
	return k
}

// UpdateSSMProfile writes the profile. Turning a profile that other assume-role profiles use
// as their source into an assume-role profile itself is refused with *SSMProfileInUseError
// (Dependents), for the same reason DeleteSSMProfile refuses: the chain would be two hops.
func (s *SQL) UpdateSSMProfile(ctx context.Context, p SSMProfile) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockSSMMember(ctx, tx, p.MembershipID); err != nil {
		return err
	}
	if err := checkSSMSource(ctx, tx, p); err != nil {
		return err
	}
	// The row as it is now, under the member's lock: what an update that does not name the
	// sign-in keeps.
	cur, err := scanSSMProfile(tx.QueryRowContext(ctx, ssmProfileCols+` WHERE id=? AND membership_id=?`, p.ID, p.MembershipID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case !p.SignIn.Set && cur.SignInID != "" && ssmKindOrDefault(p.Kind) == SSMKindSSO:
		p.SignIn = SignInWrite{Set: true, Name: cur.SignInName}
	case !p.SignIn.Set:
		p.SignIn = SignInWrite{Set: true}
	}
	if p.SignIn.Name != "" {
		if err := joinSignIn(ctx, tx, &p); err != nil {
			return err
		}
	} else {
		p.SignInID = ""
	}
	if p.Kind == SSMKindAssumeRole {
		found, err := lockSSMProfile(ctx, tx, p.ID, p.MembershipID)
		if err != nil || !found {
			return err
		}
		deps, err := ssmDependents(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if len(deps) > 0 {
			return &SSMProfileInUseError{Dependents: deps}
		}
	}
	// membership_id in the WHERE so a member can only update their own row. The CASE keeps
	// confirm_use in the same statement when the write did not name it, so no value read
	// earlier can overwrite one committed since.
	keep := 0
	if p.KeepConfirmUse {
		keep = 1
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE ssm_profile SET label=?, start_url=?, sso_region=?, account_id=?, role_name=?, region=?,
			kind=?, source_profile_id=?, role_arn=?, external_id=?, session_name=?, duration_seconds=?,
			confirm_use = CASE WHEN ? = 1 THEN confirm_use ELSE ? END, sign_in_id=?
		   WHERE id=? AND membership_id=?`,
		p.Label, p.StartURL, p.SSORegion, p.AccountID, p.RoleName, p.Region,
		ssmKindOrDefault(p.Kind), p.SourceProfileID, p.RoleARN, p.ExternalID, p.SessionName, p.DurationSeconds,
		keep, boolInt(p.ConfirmUse), p.SignInID,
		p.ID, p.MembershipID); err != nil {
		return err
	}
	if cur.SignInID != "" && cur.SignInID != p.SignInID {
		if err := dropEmptySignIn(ctx, tx, cur.SignInID); err != nil {
			return err
		}
	}
	if err := checkSignInNames(ctx, tx, p.MembershipID); err != nil {
		return err
	}
	return tx.Commit()
}

// ssmDependents lists the assume-role profiles whose source is id, by label.
func ssmDependents(ctx context.Context, q *sqlTx, id string) ([]SSMProfile, error) {
	rows, err := q.QueryContext(ctx, ssmProfileCols+` WHERE source_profile_id=? AND kind=? ORDER BY label`, id, SSMKindAssumeRole)
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

// lockSSMProfile takes the profile row's write lock for the rest of tx and reports whether
// the member owns such a profile. The profile delete and every host write that names a
// profile go through it, so on Postgres (READ COMMITTED, ten connections) a host saved
// against a profile being deleted either commits first and is seen by the delete's host
// query, or waits and then finds the profile gone. Without it the delete's "no hosts" read
// and the host's "profile exists" read both pass and the host is stranded. SQLite has one
// connection, so there the transaction alone serializes them.
func lockSSMProfile(ctx context.Context, q sqlExecQuery, id, membershipID string) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE ssm_profile SET id=id WHERE id=? AND membership_id=?`, id, membershipID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteSSMProfile refuses with *SSMProfileInUseError while any host references the
// profile: ssm_host.profile_id has no foreign key, so this is the only thing that keeps a
// host from being left on a dead profile. Deleting a profile that is not there is a no-op.
func (s *SQL) DeleteSSMProfile(ctx context.Context, id, membershipID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockSSMMember(ctx, tx, membershipID); err != nil {
		return err
	}
	found, err := lockSSMProfile(ctx, tx, id, membershipID)
	if err != nil || !found {
		return err
	}
	var signInID string
	if err := tx.QueryRowContext(ctx, `SELECT sign_in_id FROM ssm_profile WHERE id=?`, id).Scan(&signInID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, ssmHostCols+` WHERE profile_id=? ORDER BY alias`, id)
	if err != nil {
		return err
	}
	var using []SSMHost
	for rows.Next() {
		h, err := scanSSMHost(rows)
		if err != nil {
			rows.Close()
			return err
		}
		using = append(using, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	deps, err := ssmDependents(ctx, tx, id)
	if err != nil {
		return err
	}
	if len(using) > 0 || len(deps) > 0 {
		return &SSMProfileInUseError{Hosts: using, Dependents: deps}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM ssm_profile WHERE id=? AND membership_id=?`, id, membershipID); err != nil {
		return err
	}
	if signInID != "" {
		if err := dropEmptySignIn(ctx, tx, signInID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const ssmHostCols = `SELECT id, membership_id, alias, profile_id, region, instance_id, document_name, created_at FROM ssm_host`

func scanSSMHost(row scanner) (SSMHost, error) {
	var h SSMHost
	err := row.Scan(&h.ID, &h.MembershipID, &h.Alias, &h.ProfileID, &h.Region,
		&h.InstanceID, &h.DocumentName, &h.CreatedAt)
	return h, err
}

func (s *SQL) ListSSMHosts(ctx context.Context, membershipID string) ([]SSMHost, error) {
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

func (s *SQL) GetSSMHost(ctx context.Context, id string) (SSMHost, bool, error) {
	h, err := scanSSMHost(s.db.QueryRowContext(ctx, ssmHostCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return SSMHost{}, false, nil
	}
	return h, err == nil, err
}

// writeSSMHost runs one host write under the lock of the profile it names (lockSSMProfile),
// and returns ErrSSMProfileNotFound instead when the member has no such profile.
func (s *SQL) writeSSMHost(ctx context.Context, h SSMHost, q string, args ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	found, err := lockSSMProfile(ctx, tx, h.ProfileID, h.MembershipID)
	if err != nil {
		return err
	}
	if !found {
		return ErrSSMProfileNotFound
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQL) CreateSSMHost(ctx context.Context, h SSMHost) error {
	return s.writeSSMHost(ctx, h,
		`INSERT INTO ssm_host(id, membership_id, alias, profile_id, region, instance_id, document_name, created_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		h.ID, h.MembershipID, h.Alias, h.ProfileID, h.Region, h.InstanceID, h.DocumentName, h.CreatedAt)
}

func (s *SQL) UpdateSSMHost(ctx context.Context, h SSMHost) error {
	return s.writeSSMHost(ctx, h,
		`UPDATE ssm_host SET alias=?, profile_id=?, region=?, instance_id=?, document_name=?
		   WHERE id=? AND membership_id=?`,
		h.Alias, h.ProfileID, h.Region, h.InstanceID, h.DocumentName, h.ID, h.MembershipID)
}

func (s *SQL) DeleteSSMHost(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ssm_host WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}
