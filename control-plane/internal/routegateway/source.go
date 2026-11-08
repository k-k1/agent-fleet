package routegateway

// The super_admin's registration of the deployment source (ADR 0129
// D1b, D3; decided 2026-11-08: by REST with an audit row, the stack granting only the IAM).
//
// There is one deployment source. Its id survives every edit; a changed role or Region moves the
// fingerprint, which refuses every session bound to the old one (decision 1), and a changed
// model list bumps the policy version, which the next call's credentials are keyed by. In Q1 a
// deployment source calls exactly one Region.
//
// Activation needs no external-id probes: the role is the operator's own, defined by the stack.
// The ADR's one minimal model call under the session policy is not made at registration; the
// first member call is that call, and its refusal names the missing permission (the live gate
// checks the intended role and policy, never the deployer's).

import (
	"context"
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// SourceView is the deployment source as the admin API shows it.
type SourceView struct {
	ID            string   `json:"id"`
	Binding       string   `json:"binding"`
	Name          string   `json:"name"`
	RoleARN       string   `json:"role_arn"`
	Region        string   `json:"region"`
	Models        []string `json:"models"`
	Fingerprint   string   `json:"fingerprint"`
	PolicyVersion int      `json:"policy_version"`
	Active        bool     `json:"active"`
	UpdatedAt     string   `json:"updated_at"`
}

// ViewSource is s as a SourceView.
func ViewSource(s store.ProvidedRouteSource) SourceView {
	v := SourceView{ID: s.ID, Binding: s.Binding(), Name: s.Name, RoleARN: s.RoleARN, Models: s.AllowList,
		Fingerprint: s.Fingerprint, PolicyVersion: s.PolicyVersion, Active: s.Active, UpdatedAt: s.UpdatedAt}
	if len(s.Regions) > 0 {
		v.Region = s.Regions[0]
	}
	if v.Models == nil {
		v.Models = []string{}
	}
	return v
}

// DeploymentSource is the one registered deployment source, ok=false when there is none.
func (g *Gateway) DeploymentSource(ctx context.Context) (store.ProvidedRouteSource, bool, error) {
	rows, err := g.d.Store.ListProvidedRouteSources(ctx, "")
	if err != nil {
		return store.ProvidedRouteSource{}, false, err
	}
	for _, r := range rows {
		if r.Source == providedroutes.SourceDeployment {
			return r, true, nil
		}
	}
	return store.ProvidedRouteSource{}, false, nil
}

// SourceInput is a registration or an edit of the deployment source.
type SourceInput struct {
	Name    string   `json:"name"`
	RoleARN string   `json:"role_arn"`
	Region  string   `json:"region"`
	Models  []string `json:"models"`
	// Active nil = true: a registration is meant to be used. false switches the route off
	// without deleting the row (its spend and the sessions bound to it stay attributable).
	Active *bool `json:"active"`
}

// PutDeploymentSource registers or edits the deployment source and sets its activation.
func (g *Gateway) PutDeploymentSource(ctx context.Context, in SourceInput) (store.ProvidedRouteSource, error) {
	region := strings.TrimSpace(in.Region)
	if region == "" || strings.ContainsAny(region, ", ") {
		return store.ProvidedRouteSource{}, fmt.Errorf("a deployment source calls exactly one Region")
	}
	// The closure must be buildable and fit AWS's session policy limit, or no call could run.
	if _, err := providedroutes.SessionPolicy(in.RoleARN, region, in.Models); err != nil {
		return store.ProvidedRouteSource{}, err
	}
	put := store.ProvidedRouteSourceInput{Source: providedroutes.SourceDeployment, Name: strings.TrimSpace(in.Name),
		RoleARN: in.RoleARN, Regions: []string{region}, AllowList: in.Models}
	if put.Name == "" {
		put.Name = "deployment"
	}
	cur, ok, err := g.DeploymentSource(ctx)
	if err != nil {
		return store.ProvidedRouteSource{}, err
	}
	if ok {
		put.ID = cur.ID
	}
	src, err := g.d.Store.PutProvidedRouteSource(ctx, put)
	if err != nil {
		return store.ProvidedRouteSource{}, err
	}
	active := in.Active == nil || *in.Active
	if src.Active != active {
		if err := g.d.Store.SetProvidedRouteSourceActive(ctx, src.ID, src.Fingerprint, active); err != nil {
			return store.ProvidedRouteSource{}, err
		}
		src.Active = active
	}
	return src, nil
}
