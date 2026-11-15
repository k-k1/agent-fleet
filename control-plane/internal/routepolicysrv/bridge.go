package routepolicysrv

// The Agent's pull of the effective policy (ADR 0138 D12):
//
//	member's Agent ──(AF_ROUTE_POLICY_TOKEN)──▶ GET /internal/route-policy   (at least every 30 s)
//
// and the same answer in AF_ROUTE_POLICY at the Workspace's start. The Agent accepts an answer
// only with a revision higher than the one it holds, so the answer carries the revision of the
// snapshot it was computed from (TenantPolicy).

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// EnvToken and EnvPolicy are the Workspace's variables: the bridge credential and the effective
// policy at start.
const (
	EnvToken  = "AF_ROUTE_POLICY_TOKEN"
	EnvPolicy = "AF_ROUTE_POLICY"
)

// BridgePath is the Agent's pull.
const BridgePath = "/internal/route-policy"

// MemberPolicy is the effective policy as the Agent receives it. Allowed lists the catalogued
// route ids the policy allows; with Restricted false every route is allowed, catalogued or not,
// and with it true a route not listed is refused (an id the CP's catalogue does not know reads as
// unclassified, D1c). CeilingRestricted says the deployment has a ceiling, sent apart because the
// Agent's freshness rule treats it on its own (D12).
type MemberPolicy struct {
	Revision          int64    `json:"revision"`
	Restricted        bool     `json:"restricted"`
	CeilingRestricted bool     `json:"ceilingRestricted"`
	Allowed           []string `json:"allowed"`
	// IssuedAt is when the CP computed the answer (Unix seconds). The Agent counts a start
	// environment as confirmation only up to it, so an old environment re-read by a restarted
	// Agent confirms nothing.
	IssuedAt int64 `json:"issuedAt"`
}

// unknownPolicy is the start environment when the policy cannot be computed now while this CP's
// ceiling restricts: the Agent then holds a placeholder that allows nothing until it pulls an
// answer, rather than starting with no policy at all.
type unknownPolicy struct {
	Unknown bool `json:"unknown"`
	// ID names this marker, new for every start. The marker stays in the container's environment
	// for its whole life; by its id the Agent takes it once and never again after a restart, so a
	// live answer taken since is not undone by it, whatever the two hosts' clocks say.
	ID       string `json:"id"`
	IssuedAt int64  `json:"issuedAt"`
}

// newMarkerID is a fresh random id for an unknown marker. A var for tests.
var newMarkerID = func() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// MemberPolicyOf is the effective policy of a member of tenantID.
func MemberPolicyOf(ctx context.Context, st store.Store, tenantID string) (MemberPolicy, error) {
	v, err := TenantPolicy(ctx, st, tenantID)
	if err != nil {
		return MemberPolicy{}, err
	}
	out := MemberPolicy{Revision: v.Revision, Restricted: v.Restricted, CeilingRestricted: v.Ceiling.Rule.Restricted(), Allowed: []string{},
		IssuedAt: nowUnix()}
	for _, r := range v.Routes {
		if r.Allowed {
			out.Allowed = append(out.Allowed, r.ID)
		}
	}
	return out, nil
}

// nowUnix is the clock IssuedAt reads. A var for tests.
var nowUnix = func() int64 { return time.Now().Unix() }

// PolicyEnv is the AF_ROUTE_POLICY entry for a Workspace start. When the policy cannot be computed
// now, it is the unknown marker if this CP's ceiling restricts (the Agent then allows nothing until
// it pulls an answer), and nothing otherwise: the Agent then pulls, and until it holds an answer
// it judges by what it held before, which for a member nobody is known to restrict is nothing.
func PolicyEnv(ctx context.Context, st store.Store, tenantID string) []string {
	var v any
	p, err := MemberPolicyOf(ctx, st, tenantID)
	switch {
	case err == nil:
		v = p
	case currentCeiling().rule.Restricted():
		v = unknownPolicy{Unknown: true, ID: newMarkerID(), IssuedAt: nowUnix()}
	default:
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return []string{EnvPolicy + "=" + string(b)}
}

// SignKey derives the bridge token's key from the CP's signing root.
func SignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-route-policy-token-sign/v1"))
	return mac.Sum(nil)
}

// MintToken returns the deterministic bridge token for a membership: "afc_" + b64url(membership
// id) + "." + tag, like the other bridge tokens. Its own credential: a leak reads that member's
// effective policy and nothing else.
func MintToken(signKey []byte, membershipID string) string {
	return "afc_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + tokenTag(signKey, membershipID)
}

func tokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// VerifyToken checks the tag and returns the membership id.
func VerifyToken(signKey []byte, token string) (string, bool) {
	body, ok := strings.CutPrefix(strings.TrimSpace(token), "afc_")
	if !ok {
		return "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil || len(raw) == 0 {
		return "", false
	}
	mid := string(raw)
	if !hmac.Equal([]byte(body[dot+1:]), []byte(tokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// pull is GET /internal/route-policy: the caller's effective policy. The tenant comes from the
// token's membership, looked up live, so a removed member stops receiving answers.
func (a admin) pull(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := a.cp.VerifyBridge(r.Context(), SignKey(a.cp.SigningRoot()), tok, VerifyToken)
	if !ok {
		writeAPIErr(w, &APIError{http.StatusUnauthorized, "unauthenticated", "invalid route policy token"})
		return
	}
	mv, ok, err := a.cp.Store().GetMembershipByID(r.Context(), mid)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &APIError{http.StatusUnauthorized, "unauthenticated", "membership not active"})
		return
	}
	p, err := MemberPolicyOf(r.Context(), a.cp.Store(), mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}
