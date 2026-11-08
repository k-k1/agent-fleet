package routegateway

// The CP route token of ADR 0129 D2 (G3): what the Agent presents to
// the CP route gateway on a provided route's behalf.
//
// Two tokens, the engine pair's split (engine_token.go):
//
//   - The ISSUING token (afri_) is per membership, deterministic, injected into the Workspace at
//     start only when the CP runs with AF_PROVIDED_ROUTES=1, and good for nothing but buying the
//     second kind at POST /internal/route/token.
//   - The ROUTE token (afr_) is what the gateway accepts: bound to the membership, the session's
//     name and incarnation, the route and the bound source ("<source id>#<fingerprint>"), and
//     valid for ten minutes. The Agent renews it from its sweep while the session's launch pane
//     lives, as ADR 0128 P2b does for the socket token.
//
// The session name and incarnation are the Agent's assertions, which the member controls: the
// issuer proves the membership, not the session (D2, "Two kinds of revocation"). The CP boundary
// is therefore the member's grant and budget; the route and the bound source in the token are
// checked against the CP's own records on every call.
//
// Both are minted under the membership's bridge epoch (bridge_epoch.go), so a rotation ends them.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// TokenTTL is the route token's life (ADR 0129 D2: ten minutes, renewed).
const TokenTTL = 10 * time.Minute

// SignKey derives the route token family's key from the CP's token signing root.
func SignKey(root []byte) []byte {
	mac := hmac.New(sha256.New, root)
	mac.Write([]byte("af-provided-route-token-sign/v1"))
	return mac.Sum(nil)
}

// MintIssueToken is the per-membership issuing token. Deterministic, so re-injection on
// every start is idempotent.
func MintIssueToken(signKey []byte, membershipID string) string {
	return "afri_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + tokenTag(signKey, "issue", membershipID)
}

// VerifyIssueToken checks an issuing token's tag and returns the membership it names.
func VerifyIssueToken(signKey []byte, token string) (string, bool) {
	body, ok := strings.CutPrefix(strings.TrimSpace(token), "afri_")
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
	if !hmac.Equal([]byte(body[dot+1:]), []byte(tokenTag(signKey, "issue", mid))) {
		return "", false
	}
	return mid, true
}

// Claims is what a verified route token says. Nothing beyond the tag is trusted: the
// tenant, the grants, the budget and the source come from the store on every call.
type Claims struct {
	MembershipID string
	Session      string
	Incarnation  string
	Route        string
	Binding      string
	Expires      time.Time
}

const routeClaimFields = 6

func (c Claims) encode() string {
	return strings.Join([]string{c.MembershipID, c.Session, c.Incarnation, c.Route, c.Binding,
		strconv.FormatInt(c.Expires.Unix(), 10)}, "\x00")
}

// MintToken signs c. A claim holding NUL is refused by the caller before this.
func MintToken(signKey []byte, c Claims) string {
	s := c.encode()
	return "afr_" + base64.RawURLEncoding.EncodeToString([]byte(s)) + "." + tokenTag(signKey, "route", s)
}

// VerifyToken checks the tag and the expiry; an expired token is refused here, never left
// to a caller.
func VerifyToken(signKey []byte, token string, now time.Time) (Claims, bool) {
	s, ok := tokenClaims(token)
	if !ok {
		return Claims{}, false
	}
	body := strings.TrimPrefix(strings.TrimSpace(token), "afr_")
	if !hmac.Equal([]byte(body[strings.LastIndexByte(body, '.')+1:]), []byte(tokenTag(signKey, "route", s))) {
		return Claims{}, false
	}
	p := strings.Split(s, "\x00")
	if len(p) != routeClaimFields || p[0] == "" {
		return Claims{}, false
	}
	secs, err := strconv.ParseInt(p[5], 10, 64)
	if err != nil || secs <= 0 {
		return Claims{}, false
	}
	exp := time.Unix(secs, 0)
	if !now.Before(exp) {
		return Claims{}, false
	}
	return Claims{MembershipID: p[0], Session: p[1], Incarnation: p[2], Route: p[3], Binding: p[4], Expires: exp}, true
}

// tokenClaims decodes a route token's claim string without trusting it.
func tokenClaims(token string) (string, bool) {
	body, ok := strings.CutPrefix(strings.TrimSpace(token), "afr_")
	if !ok {
		return "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// tokenMembership reads the membership a route token names, so its tag is checked under
// that membership's epoch.
func tokenMembership(token string) (string, bool) {
	s, ok := tokenClaims(token)
	if !ok {
		return "", false
	}
	mid, _, _ := strings.Cut(s, "\x00")
	return mid, mid != ""
}

// tokenTag binds the tag to the token kind, so an issuing token is never a route token.
func tokenTag(signKey []byte, kind, body string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(kind))
	mac.Write([]byte{0})
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}
