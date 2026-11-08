package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
)

// Provided model routes (ADR 0129): the source registrations, the grants and budgets, the monthly
// spend and the per-call reservations. Phase Q0 builds the model only; nothing in the request
// path calls these yet. Amounts are integer micro-USD.

var (
	ErrProvidedRouteSourceNotFound = errors.New("provided route source not found")
	// ErrProvidedRouteFingerprint: the source's payer changed since the caller read it, so what
	// the caller proved (an activation probe) is about another payer.
	ErrProvidedRouteFingerprint = errors.New("provided route source was re-fingerprinted")
	// ErrProvidedRouteCallExists: the call id was admitted before with different contents. A
	// retry of the same admission is not this error: it reads as admitted (Replayed).
	ErrProvidedRouteCallExists   = errors.New("provided route call id already admitted with other contents")
	ErrProvidedRouteCallNotFound = errors.New("provided route call not found")
)

// ProvidedRouteStore holds the provided routes' data model.
type ProvidedRouteStore interface {
	PutProvidedRouteSource(ctx context.Context, in ProvidedRouteSourceInput) (ProvidedRouteSource, error)
	GetProvidedRouteSource(ctx context.Context, id string) (ProvidedRouteSource, error)
	// ListProvidedRouteSources lists one tenant's tenant sources, or with tenantID "" the
	// deployment's sources.
	ListProvidedRouteSources(ctx context.Context, tenantID string) ([]ProvidedRouteSource, error)
	DeleteProvidedRouteSource(ctx context.Context, id string) error
	RotateProvidedRouteExternalID(ctx context.Context, id string) (ProvidedRouteSource, error)
	SetProvidedRouteSourceActive(ctx context.Context, id, fingerprint string, active bool) error

	GetProvidedRouteGrant(ctx context.Context, tenantID, source string) (ProvidedRouteGrant, error)
	SetProvidedRouteGrant(ctx context.Context, tenantID, source string, granted bool, ceilingMicro *int64) error
	SetProvidedRouteMemberPolicy(ctx context.Context, tenantID, source string, membersOnly bool, defaultMicro int64) error
	GetProvidedRouteMember(ctx context.Context, membershipID, source string) (ProvidedRouteMember, error)
	SetProvidedRouteMember(ctx context.Context, tenantID, membershipID, source string, granted bool, budgetMicro *int64) error

	AdmitProvidedRouteCall(ctx context.Context, c ProvidedRouteCall, lim ProvidedRouteLimits) (ProvidedRouteAdmission, error)
	SettleProvidedRouteCall(ctx context.Context, callID string, finalMicro int64) (bool, error)
	GetProvidedRouteCall(ctx context.Context, callID string) (ProvidedRouteCall, error)
	// ListInFlightProvidedRouteCalls lists every call still in flight, oldest first, at most
	// limit of them: what the orphan sweep and a draining CP settle.
	ListInFlightProvidedRouteCalls(ctx context.Context, limit int) ([]ProvidedRouteCall, error)
	// GetProvidedRoutePeriod reads one level's month; membershipID "" is the tenant level. A month
	// nobody spent in reads as zero.
	GetProvidedRoutePeriod(ctx context.Context, tenantID, membershipID, source, month string) (ProvidedRoutePeriod, error)
}

// ProvidedRouteSourceInput is what a registration or an edit may set. The external id and its
// version are not here: the CP generates them (D1b), and only RotateProvidedRouteExternalID
// changes them.
type ProvidedRouteSourceInput struct {
	ID        string // empty registers a new source
	Source    string // providedroutes.SourceTenant or SourceDeployment, fixed at registration
	TenantID  string // the owning tenant for a tenant source, empty for a deployment source
	Name      string
	RoleARN   string
	Regions   []string
	AllowList []string
}

// ProvidedRouteSource is one stored registration.
type ProvidedRouteSource struct {
	ID                string
	Source            string
	TenantID          string
	Name              string
	RoleARN           string
	ExternalID        string
	ExternalIDVersion int
	Regions           []string
	Fingerprint       string
	AllowList         []string
	PolicyVersion     int
	Active            bool
	CreatedAt         string
	UpdatedAt         string
}

// Payer is the part of the registration the fingerprint covers.
func (s ProvidedRouteSource) Payer() providedroutes.Payer {
	return providedroutes.Payer{RoleARN: s.RoleARN, ExternalIDVersion: s.ExternalIDVersion, Regions: s.Regions}
}

// Binding is what a session's selection stores for this source (decision 1).
func (s ProvidedRouteSource) Binding() string { return providedroutes.Bind(s.ID, s.Fingerprint) }

const providedRouteSourceCols = `id, source, tenant_id, name, role_arn, external_id, external_id_version, regions,
	fingerprint, allow_list, policy_version, active, created_at, updated_at`

func scanProvidedRouteSource(sc interface{ Scan(...any) error }) (ProvidedRouteSource, error) {
	var s ProvidedRouteSource
	var regions, allow string
	var active int
	if err := sc.Scan(&s.ID, &s.Source, &s.TenantID, &s.Name, &s.RoleARN, &s.ExternalID, &s.ExternalIDVersion, &regions,
		&s.Fingerprint, &allow, &s.PolicyVersion, &active, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return s, err
	}
	s.Active = active == 1
	if err := json.Unmarshal([]byte(regions), &s.Regions); err != nil {
		return s, fmt.Errorf("provided route source %s: regions: %w", s.ID, err)
	}
	if err := json.Unmarshal([]byte(allow), &s.AllowList); err != nil {
		return s, fmt.Errorf("provided route source %s: allow list: %w", s.ID, err)
	}
	return s, nil
}

// newExternalID mints a tenant connection's external id. It is not a secret (AWS third-party
// access): it stops another tenant registering this role, which the activation probes check.
func newExternalID() string { return "af-" + randHex(16) }

// PutProvidedRouteSource registers a source (empty ID) or edits one. The store derives what must
// not be the caller's to say: the fingerprint, from the payer; the allow-list's policy version,
// bumped only when the set of models changes; and activation, which is cleared whenever the
// fingerprint changes, so a re-pointed role is inactive until its probes pass again. A tenant
// source gets a CP-generated external id at registration.
func (s *SQL) PutProvidedRouteSource(ctx context.Context, in ProvidedRouteSourceInput) (ProvidedRouteSource, error) {
	in.RoleARN = strings.TrimSpace(in.RoleARN)
	in.TenantID = strings.TrimSpace(in.TenantID)
	in.Regions = providedroutes.NormalizeRegions(in.Regions)
	in.AllowList = providedroutes.NormalizeAllowList(in.AllowList)
	if (in.Source == providedroutes.SourceTenant) != (in.TenantID != "") {
		return ProvidedRouteSource{}, fmt.Errorf("a tenant source needs its tenant, and a deployment source has none")
	}
	now := NowTS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProvidedRouteSource{}, err
	}
	defer tx.Rollback()
	var out ProvidedRouteSource
	if in.ID == "" {
		out = ProvidedRouteSource{ID: NewID(), Source: in.Source, TenantID: in.TenantID, PolicyVersion: 1, CreatedAt: now}
		if in.Source == providedroutes.SourceTenant {
			out.ExternalID, out.ExternalIDVersion = newExternalID(), 1
		}
	} else {
		lock := ""
		if s.dialect == "postgres" {
			lock = " FOR UPDATE"
		}
		out, err = scanProvidedRouteSource(tx.QueryRowContext(ctx,
			`SELECT `+providedRouteSourceCols+` FROM provided_route_source WHERE id=?`+lock, in.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ProvidedRouteSource{}, ErrProvidedRouteSourceNotFound
		}
		if err != nil {
			return ProvidedRouteSource{}, err
		}
		if out.Source != in.Source || out.TenantID != in.TenantID {
			return ProvidedRouteSource{}, fmt.Errorf("a source's kind and tenant are fixed at registration")
		}
		if !slices.Equal(out.AllowList, in.AllowList) {
			out.PolicyVersion++
		}
	}
	out.Name, out.RoleARN, out.Regions, out.AllowList, out.UpdatedAt = strings.TrimSpace(in.Name), in.RoleARN, in.Regions, in.AllowList, now
	if err := providedroutes.Validate(out.Source, out.Payer(), out.AllowList); err != nil {
		return ProvidedRouteSource{}, err
	}
	if fp := providedroutes.Fingerprint(out.Payer()); fp != out.Fingerprint {
		out.Fingerprint, out.Active = fp, false
	}
	if in.ID == "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO provided_route_source(`+providedRouteSourceCols+`)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			out.ID, out.Source, out.TenantID, out.Name, out.RoleARN, out.ExternalID, out.ExternalIDVersion, jsonList(out.Regions),
			out.Fingerprint, jsonList(out.AllowList), out.PolicyVersion, out.CreatedAt, out.UpdatedAt)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE provided_route_source SET name=?, role_arn=?, regions=?, fingerprint=?, allow_list=?,
			policy_version=?, active=?, updated_at=? WHERE id=?`,
			out.Name, out.RoleARN, jsonList(out.Regions), out.Fingerprint, jsonList(out.AllowList), out.PolicyVersion,
			boolInt(out.Active), out.UpdatedAt, out.ID)
	}
	if err != nil {
		return ProvidedRouteSource{}, err
	}
	return out, tx.Commit()
}

func (s *SQL) GetProvidedRouteSource(ctx context.Context, id string) (ProvidedRouteSource, error) {
	out, err := scanProvidedRouteSource(s.db.QueryRowContext(ctx,
		`SELECT `+providedRouteSourceCols+` FROM provided_route_source WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrProvidedRouteSourceNotFound
	}
	return out, err
}

func (s *SQL) ListProvidedRouteSources(ctx context.Context, tenantID string) ([]ProvidedRouteSource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+providedRouteSourceCols+` FROM provided_route_source WHERE tenant_id=? ORDER BY created_at, id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvidedRouteSource
	for rows.Next() {
		src, err := scanProvidedRouteSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// DeleteProvidedRouteSource removes a registration. Its spend and reservations stay: they are
// money that already moved, keyed by the source kind and the binding, not by this row.
func (s *SQL) DeleteProvidedRouteSource(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM provided_route_source WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProvidedRouteSourceNotFound
	}
	return nil
}

// RotateProvidedRouteExternalID issues a tenant source a new external id, bumps its version (and
// so the fingerprint) and leaves it inactive until the probes pass against the role's updated
// trust policy (D1b).
func (s *SQL) RotateProvidedRouteExternalID(ctx context.Context, id string) (ProvidedRouteSource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProvidedRouteSource{}, err
	}
	defer tx.Rollback()
	lock := ""
	if s.dialect == "postgres" {
		lock = " FOR UPDATE"
	}
	out, err := scanProvidedRouteSource(tx.QueryRowContext(ctx,
		`SELECT `+providedRouteSourceCols+` FROM provided_route_source WHERE id=?`+lock, id))
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrProvidedRouteSourceNotFound
	}
	if err != nil {
		return out, err
	}
	if out.Source != providedroutes.SourceTenant {
		return ProvidedRouteSource{}, fmt.Errorf("only a tenant source has an external id")
	}
	out.ExternalID, out.ExternalIDVersion, out.Active, out.UpdatedAt = newExternalID(), out.ExternalIDVersion+1, false, NowTS()
	out.Fingerprint = providedroutes.Fingerprint(out.Payer())
	if _, err := tx.ExecContext(ctx, `UPDATE provided_route_source SET external_id=?, external_id_version=?, fingerprint=?,
		active=0, updated_at=? WHERE id=?`, out.ExternalID, out.ExternalIDVersion, out.Fingerprint, out.UpdatedAt, id); err != nil {
		return ProvidedRouteSource{}, err
	}
	return out, tx.Commit()
}

// SetProvidedRouteSourceActive records the outcome of the activation probes. It is conditional
// on the fingerprint the probes ran against, so a result about an old payer never activates a
// new one.
func (s *SQL) SetProvidedRouteSourceActive(ctx context.Context, id, fingerprint string, active bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE provided_route_source SET active=?, updated_at=? WHERE id=? AND fingerprint=?`,
		boolInt(active), NowTS(), id, fingerprint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.GetProvidedRouteSource(ctx, id); err != nil {
		return err
	}
	return ErrProvidedRouteFingerprint
}

// ProvidedRouteGrant is one tenant's settings for one source (D3). The zero value, which is also
// what a tenant nobody configured reads, grants nothing: each grant is a new capability, so an
// upgrade enables nothing.
type ProvidedRouteGrant struct {
	TenantID string
	Source   string
	// Granted is the super_admin's grant: use of the deployment source, or for the tenant source
	// "may register a tenant connection".
	Granted bool
	// CeilingMicro is the tenant's monthly ceiling, nil = none. Required while a deployment
	// source is granted (the super_admin's), optional for a tenant source (the tenant_admin's).
	CeilingMicro *int64
	// MembersOnly and MemberDefaultMicro are the tenant_admin's: only granted members may use the
	// source, and the budget of a member with no override (zero until set, open question 2).
	MembersOnly        bool
	MemberDefaultMicro int64
}

// ProvidedRouteMember is one member's grant and budget override for one source.
type ProvidedRouteMember struct {
	MembershipID string
	Source       string
	Granted      bool
	BudgetMicro  *int64 // nil = the tenant default
}

// MemberBudgetMicro is the member's effective monthly budget.
func (g ProvidedRouteGrant) MemberBudgetMicro(m ProvidedRouteMember) int64 {
	if m.BudgetMicro != nil {
		return *m.BudgetMicro
	}
	return g.MemberDefaultMicro
}

// MayUse is D3's "no budget, no use": the super_admin's grant is on, the source is this one's kind
// and active, the member is granted where the tenant restricts the source, and both the member's
// budget and any tenant ceiling are above zero. The binding check (the session's fingerprint
// against the source's) is the caller's, against the selection it holds.
func (g ProvidedRouteGrant) MayUse(m ProvidedRouteMember, src ProvidedRouteSource) bool {
	return g.Granted && src.Active && src.Source == g.Source && (src.TenantID == "" || src.TenantID == g.TenantID) &&
		(!g.MembersOnly || m.Granted) && g.MemberBudgetMicro(m) > 0 && (g.CeilingMicro == nil || *g.CeilingMicro > 0)
}

func (s *SQL) GetProvidedRouteGrant(ctx context.Context, tenantID, source string) (ProvidedRouteGrant, error) {
	g := ProvidedRouteGrant{TenantID: tenantID, Source: source}
	var granted, membersOnly int
	var ceiling sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT granted, ceiling_micro, members_only, member_default_micro
		FROM provided_route_grant WHERE tenant_id=? AND source=?`, tenantID, source).
		Scan(&granted, &ceiling, &membersOnly, &g.MemberDefaultMicro)
	if errors.Is(err, sql.ErrNoRows) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	g.Granted, g.MembersOnly = granted == 1, membersOnly == 1
	if ceiling.Valid {
		g.CeilingMicro = &ceiling.Int64
	}
	return g, nil
}

func nullMicro(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// SetProvidedRouteGrant sets the grant and the tenant's ceiling. A granted deployment source with
// no ceiling is refused here, at save time (D3).
func (s *SQL) SetProvidedRouteGrant(ctx context.Context, tenantID, source string, granted bool, ceilingMicro *int64) error {
	if !providedroutes.ValidSource(source) {
		return fmt.Errorf("unknown provided source %q", source)
	}
	if ceilingMicro != nil && *ceilingMicro < 0 {
		return fmt.Errorf("a ceiling cannot be negative")
	}
	if granted && source == providedroutes.SourceDeployment && ceilingMicro == nil {
		return fmt.Errorf("granting the deployment source needs a monthly ceiling for the tenant")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO provided_route_grant(tenant_id, source, granted, ceiling_micro, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, source) DO UPDATE SET granted=excluded.granted, ceiling_micro=excluded.ceiling_micro,
		updated_at=excluded.updated_at`, tenantID, source, boolInt(granted), nullMicro(ceilingMicro), NowTS())
	return err
}

func (s *SQL) SetProvidedRouteMemberPolicy(ctx context.Context, tenantID, source string, membersOnly bool, defaultMicro int64) error {
	if !providedroutes.ValidSource(source) {
		return fmt.Errorf("unknown provided source %q", source)
	}
	if defaultMicro < 0 {
		return fmt.Errorf("a budget cannot be negative")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO provided_route_grant(tenant_id, source, members_only, member_default_micro, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, source) DO UPDATE SET members_only=excluded.members_only,
		member_default_micro=excluded.member_default_micro, updated_at=excluded.updated_at`,
		tenantID, source, boolInt(membersOnly), defaultMicro, NowTS())
	return err
}

func (s *SQL) GetProvidedRouteMember(ctx context.Context, membershipID, source string) (ProvidedRouteMember, error) {
	m := ProvidedRouteMember{MembershipID: membershipID, Source: source}
	var granted int
	var budget sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT granted, budget_micro FROM provided_route_member WHERE membership_id=? AND source=?`,
		membershipID, source).Scan(&granted, &budget)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	m.Granted = granted == 1
	if budget.Valid {
		m.BudgetMicro = &budget.Int64
	}
	return m, nil
}

// SetProvidedRouteMember sets one member's grant and override. The caller has checked that the
// membership belongs to tenantID.
func (s *SQL) SetProvidedRouteMember(ctx context.Context, tenantID, membershipID, source string, granted bool, budgetMicro *int64) error {
	if !providedroutes.ValidSource(source) {
		return fmt.Errorf("unknown provided source %q", source)
	}
	if budgetMicro != nil && *budgetMicro < 0 {
		return fmt.Errorf("a budget cannot be negative")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO provided_route_member(membership_id, tenant_id, source, granted, budget_micro, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(membership_id, source) DO UPDATE SET granted=excluded.granted, budget_micro=excluded.budget_micro,
		updated_at=excluded.updated_at`, membershipID, tenantID, source, boolInt(granted), nullMicro(budgetMicro), NowTS())
	return err
}

// ProvidedRouteCall is one call's reservation row (D4).
type ProvidedRouteCall struct {
	CallID       string
	MembershipID string
	TenantID     string
	Source       string
	Binding      string // "<source id>#<fingerprint>"
	Model        string
	API          string
	// Month is the UTC admission month, derived from AdmittedAt by the store and frozen.
	Month         string
	PriceVersion  string
	ReservedMicro int64
	FinalMicro    *int64 // nil while in flight
	State         string
	AdmittedAt    string // RFC 3339, UTC; the store fills now when empty
	SettledAt     string
	CPInstance    string
}

// ProvidedRouteLimits are the budgets an admission is checked against, resolved by the caller.
type ProvidedRouteLimits struct {
	MemberBudgetMicro  int64
	TenantCeilingMicro *int64 // nil = no tenant ceiling (a tenant source without one)
	// MaxInFlight caps the member's calls in flight on this source; 0 = no cap. The deployment's
	// value is open question 3, set from Q1's measurements.
	MaxInFlight int
}

// Why an admission was refused.
const (
	ProvidedRouteRefusedMemberBudget  = "member_budget"
	ProvidedRouteRefusedTenantCeiling = "tenant_ceiling"
	ProvidedRouteRefusedInFlight      = "in_flight"
)

// ProvidedRouteAdmission is an admission's answer. Refused names the level that ran out.
type ProvidedRouteAdmission struct {
	Admitted bool
	Refused  string
	// Replayed: this call id was already admitted with the same contents (a retry after a lost
	// response). Nothing more was reserved. The caller must not count it as a second call: if
	// the row is already settled, the call has run.
	Replayed bool
}

// sameAdmission reports whether a stored call is the admission c asks for, so a retry of it is
// answered as admitted instead of being refused against its own reservation. The admission time
// is not compared: a retry may leave it to the store.
func sameAdmission(stored, c ProvidedRouteCall) bool {
	return stored.MembershipID == c.MembershipID && stored.TenantID == c.TenantID && stored.Source == c.Source &&
		stored.Binding == c.Binding && stored.Model == c.Model && stored.API == c.API &&
		stored.PriceVersion == c.PriceVersion && stored.ReservedMicro == c.ReservedMicro
}

// replayedAdmission answers a call id that is already stored: admitted when it is the same
// admission, ErrProvidedRouteCallExists when it is not.
func (s *SQL) replayedAdmission(ctx context.Context, c ProvidedRouteCall) (ProvidedRouteAdmission, error) {
	stored, err := s.GetProvidedRouteCall(ctx, c.CallID)
	if err != nil {
		return ProvidedRouteAdmission{}, err
	}
	if !sameAdmission(stored, c) {
		return ProvidedRouteAdmission{}, ErrProvidedRouteCallExists
	}
	return ProvidedRouteAdmission{Admitted: true, Replayed: true}, nil
}

// ProvidedRoutePeriod is one level's month.
type ProvidedRoutePeriod struct {
	SpentMicro    int64
	ReservedMicro int64
}

func ensureProvidedRoutePeriod(ctx context.Context, tx *sqlTx, tenantID, membershipID, source, month, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO provided_route_period(tenant_id, membership_id, source, month, updated_at)
		VALUES(?, ?, ?, ?, ?) ON CONFLICT(tenant_id, membership_id, source, month) DO NOTHING`,
		tenantID, membershipID, source, month, now)
	return err
}

// AdmitProvidedRouteCall is D4's full-fit admission in one transaction: the member's and the
// tenant's months each take the reservation only while spent + reserved + this reservation stays
// within their limit, and the call's row is written in_flight. The condition is in the UPDATE
// itself, so two admissions that read the same sums (two requests, or two CPs in a rollout)
// cannot both pass: Postgres re-checks the WHERE on the row it waited for, and SQLite has one
// writer. Nothing may be sent upstream before this returns Admitted.
//
// The member's month row is locked first, then the tenant's, the order settlement uses too. The
// in-flight count is read under the member row's lock, so two admissions of one member in one
// month see each other. Two admitted in the same instant on either side of a month boundary lock
// different rows and can each pass a cap of one, a window of one call.
func (s *SQL) AdmitProvidedRouteCall(ctx context.Context, c ProvidedRouteCall, lim ProvidedRouteLimits) (ProvidedRouteAdmission, error) {
	if c.CallID == "" || c.MembershipID == "" || c.TenantID == "" || c.Model == "" || c.API == "" || c.PriceVersion == "" || c.CPInstance == "" {
		return ProvidedRouteAdmission{}, fmt.Errorf("a provided route call needs its id, member, tenant, model, API, price version and CP instance")
	}
	if !providedroutes.ValidSource(c.Source) {
		return ProvidedRouteAdmission{}, fmt.Errorf("unknown provided source %q", c.Source)
	}
	if _, _, ok := providedroutes.ParseBinding(c.Binding); !ok {
		return ProvidedRouteAdmission{}, fmt.Errorf("%q is not a bound source", c.Binding)
	}
	if c.ReservedMicro <= 0 {
		return ProvidedRouteAdmission{}, fmt.Errorf("a reservation must be above zero, never released to nothing")
	}
	at := time.Now().UTC()
	if c.AdmittedAt != "" {
		t, err := time.Parse(time.RFC3339Nano, c.AdmittedAt)
		if err != nil {
			return ProvidedRouteAdmission{}, fmt.Errorf("admission time: %w", err)
		}
		at = t.UTC()
	}
	c.AdmittedAt, c.Month, c.State = at.Format(time.RFC3339Nano), providedroutes.AdmissionMonth(at), providedroutes.StateInFlight
	now := NowTS()
	// A retry of an admission whose answer was lost must not be checked against its own
	// reservation (it would read as refused, and the caller would never settle it). The INSERT
	// below catches a retry racing the original.
	if _, err := s.GetProvidedRouteCall(ctx, c.CallID); err == nil {
		return s.replayedAdmission(ctx, c)
	} else if !errors.Is(err, ErrProvidedRouteCallNotFound) {
		return ProvidedRouteAdmission{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProvidedRouteAdmission{}, err
	}
	defer tx.Rollback()
	for _, mid := range []string{c.MembershipID, ""} {
		if err := ensureProvidedRoutePeriod(ctx, tx, c.TenantID, mid, c.Source, c.Month, now); err != nil {
			return ProvidedRouteAdmission{}, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE provided_route_period SET reserved_micro=reserved_micro+?, updated_at=?
		WHERE tenant_id=? AND membership_id=? AND source=? AND month=? AND spent_micro+reserved_micro+? <= ?`,
		c.ReservedMicro, now, c.TenantID, c.MembershipID, c.Source, c.Month, c.ReservedMicro, lim.MemberBudgetMicro)
	if err != nil {
		return ProvidedRouteAdmission{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ProvidedRouteAdmission{Refused: ProvidedRouteRefusedMemberBudget}, nil
	}
	if lim.MaxInFlight > 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provided_route_call WHERE membership_id=? AND source=? AND state=?`,
			c.MembershipID, c.Source, providedroutes.StateInFlight).Scan(&n); err != nil {
			return ProvidedRouteAdmission{}, err
		}
		if n >= lim.MaxInFlight {
			return ProvidedRouteAdmission{Refused: ProvidedRouteRefusedInFlight}, nil
		}
	}
	q := `UPDATE provided_route_period SET reserved_micro=reserved_micro+?, updated_at=?
		WHERE tenant_id=? AND membership_id='' AND source=? AND month=?`
	args := []any{c.ReservedMicro, now, c.TenantID, c.Source, c.Month}
	if lim.TenantCeilingMicro != nil {
		q += ` AND spent_micro+reserved_micro+? <= ?`
		args = append(args, c.ReservedMicro, *lim.TenantCeilingMicro)
	}
	if res, err = tx.ExecContext(ctx, q, args...); err != nil {
		return ProvidedRouteAdmission{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ProvidedRouteAdmission{Refused: ProvidedRouteRefusedTenantCeiling}, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO provided_route_call(call_id, membership_id, tenant_id, source, binding, model, api,
		month, price_version, reserved_micro, state, admitted_at, cp_instance) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CallID, c.MembershipID, c.TenantID, c.Source, c.Binding, c.Model, c.API, c.Month, c.PriceVersion, c.ReservedMicro,
		c.State, c.AdmittedAt, c.CPInstance); err != nil {
		if isUniqueViolation(err) {
			// The original committed between the check above and this INSERT. This transaction is
			// rolled back, so nothing is reserved twice.
			tx.Rollback()
			return s.replayedAdmission(ctx, c)
		}
		return ProvidedRouteAdmission{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProvidedRouteAdmission{}, err
	}
	return ProvidedRouteAdmission{Admitted: true}, nil
}

// SettleProvidedRouteCall replaces a call's reservation with its final amount in the month it was
// admitted in, exactly once: the row moves from in_flight to settled only if it is still
// in_flight, so a duplicate (a retry, a replay, a second CP) changes nothing and reports false.
// A final amount above the reservation is a defect of the price table, logged by the caller, and
// still settled as reported. An unknown call id reads like a duplicate, (false, nil): either way
// nothing is left to settle, and GetProvidedRouteCall tells the two apart.
func (s *SQL) SettleProvidedRouteCall(ctx context.Context, callID string, finalMicro int64) (bool, error) {
	if finalMicro < 0 {
		return false, fmt.Errorf("a final amount cannot be negative")
	}
	now := NowTS()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE provided_route_call SET state=?, final_micro=?, settled_at=?
		WHERE call_id=? AND state=?`, providedroutes.StateSettled, finalMicro, now, callID, providedroutes.StateInFlight)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	var tenantID, membershipID, source, month string
	var reserved int64
	if err := tx.QueryRowContext(ctx, `SELECT tenant_id, membership_id, source, month, reserved_micro FROM provided_route_call WHERE call_id=?`,
		callID).Scan(&tenantID, &membershipID, &source, &month, &reserved); err != nil {
		return false, err
	}
	for _, mid := range []string{membershipID, ""} {
		res, err := tx.ExecContext(ctx, `UPDATE provided_route_period SET reserved_micro=reserved_micro-?, spent_micro=spent_micro+?,
			updated_at=? WHERE tenant_id=? AND membership_id=? AND source=? AND month=?`,
			reserved, finalMicro, now, tenantID, mid, source, month)
		if err != nil {
			return false, err
		}
		// Admission wrote both rows before the call row, so a missing one is corruption. Settling
		// the call without it would make the amount vanish from the budget, so nothing settles.
		if n, _ := res.RowsAffected(); n != 1 {
			return false, fmt.Errorf("provided route call %s: no %s period row for %s", callID, source, month)
		}
	}
	return true, tx.Commit()
}

const providedRouteCallCols = `call_id, membership_id, tenant_id, source, binding, model, api, month, price_version,
	reserved_micro, final_micro, state, admitted_at, settled_at, cp_instance`

func scanProvidedRouteCall(sc interface{ Scan(...any) error }) (ProvidedRouteCall, error) {
	var c ProvidedRouteCall
	var final sql.NullInt64
	err := sc.Scan(&c.CallID, &c.MembershipID, &c.TenantID, &c.Source, &c.Binding, &c.Model, &c.API, &c.Month, &c.PriceVersion,
		&c.ReservedMicro, &final, &c.State, &c.AdmittedAt, &c.SettledAt, &c.CPInstance)
	if final.Valid {
		c.FinalMicro = &final.Int64
	}
	return c, err
}

func (s *SQL) GetProvidedRouteCall(ctx context.Context, callID string) (ProvidedRouteCall, error) {
	c, err := scanProvidedRouteCall(s.db.QueryRowContext(ctx, `SELECT `+providedRouteCallCols+` FROM provided_route_call WHERE call_id=?`, callID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrProvidedRouteCallNotFound
	}
	return c, err
}

// ListInFlightProvidedRouteCalls reads in-flight rows through the state index. The caller judges
// each row's age from AdmittedAt parsed as a time: RFC 3339 with fractional seconds does not sort
// as text.
func (s *SQL) ListInFlightProvidedRouteCalls(ctx context.Context, limit int) ([]ProvidedRouteCall, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+providedRouteCallCols+` FROM provided_route_call WHERE state=?
		ORDER BY admitted_at, call_id LIMIT ?`, providedroutes.StateInFlight, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvidedRouteCall
	for rows.Next() {
		c, err := scanProvidedRouteCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQL) GetProvidedRoutePeriod(ctx context.Context, tenantID, membershipID, source, month string) (ProvidedRoutePeriod, error) {
	var p ProvidedRoutePeriod
	err := s.db.QueryRowContext(ctx, `SELECT spent_micro, reserved_micro FROM provided_route_period
		WHERE tenant_id=? AND membership_id=? AND source=? AND month=?`, tenantID, membershipID, source, month).
		Scan(&p.SpentMicro, &p.ReservedMicro)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	return p, err
}
