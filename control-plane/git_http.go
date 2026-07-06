package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/cgi"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Internal git provider — smart-HTTP face (docs/reference/internal-git-provider,
// ADR 0010). The CP self-hosts one bare repo per (tenant, name) and serves
// clone/fetch/push over HTTP by wrapping `git http-backend` (CGI). Auth is a
// deterministic per-membership HMAC token presented as the Basic password; there
// is no token table — the CP re-derives the same token to inject into the
// workspace (manager.go) and to verify here, so injection is idempotent and no
// plaintext is ever persisted. Every request re-checks (tenant, role) live.

// gitBackendPath is the git-http-backend CGI. Debian ships it under git-core;
// overridable for other layouts.
func gitBackendPath() string {
	if p := os.Getenv("GIT_HTTP_BACKEND"); p != "" {
		return p
	}
	return "/usr/lib/git-core/git-http-backend"
}

// repoNameRE constrains a repo name to a single safe path segment (no slashes,
// dots-only, or traversal). The ".git" suffix is added/stripped by callers.
var repoNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validRepoName(name string) bool {
	return repoNameRE.MatchString(name) && name != ".git" && !strings.Contains(name, "..")
}

// gitSignKey derives the token-signing subkey from the deployment master key
// (same master that seeds AF_SECRET_KEY). In dev (no master) a fixed non-secret
// key keeps the flow working; the store is plaintext there anyway.
func gitSignKey(master32 []byte) []byte {
	if len(master32) == 0 {
		master32 = []byte("af-dev-git-token-master-not-secret")
	}
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-git-token-sign/v1"))
	return mac.Sum(nil)
}

// mintGitToken returns the deterministic git access token for a membership. The
// token carries the membership id (so verification can look up tenant+role live)
// plus a truncated HMAC tag. Format: "afg_" + b64url(membershipID) + "." + tag.
func mintGitToken(signKey []byte, membershipID string) string {
	return "afg_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + gitTokenTag(signKey, membershipID)
}

func gitTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyGitToken checks the tag and returns the embedded membership id. It does
// NOT resolve tenant/role — that is a live store lookup by the caller.
func verifyGitToken(signKey []byte, token string) (membershipID string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afg_")
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
	if !hmac.Equal([]byte(body[dot+1:]), []byte(gitTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

// isReceivePack reports whether a smart-HTTP request is a push (write). Push uses
// either GET /info/refs?service=git-receive-pack (ref advertisement) or
// POST .../git-receive-pack (the pack upload).
func isReceivePack(r *http.Request) bool {
	if r.URL.Query().Get("service") == "git-receive-pack" {
		return true
	}
	return strings.HasSuffix(r.URL.Path, "/git-receive-pack")
}

// canPush gates write access by tenant role. Read is any active membership; write
// currently requires member or tenant_admin (a future read-only "viewer" role
// would be excluded here). An unknown/empty role is denied.
func canPush(role string) bool {
	return role == "member" || role == "tenant_admin"
}

// handleGitHTTP serves clone/fetch/push for /git/{slug}/{repo...}. It authenticates
// the Basic password as a git token, enforces slug==token-tenant on every request,
// gates push by role, contains the repo to the tenant's git tree, and requires the
// repo to exist in the ledger before handing off to git-http-backend.
func (c config) handleGitHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/git/")
	slug, sub, _ := strings.Cut(rest, "/")
	if slug == "" || sub == "" {
		http.NotFound(w, r)
		return
	}

	// Basic password = git token. Verify tag, then resolve (tenant, role) live.
	_, pass, ok := r.BasicAuth()
	if !ok || pass == "" {
		requireGitAuth(w)
		return
	}
	membershipID, ok := verifyGitToken(gitSignKey(c.mgr.master32), pass)
	if !ok {
		requireGitAuth(w)
		return
	}
	mv, ok, err := c.mgr.store.GetMembershipByID(r.Context(), membershipID)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if !ok {
		requireGitAuth(w) // membership removed/inactive → token no longer valid
		return
	}

	// Tenant confinement: the URL's slug must be the token's own tenant. This is
	// the primary cross-tenant barrier, checked on info/refs, upload-pack and
	// receive-pack alike.
	if !strings.EqualFold(slug, mv.TenantSlug) {
		http.Error(w, "forbidden: tenant mismatch", http.StatusForbidden)
		return
	}
	if isReceivePack(r) && !canPush(mv.Role) {
		http.Error(w, "forbidden: read-only", http.StatusForbidden)
		return
	}

	// Repo name is the first path segment under the slug, with a required ".git".
	repoSeg, _, _ := strings.Cut(sub, "/")
	name, isGit := strings.CutSuffix(repoSeg, ".git")
	if !isGit || !validRepoName(name) {
		http.NotFound(w, r)
		return
	}
	// Ledger is the source of truth: only serve repos the API created.
	if _, ok, err := c.mgr.store.GetGitRepo(r.Context(), mv.TenantID, name); err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}

	// Path containment: the project root is the tenant's own git tree, so even a
	// crafted PATH_INFO cannot escape into another tenant (defense in depth over
	// the slug check). filepath.Join collapses any residual traversal.
	tenantRoot := filepath.Join(c.mgr.dataRoot, "git", filepath.Base(slug))
	gitBackendServe(w, r, slug, tenantRoot, membershipID)
}

// gitBackendServe hands an authorized request to git-http-backend (CGI). It is a
// package var so tests can assert the authorization decision (which tenant root a
// request resolved to) without a git binary.
var gitBackendServe = func(w http.ResponseWriter, r *http.Request, slug, tenantRoot, remoteUser string) {
	h := &cgi.Handler{
		Path: gitBackendPath(),
		Root: "/git/" + slug, // stripped to form PATH_INFO = /<repo>.git/<service>
		Dir:  tenantRoot,
		Env: []string{
			"GIT_PROJECT_ROOT=" + tenantRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=" + remoteUser,
		},
	}
	h.ServeHTTP(w, r)
}

func requireGitAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="agent-fleet internal git"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}
