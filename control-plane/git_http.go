package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Internal git provider — smart-HTTP face (docs/reference/internal-git-provider,
// ADR 0010). The CP self-hosts one bare repo per (tenant, name) and serves
// clone/fetch/push over HTTP by wrapping `git http-backend` (CGI). Auth is a
// deterministic per-membership HMAC token presented as the Basic password; there
// is no token table — the CP re-derives the same token to inject into the
// workspace (manager.go) and to verify here, so injection is idempotent and no
// plaintext is ever persisted. Every request re-checks (tenant, role) live.

// gitServerAPI is the internal-git feature handler set (docs/log/23, remaining item 3):
// the smart-HTTP + LFS + LFS-lock faces (self-authenticating Basic git token,
// session-exempt under /git/) and the CP-native management/browse API
// (/api/internal-git/*, session auth via the embedded memberAuth's
// withMembership). dataRoot and the token sign key are copied from the manager
// at construction; store is the narrow composed view (gitServerStore) of the
// sub-stores these handlers actually use.
type gitServerAPI struct {
	memberAuth
	dataRoot      string
	signKey       []byte // git-token signing key, derived from the deployment master
	publicBaseURL string // external base for clone/LFS hrefs ("" = not configured)
	// internalBaseURL is AF_CP_INTERNAL_URL (ADR 0106 decision 8): the base of the LFS
	// hrefs answered on the workspace listener. The clone URL stays public — the Console
	// shows it to people — and the Agent rewrites it for the workspace's own git.
	internalBaseURL string
	store           gitServerStore
}

// baseURLFor is the base of URLs answered to r: the internal one when r came in on the
// workspace listener (where the public base may be unreachable), else the public one.
// The listener's context marker decides, never a Host or forwarding header a caller sets.
func (a gitServerAPI) baseURLFor(r *http.Request) string {
	if a.publicBaseURL != "" && a.internalBaseURL != "" && viaWorkspaceListener(r.Context()) {
		return a.internalBaseURL
	}
	return a.publicBaseURL
}

// gitServerStore is the internal-git server's store view: the repo ledger, the
// LFS object + lock ledgers, tenant limits (quotas), membership resolution
// (git token → live tenant/role), and the audit ledger.
type gitServerStore interface {
	store.TenantStore
	store.MembershipStore
	store.GitRepoStore
	store.LFSObjectStore
	store.LFSLockStore
	store.AuditStore
}

func newGitServerAPI(m *manager, publicBaseURL string) gitServerAPI {
	return gitServerAPI{memberAuth{m}, m.dataRoot, gitSignKey(m.tokenSignMaster()), publicBaseURL, m.internalBaseURL, m.store}
}

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

// gitSignKey derives the token-signing subkey from a deployment master key
// (same master that seeds AF_SECRET_KEY).
func gitSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-git-token-sign/v1"))
	return mac.Sum(nil)
}

// tokenSignMaster returns the master the token-signing subkeys (git / memo /
// schedule / MCP) derive from: the AF_MASTER_KEY digest when configured, else a
// per-deployment RANDOM dev master — these token faces are authGate-exempt, so
// the old well-known fixed fallbacks would let anyone who learns a membership id
// forge a working token.
func (m *manager) tokenSignMaster() []byte {
	if len(m.master32) > 0 {
		return m.master32
	}
	return m.devGitTokenMaster()
}

// devGitTokenMaster loads (or creates once) the random dev-mode signing master at
// <dataRoot>/git-token-master.key. With no dataRoot (unit tests) or an unwritable
// one, the key stays in-memory only — dev tokens then rotate across restarts,
// which is still strictly safer than a fixed public key.
func (m *manager) devGitTokenMaster() []byte {
	m.gitDevMasterOnce.Do(func() {
		path := ""
		if m.dataRoot != "" {
			path = filepath.Join(m.dataRoot, datalayout.GitTokenMasterFile)
			if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
				m.gitDevMaster = b[:32]
				return
			}
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic(err) // crypto/rand failure is unrecoverable
		}
		if path != "" {
			_ = os.MkdirAll(m.dataRoot, 0o700)
			if err := os.WriteFile(path, b, 0o600); err != nil {
				log.Printf("git: persist dev token master: %v (tokens will rotate on restart)", err)
			}
		}
		m.gitDevMaster = b
	})
	return m.gitDevMaster
}

// mintGitToken returns the deterministic git access token for a membership at its
// current git token epoch (issue #1199). The token carries the membership id (so
// verification can look up tenant, role and epoch live) plus a truncated HMAC tag.
// Format: "afg_" + b64url(membershipID) + "." + tag. The epoch is not in the token:
// the verifier reads the current one from the store, so a token minted under an older
// epoch simply stops matching.
func mintGitToken(signKey []byte, membershipID string, epoch int64) string {
	return "afg_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + gitTokenTag(signKey, membershipID, epoch)
}

// gitTokenEpochDomain prefixes the HMAC input of every epoch above 0. It starts with a
// NUL, which no membership id contains (store.NewID), so an epoch-0 input (the bare id)
// can never equal an epoch>0 input. The epoch is decimal and NUL-terminated before the
// id, so two (epoch, id) pairs above 0 cannot share an input either.
const gitTokenEpochDomain = "\x00af-git-token-epoch\x00"

// gitTokenTag MACs the membership id at an epoch. Epoch 0 MACs the bare id, which is
// exactly the input used before epochs existed: changing it would invalidate every
// token in every running workspace on upgrade (pinned by TestGitTokenEpochZeroIsLegacy).
func gitTokenTag(signKey []byte, membershipID string, epoch int64) string {
	mac := hmac.New(sha256.New, signKey)
	if epoch != 0 {
		mac.Write([]byte(gitTokenEpochDomain + strconv.FormatInt(epoch, 10) + "\x00"))
	}
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// parseGitToken splits a token into the membership id it names and its tag, checking
// only the shape. It proves nothing: the tag is checked by verifyGitTokenTag against
// the epoch the store holds for that id.
func parseGitToken(token string) (membershipID, tag string, ok bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afg_")
	if !hasPrefix {
		return "", "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", "", false
	}
	idRaw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	// A NUL never occurs in a membership id, and refusing it here keeps the epoch>0 HMAC
	// input (gitTokenEpochDomain) out of reach of a crafted id.
	if err != nil || len(idRaw) == 0 || bytes.IndexByte(idRaw, 0) >= 0 {
		return "", "", false
	}
	return string(idRaw), body[dot+1:], true
}

// verifyGitTokenTag reports whether tag is the membership's tag at exactly this epoch.
// Only the current epoch is ever passed: accepting an earlier one would make rotation
// a no-op.
func verifyGitTokenTag(signKey []byte, membershipID, tag string, epoch int64) bool {
	return hmac.Equal([]byte(tag), []byte(gitTokenTag(signKey, membershipID, epoch)))
}

// verifyGitToken checks a token against the epoch the caller looked up for the id it
// names and returns that id. It does NOT resolve tenant/role — that is a live store
// lookup by the caller.
func verifyGitToken(signKey []byte, token string, epochOf func(membershipID string) (int64, bool)) (membershipID string, ok bool) {
	mid, tag, ok := parseGitToken(token)
	if !ok {
		return "", false
	}
	epoch, ok := epochOf(mid)
	if !ok || !verifyGitTokenTag(signKey, mid, tag, epoch) {
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

// gitAuthErr is a resolved-authorization failure. needAuth marks the 401 case so
// a git/LFS client knows to (re)send Basic credentials. Callers render it in their
// wire format via writeGit / writeLFS.
type gitAuthErr struct {
	status   int
	msg      string
	needAuth bool
}

func (e *gitAuthErr) writeGit(w http.ResponseWriter) {
	if e.needAuth {
		requireGitAuth(w)
		return
	}
	http.Error(w, e.msg, e.status)
}

// authorizeGitRepo runs the auth + confinement shared by the smart-HTTP and LFS
// faces: Basic password = git token → live (tenant, role) → slug==token-tenant →
// repo name valid + present in the ledger. It does NOT apply the push gate — the
// caller decides read vs write per operation. Returns the resolved repo name, the
// membership view, and the token's membership id.
func (a gitServerAPI) authorizeGitRepo(r *http.Request, slug, repoSeg string) (name string, mv store.MembershipView, membershipID string, aerr *gitAuthErr) {
	_, pass, ok := r.BasicAuth()
	if !ok || pass == "" {
		return "", mv, "", &gitAuthErr{http.StatusUnauthorized, "authentication required", true}
	}
	// The epoch is read live on every request (no cache): a rotation has to take effect
	// on the very next request, the same reason the role below is read live.
	var storeErr error
	membershipID, ok = verifyGitToken(a.signKey, pass, func(mid string) (int64, bool) {
		epoch, found, err := a.store.GitTokenEpoch(r.Context(), mid)
		storeErr = err
		return epoch, found && err == nil
	})
	if storeErr != nil {
		return "", mv, "", &gitAuthErr{http.StatusInternalServerError, "store error", false}
	}
	if !ok {
		return "", mv, "", &gitAuthErr{http.StatusUnauthorized, "authentication required", true}
	}
	mv, ok, err := a.store.GetMembershipByID(r.Context(), membershipID)
	if err != nil {
		return "", mv, "", &gitAuthErr{http.StatusInternalServerError, "store error", false}
	}
	if !ok {
		return "", mv, "", &gitAuthErr{http.StatusUnauthorized, "authentication required", true} // inactive/removed
	}
	// Tenant confinement: the URL's slug must be the token's own tenant — the
	// primary cross-tenant barrier, applied to every request.
	if !strings.EqualFold(slug, mv.TenantSlug) {
		return "", mv, "", &gitAuthErr{http.StatusForbidden, "forbidden: tenant mismatch", false}
	}
	name, isGit := strings.CutSuffix(repoSeg, ".git")
	if !isGit || !validRepoName(name) {
		return "", mv, "", &gitAuthErr{http.StatusNotFound, "not found", false}
	}
	// Ledger is the source of truth: only serve repos the API created.
	if _, present, err := a.store.GetGitRepo(r.Context(), mv.TenantID, name); err != nil {
		return "", mv, "", &gitAuthErr{http.StatusInternalServerError, "store error", false}
	} else if !present {
		return "", mv, "", &gitAuthErr{http.StatusNotFound, "not found", false}
	}
	return name, mv, membershipID, nil
}

// requireBase refuses the token-authenticated /git/ surface (smart HTTP, LFS
// transfer and locks) when PUBLIC_BASE_URL is unset. Repository creation and LFS
// batch already need the base to build URLs; without this the rest stayed open to any
// token a workspace was seeded with while the base was set, so "unset" disabled only
// half the provider. The refusal comes before authentication: it says nothing about
// a repository or a token. LFS clients get the JSON error body they expect.
func (a gitServerAPI) requireBase(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.publicBaseURL != "" {
			h(w, r)
			return
		}
		const msg = "internal git not configured (PUBLIC_BASE_URL)"
		if strings.Contains(r.URL.Path, "/info/lfs/") {
			writeLFSErr(w, http.StatusServiceUnavailable, msg)
			return
		}
		http.Error(w, msg, http.StatusServiceUnavailable)
	}
}

// gitHTTP serves clone/fetch/push for /git/{slug}/{repo...}. It authenticates
// and confines via authorizeGitRepo, gates push by role, then hands off to
// git-http-backend within the tenant's own git tree.
func (a gitServerAPI) gitHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/git/")
	slug, sub, _ := strings.Cut(rest, "/")
	if slug == "" || sub == "" {
		http.NotFound(w, r)
		return
	}
	repoSeg, _, _ := strings.Cut(sub, "/")
	_, mv, membershipID, aerr := a.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeGit(w)
		return
	}
	if isReceivePack(r) && !canPush(mv.Role) {
		http.Error(w, "forbidden: read-only", http.StatusForbidden)
		return
	}

	// Path containment: the project root is the tenant's own git tree, so even a
	// crafted PATH_INFO cannot escape into another tenant (defense in depth over
	// the slug check). filepath.Join collapses any residual traversal.
	// Use the token tenant's CANONICAL slug for the on-disk tree: the URL slug is
	// only EqualFold-equal, and a case-variant would address a sibling directory
	// outside the real repo tree (orphan objects the GC never sees).
	tenantRoot := filepath.Join(a.dataRoot, datalayout.GitDir, filepath.Base(mv.TenantSlug))
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
