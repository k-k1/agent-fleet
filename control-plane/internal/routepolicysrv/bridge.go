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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

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
}

// MemberPolicyOf is the effective policy of a member of tenantID.
func MemberPolicyOf(ctx context.Context, st store.Store, tenantID string) (MemberPolicy, error) {
	v, err := TenantPolicy(ctx, st, tenantID)
	if err != nil {
		return MemberPolicy{}, err
	}
	out := MemberPolicy{Revision: v.Revision, Restricted: v.Restricted, CeilingRestricted: v.Ceiling.Rule.Restricted(), Allowed: []string{}}
	for _, r := range v.Routes {
		if r.Allowed {
			out.Allowed = append(out.Allowed, r.ID)
		}
	}
	return out, nil
}

// PolicyEnv is the AF_ROUTE_POLICY entry for a Workspace start, or nothing when it cannot be
// computed now: the Agent then pulls it, and until it holds one it judges by what it held before.
func PolicyEnv(ctx context.Context, st store.Store, tenantID string) []string {
	p, err := MemberPolicyOf(ctx, st, tenantID)
	if err != nil {
		return nil
	}
	b, err := json.Marshal(p)
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
