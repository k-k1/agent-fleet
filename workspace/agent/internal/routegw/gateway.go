package routegw

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// RouteBedrock is the only route this gateway serves so far (ADR 0128 P2a).
const RouteBedrock = "bedrock"

// Deps is everything the gateway reads from outside itself, so a test drives every check with
// fakes and the Agent wires the real ones (Wire).
type Deps struct {
	Registry *Registry
	Now      func() time.Time
	// ReadMeta reads a session's stored meta: its existence, incarnation and selection.
	ReadMeta func(name string) (session.Meta, bool)
	// Settings is the member's Settings profiles as fresh as the Agent can make them; ok false
	// means it cannot say (the CP has been unreachable too long), and the call is refused.
	Settings func() (map[string]awsx.Profile, bool)
	// Held and Ask are ADR 0126's approval: Held reads a grant without waiting; Ask files the
	// request so the member sees it.
	Held func(settings map[string]awsx.Profile, profile string, m session.Meta, now time.Time) bool
	Ask  func(settings map[string]awsx.Profile, profile, sessionName, what string) error
	// Credentials resolves the profile's credentials the way af-aws-exec does, never logging in.
	Credentials func(p awsx.Profile, region string, settings map[string]awsx.Profile) (awsx.RouteCredentials, error)
	// Append writes ledger rows.
	Append func([]usagex.Record) error
	// Endpoint is the upstream base URL for a region (a test points it at a fake).
	Endpoint func(region string) string
	Client   *http.Client
	// BaseURL is what a session is told to call; set by Start once the listener is known.
	BaseURL func() string
}

// Gateway checks and forwards model calls.
type Gateway struct {
	d Deps

	askMu sync.Mutex
	asked map[string]time.Time // session|profile id → last time a request was filed

	credMu sync.Mutex
	creds  map[string]cachedCreds
	flight map[string]*credFlight
}

type cachedCreds struct {
	c      awsx.RouteCredentials
	usable time.Time // use until
}

// credReserve is how long before their expiry cached credentials stop being used.
const credReserve = 5 * time.Minute

// credMaxAge bounds credentials that report no expiry.
const credMaxAge = 5 * time.Minute

// askEvery is how often one session's call may file a confirmation request: a harness that
// retries a refused call must not put a card back every time.
const askEvery = 10 * time.Second

// New returns a gateway over d.
func New(d Deps) *Gateway {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Registry == nil {
		d.Registry = NewRegistry(d.Now)
	}
	if d.Held == nil {
		d.Held = awsx.ApprovalHeld
	}
	if d.Ask == nil {
		d.Ask = awsx.AskApproval
	}
	if d.ReadMeta == nil {
		d.ReadMeta = session.ReadMeta
	}
	if d.Append == nil {
		d.Append = usagex.AppendRows
	}
	if d.Endpoint == nil {
		d.Endpoint = bedrockEndpoint
	}
	if d.Client == nil {
		d.Client = upstreamClient()
	}
	return &Gateway{d: d, asked: map[string]time.Time{}, creds: map[string]cachedCreds{}, flight: map[string]*credFlight{}}
}

// Registry is the gateway's token registry.
func (g *Gateway) Registry() *Registry { return g.d.Registry }

// refusal is why a call or an issue was refused: an HTTP status, a stable code and a message
// the member can act on. It never carries a token or a credential.
type refusal struct {
	status int
	code   string
	msg    string
}

func (r *refusal) Error() string { return r.msg }

var regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)

// connection is what a selection resolves to: the profile and the region the calls go to.
type connection struct {
	profile  awsx.Profile
	region   string
	settings map[string]awsx.Profile
}

// resolve is the part of the check that depends on Settings and the selection alone, shared by
// issuing a token and by every call: the selection is this route on this connection, the profile
// is one of the member's own, marked as a model route, and a region is known.
func (g *Gateway) resolve(sel *routes.Selection) (connection, *refusal) {
	if sel == nil || sel.Route != RouteBedrock {
		return connection{}, &refusal{http.StatusUnauthorized, routes.CodeBadRoute, "the session does not select the bedrock route"}
	}
	id, region, ok := routes.ParseAWSConnection(sel.Connection)
	if !ok {
		return connection{}, &refusal{http.StatusUnauthorized, routes.CodeRouteUnavailable, "the session's route connection is not an AWS profile reference"}
	}
	settings, ok := g.d.Settings()
	if !ok {
		return connection{}, &refusal{http.StatusServiceUnavailable, routes.CodeRouteUnavailable,
			"the Agent cannot read the AWS profiles in Settings right now; the route is refused until it can"}
	}
	p, ok := awsx.ProfileByID(settings, id)
	if !ok {
		// Another member's profile and a deleted one look the same from here, on purpose: a
		// member's profiles are theirs alone to use (ADR 0128 decision 12).
		return connection{}, &refusal{http.StatusForbidden, routes.CodeRouteForbidden,
			"the route's AWS profile is not one of your Settings profiles (it was deleted, or is not yours)"}
	}
	if !p.ModelRoute {
		return connection{}, &refusal{http.StatusForbidden, routes.CodeRouteForbidden, fmt.Sprintf(
			"profile %q is not marked as a model route; mark it in Settings > AWS profiles/SSM (it must carry only bedrock:InvokeModel*)", p.Name)}
	}
	if region == "" {
		region = p.Region
	}
	if !regionRe.MatchString(region) || strings.HasPrefix(region, "cn-") {
		return connection{}, &refusal{http.StatusForbidden, routes.CodeRouteUnavailable, fmt.Sprintf(
			"no usable AWS region for profile %q (%q); set the profile's default region in Settings", p.Name, region)}
	}
	return connection{profile: p, region: region, settings: settings}, nil
}

// Issue mints the token a session's launch hands to its harness, after the same checks every
// call repeats. m is the session's stored meta: the selection is read from it, never from the
// caller, so a token can only be had for the connection the session actually selected. Only the
// Agent's own launch code calls it; there is no HTTP route that mints, because the Agent's HTTP
// token is in every session's shell.
func (g *Gateway) Issue(m session.Meta) (Issued, *agents.RouteRefusal) {
	c, ref := g.resolve(m.Route)
	if ref != nil {
		return Issued{}, &agents.RouteRefusal{Status: ref.status, Code: ref.code, Message: ref.msg}
	}
	inc := awsx.Incarnation(m)
	b := Binding{Session: m.Name, Incarnation: inc, Route: RouteBedrock, Connection: m.Route.Connection,
		ProfileID: c.profile.ID, Region: c.region, Identity: awsx.ProfileIdentity(c.settings, c.profile)}
	tok, exp, err := g.d.Registry.Mint(b, MaxTTL)
	if err != nil {
		return Issued{}, &agents.RouteRefusal{Status: http.StatusInternalServerError, Code: routes.CodeRouteUnavailable, Message: err.Error()}
	}
	base := ""
	if g.d.BaseURL != nil {
		base = g.d.BaseURL()
	}
	if base == "" {
		g.d.Registry.Revoke(tok)
		return Issued{}, &agents.RouteRefusal{Status: http.StatusServiceUnavailable, Code: routes.CodeRouteUnavailable,
			Message: "the route gateway is not listening"}
	}
	return Issued{BaseURL: base, Token: tok, Expires: exp}, nil
}

// Issued is what a launch gets: where to call, the bearer to call with, and when it ends.
type Issued struct {
	BaseURL string
	Token   string
	Expires time.Time
}

// CheckRoute is agents.RouteChecker for the bedrock route: the live half of ADR 0128 decision 12
// for a selection, before anything starts. The model is not checked here: Bedrock's model list
// is per account and region and the gateway never calls it.
func (g *Gateway) CheckRoute(_ context.Context, sel agents.RouteSelection, _ string) *agents.RouteRefusal {
	if _, ref := g.resolve(&sel); ref != nil {
		return &agents.RouteRefusal{Status: ref.status, Code: ref.code, Message: ref.msg}
	}
	return nil
}

// call is a call that passed every check.
type call struct {
	b     Binding
	meta  session.Meta
	conn  connection
	token string
}

// authorize is the check every upstream call runs, in this order, and the reason a token is
// revoked on the spot: it is what makes "revoked when the session ends, the selection changes or
// the connection changes" take effect on the next call, not on a timer.
func (g *Gateway) authorize(token string) (*call, *refusal) {
	b, ok := g.d.Registry.Lookup(token)
	if !ok {
		return nil, &refusal{http.StatusUnauthorized, "route_token_invalid", "the route token is unknown, expired or revoked; restart the session"}
	}
	revoke := func(status int, code, msg string) (*call, *refusal) {
		g.d.Registry.Revoke(token)
		return nil, &refusal{status, code, msg}
	}
	m, ok := g.d.ReadMeta(b.Session)
	if !ok || awsx.Incarnation(m) != b.Incarnation {
		return revoke(http.StatusUnauthorized, "route_session_ended", "the session this route token belongs to has ended")
	}
	if m.Route == nil || m.Route.Route != b.Route || m.Route.Connection != b.Connection {
		return revoke(http.StatusUnauthorized, "route_selection_changed", "the session no longer selects this route connection")
	}
	c, ref := g.resolve(m.Route)
	if ref != nil {
		// A deleted profile, a lost mark and a bad region all end the token; only an unreadable
		// Settings list is left for the next call to settle.
		if ref.status == http.StatusServiceUnavailable {
			return nil, ref
		}
		return revoke(ref.status, ref.code, ref.msg)
	}
	if c.profile.ID != b.ProfileID || awsx.ProfileIdentity(c.settings, c.profile) != b.Identity || c.region != b.Region {
		return revoke(http.StatusUnauthorized, "route_connection_changed", fmt.Sprintf(
			"profile %q changed in Settings after this route token was issued; restart the session", c.profile.Name))
	}
	if !g.d.Held(c.settings, c.profile.Name, m, g.d.Now()) {
		return nil, g.approvalRefusal(c, m)
	}
	return &call{b: b, meta: m, conn: c, token: token}, nil
}

// approvalRefusal files the confirmation request (at most once per askEvery per session and
// profile) and refuses the call. The token stays: the member's press is what lets it through.
func (g *Gateway) approvalRefusal(c connection, m session.Meta) *refusal {
	key := m.Name + "|" + c.profile.ID
	g.askMu.Lock()
	last, seen := g.asked[key]
	now := g.d.Now()
	due := !seen || now.Sub(last) >= askEvery
	if due {
		g.asked[key] = now
	}
	g.askMu.Unlock()
	msg := fmt.Sprintf("profile %q asks for confirmation before use; it is waiting in the Agent Fleet Console (try again after it is allowed)", c.profile.Name)
	if due {
		if err := g.d.Ask(c.settings, c.profile.Name, m.Name, "model route"); err != nil && errors.Is(err, awsx.ErrConfirmRequired) {
			msg = err.Error()
		}
	}
	return &refusal{http.StatusForbidden, "route_confirmation_required", msg}
}

// credentials returns the call's credentials, from memory until shortly before they expire. The
// cache key is the profile's identity, so an edited profile never reads the old one's.
func (g *Gateway) credentials(c *call) (awsx.RouteCredentials, *refusal) {
	key := c.b.Identity + "|" + c.conn.region
	now := g.d.Now()
	g.credMu.Lock()
	if e, ok := g.creds[key]; ok && now.Before(e.usable) {
		g.credMu.Unlock()
		return e.c, nil
	}
	delete(g.creds, key)
	// One resolution per key at a time, outside credMu: it starts the AWS CLI, which can be slow,
	// and must not hold up other sessions' calls on other profiles.
	fl, running := g.flight[key]
	if !running {
		fl = &credFlight{done: make(chan struct{})}
		g.flight[key] = fl
	}
	g.credMu.Unlock()
	if running {
		<-fl.done
		return fl.c, fl.ref
	}
	cr, err := g.d.Credentials(c.conn.profile, c.conn.region, c.conn.settings)
	var ref *refusal
	if err != nil {
		if errors.Is(err, awsx.ErrLoginRequired) {
			ref = &refusal{http.StatusForbidden, "route_login_required", fmt.Sprintf(
				"the SSO login for profile %q is required; sign in from the Console, or run `af-aws-exec --profile %s -- true` in a terminal", c.conn.profile.Name, c.conn.profile.Name)}
		} else {
			ref = &refusal{http.StatusBadGateway, "route_credentials_unavailable", fmt.Sprintf(
				"could not get credentials for profile %q: %v", c.conn.profile.Name, err)}
		}
		cr = awsx.RouteCredentials{}
	}
	g.credMu.Lock()
	if ref == nil {
		usable := now.Add(credMaxAge)
		if !cr.Expires.IsZero() {
			usable = cr.Expires.Add(-credReserve)
		}
		if now.Before(usable) {
			g.creds[key] = cachedCreds{c: cr, usable: usable}
		}
	}
	delete(g.flight, key)
	fl.c, fl.ref = cr, ref
	g.credMu.Unlock()
	close(fl.done)
	return cr, ref
}

// credFlight is one in-progress resolution that concurrent callers of the same key wait on.
type credFlight struct {
	done chan struct{}
	c    awsx.RouteCredentials
	ref  *refusal
}

// Sweep ends the tokens whose session is gone, is another incarnation or selects something else,
// and those past their time. The per-call check already refuses all of them; this frees memory
// and the cached credentials with them.
func (g *Gateway) Sweep() int {
	// keep checks existence, incarnation and selection only. A token whose profile was unmarked or
	// edited stays in memory until its next call (which refuses and revokes it) or its 8 hours; it
	// grants nothing meanwhile, because every call resolves the profile again.
	n := g.d.Registry.Sweep(func(b Binding) bool {
		m, ok := g.d.ReadMeta(b.Session)
		return ok && awsx.Incarnation(m) == b.Incarnation && m.Route != nil && m.Route.Route == b.Route && m.Route.Connection == b.Connection
	})
	g.credMu.Lock()
	now := g.d.Now()
	for k, e := range g.creds {
		if !now.Before(e.usable) {
			delete(g.creds, k)
		}
	}
	g.credMu.Unlock()
	g.askMu.Lock()
	for k, t := range g.asked {
		if g.d.Now().Sub(t) > time.Hour {
			delete(g.asked, k)
		}
	}
	g.askMu.Unlock()
	return n
}
