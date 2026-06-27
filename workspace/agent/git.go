package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

// gitClone clones remoteURL into dir (optionally at branch). GIT_TERMINAL_PROMPT=0
// fails fast instead of blocking on an interactive credential/host-key prompt.
// A failed clone leaves no half-written directory behind.
func gitClone(dir, remoteURL, branch string) error {
	if err := os.MkdirAll(reposRoot(), 0o755); err != nil {
		return err
	}
	// The parent clone must NOT use --recurse-submodules: a submodule pinned to an
	// SSH URL (git@host:) with no SSH key fails "Host key verification failed" and
	// would abort the whole clone. Clone the parent first, then fetch submodules
	// best-effort (over HTTPS via the token helper; see gitSubmodulesUpdate).
	args := []string{"clone"}
	if b := strings.TrimSpace(branch); b != "" && !strings.HasPrefix(b, "-") {
		args = append(args, "--branch", b)
	}
	args = append(args, "--", remoteURL, dir)
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	gitSubmodulesUpdate(dir)
	return nil
}

// gitSubmodulesUpdate fetches/updates submodules of a working copy (after a clone,
// reuse, or branch switch — the .gitmodules set/commits differ per branch).
//
// The workspace has no SSH key, so a submodule pinned to an SSH URL (git@host: /
// ssh://) would fail "Host key verification failed". Following CodeLeaf's JGit
// client, we `submodule init` (expanding .gitmodules into .git/config), rewrite any
// SSH-form submodule URL to HTTPS so the unified credential helper (workspace-agent
// cred) can authenticate it, then `submodule update`. Best-effort throughout: no
// submodules is a no-op, and an unreachable submodule is non-fatal so the parent
// operation still succeeds.
func gitSubmodulesUpdate(dir string) {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		_ = cmd.Run()
	}
	run("submodule", "init")
	rewriteSubmoduleSSHURLs(dir)
	run("submodule", "update", "--recursive")
}

// rewriteSubmoduleSSHURLs replaces SSH-form submodule URLs in .git/config with their
// HTTPS equivalents (so the token credential helper applies). Operates on the URLs
// `submodule init` materialized; nested submodules are handled best-effort by the
// recursive update.
func rewriteSubmoduleSSHURLs(dir string) {
	out, err := exec.Command("git", "-C", dir, "config", "--get-regexp", `^submodule\..*\.url$`).Output()
	if err != nil {
		return // no submodules / no config
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, url, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if https := sshToHTTPS(url); https != url {
			_ = exec.Command("git", "-C", dir, "config", key, https).Run()
		}
	}
}

var (
	// scp-like form: user@host:owner/repo(.git). "https://…" is not matched because
	// it contains '/' before any ':', which [^@/] rejects.
	scpURLRe = regexp.MustCompile(`^[^@/\s]+@([^:/\s]+):(.+)$`)
	// ssh://[user@]host[:port]/owner/repo(.git)
	sshURLRe = regexp.MustCompile(`^ssh://(?:[^@/\s]+@)?([^:/\s]+)(?::\d+)?/(.+)$`)
)

// sshToHTTPS converts an SSH git URL to HTTPS (returns the input unchanged if it is
// not SSH). Mirrors CodeLeaf's sshToHttps. Host-agnostic, so self-hosted providers
// work too.
func sshToHTTPS(url string) string {
	u := strings.TrimSpace(url)
	if m := scpURLRe.FindStringSubmatch(u); m != nil {
		return "https://" + m[1] + "/" + strings.TrimPrefix(m[2], "/")
	}
	if m := sshURLRe.FindStringSubmatch(u); m != nil {
		return "https://" + m[1] + "/" + m[2]
	}
	return u
}

// ensureRepo guarantees a working copy for remoteURL exists under ~/repos and
// returns its path, so a session can launch with that dir as CWD. An existing
// copy is reused (and checked out to branch when one is given); otherwise it is
// cloned at branch. This is the "clone-then-start" path for session.create.
func ensureRepo(remoteURL, branch string) (string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" || strings.HasPrefix(remoteURL, "-") {
		return "", fmt.Errorf("remote_url is required and must not start with '-'")
	}
	name := deriveRepoName(remoteURL)
	dir, ok := resolveRepoDir(name)
	if !ok {
		return "", fmt.Errorf("derived repo name is invalid: %q", name)
	}
	if isGitRepo(dir) {
		if b := strings.TrimSpace(branch); b != "" && !strings.HasPrefix(b, "-") {
			if out, err := exec.Command("git", "-C", dir, "checkout", b).CombinedOutput(); err != nil {
				return "", fmt.Errorf("checkout %s: %v: %s", b, err, strings.TrimSpace(string(out)))
			}
		}
		gitSubmodulesUpdate(dir)
		return dir, nil
	}
	if err := gitClone(dir, remoteURL, branch); err != nil {
		return "", err
	}
	return dir, nil
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
	if err := gitClone(dir, req.RemoteURL, req.Branch); err != nil {
		writeErr(w, http.StatusBadGateway, "clone_failed", err.Error())
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

// branchInfo describes one checkout target for the branch-switch modal.
type branchInfo struct {
	Name    string `json:"name"`    // checkout name (remote prefix stripped → DWIM tracking)
	Remote  bool   `json:"remote"`  // remote-only (no local branch of this name)
	Unix    int64  `json:"unix"`    // last-commit time, for newest-first sorting
	Date    string `json:"date"`    // last-commit ISO date (display/tooltip)
	Subject string `json:"subject"` // last-commit subject
	Current bool   `json:"current"` // currently checked out
}

func handleRepoBranches(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	st, _ := gitStatus(dir)
	writeJSON(w, http.StatusOK, map[string]any{"branches": gitBranchInfos(dir, st.Branch), "current": st.Branch})
}

// gitBranchInfos lists local branches plus remote-only branches (those without a
// local counterpart), each with its last-commit time and subject, sorted newest
// commit first. Remote-only entries use the short branch name (remote prefix
// stripped) so `git checkout <name>` creates a tracking branch (DWIM).
func gitBranchInfos(dir, current string) []branchInfo {
	const sep = "\x1f" // unit separator: absent from ref names, dates, and subjects
	format := strings.Join([]string{
		"%(refname:short)", "%(committerdate:unix)", "%(committerdate:iso8601)", "%(contents:subject)",
	}, sep)
	infos := []branchInfo{}
	seen := map[string]bool{}
	// Local first so a local branch wins over its remote duplicate.
	for _, ns := range []string{"refs/heads", "refs/remotes"} {
		out, err := exec.Command("git", "-C", dir, "for-each-ref", "--format="+format, ns).Output()
		if err != nil {
			continue
		}
		isRemote := ns == "refs/remotes"
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			f := strings.Split(line, sep)
			if len(f) < 4 {
				continue
			}
			name := f[0]
			if isRemote {
				if i := strings.IndexByte(name, '/'); i >= 0 {
					name = name[i+1:] // origin/foo -> foo
				}
				if name == "HEAD" || name == "" {
					continue // skip the remote's symbolic HEAD
				}
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			unix, _ := strconv.ParseInt(f[1], 10, 64)
			infos = append(infos, branchInfo{
				Name: name, Remote: isRemote, Unix: unix, Date: f[2], Subject: f[3],
				Current: name == current,
			})
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Unix > infos[j].Unix })
	return infos
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
	gitSubmodulesUpdate(dir)
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
