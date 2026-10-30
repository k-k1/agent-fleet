package store

import (
	"context"
	"database/sql"
	"errors"
)

// TenantIdP is one tenant-defined login provider (docs/log/61 §61.11, migration 0040).
// SecretEnc is opaque ciphertext here exactly like MCPServerRow.HeadersEnc: the
// SQL layer stores and returns it, and tenant_idp.go owns the sealing.
//
// ★ Status governs everything. Only "active" rows are turned into a provider, and
// a row is born "pending" until a super_admin approves it (decision 30). ApprovedBy /
// ApprovedAt are the copy of that approval kept next to the row — the audit ledger
// is the record.
type TenantIdP struct {
	ID, TenantID, Name string
	LabelJA, LabelEN   string
	// Kind selects the adapter the row is built into and therefore which of the
	// fields below mean anything: "oidc" (the P4 default) uses Issuer/Trust/
	// AllowedTIDs, "github" uses AllowedOrgs and pins Issuer to https://github.com
	// (docs/log/61 §61.15 + decision 35).
	Kind                        string
	Issuer, ClientID            string
	SecretEnc, KeyRef           string
	Trust                       string
	AllowedTIDs, AllowedDomains string // CSV
	AllowedOrgs                 string // CSV, kind=github only
	// LinkClaim names the stable claim rule 1.5 should match on, for an issuer whose
	// `sub` is pairwise (docs/log/61 §61.15.10). ★ A tenant may only name a claim from a
	// closed list (tenantLinkClaims): naming `email` or `upn` would build an
	// email join inside a shared realm, which is precisely what decision 32 refuses. The
	// VALUE is never taken from this row — only the name is configuration.
	LinkClaim                       string
	Status                          string // pending | active | suspended
	ApprovedBy, ApprovedAt          string
	CreatedBy, CreatedAt, UpdatedAt string
}

// TenantIdPStore is the tenant-defined login provider registry (docs/log/61 §61.11).
// Every mutation carries tenant_id in the WHERE, the way MCPServerStore does, so a
// tenant_admin of one tenant can never reach another's row even if an id leaks.
// ListActiveTenantIdPs is the exception by design: it is the deployment-wide read
// the login layer needs to assemble the provider set, and it is never reached from
// a tenant-scoped handler.
// TenantRef is the owning tenant of a tenant_idp row, as the deployment-wide reads
// carry it: the slug because the provider id the rest of CP sees is built from it
// (t:<slug>:<name>), and the DISPLAY NAME because the default button label has to
// say WHICH company's method it is — otherwise a tenant's GitHub row and the
// deployment's GitHub button render the same text on one page (docs/log/61 §61.15.10).
type TenantRef struct{ Slug, Name string }

type TenantIdPStore interface {
	ListTenantIdPs(ctx context.Context, tenantID string) ([]TenantIdP, error)
	// ListAllTenantIdPs returns every row with its tenant, for the super_admin
	// approval queue. The tenants are returned separately (keyed by tenant id) so
	// the store stays free of view structs; the handler joins them.
	ListAllTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error)
	// ListActiveTenantIdPs returns the approved rows only — the login layer's bulk
	// read, behind the same short TTL cache the tenant rules use.
	ListActiveTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error)
	GetTenantIdP(ctx context.Context, tenantID, id string) (TenantIdP, bool, error)
	CreateTenantIdP(ctx context.Context, row TenantIdP) error
	UpdateTenantIdP(ctx context.Context, row TenantIdP) error
	// SetTenantIdPStatus is the approval / suspension path, kept apart from the
	// content update so approving cannot accidentally rewrite the issuer that was
	// being approved.
	SetTenantIdPStatus(ctx context.Context, tenantID, id, status, approvedBy, approvedAt, updatedAt string) error
	DeleteTenantIdP(ctx context.Context, tenantID, id string) error
	// TenantIdPIssuerInUse reports whether some OTHER row already names this issuer
	// (any tenant). It is the "second app registration of the same directory" signal
	// (docs/log/61 §61.17.4 (b)): the same person then arrives with a DIFFERENT subject
	// if the issuer is pairwise, which rule 2' refuses as email_taken. Deployment-wide
	// on purpose — identities are deployment-wide, so a row in another tenant splits
	// the same person just as effectively.
	TenantIdPIssuerInUse(ctx context.Context, issuer, excludeID string) (bool, error)
	// CountMembersOnlyOnProvider counts a tenant's ACTIVE members whose only proven
	// sign-in is this provider — the people who would have no way in if it stopped
	// (docs/log/61 §61.17.4, the ordering). Used to warn before suspending, never to refuse:
	// stopping a compromised IdP must stay faster than starting one.
	CountMembersOnlyOnProvider(ctx context.Context, tenantID, providerID string) (int, error)
}

// --- tenant-defined login providers (docs/log/61 §61.11 + ADR0043 decisions 29-33) ---

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

func (s *SQL) ListTenantIdPs(ctx context.Context, tenantID string) ([]TenantIdP, error) {
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

func (s *SQL) ListAllTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error) {
	return s.listTenantIdPs(ctx, "")
}

func (s *SQL) ListActiveTenantIdPs(ctx context.Context) ([]TenantIdP, map[string]TenantRef, error) {
	return s.listTenantIdPs(ctx, "active")
}

// listTenantIdPs is the deployment-wide read. The tenant slug travels with each row
// because the provider id the rest of CP sees is built from it (t:<slug>:<name>),
// and joining here saves the caller a lookup per row. The display name comes along
// for the default button label (docs/log/61 §61.15.10).
//
// ★ Rows of a non-active tenant are left out: a suspended tenant's IdP must not keep
// minting sessions, the same way ListTenantLoginRules only loads active tenants.
func (s *SQL) listTenantIdPs(ctx context.Context, status string) ([]TenantIdP, map[string]TenantRef, error) {
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

func (s *SQL) GetTenantIdP(ctx context.Context, tenantID, id string) (TenantIdP, bool, error) {
	t, err := scanTenantIdP(s.db.QueryRowContext(ctx, tenantIdPCols+` WHERE tenant_id=? AND id=?`, tenantID, id))
	if err == sql.ErrNoRows {
		return TenantIdP{}, false, nil
	}
	return t, err == nil, err
}

func (s *SQL) CreateTenantIdP(ctx context.Context, t TenantIdP) error {
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
// trust rule sends the row back to pending (decision 30) — the caller computes that and
// this is where it lands.
func (s *SQL) UpdateTenantIdP(ctx context.Context, t TenantIdP) error {
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

func (s *SQL) SetTenantIdPStatus(ctx context.Context, tenantID, id, status, approvedBy, approvedAt, updatedAt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tenant_idp SET status=?, approved_by=?, approved_at=?, updated_at=? WHERE tenant_id=? AND id=?`,
		status, approvedBy, approvedAt, updatedAt, tenantID, id)
	return err
}

func (s *SQL) DeleteTenantIdP(ctx context.Context, tenantID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tenant_idp WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}

// TenantIdPIssuerInUse — see the interface. Rows of every tenant, and every status:
// a PENDING second registration is exactly the one worth catching, since the whole
// point is to say something before it is approved and people start signing in.
func (s *SQL) TenantIdPIssuerInUse(ctx context.Context, issuer, excludeID string) (bool, error) {
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
func (s *SQL) CountMembersOnlyOnProvider(ctx context.Context, tenantID, providerID string) (int, error) {
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
