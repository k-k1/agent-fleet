package routegateway

// The deployment source's grants and budgets (ADR 0129 D3, D4): a super_admin grants the source
// to a tenant with a monthly ceiling; under it the tenant_admin chooses "every member" or
// "granted members only", sets the members' default monthly budget and per-member overrides.
// Each grant is a new capability and starts off: no row is no grant and a zero budget.

import (
	"context"
	"fmt"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// GrantInput is the super_admin's grant to one tenant.
type GrantInput struct {
	Granted bool `json:"granted"`
	// CeilingMicro is the tenant's monthly ceiling in micro-USD; required while granted (D3:
	// an empty ceiling with the grant on is refused at save time).
	CeilingMicro *int64 `json:"ceiling_micro"`
}

// SetGrant saves in for tenantID.
func (g *Gateway) SetGrant(ctx context.Context, tenantID string, in GrantInput) error {
	if in.CeilingMicro != nil && *in.CeilingMicro < 0 {
		return &InvalidInputError{fmt.Errorf("a ceiling cannot be negative")}
	}
	if in.Granted && (in.CeilingMicro == nil || *in.CeilingMicro == 0) {
		return &InvalidInputError{fmt.Errorf("granting the deployment's Bedrock route needs a monthly ceiling above zero for the tenant")}
	}
	return g.d.Store.SetProvidedRouteGrant(ctx, tenantID, providedroutes.SourceDeployment, in.Granted, in.CeilingMicro)
}

// PolicyInput is the tenant_admin's policy for every member.
type PolicyInput struct {
	MembersOnly        bool  `json:"members_only"`
	MemberDefaultMicro int64 `json:"member_default_micro"`
}

// SetPolicy saves in for tenantID.
func (g *Gateway) SetPolicy(ctx context.Context, tenantID string, in PolicyInput) error {
	if in.MemberDefaultMicro < 0 {
		return &InvalidInputError{fmt.Errorf("a budget cannot be negative")}
	}
	return g.d.Store.SetProvidedRouteMemberPolicy(ctx, tenantID, providedroutes.SourceDeployment, in.MembersOnly, in.MemberDefaultMicro)
}

// MemberInput is the tenant_admin's grant and budget override for one member.
type MemberInput struct {
	MembershipID string `json:"membership_id"`
	Granted      bool   `json:"granted"`
	// BudgetMicro nil = the tenant's default.
	BudgetMicro *int64 `json:"budget_micro"`
}

// SetMember saves in. The caller has checked that the membership is one of tenantID's.
func (g *Gateway) SetMember(ctx context.Context, tenantID string, in MemberInput) error {
	if in.BudgetMicro != nil && *in.BudgetMicro < 0 {
		return &InvalidInputError{fmt.Errorf("a budget cannot be negative")}
	}
	return g.d.Store.SetProvidedRouteMember(ctx, tenantID, in.MembershipID, providedroutes.SourceDeployment, in.Granted, in.BudgetMicro)
}

// TenantView is one tenant's grant, policy and spend this month, with each member's.
type TenantView struct {
	Granted            bool         `json:"granted"`
	CeilingMicro       *int64       `json:"ceiling_micro"`
	MembersOnly        bool         `json:"members_only"`
	MemberDefaultMicro int64        `json:"member_default_micro"`
	Month              string       `json:"month"`
	SpentMicro         int64        `json:"spent_micro"`
	ReservedMicro      int64        `json:"reserved_micro"`
	Members            []MemberView `json:"members"`
}

// MemberView is one member's grant, effective budget and spend this month.
type MemberView struct {
	MembershipID string `json:"membership_id"`
	UserKey      string `json:"user_key"`
	Email        string `json:"email"`
	Granted      bool   `json:"granted"`
	// BudgetMicro is the override, nil for the default; EffectiveMicro is what applies.
	BudgetMicro    *int64 `json:"budget_micro"`
	EffectiveMicro int64  `json:"effective_micro"`
	SpentMicro     int64  `json:"spent_micro"`
	ReservedMicro  int64  `json:"reserved_micro"`
}

// View is tenantID's TenantView over members (the tenant's active roster).
func (g *Gateway) View(ctx context.Context, tenantID string, members []store.MemberInfo) (TenantView, error) {
	st := g.d.Store
	grant, err := st.GetProvidedRouteGrant(ctx, tenantID, providedroutes.SourceDeployment)
	if err != nil {
		return TenantView{}, err
	}
	month := providedroutes.AdmissionMonth(g.now().UTC())
	tp, err := st.GetProvidedRoutePeriod(ctx, tenantID, "", providedroutes.SourceDeployment, month)
	if err != nil {
		return TenantView{}, err
	}
	v := TenantView{Granted: grant.Granted, CeilingMicro: grant.CeilingMicro, MembersOnly: grant.MembersOnly,
		MemberDefaultMicro: grant.MemberDefaultMicro, Month: month, SpentMicro: tp.SpentMicro, ReservedMicro: tp.ReservedMicro,
		Members: make([]MemberView, 0, len(members))}
	for _, m := range members {
		pm, err := st.GetProvidedRouteMember(ctx, m.MembershipID, providedroutes.SourceDeployment)
		if err != nil {
			return TenantView{}, err
		}
		mp, err := st.GetProvidedRoutePeriod(ctx, tenantID, m.MembershipID, providedroutes.SourceDeployment, month)
		if err != nil {
			return TenantView{}, err
		}
		v.Members = append(v.Members, MemberView{MembershipID: m.MembershipID, UserKey: m.UserKey, Email: m.Email,
			Granted: pm.Granted, BudgetMicro: pm.BudgetMicro, EffectiveMicro: grant.MemberBudgetMicro(pm),
			SpentMicro: mp.SpentMicro, ReservedMicro: mp.ReservedMicro})
	}
	return v, nil
}
