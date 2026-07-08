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
// "@" is allowed so a worktree folder can be named "<repo>@<branch>" (branch slashes
// are sanitized to "-" by the caller); it can't form ".." or "/", so traversal stays
// blocked. Length is 96 to fit repo@branch without truncating common branch names.
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,95}$`)

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
	Name     string `json:"name"`
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	Dirty    bool   `json:"dirty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Provider string `json:"provider,omitempty"` // origin host slug: github/bitbucket/gitlab, or the bare host
	Remote   string `json:"remote,omitempty"`   // origin host (for a tooltip); no path/token
	// Worktree marks a linked git worktree (from `git worktree add`) rather than a
	// standalone clone, so the Console can badge it; Parent is the folder name of the
	// main working copy it hangs off (for a tooltip). Both empty/false for a plain clone.
	Worktree bool   `json:"worktree,omitempty"`
	Parent   string `json:"parent,omitempty"`
}

// gitProviderHost derives (provider slug, host) from an origin remote URL. Known SaaS
// hosts collapse to a short slug the Console can badge/icon; anything else returns the
// bare host as the slug so self-hosted remotes still identify. ("", "") when no host.
func gitProviderHost(remote string) (string, string) {
	u := sshToHTTPS(strings.TrimSpace(remote))
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/@"); i >= 0 && strings.ContainsRune(u[:i+1], '@') {
		u = u[strings.IndexByte(u, '@')+1:] // strip any leftover userinfo
	}
	host := u
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", ""
	}
	// The tenant's self-hosted git (docs/reference/internal-git-provider) has a
	// deployment-specific host, so match it dynamically from the CP-injected env
	// and badge it as "internal" rather than the bare host.
	if ih := internalGitHost(); ih != "" && strings.EqualFold(host, ih) {
		return "internal", host
	}
	switch {
	case strings.Contains(host, "github."):
		return "github", host
	case strings.Contains(host, "bitbucket."):
		return "bitbucket", host
	case strings.Contains(host, "gitlab."):
		return "gitlab", host
	default:
		return host, host
	}
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
		var provider, host string
		if origin, ok := gitOriginURL(dir); ok {
			provider, host = gitProviderHost(origin)
		}
		var parent string
		wt := isLinkedWorktree(dir)
		if wt {
			parent = filepath.Base(worktreeParent(dir))
		}
		repos = append(repos, Repo{
			Name: e.Name(), Path: dir, Branch: st.Branch,
			Dirty: st.Dirty, Ahead: st.Ahead, Behind: st.Behind,
			Provider: provider, Remote: host,
			Worktree: wt, Parent: parent,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

type cloneReq struct {
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`
	Name      string `json:"name"`
	// NewBranch, when set, is a branch to CREATE off Branch (the base) right after the
	// clone and switch to — "clone at base, fork a fresh branch". Empty = no new branch.
	NewBranch string `json:"new_branch"`
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

// sanitizeSeg makes a branch name usable as part of a folder name (repoNameRe
// charset). Mirrors the console's sanitizeSeg (console/src/lib/reponame.ts).
func sanitizeSeg(s string) string {
	s = sanitizeSegRe.ReplaceAllString(s, "-")
	s = strings.TrimLeft(s, "-")
	if len(s) > 59 {
		s = s[:59]
	}
	if s == "" {
		return "branch"
	}
	return s
}

var sanitizeSegRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// gitClone clones remoteURL into dir (optionally at branch). When newBranch is set,
// it is created off the just-cloned base branch and switched to (git checkout -b).
// GIT_TERMINAL_PROMPT=0 fails fast instead of blocking on an interactive
// credential/host-key prompt. A failed clone leaves no half-written directory behind.
func gitClone(dir, remoteURL, branch, newBranch string) error {
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
	// Fork a fresh branch off the base and switch to it, before submodules (a new
	// branch at the same commit shares the base's submodule pins).
	if nb := strings.TrimSpace(newBranch); nb != "" {
		if err := gitCheckoutNewBranch(dir, nb); err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
	}
	gitSubmodulesUpdate(dir)
	return nil
}

// gitCheckoutNewBranch creates newBranch at the current HEAD (the just-cloned or
// checked-out base branch) and switches to it. A pre-existing branch of that name
// (e.g. a reused working copy) is switched to instead of erroring.
func gitCheckoutNewBranch(dir, newBranch string) error {
	b := strings.TrimSpace(newBranch)
	if b == "" || strings.HasPrefix(b, "-") {
		return fmt.Errorf("new branch name is required and must not start with '-'")
	}
	args := []string{"-C", dir, "checkout"}
	if !branchExists(dir, b) {
		args = append(args, "-b") // create it; else fall through to a plain switch
	}
	args = append(args, b)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("create branch %s: %v: %s", b, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isLinkedWorktree reports whether dir is a linked worktree (from `git worktree
// add`), as opposed to a normal/main working copy. A linked worktree's git dir lives
// under the parent's common dir (…/.git/worktrees/<name>).
func isLinkedWorktree(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return false
	}
	return strings.Contains(filepath.ToSlash(strings.TrimSpace(string(out))), "/.git/worktrees/")
}

// worktreeParent returns the main working copy a linked worktree belongs to (the
// directory holding the shared .git), so `git worktree remove` can be run from it.
func worktreeParent(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return filepath.Dir(strings.TrimSpace(string(out))) // …/repos/app/.git -> …/repos/app
}

// linkedWorktreeCount returns how many linked worktrees hang off dir (0 for a plain
// clone). Deleting a main working copy with linked worktrees would break them, so the
// delete handler refuses while this is > 0.
func linkedWorktreeCount(dir string) int {
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return 0
	}
	total := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			total++ // one block per worktree; the first is the main one
		}
	}
	if total > 0 {
		return total - 1
	}
	return 0
}

// maybePruneWorktree auto-removes a linked worktree once nothing needs it — the
// counterpart to worktree-then-start that keeps them from piling up. It removes only
// when dir is a worktree, no session meta references it (worktreeHasSessions), AND it
// is clean: uncommitted or unpushed work is preserved for manual handling via the
// explicit force-delete path. Best-effort; called after a session's meta is forgotten.
func maybePruneWorktree(dir string) {
	if dir == "" || !isLinkedWorktree(dir) || worktreeHasSessions(dir) {
		return
	}
	if st, err := gitStatus(dir); err != nil || st.Dirty || st.Ahead > 0 {
		return // keep dirty/unpushed worktrees; the user force-deletes those explicitly
	}
	parent := worktreeParent(dir)
	if parent == "" {
		return
	}
	_ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", dir).Run()
	_ = exec.Command("git", "-C", parent, "worktree", "prune").Run()
}

// gitCurrentBranch returns dir's checked-out branch name, "(detached)" on a
// detached HEAD, or "" when dir isn't a resolvable git working tree. Cheaper than
// gitStatus (a single rev-parse, no porcelain parse) — used to stamp a session's
// start branch at create and to detect later drift on the session list.
func gitCurrentBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" {
		return "(detached)"
	}
	return b
}

// gitDirInfo returns dir's current branch AND whether it's a linked worktree in a
// single rev-parse (branch line + absolute-git-dir line), so the session list can
// enrich every row with one git call per unique dir. "" branch / false for a non-repo.
func gitDirInfo(dir string) (branch string, worktree bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD", "--absolute-git-dir").Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) >= 1 {
		branch = strings.TrimSpace(lines[0])
		if branch == "HEAD" {
			branch = "(detached)"
		}
	}
	if len(lines) >= 2 {
		worktree = strings.Contains(filepath.ToSlash(strings.TrimSpace(lines[1])), "/.git/worktrees/")
	}
	return
}

// branchExists reports whether dir already has a local branch named branch.
func branchExists(dir, branch string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// branchNameStatus reports whether name already exists as a local branch and/or as a
// remote-tracking branch (on any remote). This catches a worktree/rename name that
// would otherwise SILENTLY create a divergent branch: `git worktree add -b X` (and
// `git branch -m X`) happily make a fresh local X when only a past remote X exists,
// which then collides at push time. Callers refuse and offer the user a choice.
func branchNameStatus(dir, name string) (local, remote bool) {
	local = branchExists(dir, name)
	out, err := exec.Command("git", "-C", dir, "for-each-ref", "--format=%(refname:short)", "refs/remotes").Output()
	if err == nil {
		suffix := "/" + name
		for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if ln = strings.TrimSpace(ln); ln != "" && strings.HasSuffix(ln, suffix) {
				remote = true
				break
			}
		}
	}
	return
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

// gitOriginURL returns dir's origin remote URL (ok=false when there is none).
func gitOriginURL(dir string) (string, bool) {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// normalizeRemote canonicalizes a clone URL for equality comparison: SSH→HTTPS,
// drop a trailing ".git"/slash, lowercase (hosts and GitHub/Bitbucket owner/repo
// are case-insensitive). Good enough to tell "same repo" from "different repo".
func normalizeRemote(u string) string {
	u = sshToHTTPS(strings.TrimSpace(u))
	u = strings.TrimSuffix(strings.TrimRight(u, "/"), ".git")
	return strings.ToLower(u)
}

// ensureRepo guarantees a working copy for remoteURL exists under ~/repos and
// returns its path, so a session can launch with that dir as CWD.
//
// name is the target folder. An explicit name (e.g. "<repo>-<branch>") lets two
// branches of the same repo live side by side as independent clones; an empty
// name derives the bare repo name, keeping back-compat with existing
// ~/repos/<repo> clones. An existing copy is reused (checked out to branch when
// one is given) only when it is the SAME remote; otherwise it is cloned at
// branch. This is the "clone-then-start" path for session.create.
//
// newBranch, when set, is created off branch (the base) and switched to — a fresh
// working branch to start the session on. On a reused copy it forks from the base
// only when it does not already exist (otherwise it is simply switched to).
func ensureRepo(remoteURL, branch, newBranch, name string) (string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" || strings.HasPrefix(remoteURL, "-") {
		return "", fmt.Errorf("remote_url is required and must not start with '-'")
	}
	if name = strings.TrimSpace(name); name == "" {
		name = deriveRepoName(remoteURL)
	}
	dir, ok := resolveRepoDir(name)
	if !ok {
		return "", fmt.Errorf("repo name is invalid: %q", name)
	}
	if isGitRepo(dir) {
		// Reuse only when the existing clone is the SAME remote; otherwise two
		// distinct repos sharing a derived name (alice/app vs bob/app) would
		// silently collide on one directory. Mismatch => the caller must
		// disambiguate by passing an explicit, distinct name.
		if origin, ok := gitOriginURL(dir); ok && normalizeRemote(origin) != normalizeRemote(remoteURL) {
			return "", fmt.Errorf("repo %q already exists for a different remote (%s); choose a different name", name, origin)
		}
		nb := strings.TrimSpace(newBranch)
		// Move onto the base branch when we're about to fork a new branch from it (skip
		// if the new branch already exists — then we just switch to it below), or when a
		// plain base checkout was requested and no new branch is wanted.
		if b := strings.TrimSpace(branch); b != "" && !strings.HasPrefix(b, "-") && (nb == "" || !branchExists(dir, nb)) {
			if out, err := exec.Command("git", "-C", dir, "checkout", b).CombinedOutput(); err != nil {
				return "", fmt.Errorf("checkout %s: %v: %s", b, err, strings.TrimSpace(string(out)))
			}
		}
		if nb != "" {
			if err := gitCheckoutNewBranch(dir, nb); err != nil {
				return "", err
			}
		}
		gitSubmodulesUpdate(dir)
		return dir, nil
	}
	if err := gitClone(dir, remoteURL, branch, newBranch); err != nil {
		return "", err
	}
	applyGitIdentity(dir) // bake the provider's commit identity into the fresh clone
	return dir, nil
}

// ensureWorktree creates (or reuses) a git worktree of an existing working copy
// (parentDir) under ~/repos/<repo>@<branch> and returns its path, so a session can
// launch with that dir as CWD — the "worktree-then-start" path for session.create.
//
// This is the safe alternative to switching the parent's branch out from under its
// running sessions: the new branch gets its OWN directory. newBranch, when set, is
// created off base (git worktree add -b); otherwise the worktree checks out the
// existing base branch. Submodules are populated into the worktree's own per-worktree
// gitdir over the token-authed HTTPS path (see gitSubmodulesUpdate); this does not
// disturb the parent's submodules. git refuses to check out a branch already live in
// another worktree, which the error surfaces as-is.
func ensureWorktree(parentDir, base, newBranch, folderSeg string) (string, error) {
	if !isGitRepo(parentDir) {
		return "", fmt.Errorf("not a git working copy: %s", parentDir)
	}
	base = strings.TrimSpace(base)
	newBranch = strings.TrimSpace(newBranch)
	// The branch the worktree ends up on names its folder: the new branch when forking,
	// else the base being checked out.
	target := newBranch
	if target == "" {
		target = base
	}
	if target == "" || strings.HasPrefix(base, "-") || strings.HasPrefix(newBranch, "-") {
		return "", fmt.Errorf("a base or new branch is required and must not start with '-'")
	}
	// folderSeg lets the folder name diverge from the branch (e.g. an auto branch
	// temp/<slug> living in a wip-<slug> folder); default to the branch-derived name.
	seg := strings.TrimSpace(folderSeg)
	if seg == "" {
		seg = sanitizeSeg(target)
	} else {
		seg = sanitizeSeg(seg)
	}
	name := filepath.Base(parentDir) + "@" + seg
	dir, ok := resolveRepoDir(name)
	if !ok {
		return "", fmt.Errorf("worktree name is invalid: %q", name)
	}
	// Reuse an existing worktree at that path (idempotent re-launch); a non-git path of
	// the same name is a conflict the caller must resolve with a different branch.
	if _, err := os.Stat(dir); err == nil {
		if isGitRepo(dir) {
			return dir, nil
		}
		return "", fmt.Errorf("path already exists and is not a worktree: %s", name)
	}
	// git worktree add [-b <newBranch>] <dir> [<base>]
	args := []string{"-C", parentDir, "worktree", "add"}
	if newBranch != "" {
		args = append(args, "-b", newBranch, dir)
		if base != "" {
			args = append(args, base)
		}
	} else {
		args = append(args, dir, base)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	applyGitIdentity(dir)    // commit identity for the worktree (config is shared, but explicit)
	gitSubmodulesUpdate(dir) // per-worktree submodule checkout; parent untouched (verified)
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
		// When forking a new branch, default the folder to <repo>-<newbranch> so it lands
		// beside (not on top of) the base clone. Clients usually send an explicit name.
		if nb := strings.TrimSpace(req.NewBranch); nb != "" {
			name += "-" + sanitizeSeg(nb)
		}
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
	if err := gitClone(dir, req.RemoteURL, req.Branch, req.NewBranch); err != nil {
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
	// A working copy with running sessions is pinned to its branch: switching it
	// out from under live agents corrupts them (see liveSessionsInDir). Refuse and
	// steer the user to open the branch as its own working copy instead. This blocks
	// the Console footgun; agent/manual `git checkout` inside a session still bypasses
	// it (no pre-checkout hook exists) and is caught by branch-drift detection.
	if running := liveSessionsInDir(dir); len(running) > 0 {
		writeErr(w, http.StatusConflict, "sessions_running",
			fmt.Sprintf("%d session(s) are running in this working copy (%s); switching branches here would corrupt them. Open the branch as a new working copy instead.",
				len(running), strings.Join(running, ", ")))
		return
	}
	var args []string
	if req.Create {
		// Create a branch: `checkout -b <name> [<start-point>]`. Branch is the new name;
		// Ref (optional) is a start point (e.g. a commit sha) — "new branch at this commit".
		name := strings.TrimSpace(req.Branch)
		if name == "" || strings.HasPrefix(name, "-") {
			writeErr(w, http.StatusBadRequest, "bad_ref", "branch name is required and must not start with '-'")
			return
		}
		args = []string{"-C", dir, "checkout", "-b", name}
		if start := strings.TrimSpace(req.Ref); start != "" {
			if strings.HasPrefix(start, "-") {
				writeErr(w, http.StatusBadRequest, "bad_ref", "start ref must not start with '-'")
				return
			}
			args = append(args, start)
		}
	} else {
		ref := strings.TrimSpace(req.Branch)
		if ref == "" {
			ref = strings.TrimSpace(req.Ref)
		}
		if ref == "" || strings.HasPrefix(ref, "-") {
			writeErr(w, http.StatusBadRequest, "bad_ref", "branch/ref is required and must not start with '-'")
			return
		}
		args = []string{"-C", dir, "checkout", ref}
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadGateway, "checkout_failed", fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	gitSubmodulesUpdate(dir)
	st, _ := gitStatus(dir)
	writeJSON(w, http.StatusOK, st)
}

// renameBranchReq is the body for the session-scoped branch rename (session_title.go).
type renameBranchReq struct {
	Name string `json:"name"`
}

// handleRepoFF fast-forwards the current branch to its upstream (git pull --ff-only):
// fetches the tracked remote branch and advances the local branch only if it's a strict
// fast-forward (no merge commit). Fails cleanly when there's no upstream or the branch
// has diverged.
func handleRepoFF(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	cmd := exec.Command("git", "-C", dir, "pull", "--ff-only")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		writeErr(w, http.StatusBadGateway, "ff_failed", strings.TrimSpace(string(out)))
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
	// Deleting the working copy out from under live sessions is even worse than a
	// branch switch: their cwd vanishes mid-flight. Refuse while any session runs
	// there — the user must stop/archive them first (same guard as checkout).
	if running := liveSessionsInDir(dir); len(running) > 0 {
		writeErr(w, http.StatusConflict, "sessions_running_delete",
			fmt.Sprintf("%d session(s) are running in this working copy (%s); deleting it would break them. Stop those sessions first.",
				len(running), strings.Join(running, ", ")))
		return
	}
	force := r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1"

	// A linked worktree can't be dropped with os.RemoveAll (it would orphan the parent's
	// worktree registry) nor with a plain `git worktree remove` (git refuses when the
	// worktree has submodules). Use `remove --force`, but gate it on our own dirty/ahead
	// check so uncommitted or unpushed work isn't silently destroyed — the client must
	// re-request with force=true to override.
	if isLinkedWorktree(dir) {
		if !force {
			if st, err := gitStatus(dir); err == nil && (st.Dirty || st.Ahead > 0) {
				writeErr(w, http.StatusConflict, "worktree_dirty",
					"worktree has uncommitted or unpushed changes; pass force=true to delete anyway")
				return
			}
		}
		parent := worktreeParent(dir)
		if parent == "" {
			writeErr(w, http.StatusInternalServerError, "delete_failed", "cannot resolve worktree parent")
			return
		}
		if out, err := exec.Command("git", "-C", parent, "worktree", "remove", "--force", dir).CombinedOutput(); err != nil {
			writeErr(w, http.StatusBadGateway, "worktree_remove_failed", strings.TrimSpace(string(out)))
			return
		}
		_ = exec.Command("git", "-C", parent, "worktree", "prune").Run()
		writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("name")})
		return
	}

	// A main working copy with linked worktrees must not be removed — os.RemoveAll would
	// break every worktree hanging off it. Refuse and let the user delete the worktrees
	// first.
	if n := linkedWorktreeCount(dir); n > 0 {
		writeErr(w, http.StatusConflict, "has_worktrees",
			fmt.Sprintf("this working copy has %d worktree(s) branched off it; delete those first", n))
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("name")})
}
