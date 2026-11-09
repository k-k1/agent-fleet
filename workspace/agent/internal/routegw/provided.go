package routegw

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// cpCallHeader is the CP's call id on a forwarded answer; cpCallIDRe what one may look like
// before it becomes a ledger row's id.
const cpCallHeader = "X-Af-Route-Call"

var cpCallIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// bindingRe is a provided selection's connection: the CP's "<source id>#<16 hex fingerprint>".
var bindingRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}#[0-9a-f]{16}$`)

const (
	// checkFresh is how long the CP's answer serves the launch dialog's offer. A create, a bind
	// and a launch always ask again.
	checkFresh = 30 * time.Second
	// cpRenewBefore is how long before its expiry a running session's CP token is replaced by the
	// sweep. Ten-minute tokens and a one-minute sweep leave several tries.
	cpRenewBefore = 5 * time.Minute
	// cpCallTimeout bounds one check or mint, which a launch or a create waits on.
	cpCallTimeout = 10 * time.Second
)

type provState struct {
	mu     sync.Mutex
	toks   map[string]heldCPToken // session → its CP token
	checks map[string]cachedCheck // route → the CP's last answer
}

type heldCPToken struct {
	tok         CPToken
	incarnation string
	binding     string
}

type cachedCheck struct {
	u   CPUsable
	ref *refusal
	at  time.Time
}

func toRouteRefusal(r *refusal) *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: r.status, Code: r.code, Message: r.msg}
}

// check asks the CP whether the member may use route now; within checkFresh a cached answer
// serves unless fresh.
func (g *Gateway) check(route string, fresh bool) (CPUsable, *refusal) {
	if g.d.CP == nil {
		return CPUsable{}, notOffered()
	}
	now := g.d.Now()
	if !fresh {
		g.prov.mu.Lock()
		c, ok := g.prov.checks[route]
		g.prov.mu.Unlock()
		if ok && now.Sub(c.at) < checkFresh {
			return c.u, c.ref
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cpCallTimeout)
	defer cancel()
	u, ref := g.d.CP.Check(ctx, route)
	if ref == nil && (u.Binding == "" || !bindingRe.MatchString(u.Binding)) {
		ref = &refusal{http.StatusBadGateway, routes.CodeRouteUnavailable, "the Control Plane answered no usable binding for the route"}
	}
	g.prov.mu.Lock()
	g.prov.checks[route] = cachedCheck{u: u, ref: ref, at: now}
	g.prov.mu.Unlock()
	return u, ref
}

// Offered is agents.RouteOffering: whether the launch dialog lists route now (ADR 0084 decision
// 8's first place, with the CP as the gate). A route the CP refuses is not offered.
func (g *Gateway) Offered(route string) bool {
	if !isProvided(route) {
		return true
	}
	_, ref := g.check(route, false)
	return ref == nil
}

// bound checks a selection's connection against the CP's current binding: empty takes it, a
// different one is a source the CP has re-pointed, and the session never follows it.
func bound(sel agents.RouteSelection, u CPUsable) *refusal {
	if sel.Connection != "" && sel.Connection != u.Binding {
		return &refusal{http.StatusConflict, "route_connection_changed",
			"the deployment's Bedrock route was re-pointed (role or Region) since this session was started on it; a session never moves to another payer. Start a new session"}
	}
	return nil
}

func (g *Gateway) bindProvided(sel agents.RouteSelection) (agents.RouteSelection, *agents.RouteRefusal) {
	u, ref := g.check(sel.Route, true)
	if ref == nil {
		ref = bound(sel, u)
	}
	if ref != nil {
		return sel, toRouteRefusal(ref)
	}
	sel.Connection = u.Binding
	return sel, nil
}

// checkProvided is CheckRoute for a provided route: the CP's check, and the model on the
// source's allow-list, since the CP refuses every call that names another.
func (g *Gateway) checkProvided(sel agents.RouteSelection, model string) *agents.RouteRefusal {
	u, ref := g.check(sel.Route, true)
	if ref == nil {
		ref = bound(sel, u)
	}
	if ref != nil {
		return toRouteRefusal(ref)
	}
	if model != "" && !slices.Contains(u.Models, model) {
		return &agents.RouteRefusal{Status: http.StatusBadRequest, Code: routes.CodeRouteModel, Message: fmt.Sprintf(
			"the deployment's Bedrock route serves %s; %s is not on its list", strings.Join(u.Models, ", "), model)}
	}
	return nil
}

func (g *Gateway) issueProvided(m session.Meta, transport string) (Issued, *agents.RouteRefusal) {
	binding := selConnection(m)
	if !bindingRe.MatchString(binding) {
		return Issued{}, &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteUnavailable,
			Message: "this session's route is not bound to the deployment's Bedrock route as it was; start a new session on it"}
	}
	u, ref := g.check(m.Route.Route, true)
	if ref == nil {
		ref = bound(*m.Route, u)
	}
	if ref != nil {
		return Issued{}, toRouteRefusal(ref)
	}
	inc := awsx.Incarnation(m)
	ctx, cancel := context.WithTimeout(context.Background(), cpCallTimeout)
	defer cancel()
	tok, ref := g.d.CP.Mint(ctx, m.Name, inc, m.Route.Route, binding)
	if ref != nil {
		return Issued{}, toRouteRefusal(ref)
	}
	sock, base, listening := "", SocketBaseURL, false
	switch {
	case transport == transportTCP && g.d.Loopback != nil:
		base = g.d.Loopback()
		listening = base != ""
	case transport != transportTCP && g.d.Socket != nil:
		sock = g.d.Socket()
		listening = sock != ""
	}
	if !listening {
		return Issued{}, &agents.RouteRefusal{Status: http.StatusServiceUnavailable, Code: routes.CodeRouteUnavailable,
			Message: "the route gateway is not listening"}
	}
	source, _, _ := strings.Cut(binding, "#")
	b := Binding{Session: m.Name, Incarnation: inc, Route: m.Route.Route, Connection: binding, ProfileID: source,
		Region: u.Region, Model: pinnedModel(m), Transport: transport}
	local, exp, err := g.d.Registry.Mint(b, TokenTTL)
	if err != nil {
		return Issued{}, &agents.RouteRefusal{Status: http.StatusInternalServerError, Code: routes.CodeRouteUnavailable, Message: err.Error()}
	}
	g.prov.mu.Lock()
	g.prov.toks[m.Name] = heldCPToken{tok: tok, incarnation: inc, binding: binding}
	g.prov.mu.Unlock()
	return Issued{Socket: sock, BaseURL: base, Token: local, Region: u.Region, Expires: exp}, nil
}

// cpTokenFor is the CP token a call of b forwards with: the held one while it has time left,
// otherwise a new one (a sweep that could not renew it, or an Agent that was asleep).
func (g *Gateway) cpTokenFor(b Binding) (CPToken, *refusal) {
	now := g.d.Now()
	g.prov.mu.Lock()
	h, ok := g.prov.toks[b.Session]
	g.prov.mu.Unlock()
	if ok && h.incarnation == b.Incarnation && h.binding == b.Connection && now.Add(time.Minute).Before(h.tok.expires()) {
		return h.tok, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cpCallTimeout)
	defer cancel()
	tok, ref := g.d.CP.Mint(ctx, b.Session, b.Incarnation, b.Route, b.Connection)
	if ref != nil {
		return CPToken{}, ref
	}
	g.prov.mu.Lock()
	g.prov.toks[b.Session] = heldCPToken{tok: tok, incarnation: b.Incarnation, binding: b.Connection}
	g.prov.mu.Unlock()
	return tok, nil
}

// renewProvided is the sweep's half for provided routes: a CP token whose session no longer holds
// a gateway token is dropped, and one whose session is running is replaced before it expires.
func (g *Gateway) renewProvided(alive func(Binding) bool) {
	live := map[string]Binding{}
	for _, b := range g.d.Registry.Bindings() {
		if isProvided(b.Route) {
			live[b.Session] = b
		}
	}
	now := g.d.Now()
	var due []Binding
	g.prov.mu.Lock()
	for name, h := range g.prov.toks {
		b, ok := live[name]
		if !ok || b.Incarnation != h.incarnation || b.Connection != h.binding {
			delete(g.prov.toks, name)
			continue
		}
		if now.Add(cpRenewBefore).After(h.tok.expires()) && alive(b) {
			due = append(due, b)
		}
	}
	for route, c := range g.prov.checks {
		if now.Sub(c.at) > time.Hour {
			delete(g.prov.checks, route)
		}
	}
	g.prov.mu.Unlock()
	// Outside the lock: each is a CP round trip.
	for _, b := range due {
		ctx, cancel := context.WithTimeout(context.Background(), cpCallTimeout)
		tok, ref := g.d.CP.Mint(ctx, b.Session, b.Incarnation, b.Route, b.Connection)
		cancel()
		if ref != nil {
			// The held token runs out; the next call then mints or shows the CP's refusal.
			log.Printf("route gateway: renewing the CP route token of %s refused (%s)", b.Session, ref.code)
			continue
		}
		g.prov.mu.Lock()
		if h, ok := g.prov.toks[b.Session]; ok && h.incarnation == b.Incarnation && h.binding == b.Connection {
			g.prov.toks[b.Session] = heldCPToken{tok: tok, incarnation: b.Incarnation, binding: b.Connection}
		}
		g.prov.mu.Unlock()
	}
}

// forward sends one checked call to the CP's route gateway and streams the answer back, as the
// member route streams Bedrock's. The CP's refusals already carry the status, x-should-retry and
// message the harness reacts to (a used-up budget is a 503 naming the spend limit, which the
// claude adapter reads as a spend-limit episode), so they pass as they are.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, c *call, modelID, op string, body []byte) {
	base := g.d.CP.Base()
	if base == "" {
		writeRefusal(w, notOffered())
		return
	}
	tok, ref := g.cpTokenFor(c.b)
	if ref != nil {
		log.Printf("route gateway: refused (%s)", ref.code)
		writeRefusal(w, ref)
		return
	}
	path := tok.BasePath
	if path == "" {
		path = "/route/v1"
	}
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base+path+r.URL.EscapedPath(), bytes.NewReader(body))
	if err != nil {
		writeRefusal(w, &refusal{http.StatusBadGateway, "route_upstream_unavailable", "could not build the request to the Control Plane"})
		return
	}
	up.Header.Set("Content-Type", firstNonEmpty(r.Header.Get("Content-Type"), "application/json"))
	if a := r.Header.Get("Accept"); a != "" {
		up.Header.Set("Accept", a)
	}
	// x-amzn-bedrock-* goes on to the CP, which passes or refuses each by its own list, so a
	// Bedrock option it does not take is refused visibly rather than ignored. Every other header
	// and the query string are dropped here: none of the four operations takes one.
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amzn-bedrock-") {
			for _, one := range v {
				up.Header.Add(k, one)
			}
		}
	}
	up.Header.Set("User-Agent", "agent-fleet-route-gateway")
	up.Header.Set("Authorization", "Bearer "+tok.Token)
	start := g.d.Now()
	resp, err := g.d.Client.Do(up)
	if err != nil {
		log.Printf("route gateway: the Control Plane's route gateway is unreachable")
		writeRefusal(w, &refusal{http.StatusBadGateway, "route_upstream_unavailable", "the Control Plane's route gateway could not be reached from this Workspace"})
		return
	}
	defer resp.Body.Close()
	if lvl := resp.Header.Get("X-Af-Route-Budget-Level"); lvl != "" {
		log.Printf("route gateway: %s refused by the provided route's budget (%s)", c.b.Session, lvl)
	}
	for k, v := range resp.Header {
		l := strings.ToLower(k)
		if l == "content-type" || l == "content-length" || l == "retry-after" || l == "x-should-retry" ||
			strings.HasPrefix(l, "x-amzn-") || strings.HasPrefix(l, "x-amz-") {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	obs := newUsageObserver(op)
	copyErr := streamCopy(w, resp.Body, obs)

	// A ledger row is a call the CP admitted: it carries the CP's call id and a reservation. A
	// refusal of the CP's own (grant, budget, contract) was never admitted, reached no payer and
	// costs nothing, so it writes no row, as the member route's own refusals write none; the
	// CP's log and the harness's error are its record.
	if !cpCallIDRe.MatchString(resp.Header.Get(cpCallHeader)) {
		return
	}
	g.record(c, modelID, op, resp, obs, copyErr == nil && resp.StatusCode < 400, start)
}
