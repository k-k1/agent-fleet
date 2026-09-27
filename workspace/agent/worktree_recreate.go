package main

// Recreating a deleted worktree at its original path (issue #1040). The git work is
// gitx.ResolveRecreate / gitx.RecreateWorktreeAt; this file adds what only package main
// knows — the sessions that ran in the folder and the SHAs of branches the Console deleted —
// and the HTTP shell. Bringing the sessions back is the ordinary restore / resume: once the
// folder is back at the same path, every kind finds its conversation again.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// recreatePlan is GET /repos/{name}/recreate.
type recreatePlan struct {
	Name       string                   `json:"name"`
	Path       string                   `json:"path"`
	Parent     string                   `json:"parent"`
	Candidates []gitx.RecreateCandidate `json:"candidates"`
}

// recreateResult is POST /repos/{name}/recreate's answer: the folder that is back and the
// branch it is on (NewBranch when one was asked for).
type recreateResult struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Source string `json:"source"`
}

type recreateReq struct {
	Source    string `json:"source"`
	Branch    string `json:"branch"`
	NewBranch string `json:"new_branch"`
}

// recreateTarget validates {name} as a deleted worktree folder and resolves its parent,
// answering the error itself when it is not one.
func recreateTarget(w http.ResponseWriter, r *http.Request) (name, dir, parent string, ok bool) {
	name = r.PathValue("name")
	dir, ok = gitx.ResolveRepoDir(name)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid repo name")
		return "", "", "", false
	}
	if _, err := os.Lstat(dir); err == nil {
		httpx.WriteErr(w, http.StatusConflict, errCodeRecreatePathExists,
			"something already exists at "+name+"; only a folder that is gone can be recreated")
		return "", "", "", false
	}
	parent, ok = gitx.RecreateParent(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, errCodeRecreateParentMissing,
			"no git working copy this folder could have been a worktree of: "+name)
		return "", "", "", false
	}
	return name, dir, parent, true
}

// recreateCandidates resolves the ways to put dir back, from the branches its sessions
// recorded (newest session first) and the branches the Console archived before deleting.
func recreateCandidates(dir, parent string) []gitx.RecreateCandidate {
	return gitx.ResolveRecreate(parent, filepath.Base(dir), sessionBranchesIn(dir), trashedBranchSHA(parent), latestTombstone(dir))
}

// sessionBranchesIn lists the start branches of the sessions whose working copy was dir,
// newest session first. Archived or not: a restored row keeps its Dir too.
func sessionBranchesIn(dir string) []string {
	var ms []session.Meta
	for _, m := range session.ListMetas() {
		if filepath.Clean(m.Dir) == dir && m.Branch != "" {
			ms = append(ms, m)
		}
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].CreatedAt > ms[j].CreatedAt })
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Branch)
	}
	return out
}

// trashedBranchSHA answers the SHA of the newest branch deletion the cleanup archive recorded
// for parent's repository. The record names the working copy the delete was issued from,
// which may be the parent or any of its worktrees — all of them share the refs.
func trashedBranchSHA(parent string) func(branch string) string {
	var archives []cleanupManifest
	loaded := false
	base := filepath.Base(parent)
	return func(branch string) string {
		if !loaded {
			archives, loaded = listCleanupArchives(), true
		}
		for _, m := range archives { // newest first
			for _, b := range m.Branches {
				if b.Name == branch && b.SHA != "" && (b.Repo == base || strings.HasPrefix(b.Repo, base+"@")) {
					return b.SHA
				}
			}
		}
		return ""
	}
}

func handleRecreatePlan(w http.ResponseWriter, r *http.Request) {
	name, dir, parent, ok := recreateTarget(w, r)
	if !ok {
		return
	}
	cands := recreateCandidates(dir, parent)
	if cands == nil {
		cands = []gitx.RecreateCandidate{}
	}
	httpx.WriteJSON(w, http.StatusOK, recreatePlan{Name: name, Path: dir, Parent: filepath.Base(parent), Candidates: cands})
}

// handleRecreateWorktree is POST /repos/{name}/recreate. The client names the candidate it
// was shown by source and branch; the server resolves again rather than trusting a SHA from
// the request, and answers recreate_stale when that candidate no longer resolves. It runs in
// the deletion gate so a delete of the parent cannot interleave with the worktree add.
func handleRecreateWorktree(w http.ResponseWriter, r *http.Request) {
	var req recreateReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	sessionx.WithDeletionGate(func() { recreateWorktreeGated(w, r, req) })
}

func recreateWorktreeGated(w http.ResponseWriter, r *http.Request, req recreateReq) {
	name, dir, parent, ok := recreateTarget(w, r)
	if !ok {
		return
	}
	var c *gitx.RecreateCandidate
	for _, cand := range recreateCandidates(dir, parent) {
		if cand.Source == req.Source && cand.Branch == req.Branch {
			c = &cand
			break
		}
	}
	if c == nil {
		httpx.WriteErr(w, http.StatusConflict, errCodeRecreateStale,
			"that way of recreating the working copy is no longer available; reload and choose again")
		return
	}
	nb := strings.TrimSpace(req.NewBranch)
	if nb != "" {
		if !gitx.ValidBranchName(parent, nb) {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_branch", "invalid branch name")
			return
		}
		if local, remote := gitx.BranchNameStatus(parent, nb); local {
			httpx.WriteErr(w, http.StatusConflict, "branch_exists", "branch "+nb+" already exists locally; choose another name")
			return
		} else if remote {
			httpx.WriteErr(w, http.StatusConflict, "branch_exists_remote", "a remote branch "+nb+" already exists; choose another name")
			return
		}
	} else if c.InUse != "" {
		gitx.WriteBranchInUse(w, c.Branch, c.InUse)
		return
	} else if c.NeedsNewBranch() {
		httpx.WriteErr(w, http.StatusConflict, errCodeRecreateNeedsNewBranch,
			"the branch has moved since the delete, or there was none; choose a new branch name")
		return
	}
	if err := gitx.RecreateWorktreeAt(parent, dir, *c, nb); err != nil {
		if errors.Is(err, gitx.ErrRecreatePathExists) {
			httpx.WriteErr(w, http.StatusConflict, errCodeRecreatePathExists, err.Error())
			return
		}
		httpx.WriteErr(w, http.StatusBadGateway, errCodeRecreateFailed, err.Error())
		return
	}
	branch := c.Branch
	if nb != "" {
		branch = nb
	}
	httpx.WriteJSON(w, http.StatusCreated, recreateResult{Name: name, Path: dir, Branch: branch, Source: c.Source})
}
