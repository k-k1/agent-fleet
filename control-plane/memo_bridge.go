package main

// Memo bridge — the internal (operator-token) face of the memo queue (docs/log/21).
// The Console reaches memos through the session gateway (memo.go withMembership); an
// in-container フリート・オペレーター has NO gateway session, so it authenticates to
// /internal/memos with a per-membership MEMO TOKEN injected into its Workspace
// (AF_MEMO_TOKEN). The token carries the membership id + a truncated HMAC tag — it
// mirrors the internal-git token (git_http.go) and is a SEPARATE credential, so a leak
// is scoped to memo access only. CP maps token -> membership (never client-supplied),
// so there is no cross-membership access. The route is under /internal/* (session-
// exempt bearer), reached by the container over the public hairpin like internal-git.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func memoSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-memo-token-sign/v1"))
	return mac.Sum(nil)
}

// mintMemoToken returns the deterministic memo token for a membership. Format:
// "afm_" + b64url(membershipID) + "." + tag. Deterministic, so re-injection on every
// container start is idempotent (same as the internal-git token).
func mintMemoToken(signKey []byte, membershipID string) string {
	return "afm_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + memoTokenTag(signKey, membershipID)
}

func memoTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyMemoToken checks the tag and returns the embedded membership id. It does NOT
// resolve tenant/role — that is a live store lookup by the caller.
func verifyMemoToken(signKey []byte, token string) (membershipID string, ok bool) {
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
	if !hmac.Equal([]byte(body[dot+1:]), []byte(memoTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// memoTokenMembership authenticates a /internal/memos request by its Bearer memo token
// and resolves the live membership (a revoked membership -> 401). Tenant/role come from
// the live store, never the token.
func (a memoAPI) memoTokenMembership(r *http.Request) (store.MembershipView, *apiError) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyMemoToken(memoSignKey(a.mgr.tokenSignMaster()), tok)
	if !ok {
		return store.MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid memo token"}
	}
	mv, ok, err := a.mgr.store.GetMembershipByID(r.Context(), mid)
	if err != nil {
		return store.MembershipView{}, internalErr(err)
	}
	if !ok {
		return store.MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"}
	}
	return mv, nil
}

// withMemoToken adapts a membership-scoped memo handler to the internal token face —
// the SAME handler bodies the session face uses (list/create/update/delete); only the
// membership resolution differs.
func (a memoAPI) withMemoToken(h func(http.ResponseWriter, *http.Request, store.Identity, store.MembershipView)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mv, aerr := a.memoTokenMembership(r)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		h(w, r, store.Identity{}, mv)
	}
}

// withMemoTokenResolved is withMemoToken for flush, which additionally needs the
// workspace runtime: it maps membership -> identity -> resolved runtime (the same path
// the PAT MCP uses, resolveByMembership).
func (a memoAPI) withMemoTokenResolved(h func(http.ResponseWriter, *http.Request, *resolved)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mv, aerr := a.memoTokenMembership(r)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		identityID, ok, err := a.mgr.store.IdentityIDForMembership(r.Context(), mv.MembershipID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		if !ok {
			writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"})
			return
		}
		res, aerr := a.mgr.resolveByMembership(r.Context(), identityID, mv.MembershipID)
		if aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		h(w, r, res)
	}
}
