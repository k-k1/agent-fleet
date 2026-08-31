package main

// MCP registry bridge — the internal (per-membership token) face of tenant-distributed
// MCP servers (docs/log/48 P4 + ADR0031). It mirrors the memo / schedule bridges
// (memo_bridge.go / schedule_bridge.go): the Workspace agent has no user session, so it
// authenticates to /internal/mcp-servers with an MCP TOKEN injected into its container
// (AF_MCP_TOKEN). The token carries the membership id plus a truncated HMAC tag and is a
// SEPARATE credential from the memo / schedule / git tokens, so a leak is scoped to
// reading this tenant's distributed MCP definitions.
//
// CP maps token -> membership -> tenant (never client-supplied), so a member can only
// ever pull their OWN tenant's set. That matters more here than for memo/schedule: the
// response can carry tenant credentials (the headers of a user_secret=0 server), so a
// client-chosen tenant id would be a cross-tenant secret read.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

func mcpSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-mcp-token-sign/v1"))
	return mac.Sum(nil)
}

// mintMCPToken returns the deterministic MCP registry token for a membership. Format:
// "afm_" + b64url(membershipID) + "." + tag. Deterministic, so re-injecting it on every
// container start is idempotent (same as the memo / schedule / internal-git tokens).
func mintMCPToken(signKey []byte, membershipID string) string {
	return "afm_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + mcpTokenTag(signKey, membershipID)
}

func mcpTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyMCPToken checks the tag and returns the embedded membership id. It does NOT
// resolve tenant/role — that is a live store lookup by the caller, so a revoked
// membership stops working immediately rather than until the token rotates.
func verifyMCPToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afm_")
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
	if !hmac.Equal([]byte(body[dot+1:]), []byte(mcpTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// mcpTokenMembership authenticates a /internal/mcp-servers request by its Bearer MCP
// token and resolves the live membership (a revoked membership -> 401). Tenant comes
// from the live store, never from the request.
func (a mcpServerAPI) mcpTokenMembership(r *http.Request) (MembershipView, *apiError) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyMCPToken(mcpSignKey(a.mgr.tokenSignMaster()), tok)
	if !ok {
		return MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid mcp token"}
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

// withMCPToken adapts a membership-scoped handler to the internal token face.
func (a mcpServerAPI) withMCPToken(h func(http.ResponseWriter, *http.Request, MembershipView)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mv, aerr := a.mcpTokenMembership(r)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		h(w, r, mv)
	}
}
