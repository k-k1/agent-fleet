package routegateway

// The deployment source's grants and budgets (ADR 0129 D3, D4): a super_admin grants the source
// to a tenant with a monthly ceiling; under it the tenant_admin chooses "every member" or
// "granted members only", sets the members' default monthly budget and per-member overrides.
// Each grant is a new capability and starts off: no row is no grant and a zero budget.

import (
	"context"
	"fmt"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ModelReservation is what one call of a model reserves before it runs (D4: the whole context
// window at the dearest input rate plus the output cap at the dearest output rate). MinMicro is
// the smallest reservation any call can have (an output cap of one token); MaxOutputMicro is a
// call asking for the model's whole output, which is near what a coding harness asks for.
type ModelReservation struct {
	Model          string `json:"model"`
	MinMicro       int64  `json:"min_micro"`
	MaxOutputMicro int64  `json:"max_output_micro"`
}

// reservations is the one-call reservation of each model on the deployment source's list, and
// the smallest of them: below it no call of any model can ever be admitted. ok false when no
// source is registered or none of its models is priced (nothing to hold a budget to).
func (g *Gateway) reservations(ctx context.Context) (rows []ModelReservation, floor int64, ok bool, err error) {
	src, found, err := g.DeploymentSource(ctx)
	if err != nil || !found {
		return nil, 0, false, err
	}
	for _, id := range src.AllowList {
		m, priced := routeprice.Lookup(id)
		if !priced {
			continue
		}
		low, lerr := m.UpperBoundMicroUSD(1)
		high, herr := m.UpperBoundMicroUSD(m.MaxOutput)
		if lerr != nil || herr != nil {
			continue
		}
		rows = append(rows, ModelReservation{Model: id, MinMicro: low, MaxOutputMicro: high})
		if !ok || low < floor {
			floor, ok = low, true
		}
	}
	return rows, floor, ok, nil
}

// belowFloor holds a positive amount to the reservations (D4's full-fit admission admits a call
// only when its whole reservation fits). Below the smallest one-call reservation of every model
// no call can ever be admitted: that is refused, as D3 refuses a grant with no ceiling at save
// time. Above it, a model whose full-output call does not fit gets a warning: whether a call
// fits then depends on the output cap the harness asks for, which the CP learns only per call
// (claude asked for 32,000 tokens, measured on the sandbox 2026-11-09). Zero stays legal: it is
// "no use", and says so by itself. what names the amount.
func (g *Gateway) belowFloor(ctx context.Context, what string, micro *int64) ([]string, error) {
	if micro == nil || *micro <= 0 {
		return nil, nil
	}
	rows, floor, ok, err := g.reservations(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if *micro >= floor {
		var warn []string
		for _, r := range rows {
			if *micro < r.MaxOutputMicro {
				warn = append(warn, fmt.Sprintf("%s %s admits a call of %s only while its output cap is small: one call reserves %s with a one-token cap and %s with the model's whole output (a coding session asks for tens of thousands of tokens)",
					what, usd(*micro), r.Model, usd(r.MinMicro), usd(r.MaxOutputMicro)))
			}
		}
		return warn, nil
	}
	cheapest := ""
	for _, r := range rows {
		if r.MinMicro == floor {
			cheapest = r.Model
			break
		}
	}
	return nil, &InvalidInputError{fmt.Errorf("%s %s is below the smallest reservation of one call (%s, for %s with a one-token output cap), "+
		"so no call could ever be admitted; set at least that, or 0 for no use. A coding session asks for far more output: "+
		"GET this view's reservations for each model", what, usd(*micro), usd(floor), cheapest)}
}

// GrantInput is the super_admin's grant to one tenant.
type GrantInput struct {
	Granted bool `json:"granted"`
	// CeilingMicro is the tenant's monthly ceiling in micro-USD; required while granted (D3:
	// an empty ceiling with the grant on is refused at save time).
	CeilingMicro *int64 `json:"ceiling_micro"`
}

// SetGrant saves in for tenantID.
func (g *Gateway) SetGrant(ctx context.Context, tenantID string, in GrantInput) ([]string, error) {
	if in.CeilingMicro != nil && *in.CeilingMicro < 0 {
		return nil, &InvalidInputError{fmt.Errorf("a ceiling cannot be negative")}
	}
	if in.Granted && (in.CeilingMicro == nil || *in.CeilingMicro == 0) {
		return nil, &InvalidInputError{fmt.Errorf("granting the deployment's Bedrock route needs a monthly ceiling above zero for the tenant")}
	}
	// Off, the ceiling is inert: saving the grant off with an old ceiling below a floor that has
	// risen since must not be refused.
	var warn []string
	if in.Granted {
		w, err := g.belowFloor(ctx, "the tenant's monthly ceiling", in.CeilingMicro)
		if err != nil {
			return nil, err
		}
		warn = w
	}
	return warn, g.d.Store.SetProvidedRouteGrant(ctx, tenantID, providedroutes.SourceDeployment, in.Granted, in.CeilingMicro)
}

// PolicyInput is the tenant_admin's policy for every member.
type PolicyInput struct {
	MembersOnly        bool  `json:"members_only"`
	MemberDefaultMicro int64 `json:"member_default_micro"`
}

// SetPolicy saves in for tenantID.
func (g *Gateway) SetPolicy(ctx context.Context, tenantID string, in PolicyInput) ([]string, error) {
	if in.MemberDefaultMicro < 0 {
		return nil, &InvalidInputError{fmt.Errorf("a budget cannot be negative")}
	}
	warn, err := g.belowFloor(ctx, "the members' default monthly budget", &in.MemberDefaultMicro)
	if err != nil {
		return nil, err
	}
	return warn, g.d.Store.SetProvidedRouteMemberPolicy(ctx, tenantID, providedroutes.SourceDeployment, in.MembersOnly, in.MemberDefaultMicro)
}

// MemberInput is the tenant_admin's grant and budget override for one member.
type MemberInput struct {
	MembershipID string `json:"membership_id"`
	Granted      bool   `json:"granted"`
	// BudgetMicro nil = the tenant's default.
	BudgetMicro *int64 `json:"budget_micro"`
}

// SetMember saves in. The caller has checked that the membership is one of tenantID's.
func (g *Gateway) SetMember(ctx context.Context, tenantID string, in MemberInput) ([]string, error) {
	if in.BudgetMicro != nil && *in.BudgetMicro < 0 {
		return nil, &InvalidInputError{fmt.Errorf("a budget cannot be negative")}
	}
	warn, err := g.belowFloor(ctx, "the member's monthly budget", in.BudgetMicro)
	if err != nil {
		return nil, err
	}
	return warn, g.d.Store.SetProvidedRouteMember(ctx, tenantID, in.MembershipID, providedroutes.SourceDeployment, in.Granted, in.BudgetMicro)
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
	// Reservations is one call's reservation for each model on the source's list, and
	// FloorMicro the smallest of them: a budget or ceiling above zero and below it is refused.
	Reservations []ModelReservation `json:"reservations"`
	FloorMicro   int64              `json:"floor_micro"`
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
	rows, floor, _, err := g.reservations(ctx)
	if err != nil {
		return TenantView{}, err
	}
	v.Reservations, v.FloorMicro = rows, floor
	if v.Reservations == nil {
		v.Reservations = []ModelReservation{}
	}
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
