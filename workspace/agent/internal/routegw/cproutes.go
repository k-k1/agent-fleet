package routegw

// The Agent's half of a provided route (ADR 0129 G3): the harness talks to this gateway exactly as
// on the member's own bedrock route, and the gateway forwards the call to the Control Plane's route
// gateway with a CP route token instead of signing it. The CP is the boundary: it checks the
// membership, the grants, the source, the contract and the budget on every call, so nothing here
// protects the payer. What the Agent keeps is ADR 0128's per-session binding (caller tree,
// incarnation, selection) and the session's model pin, which a provided route needs as much as
// the member's own.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cpurl"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/redirectpolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// RouteBedrockDeployment is the Bedrock route the deployment provides and pays for.
const RouteBedrockDeployment = "bedrock-deployment"

// isProvided reports whether route is one this gateway forwards to the CP.
func isProvided(route string) bool { return route == RouteBedrockDeployment }

// CPUsable is the CP's answer that a member may use a provided route now
// (POST /internal/route/check).
type CPUsable struct {
	Route          string   `json:"route"`
	Binding        string   `json:"binding"`
	Region         string   `json:"region"`
	Models         []string `json:"models"`
	RemainingMicro int64    `json:"remaining_micro"`
	Reset          string   `json:"reset"`
}

// CPToken is a CP route token (POST /internal/route/token).
type CPToken struct {
	Token      string `json:"token"`
	ExpiresAt  string `json:"expires_at"`
	BasePath   string `json:"base_path"`
	CallHeader string `json:"call_header"`
}

func (t CPToken) expires() time.Time {
	at, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil {
		return time.Time{}
	}
	return at
}

// CPRoutes is the Control Plane as the gateway asks it about provided routes. The real one is
// cpRoutes; a test stands in for it.
type CPRoutes interface {
	Check(ctx context.Context, route string) (CPUsable, *refusal)
	Mint(ctx context.Context, session, incarnation, route, binding string) (CPToken, *refusal)
	// Base is where forwarded calls go: AF_CP_INTERNAL_URL where the operator set one, else the
	// public base (ADR 0129 D2, "Latency"). "" when the Agent knows no CP.
	Base() string
}

// cpRoutes calls the CP on the Workspace's route issuing credential (AF_ROUTE_ISSUE_TOKEN), which
// the CP injects only while it serves provided routes.
type cpRoutes struct{ client *http.Client }

func newCPRoutes() CPRoutes { return cpRoutes{client: redirectpolicy.Client(15 * time.Second)} }

func (c cpRoutes) Base() string { return cpurl.Request() }

func (c cpRoutes) Check(ctx context.Context, route string) (CPUsable, *refusal) {
	var out CPUsable
	ref := c.post(ctx, "/internal/route/check", map[string]string{"route": route}, &out)
	return out, ref
}

func (c cpRoutes) Mint(ctx context.Context, session, incarnation, route, binding string) (CPToken, *refusal) {
	var out CPToken
	ref := c.post(ctx, "/internal/route/token", map[string]string{"session": session, "incarnation": incarnation,
		"route": route, "binding": binding}, &out)
	if ref == nil && (out.Token == "" || out.expires().IsZero()) {
		ref = &refusal{http.StatusBadGateway, routes.CodeRouteUnavailable, "the Control Plane answered no route token"}
	}
	return out, ref
}

// cpWireRefusal is the body of a CP refusal.
type cpWireRefusal struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func (c cpRoutes) post(ctx context.Context, path string, in, out any) *refusal {
	base, tok := c.Base(), strings.TrimSpace(os.Getenv("AF_ROUTE_ISSUE_TOKEN"))
	if base == "" || tok == "" {
		return notOffered()
	}
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(b))
	if err != nil {
		return cpUnreachable()
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return cpUnreachable()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		// The CP registers the routes only with its switch on.
		return notOffered()
	}
	if resp.StatusCode != http.StatusOK {
		var w cpWireRefusal
		if json.Unmarshal(body, &w) != nil || w.Message == "" {
			return &refusal{http.StatusBadGateway, routes.CodeRouteUnavailable, "the Control Plane refused the route (" + resp.Status + ")"}
		}
		return &refusal{resp.StatusCode, w.Code, w.Message}
	}
	if json.Unmarshal(body, out) != nil {
		return &refusal{http.StatusBadGateway, routes.CodeRouteUnavailable, "the Control Plane's answer could not be read"}
	}
	return nil
}

func notOffered() *refusal {
	return &refusal{http.StatusConflict, routes.CodeRouteUnavailable,
		"this Workspace's Control Plane does not offer the deployment's Bedrock route (its provided routes are off, or the Workspace was started before they were turned on: Stop → Start)"}
}

func cpUnreachable() *refusal {
	return &refusal{http.StatusServiceUnavailable, routes.CodeRouteUnavailable, "the Control Plane could not be reached to check the route"}
}
