package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Internal git provider — management face (docs/reference/internal-git-provider,
// ADR 0010). Unlike every other provider (listed via the per-user Agent), the
// internal repo list/create/delete is CP-NATIVE: the CP owns the bare repos, so
// it answers directly instead of proxying to a workspace. All routes are scoped
// to the caller's resolved tenant (X-AF-Tenant → withMembership); the handlers
// are gitServerAPI methods (struct in git_http.go, docs/log/23 残③).

// cloneURL builds the clone URL a workspace container uses. It is the public
// base (Caddy TLS terminus, reachable from the container via hairpin NAT) so the
// unified cred helper's token injection authenticates it transparently.
func (a gitServerAPI) cloneURL(slug, name string) string {
	return strings.TrimRight(a.publicBaseURL, "/") + "/git/" + slug + "/" + name + ".git"
}

func (a gitServerAPI) repoDTO(slug string, g GitRepo) map[string]any {
	return map[string]any{
		"name":           g.Name,
		"default_branch": g.DefaultBranch,
		"clone_url":      a.cloneURL(slug, g.Name),
		"created_at":     g.CreatedAt,
		"provider":       "internal",
	}
}

// reposList (GET /api/internal-git/repos) lists the tenant's internal repos for
// the RepoPicker/GitTab.
func (a gitServerAPI) reposList(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
	repos, err := a.store.ListGitReposByTenant(r.Context(), mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(repos))
	for _, g := range repos {
		out = append(out, a.repoDTO(mv.TenantSlug, g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

// repoCreate (POST /api/internal-git/repos {name}) creates a bare repo on disk
// and its ledger row, then returns the clone URL. Idempotent-ish: a duplicate
// name is a 409.
func (a gitServerAPI) repoCreate(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	if a.publicBaseURL == "" {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, "not_configured", "internal git requires PUBLIC_BASE_URL"})
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

	dir := filepath.Join(a.dataRoot, "git", mv.TenantSlug, name+".git")
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
	g := GitRepo{
		ID:            newID(),
		TenantID:      mv.TenantID,
		Name:          name,
		DefaultBranch: branch,
		CreatedBy:     mv.MembershipID,
		CreatedAt:     nowTS(),
	}
	if err := a.store.CreateGitRepo(r.Context(), g); err != nil {
		_ = os.RemoveAll(dir) // roll back the bare so a retry isn't blocked by an orphan
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditGit(r.Context(), mv.TenantID, ident.ID, "internal_git.repo.create", name, "branch="+branch)
	writeJSON(w, http.StatusOK, a.repoDTO(mv.TenantSlug, g))
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

// auditGit records an internal-git mutation in the audit ledger. Best-effort:
// a logging failure never blocks the operation.
func (a gitServerAPI) auditGit(ctx context.Context, tenantID, actorID, action, target, detail string) {
	_ = a.store.InsertAudit(ctx, AuditLog{
		ID: newID(), TenantID: tenantID, ActorKind: "user", ActorID: actorID,
		Action: action, Target: target, Detail: detail, At: nowTS(),
	})
}

// repoDelete (DELETE /api/internal-git/repos/{name}) removes the ledger row and
// the bare. Tenant-scoped: only repos owned by the caller's tenant.
func (a gitServerAPI) repoDelete(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
	name := r.PathValue("name")
	if !validRepoName(name) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "invalid repo name"})
		return
	}
	if _, exists, err := a.store.GetGitRepo(r.Context(), mv.TenantID, name); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return
	}
	if err := a.store.DeleteGitRepo(r.Context(), mv.TenantID, name); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = os.RemoveAll(filepath.Join(a.dataRoot, "git", mv.TenantSlug, name+".git"))
	// The bare (incl. its lfs/objects) is gone; drop the LFS ledger + lock rows so
	// the tenant's capacity quota frees up and no stale locks linger.
	_ = a.store.DeleteLFSObjectsByRepo(r.Context(), mv.TenantID, name)
	_ = a.store.DeleteLFSLocksByRepo(r.Context(), mv.TenantID, name)
	a.auditGit(r.Context(), mv.TenantID, ident.ID, "internal_git.repo.delete", name, "")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// repoRename (POST /api/internal-git/repos/{name}/rename {new_name}) renames a
// repo: the ledger row and the on-disk bare move together. Existing clones keep
// their old origin URL and must update the remote to the new clone_url.
func (a gitServerAPI) repoRename(w http.ResponseWriter, r *http.Request, ident Identity, mv MembershipView) {
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
	if _, taken, err := a.store.GetGitRepo(r.Context(), mv.TenantID, newName); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if taken {
		writeAPIErr(w, &apiError{http.StatusConflict, "exists", "a repo with the new name already exists"})
		return
	}

	oldDir := filepath.Join(a.dataRoot, "git", mv.TenantSlug, oldName+".git")
	newDir := filepath.Join(a.dataRoot, "git", mv.TenantSlug, newName+".git")
	if err := os.Rename(oldDir, newDir); err != nil {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "rename_failed", err.Error()})
		return
	}
	if err := a.store.RenameGitRepo(r.Context(), mv.TenantID, oldName, newName); err != nil {
		_ = os.Rename(newDir, oldDir) // roll back the move so disk and ledger stay consistent
		writeAPIErr(w, internalErr(err))
		return
	}
	// The on-disk lfs/objects moved with the .git dir; repoint the LFS ledger + locks.
	_ = a.store.RenameLFSObjectsRepo(r.Context(), mv.TenantID, oldName, newName)
	_ = a.store.RenameLFSLocksRepo(r.Context(), mv.TenantID, oldName, newName)
	a.auditGit(r.Context(), mv.TenantID, ident.ID, "internal_git.repo.rename", oldName, "to="+newName)
	g.Name = newName
	writeJSON(w, http.StatusOK, a.repoDTO(mv.TenantSlug, g))
}

// branches (GET /api/internal-git/repos/{name}/branches) reads the bare's refs
// directly (no clone) for the RepoPicker branch list.
func (a gitServerAPI) branches(w http.ResponseWriter, r *http.Request, _ Identity, mv MembershipView) {
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
	dir := filepath.Join(a.dataRoot, "git", mv.TenantSlug, name+".git")
	out, err := exec.CommandContext(r.Context(), "git", "--git-dir", dir,
		"for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
	if err != nil {
		// bare 破損等を空配列 200 で隠さない
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
