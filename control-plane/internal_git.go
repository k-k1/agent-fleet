package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	_, mv, ok := c.membershipFor(w, r)
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
	writeJSON(w, http.StatusOK, c.gitRepoDTO(mv.TenantSlug, g))
}

// handleInternalGitRepoDelete (DELETE /api/internal-git/repos/{name}) removes the
// ledger row and the bare. Tenant-scoped: only repos owned by the caller's tenant.
func (c config) handleInternalGitRepoDelete(w http.ResponseWriter, r *http.Request) {
	_, mv, ok := c.membershipFor(w, r)
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
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
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
