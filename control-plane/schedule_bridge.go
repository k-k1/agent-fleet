package main

// Schedule bridge — the internal (operator-token) face of scheduled execution
// (docs/log/38 + ADR0021 P3). It mirrors the memo bridge (memo_bridge.go): the Console
// reaches schedules through the session gateway, but an in-container フリート・
// オペレーター has NO gateway session, so it authenticates to /internal/schedules with a
// per-membership SCHEDULE TOKEN injected into its Workspace (AF_SCHEDULE_TOKEN). The
// token carries the membership id + a truncated HMAC tag and is a SEPARATE credential
// from the memo/git tokens, so a leak is scoped to schedule access only. CP maps token
// -> membership (never client-supplied), so there is no cross-membership access.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

func scheduleSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-schedule-token-sign/v1"))
	return mac.Sum(nil)
}

// mintScheduleToken returns the deterministic schedule token for a membership. Format:
// "afs_" + b64url(membershipID) + "." + tag. Deterministic, so re-injection on every
// container start is idempotent (same as the memo/internal-git tokens).
func mintScheduleToken(signKey []byte, membershipID string) string {
	return "afs_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + scheduleTokenTag(signKey, membershipID)
}

func scheduleTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyScheduleToken checks the tag and returns the embedded membership id. It does
// NOT resolve tenant/role — that is a live store lookup by the caller.
func verifyScheduleToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afs_")
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
	if !hmac.Equal([]byte(body[dot+1:]), []byte(scheduleTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// scheduleTokenMembership authenticates a /internal/schedules request by its Bearer
// schedule token and resolves the live membership (a revoked membership -> 401).
// Tenant/role come from the live store, never the token.
func (a scheduleAPI) scheduleTokenMembership(r *http.Request) (MembershipView, *apiError) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyScheduleToken(scheduleSignKey(a.mgr.tokenSignMaster()), tok)
	if !ok {
		return MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid schedule token"}
	}
	mv, ok, err := a.mgr.store.GetMembershipByID(r.Context(), mid)
	if err != nil {
		return MembershipView{}, internalErr(err)
	}
	if !ok {
		return MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"}
	}
	return mv, nil
}

// withScheduleToken adapts a membership-scoped schedule handler to the internal token
// face — the SAME handler bodies the (future) session face would use; only the
// membership resolution differs.
func (a scheduleAPI) withScheduleToken(h func(http.ResponseWriter, *http.Request, MembershipView)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mv, aerr := a.scheduleTokenMembership(r)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		h(w, r, mv)
	}
}
