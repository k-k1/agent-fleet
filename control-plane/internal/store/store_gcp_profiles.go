package store

import (
	"context"
	"database/sql"
)

// GCPProfile is one Google Cloud profile (ADR 0107 decision 1), personal scope like
// SSMProfile. NON-SECRET: the CP never holds a Google credential — the Agent's own gcloud
// store does — and no field is meant to carry a service-account key.
type GCPProfile struct {
	ID, MembershipID, Label   string
	LoginMethod               string // "google" only (Workforce Identity Federation is #1487)
	Project, QuotaProject     string // QuotaProject "" = the project
	Account                   string // "" = whichever account the login selects
	Region, Zone              string
	ImpersonateServiceAccount string
	CreatedAt, UpdatedAt      string
}

// GCPProfileStore holds the Google Cloud profiles. Every write carries membership_id, so a
// member can only change their own rows.
type GCPProfileStore interface {
	ListGCPProfiles(ctx context.Context, membershipID string) ([]GCPProfile, error)
	GetGCPProfile(ctx context.Context, id string) (GCPProfile, bool, error)
	CreateGCPProfile(ctx context.Context, p GCPProfile) error
	UpdateGCPProfile(ctx context.Context, p GCPProfile) error
	DeleteGCPProfile(ctx context.Context, id, membershipID string) error
}

const gcpProfileCols = `SELECT id, membership_id, label, login_method, project, quota_project, account,
	region, zone, impersonate_service_account, created_at, updated_at FROM gcp_profiles`

func scanGCPProfile(row scanner) (GCPProfile, error) {
	var p GCPProfile
	err := row.Scan(&p.ID, &p.MembershipID, &p.Label, &p.LoginMethod, &p.Project, &p.QuotaProject,
		&p.Account, &p.Region, &p.Zone, &p.ImpersonateServiceAccount, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ListGCPProfiles orders by label, then id, so the collision report lists the same label
// first on every pull.
func (s *SQL) ListGCPProfiles(ctx context.Context, membershipID string) ([]GCPProfile, error) {
	rows, err := s.db.QueryContext(ctx, gcpProfileCols+` WHERE membership_id=? ORDER BY label, id`, membershipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GCPProfile
	for rows.Next() {
		p, err := scanGCPProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQL) GetGCPProfile(ctx context.Context, id string) (GCPProfile, bool, error) {
	p, err := scanGCPProfile(s.db.QueryRowContext(ctx, gcpProfileCols+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return GCPProfile{}, false, nil
	}
	return p, err == nil, err
}

func (s *SQL) CreateGCPProfile(ctx context.Context, p GCPProfile) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO gcp_profiles(id, membership_id, label, login_method, project, quota_project, account,
		   region, zone, impersonate_service_account, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.MembershipID, p.Label, p.LoginMethod, p.Project, p.QuotaProject, p.Account,
		p.Region, p.Zone, p.ImpersonateServiceAccount, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *SQL) UpdateGCPProfile(ctx context.Context, p GCPProfile) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gcp_profiles SET label=?, login_method=?, project=?, quota_project=?, account=?,
		   region=?, zone=?, impersonate_service_account=?, updated_at=?
		 WHERE id=? AND membership_id=?`,
		p.Label, p.LoginMethod, p.Project, p.QuotaProject, p.Account,
		p.Region, p.Zone, p.ImpersonateServiceAccount, p.UpdatedAt, p.ID, p.MembershipID)
	return err
}

func (s *SQL) DeleteGCPProfile(ctx context.Context, id, membershipID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM gcp_profiles WHERE id=? AND membership_id=?`, id, membershipID)
	return err
}
