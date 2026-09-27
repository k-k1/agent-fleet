package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func recreateServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent) // main, plus a "feature" branch

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /repos/{name}", gitx.HandleDeleteRepo)
	mux.HandleFunc("DELETE /repos/{name}/branch", handleDeleteBranch)
	mux.HandleFunc("GET /repos/{name}/recreate", handleRecreatePlan)
	mux.HandleFunc("POST /repos/{name}/recreate", handleRecreateWorktree)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, parent
}

// TestRecreateDeletedWorktreeFlow deletes a worktree and its branch through the Console's own
// endpoints, then puts the worktree back at the same path from the SHA the branch delete
// archived — the path the archived session's id is derived from.
func TestRecreateDeletedWorktreeFlow(t *testing.T) {
	srv, parent := recreateServer(t)
	root := filepath.Dir(parent)
	wt := filepath.Join(root, "app@feat-z")
	gitAt(t, parent, "worktree", "add", "-q", "-b", "feat-z", wt, "main")
	sha := strings.TrimSpace(gitAt(t, wt, "rev-parse", "HEAD"))
	session.WriteMeta(session.Meta{Name: "old", Kind: session.KindClaude, Dir: wt, Branch: "feat-z",
		CreatedAt: "2026-09-01T00:00:00Z", Archived: true})
	session.WriteMeta(session.Meta{Name: "older", Kind: session.KindClaude, Dir: wt, Branch: "feat-renamed-away",
		CreatedAt: "2026-08-01T00:00:00Z", Archived: true})

	// The folder is still there: nothing to recreate.
	if code := httpStatus(t, srv, "GET", "/repos/app@feat-z/recreate", nil); code != http.StatusConflict {
		t.Fatalf("plan for a live folder = %d, want 409", code)
	}

	do(t, srv, "DELETE", "/repos/app@feat-z", nil, http.StatusOK, nil)
	do(t, srv, "DELETE", "/repos/app/branch?branch=feat-z", nil, http.StatusOK, nil)
	if gitx.GitBranchExists(parent, "feat-z") {
		t.Fatal("branch survived its delete")
	}

	var plan recreatePlan
	do(t, srv, "GET", "/repos/app@feat-z/recreate", nil, http.StatusOK, &plan)
	// The delete's own record comes first (clean tree: no snapshot), then the SHA the branch
	// delete archived.
	if plan.Path != wt || plan.Parent != "app" || len(plan.Candidates) != 2 ||
		plan.Candidates[0] != (gitx.RecreateCandidate{Source: gitx.RecreateDeleted, Branch: "feat-z", SHA: sha}) ||
		plan.Candidates[1] != (gitx.RecreateCandidate{Source: gitx.RecreateTrash, Branch: "feat-z", SHA: sha}) {
		t.Fatalf("plan = %+v, want the deleted state then the archived SHA of feat-z at %s", plan, wt)
	}

	// A candidate the plan did not offer is refused rather than trusted.
	if code := httpStatus(t, srv, "POST", "/repos/app@feat-z/recreate",
		map[string]any{"source": "local", "branch": "feat-z"}); code != http.StatusConflict {
		t.Fatalf("stale candidate = %d, want 409", code)
	}

	var res map[string]any
	do(t, srv, "POST", "/repos/app@feat-z/recreate", map[string]any{"source": "trash", "branch": "feat-z"}, http.StatusCreated, &res)
	if res["path"] != wt || res["branch"] != "feat-z" {
		t.Fatalf("recreate = %v", res)
	}
	if got := gitx.GitCurrentBranch(wt); got != "feat-z" || !gitx.IsLinkedWorktree(wt) {
		t.Fatalf("recreated worktree on %q linked=%v", got, gitx.IsLinkedWorktree(wt))
	}
	// The sessions' id is keyed on this exact path, so their meta needs no edit to resume.
	if m, _ := session.ReadMeta("old"); m.Dir != wt {
		t.Fatalf("session dir = %q", m.Dir)
	}
}

func TestRecreateWorktreeRefusals(t *testing.T) {
	srv, parent := recreateServer(t)
	root := filepath.Dir(parent)

	if code := httpStatus(t, srv, "GET", "/repos/nowhere@x/recreate", nil); code != http.StatusNotFound {
		t.Errorf("no parent = %d, want 404", code)
	}
	if code := httpStatus(t, srv, "GET", "/repos/..@x/recreate", nil); code != http.StatusBadRequest {
		t.Errorf("bad name = %d, want 400", code)
	}

	// The recorded branch is checked out in another worktree: branch_in_use naming it, and a new
	// branch at its tip as the way through.
	gitAt(t, parent, "worktree", "add", "-q", filepath.Join(root, "app@holder"), "feature")
	gone := filepath.Join(root, "app@feature")
	session.WriteMeta(session.Meta{Name: "s1", Kind: session.KindClaude, Dir: gone, Branch: "feature", Archived: true})
	var plan recreatePlan
	do(t, srv, "GET", "/repos/app@feature/recreate", nil, http.StatusOK, &plan)
	if len(plan.Candidates) != 1 || plan.Candidates[0].InUse != "app@holder" {
		t.Fatalf("plan = %+v, want feature in use by app@holder", plan)
	}
	var busy struct {
		Error struct{ Code, Worktree string }
	}
	do(t, srv, "POST", "/repos/app@feature/recreate", map[string]any{"source": "local", "branch": "feature"}, http.StatusConflict, &busy)
	if busy.Error.Code != errCodeBranchInUse || busy.Error.Worktree != "app@holder" {
		t.Fatalf("in use = %+v", busy)
	}
	for nb, want := range map[string]int{"main": http.StatusConflict, "-x": http.StatusBadRequest, "a..b": http.StatusBadRequest} {
		if code := httpStatus(t, srv, "POST", "/repos/app@feature/recreate",
			map[string]any{"source": "local", "branch": "feature", "new_branch": nb}); code != want {
			t.Errorf("new_branch %q = %d, want %d", nb, code, want)
		}
	}
	do(t, srv, "POST", "/repos/app@feature/recreate",
		map[string]any{"source": "local", "branch": "feature", "new_branch": "feature-2"}, http.StatusCreated, nil)
	if got := gitx.GitCurrentBranch(gone); got != "feature-2" {
		t.Fatalf("recreated on %q, want feature-2", got)
	}

	// A plain folder at the path is never touched.
	junk := filepath.Join(root, "app@junk")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := httpStatus(t, srv, "POST", "/repos/app@junk/recreate",
		map[string]any{"source": "new", "branch": "junk"}); code != http.StatusConflict {
		t.Errorf("occupied path = %d, want 409", code)
	}
}

// A worktree removed behind git's back stays registered, with its branch, until pruned. The
// plan must not read that leftover as "checked out elsewhere" — it is the folder being
// recreated — and the POST must get past it.
func TestRecreateWorktreeDeletedOutsideTheConsole(t *testing.T) {
	srv, parent := recreateServer(t)
	wt := filepath.Join(filepath.Dir(parent), "app@st")
	gitAt(t, parent, "worktree", "add", "-q", "-b", "st", wt, "main")
	session.WriteMeta(session.Meta{Name: "s", Kind: session.KindClaude, Dir: wt, Branch: "st", Archived: true})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	var plan recreatePlan
	do(t, srv, "GET", "/repos/app@st/recreate", nil, http.StatusOK, &plan)
	if len(plan.Candidates) != 1 || plan.Candidates[0].Source != gitx.RecreateLocal || plan.Candidates[0].InUse != "" {
		t.Fatalf("plan = %+v, want local st, not in use", plan)
	}
	do(t, srv, "POST", "/repos/app@st/recreate", map[string]any{"source": "local", "branch": "st"}, http.StatusCreated, nil)
	if got := gitx.GitCurrentBranch(wt); got != "st" {
		t.Fatalf("recreated on %q, want st", got)
	}
}

// The branch the parent working copy itself has checked out is as unavailable as one held by
// another worktree: the plan says so, instead of the add failing with git's raw error.
func TestRecreateWorktreeBranchHeldByTheParent(t *testing.T) {
	srv, parent := recreateServer(t)
	gone := filepath.Join(filepath.Dir(parent), "app@main")
	session.WriteMeta(session.Meta{Name: "s", Kind: session.KindClaude, Dir: gone, Branch: "main", Archived: true})

	var plan recreatePlan
	do(t, srv, "GET", "/repos/app@main/recreate", nil, http.StatusOK, &plan)
	if len(plan.Candidates) != 1 || plan.Candidates[0].InUse != "app" {
		t.Fatalf("plan = %+v, want main in use by app", plan)
	}
	if code := httpStatus(t, srv, "POST", "/repos/app@main/recreate",
		map[string]any{"source": "local", "branch": "main"}); code != http.StatusConflict {
		t.Fatalf("recreate on the parent's branch = %d, want 409", code)
	}
	do(t, srv, "POST", "/repos/app@main/recreate",
		map[string]any{"source": "local", "branch": "main", "new_branch": "main-2"}, http.StatusCreated, nil)
	if got := gitx.GitCurrentBranch(gone); got != "main-2" {
		t.Fatalf("recreated on %q, want main-2", got)
	}
}
