package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/datalayout"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Internal git provider — management face (docs/reference/internal-git-provider,
// ADR 0010). Unlike every other provider (listed via the per-user Agent), the
// internal repo list/create/delete is CP-NATIVE: the CP owns the bare repos, so
// it answers directly instead of proxying to a workspace. All routes are scoped
// to the caller's resolved tenant (X-AF-Tenant → withMembership); the handlers
// are gitServerAPI methods (struct in git_http.go, docs/log/23 remainder 3).

// cloneURL builds the clone URL a workspace container uses. It is the public
// base (Caddy TLS terminus, reachable from the container via hairpin NAT), or the
// internal one where the CP has it, so the unified cred helper's token injection
// (keyed by AF_INTERNAL_GIT_HOST, the same base's host) authenticates it transparently.
func (a gitServerAPI) cloneURL(slug, name string) string {
	return strings.TrimRight(a.workspaceBaseURL(), "/") + "/git/" + slug + "/" + name + ".git"
}

// internalGitCredentialHost is the key the Agent seeds the internal git credential
// under (AF_INTERNAL_GIT_HOST). It has to equal the `host=` line git's credential
// protocol sends for cloneURL, and git sends the authority with its port whenever the
// URL carries one — so this is u.Host, not u.Hostname(): a base such as
// http://127.0.0.1:8080 otherwise leaves every clone and push without credentials.
// Empty when the base is unset or unparsable (internal git disabled).
func internalGitCredentialHost(publicBaseURL string) string {
	u, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil {
		return ""
	}
	return u.Host
}

// internalRepoWire is the wire shape of one internal git repository (the Console's
// `InternalRepo`, console/src/features/settings/workspace/InternalReposTab.tsx).
//
// was: map[string]any{"name":…, "default_branch":…, "clone_url":…, "created_at":…, "provider":…}
// All six keys are unconditional, so no omitempty: default_branch and created_at can be
// empty strings, and omitempty would drop the key and change the wire.
//
// provider is always the constant "internal". The Console's `InternalRepo` neither
// declares nor reads it (`ProviderCard` is a different UI part), so typing it changes
// neither the wire nor the screen — but from here on wire.golden is what captures it.
type internalRepoWire struct {
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	CreatedAt     string `json:"created_at"`
	Provider      string `json:"provider"`
	// CanManage says whether the caller may rename or delete this repository
	// (canManageRepo), so the Console offers only the buttons that would succeed.
	CanManage bool `json:"can_manage"`
}

func (a gitServerAPI) repoDTO(mv store.MembershipView, g store.GitRepo) internalRepoWire {
	return internalRepoWire{
		Name:          g.Name,
		DefaultBranch: g.DefaultBranch,
		CloneURL:      a.cloneURL(mv.TenantSlug, g.Name),
		CreatedAt:     g.CreatedAt,
		Provider:      "internal",
		CanManage:     canManageRepo(mv, g),
	}
}

// canManageRepo reports whether mv may rename or delete g: a role that may push, and
// either the repository's creator or a tenant_admin. Deleting removes the bare and
// its LFS objects for everyone in the tenant, so pushing to a repository is not enough.
// created_by holds the creator's membership id; a row without one is the tenant_admin's.
func canManageRepo(mv store.MembershipView, g store.GitRepo) bool {
	if !canPush(mv.Role) {
		return false
	}
	return mv.Role == "tenant_admin" || (g.CreatedBy != "" && g.CreatedBy == mv.MembershipID)
}

var errGitRepoManageForbidden = &apiError{http.StatusForbidden, errCodeGitRepoManageForbidden,
	"only the repository's creator or a tenant administrator may rename or delete it"}

// reposList (GET /api/internal-git/repos) lists the tenant's internal repos for
// the RepoPicker/GitTab.
func (a gitServerAPI) reposList(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	repos, err := a.store.ListGitReposByTenant(r.Context(), mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]internalRepoWire, 0, len(repos))
	for _, g := range repos {
		out = append(out, a.repoDTO(mv, g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

// repoCreate (POST /api/internal-git/repos {name}) creates a bare repo on disk
// and its ledger row, then returns the clone URL. Idempotent-ish: a duplicate
// name is a 409.
func (a gitServerAPI) repoCreate(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	if a.publicBaseURL == "" {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, "not_configured", "internal git requires PUBLIC_BASE_URL"})
		return
	}
	// The same gate as a push: a role that may not write to a repository may not bring
	// one into existence either.
	if !canPush(mv.Role) {
		writeAPIErr(w, &apiError{http.StatusForbidden, errCodeGitRepoCreateForbidden,
			"your role in this tenant may not create repositories"})
		return
	}
	var body struct {
		Name          string `json:"name"`
		DefaultBranch string `json:"default_branch"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := sanitizeUser(body.Name)
	if !validRepoName(name) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "repo name must be 1-64 chars: letters, digits, . _ -"})
		return
	}
	branch := strings.TrimSpace(body.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	// Quota (P2): cap internal repos per tenant when the tenant sets max_git_repos.
	if aerr := a.enforceGitRepoQuota(r.Context(), mv.TenantID); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if _, exists, err := a.store.GetGitRepo(r.Context(), mv.TenantID, name); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if exists {
		writeAPIErr(w, &apiError{http.StatusConflict, "exists", "a repo with that name already exists"})
		return
	}

	dir := filepath.Join(a.dataRoot, datalayout.GitDir, mv.TenantSlug, name+".git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// git init --bare with a fixed initial branch (git >= 2.28). The bare is the
	// only on-disk artifact; the row below is the ledger.
	if out, err := exec.CommandContext(r.Context(), "git", "init", "--bare", "--initial-branch="+branch, dir).CombinedOutput(); err != nil {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "init_failed", strings.TrimSpace(string(out))})
		return
	}
	g := store.GitRepo{
		ID:            store.NewID(),
		TenantID:      mv.TenantID,
		Name:          name,
		DefaultBranch: branch,
		CreatedBy:     mv.MembershipID,
		CreatedAt:     store.NowTS(),
	}
	if err := a.store.CreateGitRepo(r.Context(), g); err != nil {
		_ = os.RemoveAll(dir) // roll back the bare so a retry isn't blocked by an orphan
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditGit(r.Context(), mv.TenantID, ident.ID, "internal_git.repo.create", name, "branch="+branch)
	writeJSON(w, http.StatusOK, a.repoDTO(mv, g))
}

// enforceGitRepoQuota returns a 409 apiError when the tenant is at or over its
// max_git_repos cap (0 = unlimited). Nil when creation is allowed.
func (a gitServerAPI) enforceGitRepoQuota(ctx context.Context, tenantID string) *apiError {
	t, err := a.store.GetTenant(ctx, tenantID)
	if err != nil {
		return internalErr(err)
	}
	max := parseLimits(t.Limits).MaxGitRepos
	if max <= 0 {
		return nil
	}
	n, err := a.store.CountGitReposByTenant(ctx, tenantID)
	if err != nil {
		return internalErr(err)
	}
	if n >= max {
		return &apiError{http.StatusConflict, "quota_exceeded",
			"internal repo limit reached for this tenant (max " + strconv.Itoa(max) + ")"}
	}
	return nil
}

// auditGit records a reversible internal-git mutation (create) in the audit ledger.
// Best-effort: a logging failure never blocks the operation. Delete and rename cannot be
// undone and go through beginIrreversible instead.
func (a gitServerAPI) auditGit(ctx context.Context, tenantID, actorID, action, target, detail string) {
	_ = a.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: tenantID, ActorKind: "user", ActorID: actorID,
		Action: action, Target: target, Detail: detail, At: store.NowTS(),
	})
}

// repoDelete (DELETE /api/internal-git/repos/{name}) removes the ledger row and
// the bare. Tenant-scoped: only repos owned by the caller's tenant.
func (a gitServerAPI) repoDelete(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	name := r.PathValue("name")
	if !validRepoName(name) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "invalid repo name"})
		return
	}
	g, exists, err := a.store.GetGitRepo(r.Context(), mv.TenantID, name)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return
	}
	if !canManageRepo(mv, g) {
		writeAPIErr(w, errGitRepoManageForbidden)
		return
	}
	// The bare and its LFS objects cannot be restored, so who asked is on record before
	// anything goes, and nothing goes when that record cannot be written.
	in, ok := beginIrreversible(w, r, a.store, store.AuditLog{
		TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID,
		Action: "internal_git.repo.delete", Target: name,
	})
	if !ok {
		return
	}
	// The repo row goes with its LFS ledger and lock rows in one transaction, before
	// the disk: a failure leaves everything as it was and the request can be retried.
	if err := a.store.DeleteGitRepo(r.Context(), mv.TenantID, name); err != nil {
		refuseIrreversible(w, r, in, internalErr(err))
		return
	}
	dir := filepath.Join(a.dataRoot, datalayout.GitDir, mv.TenantSlug, name+".git")
	outcome := ""
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("internal git: delete %s: ledger rows removed but the bare remains: %v", dir, err)
		outcome = "ledger rows removed but the bare remains: " + err.Error()
	}
	in.Done(r.Context(), outcome, http.StatusOK)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// repoRename (POST /api/internal-git/repos/{name}/rename {new_name}) renames a
// repo: the ledger row and the on-disk bare move together. Existing clones keep
// their old origin URL and must update the remote to the new clone_url.
func (a gitServerAPI) repoRename(w http.ResponseWriter, r *http.Request, ident store.Identity, mv store.MembershipView) {
	oldName := r.PathValue("name")
	if !validRepoName(oldName) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "invalid repo name"})
		return
	}
	var body struct {
		NewName string `json:"new_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	newName := sanitizeUser(body.NewName)
	if !validRepoName(newName) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "new name must be 1-64 chars: letters, digits, . _ -"})
		return
	}
	if newName == oldName {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "same_name", "new name is identical"})
		return
	}
	g, exists, err := a.store.GetGitRepo(r.Context(), mv.TenantID, oldName)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return
	}
	if !canManageRepo(mv, g) {
		writeAPIErr(w, errGitRepoManageForbidden)
		return
	}
	if _, taken, err := a.store.GetGitRepo(r.Context(), mv.TenantID, newName); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if taken {
		writeAPIErr(w, &apiError{http.StatusConflict, "exists", "a repo with the new name already exists"})
		return
	}

	// A rename breaks every existing clone's origin URL and frees the old name for someone
	// else's repository, so it is held to the same intent-first record as a delete.
	in, ok := beginIrreversible(w, r, a.store, store.AuditLog{
		TenantID: mv.TenantID, ActorKind: "user", ActorID: ident.ID,
		Action: "internal_git.repo.rename", Target: oldName, Detail: "to=" + newName,
	})
	if !ok {
		return
	}
	oldDir := filepath.Join(a.dataRoot, datalayout.GitDir, mv.TenantSlug, oldName+".git")
	newDir := filepath.Join(a.dataRoot, datalayout.GitDir, mv.TenantSlug, newName+".git")
	if err := os.Rename(oldDir, newDir); err != nil {
		refuseIrreversible(w, r, in, &apiError{http.StatusInternalServerError, "rename_failed", err.Error()})
		return
	}
	// RenameGitRepo repoints the LFS ledger and locks in the same transaction, matching
	// the lfs/objects that just moved with the .git dir.
	if err := a.store.RenameGitRepo(r.Context(), mv.TenantID, oldName, newName); err != nil {
		_ = os.Rename(newDir, oldDir) // roll back the move so disk and ledger stay consistent
		refuseIrreversible(w, r, in, internalErr(err))
		return
	}
	in.Done(r.Context(), "to="+newName, http.StatusOK)
	g.Name = newName
	writeJSON(w, http.StatusOK, a.repoDTO(mv, g))
}

// branches (GET /api/internal-git/repos/{name}/branches) reads the bare's refs
// directly (no clone) for the RepoPicker branch list.
func (a gitServerAPI) branches(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	name := r.PathValue("name")
	if !validRepoName(name) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "invalid repo name"})
		return
	}
	g, exists, err := a.store.GetGitRepo(r.Context(), mv.TenantID, name)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return
	}
	dir := filepath.Join(a.dataRoot, datalayout.GitDir, mv.TenantSlug, name+".git")
	out, err := exec.CommandContext(r.Context(), "git", "--git-dir", dir,
		"for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
	if err != nil {
		// Never hide a corrupt bare repo behind an empty array and a 200.
		log.Printf("internal-git: for-each-ref failed repo=%s/%s: %v", mv.TenantSlug, name, err)
		writeAPIErr(w, internalErr(err))
		return
	}
	branches := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			branches = append(branches, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": branches, "default_branch": g.DefaultBranch})
}
