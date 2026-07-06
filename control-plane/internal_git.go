package main

import (
	"context"
	"encoding/json"
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
// to the caller's resolved tenant (X-AF-Tenant → membershipFor).

// gitCloneURL builds the clone URL a workspace container uses. It is the public
// base (Caddy TLS terminus, reachable from the container via hairpin NAT) so the
// unified cred helper's token injection authenticates it transparently.
func (c config) gitCloneURL(slug, name string) string {
	return strings.TrimRight(c.publicBaseURL, "/") + "/git/" + slug + "/" + name + ".git"
}

func (c config) gitRepoDTO(slug string, g GitRepo) map[string]any {
	return map[string]any{
		"name":           g.Name,
		"default_branch": g.DefaultBranch,
		"clone_url":      c.gitCloneURL(slug, g.Name),
		"created_at":     g.CreatedAt,
		"provider":       "internal",
	}
}

// handleInternalGitReposList (GET /api/internal-git/repos) lists the tenant's
// internal repos for the RepoPicker/GitTab.
func (c config) handleInternalGitReposList(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	repos, err := c.mgr.store.ListGitReposByTenant(r.Context(), mv.TenantID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(repos))
	for _, g := range repos {
		out = append(out, c.gitRepoDTO(mv.TenantSlug, g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

// handleInternalGitRepoCreate (POST /api/internal-git/repos {name}) creates a bare
// repo on disk and its ledger row, then returns the clone URL. Idempotent-ish:
// a duplicate name is a 409.
func (c config) handleInternalGitRepoCreate(w http.ResponseWriter, r *http.Request) {
	ident, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	if c.publicBaseURL == "" {
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
	if aerr := c.enforceGitRepoQuota(r.Context(), mv.TenantID); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if _, exists, err := c.mgr.store.GetGitRepo(r.Context(), mv.TenantID, name); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if exists {
		writeAPIErr(w, &apiError{http.StatusConflict, "exists", "a repo with that name already exists"})
		return
	}

	dir := filepath.Join(c.mgr.dataRoot, "git", mv.TenantSlug, name+".git")
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
	if err := c.mgr.store.CreateGitRepo(r.Context(), g); err != nil {
		_ = os.RemoveAll(dir) // roll back the bare so a retry isn't blocked by an orphan
		writeAPIErr(w, internalErr(err))
		return
	}
	c.auditGit(r.Context(), mv.TenantID, ident.ID, "internal_git.repo.create", name, "branch="+branch)
	writeJSON(w, http.StatusOK, c.gitRepoDTO(mv.TenantSlug, g))
}

// enforceGitRepoQuota returns a 409 apiError when the tenant is at or over its
// max_git_repos cap (0 = unlimited). Nil when creation is allowed.
func (c config) enforceGitRepoQuota(ctx context.Context, tenantID string) *apiError {
	t, err := c.mgr.store.GetTenant(ctx, tenantID)
	if err != nil {
		return internalErr(err)
	}
	max := parseLimits(t.Limits).MaxGitRepos
	if max <= 0 {
		return nil
	}
	n, err := c.mgr.store.CountGitReposByTenant(ctx, tenantID)
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
func (c config) auditGit(ctx context.Context, tenantID, actorID, action, target, detail string) {
	_ = c.mgr.store.InsertAudit(ctx, AuditLog{
		ID: newID(), TenantID: tenantID, ActorKind: "user", ActorID: actorID,
		Action: action, Target: target, Detail: detail, At: nowTS(),
	})
}

// handleInternalGitRepoDelete (DELETE /api/internal-git/repos/{name}) removes the
// ledger row and the bare. Tenant-scoped: only repos owned by the caller's tenant.
func (c config) handleInternalGitRepoDelete(w http.ResponseWriter, r *http.Request) {
	ident, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !validRepoName(name) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "invalid repo name"})
		return
	}
	if _, exists, err := c.mgr.store.GetGitRepo(r.Context(), mv.TenantID, name); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return
	}
	if err := c.mgr.store.DeleteGitRepo(r.Context(), mv.TenantID, name); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = os.RemoveAll(filepath.Join(c.mgr.dataRoot, "git", mv.TenantSlug, name+".git"))
	// The bare (incl. its lfs/objects) is gone; drop the LFS ledger rows so the
	// tenant's capacity quota frees up.
	_ = c.mgr.store.DeleteLFSObjectsByRepo(r.Context(), mv.TenantID, name)
	c.auditGit(r.Context(), mv.TenantID, ident.ID, "internal_git.repo.delete", name, "")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// handleInternalGitRepoRename (POST /api/internal-git/repos/{name}/rename {new_name})
// renames a repo: the ledger row and the on-disk bare move together. Existing clones
// keep their old origin URL and must update the remote to the new clone_url.
func (c config) handleInternalGitRepoRename(w http.ResponseWriter, r *http.Request) {
	ident, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
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
	g, exists, err := c.mgr.store.GetGitRepo(r.Context(), mv.TenantID, oldName)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return
	}
	if _, taken, err := c.mgr.store.GetGitRepo(r.Context(), mv.TenantID, newName); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if taken {
		writeAPIErr(w, &apiError{http.StatusConflict, "exists", "a repo with the new name already exists"})
		return
	}

	oldDir := filepath.Join(c.mgr.dataRoot, "git", mv.TenantSlug, oldName+".git")
	newDir := filepath.Join(c.mgr.dataRoot, "git", mv.TenantSlug, newName+".git")
	if err := os.Rename(oldDir, newDir); err != nil {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "rename_failed", err.Error()})
		return
	}
	if err := c.mgr.store.RenameGitRepo(r.Context(), mv.TenantID, oldName, newName); err != nil {
		_ = os.Rename(newDir, oldDir) // roll back the move so disk and ledger stay consistent
		writeAPIErr(w, internalErr(err))
		return
	}
	// The on-disk lfs/objects moved with the .git dir; repoint the LFS ledger rows.
	_ = c.mgr.store.RenameLFSObjectsRepo(r.Context(), mv.TenantID, oldName, newName)
	c.auditGit(r.Context(), mv.TenantID, ident.ID, "internal_git.repo.rename", oldName, "to="+newName)
	g.Name = newName
	writeJSON(w, http.StatusOK, c.gitRepoDTO(mv.TenantSlug, g))
}

// handleInternalGitBranches (GET /api/internal-git/repos/{name}/branches) reads
// the bare's refs directly (no clone) for the RepoPicker branch list.
func (c config) handleInternalGitBranches(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !validRepoName(name) {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_name", "invalid repo name"})
		return
	}
	g, exists, err := c.mgr.store.GetGitRepo(r.Context(), mv.TenantID, name)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !exists {
		writeAPIErr(w, &apiError{http.StatusNotFound, "not_found", "no such repo"})
		return
	}
	dir := filepath.Join(c.mgr.dataRoot, "git", mv.TenantSlug, name+".git")
	out, err := exec.CommandContext(r.Context(), "git", "--git-dir", dir,
		"for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
	branches := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			branches = append(branches, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": branches, "default_branch": g.DefaultBranch})
}
