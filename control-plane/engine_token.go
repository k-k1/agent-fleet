package main

// engine_token.go — the credential a Workspace presents to /engine/* (ADR 0071 decision 4,
// open question 2).
//
// Two tokens, and the split is the whole point.
//
//   - The ISSUING token is per-membership, deterministic and injected into the Workspace's
//     environment at container start, exactly like AF_MEMO_TOKEN and AF_SCHEDULE_TOKEN
//     (memo_bridge.go). Only the Agent ever sees it, and all it can do is ask for the
//     second kind.
//   - The SESSION token is what actually opens the engine: minted per session, expiring,
//     and good for nothing but `engine:<key>`. It is the value that ends up in
//     opencode.json's `apiKey: "{env:AF_ENGINE_TOKEN}"`, which means a model running in that
//     session can read it. Putting the git/MCP PAT there instead — the other candidate in
//     open question 2 — would hand a leaked value everything that PAT can do, and would not
//     even answer the question the usage ledger asks, which is cut by SESSION.
//
// Both carry an HMAC tag over their own fields and are verified without a lookup; the
// membership is then resolved live, so a revoked member is refused even inside a token's
// lifetime. The shape mirrors the memo token deliberately — one more format to review is
// worth less than one more format to get subtly wrong.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

func engineSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-engine-token-sign/v1"))
	return mac.Sum(nil)
}

// mintEngineIssueToken returns the per-membership issuing token. Deterministic, so
// re-injecting it on every container start is idempotent.
func mintEngineIssueToken(signKey []byte, membershipID string) string {
	body := base64.RawURLEncoding.EncodeToString([]byte(membershipID))
	return "afei_" + body + "." + engineTag(signKey, "issue", membershipID)
}

func verifyEngineIssueToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afei_")
	if !hasPrefix {
		return "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", false
	}
	idRaw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil || len(idRaw) == 0 {
		return "", false
	}
	mid := string(idRaw)
	if !hmac.Equal([]byte(body[dot+1:]), []byte(engineTag(signKey, "issue", mid))) {
		return "", false
	}
	return mid, true
}

// engineSessionTokenTTL is how long a minted session token stays valid. Long enough that a
// session working through the night does not lose the engine mid-answer, short enough that
// a value which has leaked out of a transcript is not still a key next week. It is NOT tied
// to the session's own lifetime: nothing revokes a token when a session ends, so the clock
// is the only bound there is.
const engineSessionTokenTTL = 24 * time.Hour

// mintEngineSessionToken returns a token bound to one membership, one session and one
// engine key, expiring at exp.
func mintEngineSessionToken(signKey []byte, membershipID, session, key string, exp time.Time) string {
	claims := engineClaimString(membershipID, session, key, exp.Unix())
	return "afe_" + base64.RawURLEncoding.EncodeToString([]byte(claims)) + "." + engineTag(signKey, "session", claims)
}

// engineSessionClaims is what a verified session token says about its bearer. Nothing here
// is trusted beyond the tag: tenant and role come from a live store lookup by the caller.
type engineSessionClaims struct {
	MembershipID string
	Session      string
	Key          string // the engine this token opens, and only this one
	Expires      time.Time
}

// verifyEngineSessionToken checks the tag and the expiry. An expired token is refused here
// rather than by the caller — "the tag was fine, somebody else should have checked the
// clock" is how an expiry becomes decorative.
func verifyEngineSessionToken(signKey []byte, token string, now time.Time) (engineSessionClaims, bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afe_")
	if !hasPrefix {
		return engineSessionClaims{}, false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return engineSessionClaims{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil {
		return engineSessionClaims{}, false
	}
	claims := string(raw)
	if !hmac.Equal([]byte(body[dot+1:]), []byte(engineTag(signKey, "session", claims))) {
		return engineSessionClaims{}, false
	}
	parts := strings.Split(claims, "\x00")
	if len(parts) != 4 {
		return engineSessionClaims{}, false
	}
	secs, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || secs <= 0 {
		return engineSessionClaims{}, false
	}
	exp := time.Unix(secs, 0)
	if !now.Before(exp) {
		return engineSessionClaims{}, false
	}
	return engineSessionClaims{MembershipID: parts[0], Session: parts[1], Key: parts[2], Expires: exp}, true
}

// engineClaimString joins the claims with NUL, which cannot occur in any of them, so no
// membership id and session name can be re-cut into a different pair.
func engineClaimString(membershipID, session, key string, exp int64) string {
	return strings.Join([]string{membershipID, session, key, strconv.FormatInt(exp, 10)}, "\x00")
}

// engineTag binds the tag to the token KIND as well as to its body, so an issuing token can
// never be replayed as a session token or the other way round.
func engineTag(signKey []byte, kind, body string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(kind))
	mac.Write([]byte{0})
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}
