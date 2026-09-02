package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Git LFS file-locking API for the internal git provider (docs/reference/
// internal-git-provider, P3). Implements the four endpoints git-lfs uses:
// create / list / verify / unlock, under <repo>.git/info/lfs/locks. Auth +
// tenant confinement reuse authorizeGitRepo; create/unlock additionally require
// write (canPush), list/verify need only read. Locks are tenant+repo scoped and
// follow their repo on delete/rename.

// lfsLockDTO renders a lock in the wire shape git-lfs expects.
func lfsLockDTO(l store.LFSLock) map[string]any {
	owner := l.OwnerName
	if owner == "" {
		owner = l.OwnerID
	}
	return map[string]any{
		"id":        l.ID,
		"path":      l.Path,
		"locked_at": l.LockedAt,
		"owner":     map[string]any{"name": owner},
	}
}

func lfsRefName(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := m["name"].(string); ok {
		return s
	}
	return ""
}

// lfsLockCreate (POST .../info/lfs/locks) creates a lock on a path. A path
// already locked returns 409 with the existing lock (the git-lfs contract).
func (a gitServerAPI) lfsLockCreate(w http.ResponseWriter, r *http.Request) {
	slug, repoSeg := r.PathValue("slug"), r.PathValue("repo")
	name, mv, membershipID, aerr := a.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeLFS(w)
		return
	}
	if !canPush(mv.Role) {
		writeLFSErr(w, http.StatusForbidden, "read-only: locking requires write access")
		return
	}
	var body struct {
		Path string `json:"path"`
		Ref  any    `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		writeLFSErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if existing, ok, err := a.store.GetLFSLockByPath(r.Context(), mv.TenantID, name, body.Path); err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "store error")
		return
	} else if ok {
		w.Header().Set("Content-Type", lfsContentType)
		writeJSON(w, http.StatusConflict, map[string]any{
			"lock": lfsLockDTO(existing), "message": "already created lock",
		})
		return
	}
	ownerName, _ := a.store.MembershipOwnerName(r.Context(), membershipID)
	lock := store.LFSLock{
		ID: store.NewID(), TenantID: mv.TenantID, RepoName: name, Path: body.Path,
		RefName: lfsRefName(body.Ref), OwnerID: membershipID, OwnerName: ownerName, LockedAt: store.NowTS(),
	}
	if err := a.store.CreateLFSLock(r.Context(), lock); err != nil {
		// Lost a race on the (tenant, repo, path) uniqueness → report the winner.
		if existing, ok, gerr := a.store.GetLFSLockByPath(r.Context(), mv.TenantID, name, body.Path); gerr == nil && ok {
			w.Header().Set("Content-Type", lfsContentType)
			writeJSON(w, http.StatusConflict, map[string]any{"lock": lfsLockDTO(existing), "message": "already created lock"})
			return
		}
		writeLFSErr(w, http.StatusInternalServerError, "store error")
		return
	}
	w.Header().Set("Content-Type", lfsContentType)
	writeJSON(w, http.StatusCreated, map[string]any{"lock": lfsLockDTO(lock)})
}

// lfsLocksList (GET .../info/lfs/locks) lists locks, optionally filtered by
// ?path= or ?id=, paginated by ?cursor= / ?limit=.
func (a gitServerAPI) lfsLocksList(w http.ResponseWriter, r *http.Request) {
	slug, repoSeg := r.PathValue("slug"), r.PathValue("repo")
	name, mv, _, aerr := a.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeLFS(w)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	locks, next, err := a.store.ListLFSLocks(r.Context(), mv.TenantID, name, q.Get("path"), q.Get("id"), limit, q.Get("cursor"))
	if err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "store error")
		return
	}
	out := make([]map[string]any, 0, len(locks))
	for _, l := range locks {
		out = append(out, lfsLockDTO(l))
	}
	w.Header().Set("Content-Type", lfsContentType)
	writeJSON(w, http.StatusOK, map[string]any{"locks": out, "next_cursor": next})
}

// lfsLocksVerify (POST .../info/lfs/locks/verify) partitions locks into the
// caller's own ("ours") and everyone else's ("theirs"); git-lfs checks "theirs"
// before a push to block changes to files locked by others.
func (a gitServerAPI) lfsLocksVerify(w http.ResponseWriter, r *http.Request) {
	slug, repoSeg := r.PathValue("slug"), r.PathValue("repo")
	name, mv, membershipID, aerr := a.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeLFS(w)
		return
	}
	var body struct {
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	locks, next, err := a.store.ListLFSLocks(r.Context(), mv.TenantID, name, "", "", body.Limit, body.Cursor)
	if err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "store error")
		return
	}
	ours, theirs := []map[string]any{}, []map[string]any{}
	for _, l := range locks {
		if l.OwnerID == membershipID {
			ours = append(ours, lfsLockDTO(l))
		} else {
			theirs = append(theirs, lfsLockDTO(l))
		}
	}
	w.Header().Set("Content-Type", lfsContentType)
	writeJSON(w, http.StatusOK, map[string]any{"ours": ours, "theirs": theirs, "next_cursor": next})
}

// lfsUnlock (POST .../info/lfs/locks/{id}/unlock) releases a lock. Only the
// owner may unlock unless force=true AND the caller is a tenant_admin (an operator
// override for abandoned locks).
func (a gitServerAPI) lfsUnlock(w http.ResponseWriter, r *http.Request) {
	slug, repoSeg, id := r.PathValue("slug"), r.PathValue("repo"), r.PathValue("id")
	name, mv, membershipID, aerr := a.authorizeGitRepo(r, slug, repoSeg)
	if aerr != nil {
		aerr.writeLFS(w)
		return
	}
	if !canPush(mv.Role) {
		writeLFSErr(w, http.StatusForbidden, "read-only: unlocking requires write access")
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	lock, ok, err := a.store.GetLFSLock(r.Context(), mv.TenantID, name, id)
	if err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if !ok {
		writeLFSErr(w, http.StatusNotFound, "lock does not exist")
		return
	}
	if lock.OwnerID != membershipID && !(body.Force && mv.Role == "tenant_admin") {
		writeLFSErr(w, http.StatusForbidden, "lock belongs to another user (force unlock requires tenant_admin)")
		return
	}
	if err := a.store.DeleteLFSLock(r.Context(), mv.TenantID, name, id); err != nil {
		writeLFSErr(w, http.StatusInternalServerError, "store error")
		return
	}
	w.Header().Set("Content-Type", lfsContentType)
	writeJSON(w, http.StatusOK, map[string]any{"lock": lfsLockDTO(lock)})
}
