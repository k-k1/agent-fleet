package main

// Docs bridge — the PULL face of the agent-fleet user guide (docs/build/04 §4.9).
//
//	tenant member's container ──(AF_DOCS_TOKEN)──▶ CP GET /internal/docs  (tar.gz)
//	                                                   │ the guide, served by the CP
//	                                          /usr/local/share/agent-fleet/docs
//
// Why this exists. docker / native hand the container its docs as a read-only bind
// mount of the per-start staging dir (stageWorkspaceDocs → runtime_docker.go /
// runtime_native.go). ECS has no such seam: the task runs on Fargate or an EC2
// instance with no host path the CP can write, so those workspaces were starting with
// an EMPTY docs dir — the Console's 利用ガイド opened nothing, and the in-container
// agents had no docs to cite for environment questions. The container pulls instead.
//
// What is served is decided entirely on the CP, exactly as it is for the mount: the
// token proves the caller is an active membership, and the response then carries the
// guide — the same tree for everyone (ADR 0064). **Nothing in the request selects
// scope**, so there is no shape of request that reaches the developer tree; the
// decision records and the frozen work journals are not baked into the image at all.
//
// The token is a SEPARATE credential from the memo / schedule / MCP / git tokens
// (mcp_server_bridge.go and friends), so a leak is scoped to reading this member's own
// docs subset and grants nothing else.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log"
	"net/http"
	"strings"
)

func docsSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-docs-token-sign/v1"))
	return mac.Sum(nil)
}

// mintDocsToken returns the deterministic docs token for a membership. Format:
// "afd_" + b64url(membershipID) + "." + tag. Deterministic, so re-injecting it on
// every container start is idempotent (same as the other bridge tokens).
func mintDocsToken(signKey []byte, membershipID string) string {
	return "afd_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + docsTokenTag(signKey, membershipID)
}

func docsTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyDocsToken checks the tag and returns the embedded membership id. Like the MCP
// token it does NOT resolve role — that is a live store lookup by the caller, so a
// role change (or a revoked membership) takes effect on the next pull rather than when
// the token rotates.
func verifyDocsToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afd_")
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
	if !hmac.Equal([]byte(body[dot+1:]), []byte(docsTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

type docsAPI struct{ mgr *manager }

func newDocsAPI(m *manager) docsAPI { return docsAPI{mgr: m} }

// docsTokenMembership authenticates a /internal/docs request by its Bearer docs token
// and resolves the live membership (revoked → 401).
func (a docsAPI) docsTokenMembership(r *http.Request) (MembershipView, *apiError) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyDocsToken(docsSignKey(a.mgr.tokenSignMaster()), tok)
	if !ok {
		return MembershipView{}, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid docs token"}
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

// download (GET /internal/docs) streams the guide as a tar.gz. A deployment with no baked docs answers 404 so the agent can say "this
// deployment ships no docs" rather than silently unpacking an empty archive.
func (a docsAPI) download(w http.ResponseWriter, r *http.Request) {
	mv, aerr := a.docsTokenMembership(r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if !isDirPath(docsSrcDir()) {
		writeAPIErr(w, &apiError{http.StatusNotFound, "docs_unavailable", "this deployment has no bundled docs"})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-store")
	// Status and headers are committed the moment the first byte goes out, so a
	// mid-stream failure can only be logged — the agent sees a truncated archive,
	// refuses it (gzip/tar both detect the truncation) and keeps whatever it had.
	if _, err := writeGuideTarGz(w); err != nil {
		log.Printf("docs bridge: stream (membership=%s): %v", mv.MembershipID, err)
	}
}
