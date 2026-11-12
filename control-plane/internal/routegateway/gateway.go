// Package routegateway is the CP route gateway of ADR 0129 (G3, D2), phase Q1: Bedrock that
// the deployment pays for, signed by the Control Plane under the deployment's route role.
//
//	Agent routegw ──CP route token──▶ this gateway ──AssumeRole(source, membership) + SigV4──▶ Bedrock
//
// The CP is the boundary and trusts nothing the caller says: the member can extract the route
// token, or the Agent's issuing token, and call here directly. So every call, in this order:
// the token's tag and expiry; the membership is live; the tenant's grant; the member's grant;
// the bound source exists with the same fingerprint and is active; the signing contract
// (internal/providedroutes: operation, model, headers, body); budget admission, durable before
// anything is sent; then cached session-tagged credentials, SigV4, the stream with a flush per
// read, and settlement exactly once.
//
// Off unless the CP runs with AF_PROVIDED_ROUTES=1 (an acceptance switch, ADR 0129 D7 Q1, not a
// release setting): nothing is registered and no issuing token is injected, so nothing new is
// offered or accepted.
package routegateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/k-k1/agent-fleet/control-plane/internal/providedroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// EnvSwitch is the acceptance switch. Exactly "1"; anything else leaves it off.
	EnvSwitch = "AF_PROVIDED_ROUTES"
	// Prefix is where the gateway answers; the rest of the path is Bedrock's own.
	Prefix = "/route/v1"
	// CallHeader carries the CP's call id on the answer, so the Agent's ledger row and the
	// CP's reservation are the same call (D2, "Usage").
	CallHeader = "X-Af-Route-Call"
	// routeMaxBody bounds the request body the CP buffers to sign, as the Agent's gateway does.
	routeMaxBody = 32 << 20
)

// Enabled reports whether the CP runs with the acceptance switch on.
func Enabled() bool { return os.Getenv(EnvSwitch) == "1" }

// Config is the deployment's settings for the route gateway. The deployment source
// itself (role, Region, model list) is registered by a super_admin over REST
// (the CP's provided_routes.go, PutDeploymentSource); the stack only grants the IAM.
type Config struct {
	// MaxInFlight is the per-member cap on concurrent calls (D4; open question 3 sets it from
	// Q1's measurements).
	MaxInFlight int
	// Deadline bounds one upstream call. It is also what D2's path rule is held against: every
	// hop between the Agent and the CP must idle out later than this.
	Deadline time.Duration
	// Drain is how long a stopping CP lets admitted calls finish.
	Drain time.Duration
}

const (
	defaultRouteMaxInFlight = 4
	defaultRouteDeadline    = 15 * time.Minute
	routeDrainCap           = 25 * time.Second
	// routeOrphanGrace is added to the deadline before an in-flight row with no live call in
	// this process is settled as an orphan: a CP that admitted it may still be finishing it.
	routeOrphanGrace = time.Minute
)

// ConfigFromEnv reads AF_PROVIDED_ROUTE_MAX_IN_FLIGHT and AF_PROVIDED_ROUTE_DEADLINE.
func ConfigFromEnv() Config {
	c := Config{MaxInFlight: defaultRouteMaxInFlight, Deadline: defaultRouteDeadline}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AF_PROVIDED_ROUTE_MAX_IN_FLIGHT"))); err == nil && n > 0 {
		c.MaxInFlight = n
	}
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("AF_PROVIDED_ROUTE_DEADLINE"))); err == nil && d > 0 {
		c.Deadline = d
	}
	c.Drain = min(c.Deadline, routeDrainCap)
	return c
}

// Assumer takes the route role for one membership. The STS implementation is
// stsAssumer; tests stand in for it, and no test calls AWS.
type Assumer interface {
	Assume(ctx context.Context, in AssumeInput) (aws.Credentials, error)
}

// AssumeInput is one AssumeRole for one membership.
type AssumeInput struct {
	RoleARN, ExternalID, Region string
	// SessionName is the membership id (D5): per-member IAM principal attribution.
	SessionName string
	// Tags attribute; they never authorize (D2). af-membership and af-tenant.
	Tags   map[string]string
	Policy string
}

// routeCredReserve is how long before their expiry cached credentials are replaced (D5).
const routeCredReserve = 5 * time.Minute

type cachedRouteCreds struct {
	c      aws.Credentials
	usable time.Time
}

type routeCredFlight struct {
	done chan struct{}
	c    aws.Credentials
	err  error
}

// Store is what the gateway reads and writes; store.Store has all of it.
type Store interface {
	GetMembershipByID(ctx context.Context, membershipID string) (store.MembershipView, bool, error)
	GetProvidedRouteGrant(ctx context.Context, tenantID, source string) (store.ProvidedRouteGrant, error)
	GetProvidedRouteMember(ctx context.Context, membershipID, source string) (store.ProvidedRouteMember, error)
	GetProvidedRouteSource(ctx context.Context, id string) (store.ProvidedRouteSource, error)
	ListProvidedRouteSources(ctx context.Context, tenantID string) ([]store.ProvidedRouteSource, error)
	PutProvidedRouteSource(ctx context.Context, in store.ProvidedRouteSourceInput) (store.ProvidedRouteSource, error)
	SetProvidedRouteSourceActive(ctx context.Context, id, fingerprint string, active bool) error
	AdmitProvidedRouteCall(ctx context.Context, c store.ProvidedRouteCall, lim store.ProvidedRouteLimits) (store.ProvidedRouteAdmission, error)
	SettleProvidedRouteCall(ctx context.Context, callID string, finalMicro int64) (bool, error)
	ListInFlightProvidedRouteCalls(ctx context.Context, limit int) ([]store.ProvidedRouteCall, error)
	GetProvidedRoutePeriod(ctx context.Context, tenantID, membershipID, source, month string) (store.ProvidedRoutePeriod, error)
	SetProvidedRouteGrant(ctx context.Context, tenantID, source string, granted bool, ceilingMicro *int64) error
	SetProvidedRouteMemberPolicy(ctx context.Context, tenantID, source string, membersOnly bool, defaultMicro int64) error
	SetProvidedRouteMember(ctx context.Context, tenantID, membershipID, source string, granted bool, budgetMicro *int64) error
}

// Deps is what the Control Plane hands the gateway: its store and the bridge-token machinery,
// which lives with the manager (bridge_epoch.go).
type Deps struct {
	Store Store
	// KeyAt is the route token family's key at a membership's bridge epoch.
	KeyAt func(epoch int64) []byte
	// BridgeEpoch is the membership's current epoch; ok false refuses the token.
	BridgeEpoch func(ctx context.Context, membershipID string) (int64, bool)
	// VerifyIssuer verifies an issuing token (VerifyIssueToken) under its membership's current
	// epoch and returns the epoch it verified under.
	VerifyIssuer func(ctx context.Context, token string) (membershipID string, epoch int64, ok bool)
	// Track files a request under the membership, so its removal cuts the request
	// (member_removal.go). The returned func ends the tracking.
	Track   func(ctx context.Context, membershipID string) (context.Context, func())
	Assumer Assumer
	Config  Config
}

// Gateway is the CP route gateway.
type Gateway struct {
	d        Deps
	cfg      Config
	assumer  Assumer
	client   *http.Client
	endpoint func(region string) string
	now      func() time.Time
	instance string
	newCall  func() string
	// zeroCleanRefusal settles an upstream answer that refused the call before inference (a
	// whole AWS JSON error, no stream) at zero instead of the reservation (decision B1).
	zeroCleanRefusal bool

	credMu sync.Mutex
	creds  map[string]cachedRouteCreds
	flight map[string]*routeCredFlight

	draining atomic.Bool
	callMu   sync.Mutex
	calls    map[string]int64 // admitted calls of this process → their reservation
}

// New returns a gateway over d.
func New(d Deps) *Gateway {
	return &Gateway{
		d: d, cfg: d.Config, assumer: d.Assumer,
		client:   routeUpstreamClient(),
		endpoint: func(region string) string { return "https://bedrock-runtime." + region + ".amazonaws.com" },
		now:      time.Now, instance: store.NewID(), newCall: store.NewID, zeroCleanRefusal: true,
		creds: map[string]cachedRouteCreds{}, flight: map[string]*routeCredFlight{}, calls: map[string]int64{},
	}
}

// routeUpstreamClient has no overall timeout: the call's own deadline bounds it.
func routeUpstreamClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Run sweeps until ctx ends: expired credentials and orphaned reservations, once a minute.
func (g *Gateway) Run(ctx context.Context) { g.sweepLoop(ctx) }

// --- refusals ----------------------------------------------------------------

// routeRefusal is a refusal of the gateway's own, never an upstream answer.
type routeRefusal struct {
	status int
	code   string
	msg    string
	header map[string]string
}

func (r *routeRefusal) Error() string { return r.code + ": " + r.msg }

func fromContract(r *providedroutes.Refusal) *routeRefusal {
	return &routeRefusal{status: r.Status, code: r.Code, msg: r.Msg}
}

// writeRouteRefusal answers in the shape and statuses the Agent's gateway answers a harness in
// (workspace/agent/internal/routegw/bedrock.go writeRefusal), because the Agent passes this
// answer through: a refusal of authentication goes out as 401, one of what the caller may do
// (forbidden, conflict) as 412, one waiting may clear as 503, all with x-should-retry: false,
// which Claude Code shows once instead of retrying (measured in ADR 0128 P2b part 2). 412, not
// 401, for the middle class: Claude Code prefixes every 401 with "AWS authentication failed ·
// refresh your AWS credentials", which sends a member after credentials that are not the
// problem, while 412 shows the message alone and still goes out once (measured, #3891).
func writeRouteRefusal(w http.ResponseWriter, r *routeRefusal) {
	for k, v := range r.header {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-Errortype", r.code)
	w.Header().Set("X-Should-Retry", "false")
	status := r.status
	switch {
	case status >= 500:
		status = http.StatusServiceUnavailable
	case status == http.StatusForbidden || status == http.StatusConflict:
		status = http.StatusPreconditionFailed
	}
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]string{"message": r.msg, "code": r.code})
	_, _ = w.Write(b)
}

func routeBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// --- who may use it ----------------------------------------------------------

// routeUse is what the CP's own records say a call may use.
type routeUse struct {
	mv     store.MembershipView
	src    store.ProvidedRouteSource
	region string
	lim    store.ProvidedRouteLimits
}

// authorizeUse is D3 for one membership and one bound source: the tenant's grant, the member's
// grant, the source as bound, and a budget above zero. Every answer comes from the store.
func (g *Gateway) authorizeUse(ctx context.Context, mv store.MembershipView, route, binding string) (routeUse, *routeRefusal) {
	st := g.d.Store
	source := providedroutes.SourceOf(route)
	if source != providedroutes.SourceDeployment {
		return routeUse{}, &routeRefusal{status: http.StatusForbidden, code: "route_unsupported",
			msg: "this Control Plane serves the deployment's Bedrock route only"}
	}
	grant, err := st.GetProvidedRouteGrant(ctx, mv.TenantID, source)
	if err != nil {
		return routeUse{}, &routeRefusal{status: http.StatusServiceUnavailable, code: "route_unavailable", msg: "the route's grant could not be read"}
	}
	if !grant.Granted {
		return routeUse{}, &routeRefusal{status: http.StatusForbidden, code: "route_not_granted",
			msg: "this tenant is not granted the deployment's Bedrock route"}
	}
	member, err := st.GetProvidedRouteMember(ctx, mv.MembershipID, source)
	if err != nil {
		return routeUse{}, &routeRefusal{status: http.StatusServiceUnavailable, code: "route_unavailable", msg: "the route's grant could not be read"}
	}
	if grant.MembersOnly && !member.Granted {
		return routeUse{}, &routeRefusal{status: http.StatusForbidden, code: "route_member_not_granted",
			msg: "your tenant administrator has not granted you the deployment's Bedrock route"}
	}
	id, fp, ok := providedroutes.ParseBinding(binding)
	if !ok {
		return routeUse{}, &routeRefusal{status: http.StatusUnauthorized, code: "route_token_invalid", msg: "the route token names no bound source"}
	}
	src, err := st.GetProvidedRouteSource(ctx, id)
	if errors.Is(err, store.ErrProvidedRouteSourceNotFound) || err == nil && (src.Source != source || src.TenantID != "") {
		return routeUse{}, &routeRefusal{status: http.StatusForbidden, code: "route_source_missing",
			msg: "the route this session was started on no longer exists; start a new session"}
	}
	if err != nil {
		return routeUse{}, &routeRefusal{status: http.StatusServiceUnavailable, code: "route_unavailable", msg: "the route could not be read"}
	}
	if src.Fingerprint != fp {
		return routeUse{}, &routeRefusal{status: http.StatusConflict, code: "route_connection_changed",
			msg: "the deployment's Bedrock route was re-pointed (role or Region) since this session was started on it; a session never moves to another payer. Start a new session"}
	}
	if !src.Active {
		return routeUse{}, &routeRefusal{status: http.StatusForbidden, code: "route_source_inactive",
			msg: "the deployment's Bedrock route is switched off"}
	}
	if len(src.Regions) != 1 {
		return routeUse{}, &routeRefusal{status: http.StatusForbidden, code: "route_source_inactive",
			msg: "the deployment's Bedrock route has no single Region to call"}
	}
	budget := grant.MemberBudgetMicro(member)
	if grant.CeilingMicro == nil {
		return routeUse{}, &routeRefusal{status: http.StatusForbidden, code: "route_not_granted",
			msg: "the deployment's Bedrock route has no monthly ceiling for this tenant"}
	}
	use := routeUse{mv: mv, src: src, region: src.Regions[0],
		lim: store.ProvidedRouteLimits{MemberBudgetMicro: budget, TenantCeilingMicro: grant.CeilingMicro, MaxInFlight: g.cfg.MaxInFlight}}
	if budget <= 0 {
		return routeUse{}, g.budgetRefusal(ctx, use, store.ProvidedRouteRefusedMemberBudget, 0)
	}
	if *grant.CeilingMicro <= 0 {
		return routeUse{}, g.budgetRefusal(ctx, use, store.ProvidedRouteRefusedTenantCeiling, 0)
	}
	return use, nil
}

// budgetRefusal is D4's "when it runs out": 503 with x-should-retry: false, naming the budget,
// the amount used, the level that ran out and the reset date. The X-Af-Route-* headers carry the
// same for the Agent's spend-limit episode.
func (g *Gateway) budgetRefusal(ctx context.Context, use routeUse, level string, next int64) *routeRefusal {
	now := g.now().UTC()
	month := providedroutes.AdmissionMonth(now)
	reset := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	if level == store.ProvidedRouteRefusedInFlight {
		return &routeRefusal{status: http.StatusServiceUnavailable, code: "route_in_flight_cap", msg: fmt.Sprintf(
			"you already have %d calls in flight on the deployment's Bedrock route; try again when one finishes", use.lim.MaxInFlight)}
	}
	mid, limit, name := use.mv.MembershipID, use.lim.MemberBudgetMicro, "your monthly budget"
	if level == store.ProvidedRouteRefusedTenantCeiling {
		mid, name = "", "the tenant's monthly ceiling"
		if use.lim.TenantCeilingMicro != nil {
			limit = *use.lim.TenantCeilingMicro
		}
	}
	p, _ := g.d.Store.GetProvidedRoutePeriod(ctx, use.mv.TenantID, mid, providedroutes.SourceDeployment, month)
	used := p.SpentMicro + p.ReservedMicro
	// "spend limit" is what the Agent's claude adapter reads in the turn's error to open a spend
	// limit episode (one that waiting does not clear before the reset): keep the words.
	msg := fmt.Sprintf("you have hit %s, the spend limit of the deployment's Bedrock route: ≈%s of %s used in %s; it resets on %s",
		name, usdNear(used, limit), usdNear(limit, used), month, reset.Format("2006-01-02 15:04 UTC"))
	if used == 0 && limit > 0 {
		// Nothing spent: the budget is smaller than one call's reservation (one saved before the
		// source's models or the price table raised the floor, #3683), not used up.
		msg = fmt.Sprintf("%s (%s) is too small for the next call: it is the spend limit of the deployment's Bedrock route, nothing has been used in %s, "+
			"and a call is admitted only when its whole reservation fits; ask your tenant administrator to raise it", name, usd(limit), month)
	}
	if next > 0 {
		msg += fmt.Sprintf(" (the next call could cost up to %s)", usd(next))
	}
	return &routeRefusal{status: http.StatusServiceUnavailable, code: "route_budget_exhausted", msg: msg, header: map[string]string{
		"X-Af-Route-Budget-Level":       level,
		"X-Af-Route-Budget-Used-Micro":  strconv.FormatInt(used, 10),
		"X-Af-Route-Budget-Limit-Micro": strconv.FormatInt(limit, 10),
		"X-Af-Route-Budget-Reset":       reset.Format(time.RFC3339),
	}}
}

func usd(micro int64) string { return fmt.Sprintf("$%.2f", float64(micro)/1e6) }

// usdNear is usd, except that it prints every micro-USD digit when other would round to the same
// cents: a message that compares two amounts must not read "$0.44 is below $0.44".
func usdNear(micro, other int64) string {
	if micro != other && usd(micro) == usd(other) {
		return fmt.Sprintf("$%.6f", float64(micro)/1e6)
	}
	return usd(micro)
}

// --- token -------------------------------------------------------------------

func (g *Gateway) liveMembership(ctx context.Context, mid string) (store.MembershipView, *routeRefusal) {
	mv, ok, err := g.d.Store.GetMembershipByID(ctx, mid)
	if err != nil {
		return mv, &routeRefusal{status: http.StatusServiceUnavailable, code: "route_unavailable", msg: "the membership could not be read"}
	}
	if !ok {
		return mv, &routeRefusal{status: http.StatusUnauthorized, code: "route_membership_inactive", msg: "your membership is not active"}
	}
	return mv, nil
}

// Issue mints a route token for the Agent, on the Agent's own issuing token, after the same
// grant, source and budget checks every call repeats (ADR 0084 decision 8's gate 2).
func (g *Gateway) Issue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mid, epoch, ok := g.d.VerifyIssuer(ctx, routeBearer(r))
	if !ok {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusUnauthorized, code: "route_token_invalid", msg: "invalid route issuing token"})
		return
	}
	mv, ref := g.liveMembership(ctx, mid)
	if ref != nil {
		writeRouteRefusal(w, ref)
		return
	}
	var req struct {
		Session     string `json:"session"`
		Incarnation string `json:"incarnation"`
		Route       string `json:"route"`
		Binding     string `json:"binding"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusBadRequest, code: "route_bad_request", msg: "invalid body"})
		return
	}
	for _, f := range []string{req.Session, req.Incarnation, req.Route, req.Binding} {
		if strings.ContainsRune(f, 0) || len(f) > 512 {
			writeRouteRefusal(w, &routeRefusal{status: http.StatusBadRequest, code: "route_bad_request", msg: "invalid body"})
			return
		}
	}
	if strings.TrimSpace(req.Session) == "" {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusBadRequest, code: "route_bad_request", msg: "a session name is required"})
		return
	}
	if _, ref := g.authorizeUse(ctx, mv, req.Route, req.Binding); ref != nil {
		writeRouteRefusal(w, ref)
		return
	}
	exp := g.now().Add(TokenTTL)
	// Under the epoch the issuing token verified with, never one read again later.
	tok := MintToken(g.d.KeyAt(epoch), Claims{MembershipID: mv.MembershipID, Session: req.Session,
		Incarnation: req.Incarnation, Route: req.Route, Binding: req.Binding, Expires: exp})
	writeJSON(w, http.StatusOK, IssuedToken{Token: tok, ExpiresAt: exp.UTC().Format(time.RFC3339), BasePath: Prefix, CallHeader: CallHeader})
}

// verify checks a route token under its membership's current epoch.
func (g *Gateway) verify(ctx context.Context, tok string) (Claims, bool) {
	mid, ok := tokenMembership(tok)
	if !ok {
		return Claims{}, false
	}
	epoch, ok := g.d.BridgeEpoch(ctx, mid)
	if !ok {
		return Claims{}, false
	}
	c, ok := VerifyToken(g.d.KeyAt(epoch), tok, g.now())
	if !ok || c.MembershipID != mid {
		return Claims{}, false
	}
	return c, true
}

// IssuedToken is POST /internal/route/token's answer.
type IssuedToken struct {
	Token      string `json:"token"`
	ExpiresAt  string `json:"expires_at"`
	BasePath   string `json:"base_path"`
	CallHeader string `json:"call_header"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- the call ----------------------------------------------------------------

func (g *Gateway) Serve(w http.ResponseWriter, r *http.Request) {
	if g.draining.Load() {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusServiceUnavailable, code: "route_draining",
			msg: "the Control Plane is restarting; send the message again in a moment"})
		return
	}
	ctx := r.Context()
	claims, ok := g.verify(ctx, routeBearer(r))
	if !ok {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusUnauthorized, code: "route_token_invalid",
			msg: "the route token is unknown, expired or revoked; restart the session"})
		return
	}
	mv, ref := g.liveMembership(ctx, claims.MembershipID)
	if ref != nil {
		writeRouteRefusal(w, ref)
		return
	}
	// A removal of the membership cuts this call, streams in flight included (member_removal.go).
	ctx, untrack := g.d.Track(ctx, mv.MembershipID)
	defer untrack()
	use, ref := g.authorizeUse(ctx, mv, claims.Route, claims.Binding)
	if ref != nil {
		writeRouteRefusal(w, ref)
		return
	}
	model, op, cref := providedroutes.ParseCall(r.Method, strings.TrimPrefix(r.URL.EscapedPath(), Prefix))
	if cref != nil {
		writeRouteRefusal(w, fromContract(cref))
		return
	}
	// None of the four operations takes a query; a URL, a Region or a role in one is refused.
	if r.URL.RawQuery != "" {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusBadRequest, code: providedroutes.CodeRequestRefused,
			msg: "a provided route takes no query string"})
		return
	}
	if cref := providedroutes.CheckModel(use.src.AllowList, model); cref != nil {
		writeRouteRefusal(w, fromContract(cref))
		return
	}
	hdr, cref := providedroutes.UpstreamHeaders(r.Header)
	if cref != nil {
		writeRouteRefusal(w, fromContract(cref))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, routeMaxBody))
	if err != nil {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusRequestEntityTooLarge, code: "route_request_too_large",
			msg: "the request is larger than the route forwards"})
		return
	}
	outCap, cref := providedroutes.OutputCap(op, body)
	if cref != nil {
		writeRouteRefusal(w, fromContract(cref))
		return
	}
	row, ok := routeprice.Lookup(model)
	if !ok {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusForbidden, code: providedroutes.CodeModelUnpriced,
			msg: "model " + model + " has no price on this route, so it is not called"})
		return
	}
	reserve, err := row.UpperBoundMicroUSD(outCap)
	if err != nil {
		writeRouteRefusal(w, &routeRefusal{status: http.StatusBadRequest, code: providedroutes.CodeOutputCap, msg: err.Error()})
		return
	}
	policy, err := providedroutes.SessionPolicy(use.src.RoleARN, use.region, use.src.AllowList)
	if err != nil {
		log.Printf("provided routes: session policy for %s: %v", use.src.ID, err)
		writeRouteRefusal(w, &routeRefusal{status: http.StatusServiceUnavailable, code: "route_unavailable",
			msg: "the route's permissions could not be built; the administrator has to shorten its model list"})
		return
	}

	callID, ref := g.admit(ctx, use, model, op, reserve)
	if ref != nil {
		writeRouteRefusal(w, ref)
		return
	}
	// From here the call is reserved and settles exactly once, whatever happens below.
	final := reserve
	defer func() { g.settle(callID, reserve, final) }()

	creds, err := g.credentials(ctx, use, policy)
	if err != nil {
		log.Printf("provided routes: credentials for %s / %s: %v", use.src.ID, mv.MembershipID, err)
		// Nothing was sent upstream, so nothing can have been billed.
		final = 0
		writeRouteRefusal(w, &routeRefusal{status: http.StatusServiceUnavailable, code: "route_credentials_unavailable",
			msg: "the Control Plane could not take the route's role"})
		return
	}
	uctx, cancel := context.WithTimeout(ctx, g.cfg.Deadline)
	defer cancel()
	path, raw := providedroutes.UpstreamPath(model, op)
	up, err := http.NewRequestWithContext(uctx, http.MethodPost, g.endpoint(use.region), nil)
	if err != nil {
		final = 0
		writeRouteRefusal(w, &routeRefusal{status: http.StatusBadGateway, code: "route_upstream_unavailable", msg: "could not build the upstream request"})
		return
	}
	up.URL.Path, up.URL.RawPath = path, raw
	up.Body, up.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	up.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	up.Header = hdr
	up.Header.Set("User-Agent", "agent-fleet-cp-route-gateway")
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(uctx, creds, up, hex.EncodeToString(sum[:]), "bedrock", use.region, g.now()); err != nil {
		final = 0
		writeRouteRefusal(w, &routeRefusal{status: http.StatusBadGateway, code: "route_upstream_unavailable", msg: "could not sign the upstream request"})
		return
	}
	resp, err := g.client.Do(up)
	if err != nil {
		// The request may have reached Bedrock: the reservation stands.
		log.Printf("provided routes: upstream %s: %v", use.region, err)
		writeRouteRefusal(w, &routeRefusal{status: http.StatusBadGateway, code: "route_upstream_unavailable",
			msg: "Amazon Bedrock could not be reached from the Control Plane"})
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if routeAnswerHeader(k) {
			w.Header()[k] = v
		}
	}
	w.Header().Set(CallHeader, callID)
	w.WriteHeader(resp.StatusCode)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	term := providedroutes.NewTerminal(op, resp.StatusCode)
	clean := routeStreamCopy(w, resp.Body, term) == nil
	if u, ok := term.Final(clean); ok {
		final = row.PriceMicroUSD(u)
		if final > reserve {
			// Full-fit admission bounds every price; above it is a defect of the table.
			log.Printf("provided routes: DEFECT call %s settled at %d micro-USD above its reservation %d (model %s, price table %s)",
				callID, final, reserve, model, routeprice.Version())
		}
	} else if g.zeroCleanRefusal && term.CleanRefusal(clean) {
		final = 0
	}
}

// routeAnswerHeader is an upstream response header that reaches the caller. None carries a
// credential; everything else is dropped.
func routeAnswerHeader(k string) bool {
	switch l := strings.ToLower(k); {
	case l == "content-type", l == "content-length", l == "retry-after", l == "x-amzn-requestid", l == "x-amzn-errortype":
		return true
	case strings.HasPrefix(l, "x-amzn-bedrock-"):
		return true
	}
	return false
}

// routeStreamCopy forwards the body with a flush per read, as the engine gateway does, and lets
// the observer see each chunk after it was written.
func routeStreamCopy(w http.ResponseWriter, body io.Reader, obs io.Writer) error {
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if fl != nil {
				fl.Flush()
			}
			_, _ = obs.Write(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// admit is D4's admission: durable before anything is dispatched. The call is counted in this
// process first, so a drain that starts meanwhile waits for it or settles it.
func (g *Gateway) admit(ctx context.Context, use routeUse, model, op string, reserve int64) (string, *routeRefusal) {
	callID := g.newCall()
	g.callMu.Lock()
	g.calls[callID] = reserve
	g.callMu.Unlock()
	release := func() {
		g.callMu.Lock()
		delete(g.calls, callID)
		g.callMu.Unlock()
	}
	if g.draining.Load() {
		release()
		return "", &routeRefusal{status: http.StatusServiceUnavailable, code: "route_draining",
			msg: "the Control Plane is restarting; send the message again in a moment"}
	}
	adm, err := g.d.Store.AdmitProvidedRouteCall(context.WithoutCancel(ctx), store.ProvidedRouteCall{
		CallID: callID, MembershipID: use.mv.MembershipID, TenantID: use.mv.TenantID, Source: use.src.Source,
		Binding: use.src.Binding(), Model: model, API: op, PriceVersion: routeprice.Version(), ReservedMicro: reserve,
		AdmittedAt: g.now().UTC().Format(time.RFC3339Nano), CPInstance: g.instance,
	}, use.lim)
	if err != nil {
		release()
		log.Printf("provided routes: admission of %s: %v", callID, err)
		return "", &routeRefusal{status: http.StatusServiceUnavailable, code: "route_unavailable", msg: "the route's budget could not be checked"}
	}
	if !adm.Admitted {
		release()
		return "", g.budgetRefusal(ctx, use, adm.Refused, reserve)
	}
	// A drain that began while the row was being written may have taken its snapshot before the
	// row existed, and its settlement then matched nothing. The call has not been dispatched, so
	// it is settled here at zero; without this the row waits for the orphan sweep (deadline+1m).
	if g.draining.Load() {
		g.settle(callID, reserve, 0)
		return "", &routeRefusal{status: http.StatusServiceUnavailable, code: "route_draining",
			msg: "the Control Plane is restarting; send the message again in a moment"}
	}
	return callID, nil
}

// settle replaces the reservation by amount exactly once (the store's condition) and releases the
// call in this process. A settlement the store cannot take is left to the orphan sweep, which
// settles at the reservation: never less than it might have cost.
func (g *Gateway) settle(callID string, reserve, amount int64) {
	defer func() {
		g.callMu.Lock()
		delete(g.calls, callID)
		g.callMu.Unlock()
	}()
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := g.d.Store.SettleProvidedRouteCall(ctx, callID, amount)
		cancel()
		if err == nil {
			return
		}
		log.Printf("provided routes: settling %s (attempt %d): %v", callID, attempt+1, err)
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}
	log.Printf("provided routes: call %s left in flight (reservation %d); the orphan sweep settles it", callID, reserve)
}

// --- credentials ---------------------------------------------------------------

func routeCredKey(use routeUse) string {
	return strings.Join([]string{use.src.ID, use.src.Fingerprint, strconv.Itoa(use.src.PolicyVersion), use.mv.MembershipID}, "|")
}

// credentials are the membership's session on the route role, from memory until routeCredReserve
// before they expire (D5: one STS call per member per hour, not per request). The key carries the
// source, its fingerprint and its policy version, so an edited payer never reads the old payer's
// session, and a model added to the list is reachable at the next call.
func (g *Gateway) credentials(ctx context.Context, use routeUse, policy string) (aws.Credentials, error) {
	key := routeCredKey(use)
	now := g.now()
	g.credMu.Lock()
	if e, ok := g.creds[key]; ok && now.Before(e.usable) {
		g.credMu.Unlock()
		return e.c, nil
	}
	delete(g.creds, key)
	fl, running := g.flight[key]
	if !running {
		fl = &routeCredFlight{done: make(chan struct{})}
		g.flight[key] = fl
	}
	g.credMu.Unlock()
	if running {
		select {
		case <-fl.done:
			return fl.c, fl.err
		case <-ctx.Done():
			return aws.Credentials{}, ctx.Err()
		}
	}
	var c aws.Credentials
	err := errors.New("credential resolution failed")
	defer func() {
		g.credMu.Lock()
		if err == nil && c.CanExpire && now.Before(c.Expires.Add(-routeCredReserve)) {
			g.creds[key] = cachedRouteCreds{c: c, usable: c.Expires.Add(-routeCredReserve)}
		}
		delete(g.flight, key)
		fl.c, fl.err = c, err
		g.credMu.Unlock()
		close(fl.done)
	}()
	tags := map[string]string{"af-membership": use.mv.MembershipID}
	// The slug is omitted, never empty, when unknown (ADR 0048 decision 3).
	if use.mv.TenantSlug != "" {
		tags["af-tenant"] = use.mv.TenantSlug
	}
	c, err = g.assumer.Assume(context.WithoutCancel(ctx), AssumeInput{RoleARN: use.src.RoleARN, ExternalID: use.src.ExternalID,
		Region: use.region, SessionName: use.mv.MembershipID, Tags: tags, Policy: policy})
	return c, err
}

// --- sweeps and shutdown -----------------------------------------------------

func (g *Gateway) sweepLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Sweep(ctx)
		}
	}
}

// Sweep drops expired credentials and settles orphans: an in-flight row that no call of this
// process holds, admitted longer ago than the deadline and a grace, settles at its reservation
// (D4), because Bedrock may still have run a call whose CP died. Any CP may settle any orphan;
// the store's condition makes the first one win.
func (g *Gateway) Sweep(ctx context.Context) (settled int) {
	now := g.now()
	g.credMu.Lock()
	for k, e := range g.creds {
		if !now.Before(e.usable) {
			delete(g.creds, k)
		}
	}
	g.credMu.Unlock()
	rows, err := g.d.Store.ListInFlightProvidedRouteCalls(ctx, 0)
	if err != nil {
		log.Printf("provided routes: orphan sweep: %v", err)
		return 0
	}
	cutoff := now.Add(-g.cfg.Deadline - routeOrphanGrace)
	for _, c := range rows {
		g.callMu.Lock()
		_, mine := g.calls[c.CallID]
		g.callMu.Unlock()
		at, err := time.Parse(time.RFC3339Nano, c.AdmittedAt)
		if mine || err != nil || !at.Before(cutoff) {
			continue
		}
		if ok, err := g.d.Store.SettleProvidedRouteCall(ctx, c.CallID, c.ReservedMicro); err != nil {
			log.Printf("provided routes: settling orphan %s: %v", c.CallID, err)
		} else if ok {
			settled++
			log.Printf("provided routes: orphan %s (CP %s, admitted %s) settled at its reservation %d", c.CallID, c.CPInstance, c.AdmittedAt, c.ReservedMicro)
		}
	}
	return settled
}

// Drain stops admitting, lets admitted calls finish for up to Config.Drain, then settles what is
// still running at its reservation before the process exits (D2, "CP capacity").
func (g *Gateway) Drain() {
	g.draining.Store(true)
	for end := time.Now().Add(g.cfg.Drain); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		g.callMu.Lock()
		n := len(g.calls)
		g.callMu.Unlock()
		if n == 0 {
			return
		}
	}
	g.callMu.Lock()
	left := make(map[string]int64, len(g.calls))
	for id, r := range g.calls {
		left[id] = r
	}
	g.callMu.Unlock()
	for id, r := range left {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := g.d.Store.SettleProvidedRouteCall(ctx, id, r); err != nil {
			log.Printf("provided routes: drain: settling %s: %v", id, err)
		}
		cancel()
	}
	log.Printf("provided routes: drained with %d calls cut and settled at their reservation", len(left))
}
