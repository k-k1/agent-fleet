package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Repositories are plain working copies under ~/repos/<name>. The folder name
// is the repo id — there is no metadata store in this phase (docs/09 §9.6).
// git auth uses the user's ~/.ssh keys; the Control Plane never holds keys and
// delegates every operation here (docs/06 §6.4, docs/07 §7.3).

// repoNameRe constrains a repo (folder) name; it doubles as path-traversal guard.
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$`)

func reposRoot() string { return filepath.Join(homeDir(), "repos") }

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// resolveRepoDir maps a validated name to its working-copy path.
func resolveRepoDir(name string) (string, bool) {
	if !repoNameRe.MatchString(name) {
		return "", false
	}
	return filepath.Join(reposRoot(), name), true
}

func isGitRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular()) // dir, or a file for worktrees/submodules
}

// Repo is the list-view representation of a working copy.
type Repo struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Dirty  bool   `json:"dirty"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
}

// RepoStatus mirrors docs/06 §6.4's status response shape.
type RepoStatus struct {
	Branch    string `json:"branch"`
	Detached  bool   `json:"detached"`
	Dirty     bool   `json:"dirty"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
}

// gitStatus parses `git status --porcelain=v2 --branch` into a RepoStatus.
// porcelain=v2 is stable across git versions and unambiguous to parse, unlike
// the human-readable output.
func gitStatus(dir string) (RepoStatus, error) {
	var s RepoStatus
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain=v2", "--branch").Output()
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			h := strings.TrimPrefix(line, "# branch.head ")
			if h == "(detached)" {
				s.Detached = true
			} else {
				s.Branch = h
			}
		case strings.HasPrefix(line, "# branch.ab "):
			// "# branch.ab +N -M"
			if f := strings.Fields(strings.TrimPrefix(line, "# branch.ab ")); len(f) == 2 {
				s.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				s.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			// "<1|2> <XY> ..." — X=staged, Y=worktree; '.' means unchanged.
			if f := strings.Fields(line); len(f) >= 2 && len(f[1]) == 2 {
				if f[1][0] != '.' {
					s.Staged++
				}
				if f[1][1] != '.' {
					s.Unstaged++
				}
			}
		case strings.HasPrefix(line, "u "):
			s.Unstaged++ // unmerged path needs attention
		case strings.HasPrefix(line, "? "):
			s.Untracked++
		}
	}
	if s.Detached {
		s.Branch = "(detached)"
	}
	s.Dirty = s.Staged+s.Unstaged+s.Untracked > 0
	return s, nil
}

func handleListRepos(w http.ResponseWriter, r *http.Request) {
	repos := []Repo{}
	entries, _ := os.ReadDir(reposRoot()) // missing root => empty list
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(reposRoot(), e.Name())
		if !isGitRepo(dir) {
			continue
		}
		st, _ := gitStatus(dir)
		repos = append(repos, Repo{
			Name: e.Name(), Path: dir, Branch: st.Branch,
			Dirty: st.Dirty, Ahead: st.Ahead, Behind: st.Behind,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

type cloneReq struct {
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`
	Name      string `json:"name"`
}

// deriveRepoName turns a clone URL into a folder name: last path segment minus
// a trailing ".git" (handles both scp-like and URL forms).
func deriveRepoName(remote string) string {
	s := strings.TrimSuffix(strings.TrimRight(remote, "/"), ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func handleCloneRepo(w http.ResponseWriter, r *http.Request) {
	var req cloneReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.RemoteURL = strings.TrimSpace(req.RemoteURL)
	if req.RemoteURL == "" || strings.HasPrefix(req.RemoteURL, "-") {
		writeErr(w, http.StatusBadRequest, "bad_url", "remote_url is required and must not start with '-'")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = deriveRepoName(req.RemoteURL)
	}
	dir, ok := resolveRepoDir(name)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_name", "name must match [A-Za-z0-9][A-Za-z0-9._-]{0,59}")
		return
	}
	if _, err := os.Stat(dir); err == nil {
		writeErr(w, http.StatusConflict, "exists", "repo already exists: "+name)
		return
	}
	if err := os.MkdirAll(reposRoot(), 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir_failed", err.Error())
		return
	}

	args := []string{"clone"}
	if b := strings.TrimSpace(req.Branch); b != "" && !strings.HasPrefix(b, "-") {
		args = append(args, "--branch", b)
	}
	args = append(args, "--", req.RemoteURL, dir)
	cmd := exec.Command("git", args...)
	// Never block on an interactive credential/host-key prompt; fail fast instead.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir) // don't leave a half-clone behind
		writeErr(w, http.StatusBadGateway, "clone_failed", fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	st, _ := gitStatus(dir)
	writeJSON(w, http.StatusCreated, Repo{
		Name: name, Path: dir, Branch: st.Branch, Dirty: st.Dirty, Ahead: st.Ahead, Behind: st.Behind,
	})
}

// repoDirFromPath validates {name} and ensures the working copy exists.
func repoDirFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir, ok := resolveRepoDir(r.PathValue("name"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_name", "invalid repo name")
		return "", false
	}
	if !isGitRepo(dir) {
		writeErr(w, http.StatusNotFound, "not_found", "no such repo: "+r.PathValue("name"))
		return "", false
	}
	return dir, true
}

func handleRepoStatus(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	st, err := gitStatus(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func handleRepoBranches(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	local := gitRefs(dir, "refs/heads")
	remote := gitRefs(dir, "refs/remotes")
	st, _ := gitStatus(dir)
	writeJSON(w, http.StatusOK, map[string]any{"local": local, "remote": remote, "current": st.Branch})
}

// gitRefs lists short ref names under a namespace, one per line. Ref names
// contain no spaces, so a bare for-each-ref format parses cleanly.
func gitRefs(dir, namespace string) []string {
	refs := []string{}
	out, err := exec.Command("git", "-C", dir, "for-each-ref", "--format=%(refname:short)", namespace).Output()
	if err != nil {
		return refs
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			refs = append(refs, l)
		}
	}
	return refs
}

type checkoutReq struct {
	Branch string `json:"branch"`
	Ref    string `json:"ref"`
	Create bool   `json:"create"`
}

func handleRepoCheckout(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	var req checkoutReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	ref := strings.TrimSpace(req.Branch)
	if ref == "" {
		ref = strings.TrimSpace(req.Ref)
	}
	if ref == "" || strings.HasPrefix(ref, "-") {
		writeErr(w, http.StatusBadRequest, "bad_ref", "branch/ref is required and must not start with '-'")
		return
	}
	args := []string{"-C", dir, "checkout"}
	if req.Create {
		args = append(args, "-b")
	}
	args = append(args, ref)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadGateway, "checkout_failed", fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	st, _ := gitStatus(dir)
	writeJSON(w, http.StatusOK, st)
}

type fetchReq struct {
	Prune bool `json:"prune"`
}

func handleRepoFetch(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	var req fetchReq
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body is fine
	args := []string{"-C", dir, "fetch"}
	if req.Prune {
		args = append(args, "--prune")
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadGateway, "fetch_failed", fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	st, _ := gitStatus(dir)
	writeJSON(w, http.StatusOK, st)
}

func handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("name")})
}
