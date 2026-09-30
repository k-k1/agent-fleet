package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Initialize Git Flow (ADR 0103 decision 9): the dialog's opening state and the write.

func gitflowDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir, ok := gitx.ResolveRepoDir(r.PathValue("name"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_repo", "invalid repo name")
		return "", false
	}
	if !gitx.IsGitRepo(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_git", "not a git working copy")
		return "", false
	}
	return dir, true
}

func gitflowReadOptions(dir string) branchrule.ReadOptions {
	origin, _ := gitx.GitOriginURL(dir)
	return branchrule.ReadOptions{ID: branchrule.RepoID(origin), Bitbucket: branchBitbucket}
}

// GET /repos/{name}/gitflow
func handleGetGitflow(w http.ResponseWriter, r *http.Request) {
	dir, ok := gitflowDir(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, branchrule.ReadGitflow(r.Context(), dir, gitflowReadOptions(dir)))
}

type gitflowInitRequest struct {
	// Expected is GET's current, the keys the dialog was opened with.
	Expected map[string]string        `json:"expected"`
	Values   branchrule.GitflowValues `json:"values"`
}

type gitflowInitOut struct {
	Written []string                `json:"written"`
	State   branchrule.GitflowState `json:"state"`
}

// gitflowWriteFailed is the 500 of a `git config` that failed part-way: the keys written
// before it travel with the error, so the dialog can say what state the clone is in.
type gitflowWriteFailed struct {
	Error   gitflowErrBody `json:"error"`
	Written []string       `json:"written"`
}

// gitflowErrOut is the usual error envelope plus the field the dialog marks.
type gitflowErrOut struct {
	Error gitflowErrBody `json:"error"`
}

type gitflowErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// POST /repos/{name}/gitflow/init writes git-flow's keys into the clone's config. The
// config is shared by every worktree of the repository, so a name that is a worktree
// initialises its parent clone; nothing switches a branch and nothing creates one.
func handleGitflowInit(w http.ResponseWriter, r *http.Request) {
	var req gitflowInitRequest
	if e := httpx.DecodeStrictJSON(r, &req, 8<<10); e != nil {
		httpx.WriteErr(w, e.Status, e.Code, e.Message)
		return
	}
	dir, ok := gitflowDir(w, r)
	if !ok {
		return
	}
	v := req.Values
	for _, p := range []*string{&v.Production, &v.Development, &v.Feature, &v.Bugfix, &v.Release, &v.Hotfix, &v.VersionTag} {
		*p = strings.TrimSpace(*p)
	}
	written, err := branchrule.InitGitflow(dir, req.Expected, v)
	var inv *branchrule.GitflowInvalidError
	var miss *branchrule.GitflowBranchMissingError
	var wf *branchrule.GitflowWriteError
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, gitflowInitOut{Written: written, State: branchrule.ReadGitflow(r.Context(), dir, gitflowReadOptions(dir))})
	case errors.Is(err, branchrule.ErrGitflowChanged):
		httpx.WriteErr(w, http.StatusConflict, "gitflow_changed", err.Error())
	case errors.As(err, &inv):
		httpx.WriteJSON(w, http.StatusBadRequest, gitflowErrOut{Error: gitflowErrBody{Code: "invalid_value", Message: err.Error(), Field: inv.Field}})
	case errors.As(err, &miss):
		httpx.WriteJSON(w, http.StatusBadRequest, gitflowErrOut{Error: gitflowErrBody{Code: "branch_missing", Message: err.Error(), Field: miss.Field}})
	case errors.As(err, &wf):
		httpx.WriteJSON(w, http.StatusInternalServerError, gitflowWriteFailed{
			Error: gitflowErrBody{Code: "write_failed", Message: err.Error()}, Written: wf.Written,
		})
	default:
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
	}
}
