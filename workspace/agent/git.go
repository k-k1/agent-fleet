package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Repositories are plain working copies under ~/repos/<name>. The folder name
// is the repo id — there is no metadata store in this phase (docs/09 §9.6).
// git auth uses the user's ~/.ssh keys; the Control Plane never holds keys and
// delegates every operation here (docs/06 §6.4, docs/07 §7.3).

// repoNameRe constrains a repo (folder) name; it doubles as path-traversal guard.
// The charset is Unicode letters/numbers (\p{L}\p{N}) so a folder can be named in
// Japanese (or any script) — an SVN checkout target, say — plus "." "_" "@" "-" in
// the body; "@" lets a worktree folder be named "<repo>@<branch>" (branch slashes
// are sanitized to "-" by the caller). The first char must be a letter/number, and
// neither "/" nor a leading "." is allowed, so a name can never form ".." or "/" —
// traversal stays blocked. Length is 96 runes to fit repo@branch without truncating.
var repoNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._@-]{0,95}$`)

func reposRoot() string { return filepath.Join(homeDir(), "repos") }

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
	Name string `json:"name"`
	// WorkingCopyID identifies this filesystem generation. Re-creating a folder with
	// the same name yields a different id, so a dynamic share never resurrects.
	WorkingCopyID string `json:"workingCopyId"`
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	Dirty         bool   `json:"dirty"`
	Ahead         int    `json:"ahead"`
	Behind        int    `json:"behind"`
	Provider      string `json:"provider,omitempty"` // origin host slug: github/bitbucket/gitlab, or the bare host
	Remote        string `json:"remote,omitempty"`   // origin host (for a tooltip); no path/token
	// Vcs discriminates the working-copy kind: "git" (default/omitted) or "svn"
	// (docs/41). SVN copies are flat — no branches/ahead/behind/worktree — so the
	// Console gates git-only actions on it; Revision/URL carry the svn-side facts.
	Vcs      string `json:"vcs,omitempty"`
	Revision string `json:"revision,omitempty"` // SVN: current working-copy revision
	URL      string `json:"url,omitempty"`      // SVN: repository URL of the working copy
	// Worktree marks a linked git worktree (from `git worktree add`) rather than a
	// standalone clone, so the Console can badge it; Parent is the folder name of the
	// main working copy it hangs off (for a tooltip). Both empty/false for a plain clone.
	Worktree bool   `json:"worktree,omitempty"`
	Parent   string `json:"parent,omitempty"`
	// CreatedAt is the linked worktree's creation time (RFC3339), taken from the
	// mtime of its `.git` gitfile — written once by `git worktree add` and left
	// untouched by ordinary work, so it is a stable creation-order key. The Console
	// orders a base's worktrees by it (folder/slug order was effectively random for
	// temp/<slug> worktrees). Empty for plain clones (their `.git` is a directory
	// whose mtime churns) and when the stat fails.
	CreatedAt string `json:"createdAt,omitempty"`
	// Integration describes the linked worktree's commit relationship to the
	// parent working copy's current HEAD. It is deliberately separate from
	// Ahead/Behind above, which describe the branch's configured upstream.
	Integration *RepoIntegration `json:"integration,omitempty"`
	// Locked marks the working copy as pinned against deletion (docs/45, locks.go):
	// DELETE /repos/{name} is refused even with force=true, and the automatic
	// worktree prune skips it. Toggled by POST /repos/{name}/lock.
	Locked bool `json:"locked,omitempty"`
}

func workingCopyID(dir string) string {
	marker, err := workingCopyMarkerPath(dir)
	if err == nil {
		if id := readWorkingCopyID(marker); id != "" {
			return id
		}
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err == nil {
			id := "wc_" + hex.EncodeToString(buf)
			f, openErr := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if openErr == nil {
				_, writeErr := f.WriteString(id + "\n")
				closeErr := f.Close()
				if writeErr == nil && closeErr == nil {
					return id
				}
				_ = os.Remove(marker)
			} else if os.IsExist(openErr) {
				if id := readWorkingCopyID(marker); id != "" {
					return id
				}
			}
		}
	}

	// Sharing must fail closed when the persistent random marker cannot be
	// created/read. A device+inode fallback can be reused after delete/recreate and
	// would silently resurrect an old repo/worktree ACL on a different copy.
	return ""
}

func workingCopyMarkerPath(dir string) (string, error) {
	gitMarker := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitMarker)
	if err == nil && fi.IsDir() {
		return filepath.Join(gitMarker, "agent-fleet-working-copy-id"), nil
	}
	if err == nil && fi.Mode().IsRegular() {
		body, readErr := os.ReadFile(gitMarker)
		if readErr != nil {
			return "", readErr
		}
		gitDir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "gitdir:"))
		if gitDir == "" {
			return "", fmt.Errorf("empty gitdir")
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
		return filepath.Join(filepath.Clean(gitDir), "agent-fleet-working-copy-id"), nil
	}
	svnMarker := filepath.Join(dir, ".svn")
	if svnInfo, svnErr := os.Stat(svnMarker); svnErr == nil && svnInfo.IsDir() {
		return filepath.Join(svnMarker, "agent-fleet-working-copy-id"), nil
	}
	return "", fmt.Errorf("working-copy metadata not found")
}

func readWorkingCopyID(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(body))
	if !strings.HasPrefix(id, "wc_") || len(id) != 35 {
		return ""
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(id, "wc_")); err != nil {
		return ""
	}
	return id
}

// RepoIntegration is a local-only comparison; it never fetches. TargetUnique is
// the number of commits reachable only from the parent HEAD, while WorktreeUnique
// is the number reachable only from the linked worktree HEAD.
type RepoIntegration struct {
	TargetBranch   string `json:"targetBranch,omitempty"`
	TargetUnique   int    `json:"targetUnique"`
	WorktreeUnique int    `json:"worktreeUnique"`
	Relation       string `json:"relation"` // same | contained | unmerged | diverged | unknown
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
	out, err := gitx.Run(dir, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
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

// gitWorktreeIntegration compares two worktree-local HEADs by object ID. A plain
// "HEAD...HEAD" invocation from one directory would resolve both names to that
// directory's HEAD, because linked worktrees share refs but have separate HEADs.
func gitWorktreeIntegration(parentDir, worktreeDir, targetBranch string) RepoIntegration {
	r := RepoIntegration{TargetBranch: targetBranch, Relation: "unknown"}
	parentHead, err := gitx.Run(parentDir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return r
	}
	worktreeHead, err := gitx.Run(worktreeDir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return r
	}
	out, err := gitx.Run(worktreeDir, "rev-list", "--left-right", "--count", parentHead+"..."+worktreeHead)
	if err != nil {
		return r
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return r
	}
	r.TargetUnique, err = strconv.Atoi(fields[0])
	if err != nil {
		return r
	}
	r.WorktreeUnique, err = strconv.Atoi(fields[1])
	if err != nil {
		return r
	}
	switch {
	case r.TargetUnique == 0 && r.WorktreeUnique == 0:
		r.Relation = "same"
	case r.WorktreeUnique == 0:
		r.Relation = "contained"
	case r.TargetUnique == 0:
		r.Relation = "unmerged"
	default:
		r.Relation = "diverged"
	}
	return r
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
			// An SVN working copy (docs/41) is a flat folder with a .svn dir; surface it
			// with revision/URL so it shows in the tree and can host a session. Anything
			// else (a plain folder) is skipped.
			if isSvnRepo(dir) {
				repos = append(repos, svnRepoEntry(e.Name(), dir))
			}
			continue
		}
		st, _ := gitStatus(dir)
		var provider, host string
		if origin, ok := gitOriginURL(dir); ok {
			provider, host = gitProviderHost(origin)
		}
		var parent, createdAt string
		wt := isLinkedWorktree(dir)
		if wt {
			parent = filepath.Base(worktreeParent(dir))
			createdAt = worktreeCreatedAt(dir)
		}
		repos = append(repos, Repo{
			Name: e.Name(), WorkingCopyID: workingCopyID(dir), Path: dir, Branch: st.Branch,
			Dirty: st.Dirty, Ahead: st.Ahead, Behind: st.Behind,
			Provider: provider, Remote: host,
			Worktree: wt, Parent: parent, CreatedAt: createdAt,
		})
	}
	// Compare linked worktrees only after the full list is available, so the
	// target label comes from the already-read parent status. The actual commit
	// comparison uses the parent path reported by Git rather than trusting names.
	byName := make(map[string]Repo, len(repos))
	for _, repo := range repos {
		byName[repo.Name] = repo
	}
	for i := range repos {
		if !repos[i].Worktree {
			continue
		}
		parentDir := worktreeParent(repos[i].Path)
		targetBranch := ""
		if parent, ok := byName[repos[i].Parent]; ok {
			targetBranch = parent.Branch
		}
		integration := gitWorktreeIntegration(parentDir, repos[i].Path, targetBranch)
		repos[i].Integration = &integration
	}
	// Delete lock (docs/45): one ledger read for the whole list, not one per row.
	if locked := lockedRepoDirs(); len(locked) > 0 {
		for i := range repos {
			repos[i].Locked = locked[absPath(repos[i].Path)]
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"repos": repos})
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
	run := func(withBranch string) (string, error) {
		args := []string{"clone"}
		if withBranch != "" {
			args = append(args, "--branch", withBranch)
		}
		args = append(args, "--", remoteURL, dir)
		return gitx.Combined("", args...)
	}
	b := strings.TrimSpace(branch)
	if strings.HasPrefix(b, "-") {
		b = ""
	}
	out, err := run(b)
	// A commit-less remote (a freshly created internal repo) has zero refs, so
	// `--branch` fails with "Remote branch X not found in upstream". Verify the
	// remote really is empty (not a typo'd branch on a populated repo), then retry
	// plain: the clone lands on the remote HEAD's unborn branch, and the first
	// push will create it.
	if err != nil && b != "" && remoteHasNoBranches(remoteURL) {
		_ = os.RemoveAll(dir)
		out, err = run("")
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("%v: %s", err, out)
	}
	// Fork a fresh branch off the base and switch to it, before submodules (a new
	// branch at the same commit shares the base's submodule pins).
	if nb := strings.TrimSpace(newBranch); nb != "" {
		if err := gitCheckoutNewBranch(dir, nb); err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
	}
	gitSubmodulesEnsure(dir) // clone-then-start lands a session here too
	scratchAutoRelocate(dir) // artifact dirs onto the working disk while the tree is still empty
	return nil
}

// remoteHasNoBranches reports whether the remote advertises zero branch refs —
// i.e. a freshly created, commit-less repository. Used to tell "empty repo" apart
// from "requested branch missing on a populated repo" before retrying a clone.
func remoteHasNoBranches(remoteURL string) bool {
	out, err := gitx.Run("", "ls-remote", "--heads", "--", remoteURL)
	return err == nil && out == ""
}

// unbornHead returns the branch name HEAD symbolically points at when the working
// copy has no commits yet (a fresh clone of an empty remote), or "" when HEAD
// resolves to a commit (the normal case).
func unbornHead(dir string) string {
	if gitx.OK(dir, "rev-parse", "--verify", "-q", "HEAD") {
		return ""
	}
	out, err := gitx.Run(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// gitCheckoutNewBranch creates newBranch at the current HEAD (the just-cloned or
// checked-out base branch) and switches to it. A pre-existing branch of that name
// (e.g. a reused working copy) is switched to instead of erroring.
func gitCheckoutNewBranch(dir, newBranch string) error {
	b := strings.TrimSpace(newBranch)
	if b == "" || strings.HasPrefix(b, "-") {
		return fmt.Errorf("new branch name is required and must not start with '-'")
	}
	args := []string{"checkout"}
	if !branchExists(dir, b) {
		args = append(args, "-b") // create it; else fall through to a plain switch
	}
	args = append(args, b)
	if out, err := gitx.Combined(dir, args...); err != nil {
		return fmt.Errorf("create branch %s: %v: %s", b, err, out)
	}
	return nil
}

// isLinkedWorktree reports whether dir is a linked worktree (from `git worktree
// add`), as opposed to a normal/main working copy. A linked worktree's git dir lives
// under the parent's common dir (…/.git/worktrees/<name>).
func isLinkedWorktree(dir string) bool {
	out, err := gitx.Run(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false
	}
	return strings.Contains(filepath.ToSlash(out), "/.git/worktrees/")
}

// worktreeCreatedAt returns a linked worktree's creation time (RFC3339) from the
// mtime of its `.git` gitfile. `git worktree add` writes that file once and normal
// work never rewrites it, so its mtime is a stable creation-order key — unlike the
// folder name (a temp/<slug> worktree sorts randomly) or the working dir's own mtime
// (which churns on every checkout/write). Returns "" if the file can't be stat'd.
func worktreeCreatedAt(dir string) string {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return ""
	}
	return fi.ModTime().UTC().Format(time.RFC3339)
}

// worktreeParent returns the main working copy a linked worktree belongs to (the
// directory holding the shared .git), so `git worktree remove` can be run from it.
func worktreeParent(dir string) string {
	out, err := gitx.Run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}
	return filepath.Dir(out) // …/repos/app/.git -> …/repos/app
}

// linkedWorktreeCount returns how many linked worktrees hang off dir (0 for a plain
// clone). Deleting a main working copy with linked worktrees would break them, so the
// delete handler refuses while this is > 0.
func linkedWorktreeCount(dir string) int {
	out, err := gitx.Run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return 0
	}
	total := 0
	for _, line := range strings.Split(out, "\n") {
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
	if repoLocked(dir) {
		return // 削除ロック（docs/45）は自動 prune にも効く
	}
	if st, err := gitStatus(dir); err != nil || st.Dirty || st.Ahead > 0 {
		return // keep dirty/unpushed worktrees; the user force-deletes those explicitly
	}
	parent := worktreeParent(dir)
	if parent == "" {
		return
	}
	_ = gitx.Cmd(parent, "worktree", "remove", "--force", dir).Run()
	_ = gitx.Cmd(parent, "worktree", "prune").Run()
}

// gitCurrentBranch returns dir's checked-out branch name, "(detached)" on a
// detached HEAD, or "" when dir isn't a resolvable git working tree. Cheaper than
// gitStatus (a single rev-parse, no porcelain parse) — used to stamp a session's
// start branch at create and to detect later drift on the session list.
func gitCurrentBranch(dir string) string {
	out, err := gitx.Run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	b := out
	if b == "HEAD" {
		return "(detached)"
	}
	return b
}

// gitBranchExists reports whether a local branch of that name exists in dir.
func gitBranchExists(dir, branch string) bool {
	return gitx.OK(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
}

// gitBranchSHA returns the tip commit of a local branch, or "" if absent.
func gitBranchSHA(dir, branch string) string {
	out, err := gitx.Run(dir, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

// gitCreateBranch creates a local branch at sha (restore path). Reports success.
func gitCreateBranch(dir, branch, sha string) bool {
	return gitx.Cmd(dir, "branch", branch, sha).Run() == nil
}

// mergedLocalBranches returns dir's local branches already contained in HEAD (safe to
// delete — their commits live in the current line), excluding the checked-out branch.
// Worktree-checked-out branches are also excluded (git refuses to delete those). The
// cleanup survey uses this to propose merged temp/* branches left behind by removed
// worktrees.
func mergedLocalBranches(dir string) []string {
	// `--merged` (no ref) = merged into HEAD. Tab-separated so empty fields survive the
	// split: name \t worktreepath (non-empty = checked out somewhere) \t HEAD ("*" =
	// current). Skip the current branch and any worktree-checked-out branch (git refuses
	// to delete those), plus the trunk.
	out, err := gitx.Run(dir, "branch", "--merged",
		"--format=%(refname:short)%09%(worktreepath)%09%(HEAD)")
	if err != nil {
		return nil
	}
	var names []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		f := strings.Split(ln, "\t")
		if len(f) < 3 {
			continue
		}
		name, worktreePath, head := f[0], f[1], f[2]
		if name == "" || name == "main" || name == "master" {
			continue
		}
		if strings.TrimSpace(worktreePath) != "" || strings.TrimSpace(head) == "*" {
			continue // checked out in a worktree or the current branch → not deletable
		}
		names = append(names, name)
	}
	return names
}

// gitDirInfo returns dir's current branch AND whether it's a linked worktree in a
// single rev-parse (branch line + absolute-git-dir line), so the session list can
// enrich every row with one git call per unique dir. "" branch / false for a non-repo.
func gitDirInfo(dir string) (branch string, worktree bool) {
	out, err := gitx.Run(dir, "rev-parse", "--abbrev-ref", "HEAD", "--absolute-git-dir")
	if err != nil {
		return "", false
	}
	lines := strings.Split(out, "\n")
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
	return gitx.OK(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
}

// branchNameStatus reports whether name already exists as a local branch and/or as a
// remote-tracking branch (on any remote). This catches a worktree/rename name that
// would otherwise SILENTLY create a divergent branch: `git worktree add -b X` (and
// `git branch -m X`) happily make a fresh local X when only a past remote X exists,
// which then collides at push time. Callers refuse and offer the user a choice.
func branchNameStatus(dir, name string) (local, remote bool) {
	local = branchExists(dir, name)
	out, err := gitx.Run(dir, "for-each-ref", "--format=%(refname:short)", "refs/remotes")
	if err == nil {
		suffix := "/" + name
		for _, ln := range strings.Split(out, "\n") {
			if ln = strings.TrimSpace(ln); ln != "" && strings.HasSuffix(ln, suffix) {
				remote = true
				break
			}
		}
	}
	return
}

// worktreeBranches maps a branch name to the working copy that has it checked out,
// for every worktree of dir's repository EXCEPT dir itself.
//
// git allows a branch to be checked out in only ONE worktree at a time — both
// `git checkout X` and `git worktree add … X` refuse with
// "fatal: 'X' is already checked out at <path>" (verified). Only two things get
// past that: a detached checkout (holds no branch ref, so it never lands in this
// map) and the explicit --ignore-other-worktrees escape hatch, which we never use
// because two working copies sharing one branch ref silently revert each other's
// commits. Knowing the occupancy UP FRONT lets the pickers disable those targets
// and offer to open the occupying copy, instead of surfacing git's raw fatal after
// a launch has already started creating directories.
//
// Best-effort: an unreadable worktree list yields an empty map, i.e. nothing is
// blocked and git's own refusal remains the backstop.
func worktreeBranches(dir string) map[string]string {
	out, err := gitx.Run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	self := realPath(dir)
	m := map[string]string{}
	cur := ""
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(ln, "worktree "):
			cur = strings.TrimSpace(strings.TrimPrefix(ln, "worktree "))
		case strings.HasPrefix(ln, "branch "):
			name := strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(ln, "branch ")), "refs/heads/")
			if name == "" || cur == "" || realPath(cur) == self {
				continue // unnamed, or dir itself — that's "current", not "occupied"
			}
			m[name] = cur
		}
	}
	return m
}

// writeBranchInUse answers a request for a branch that is live in another working
// copy. Beyond the stable code it carries that copy's FOLDER name, because the only
// useful next step is "open it" — a bare failure leaves the user guessing which of
// their worktrees holds the branch.
func writeBranchInUse(w http.ResponseWriter, branch, path string) {
	folder := filepath.Base(path)
	httpx.WriteJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{
		"code":     errCodeBranchInUse,
		"message":  fmt.Sprintf("branch %q is already checked out in working copy %q", branch, folder),
		"worktree": folder,
	}})
}

// worktreeSyncTimeout bounds each NETWORK step of an existing-branch worktree launch
// (the pre-create fetch and the post-create fast-forward). Both are best-effort, so a
// slow or unreachable remote degrades to "launched, possibly not at the newest tip"
// instead of wedging the create request past its client deadline.
const worktreeSyncTimeout = 30 * time.Second

// ensureBranchRef makes base resolvable before `git worktree add` runs. A branch
// pushed after this copy's last fetch exists on the remote but in NO local ref, and
// worktree add fails outright with "invalid reference" — the user asked to work on a
// branch that demonstrably exists, so a failed launch is the wrong answer. One bounded
// fetch fixes it. Skipped when the name already resolves locally or as a remote-tracking
// ref (the common case), so an ordinary launch pays no network cost.
func ensureBranchRef(dir, base string) {
	if base = strings.TrimSpace(base); base == "" {
		return
	}
	if local, remote := branchNameStatus(dir, base); local || remote {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeSyncTimeout)
	defer cancel()
	_ = gitx.CmdContext(ctx, dir, "fetch", "--prune").Run()
}

// fastForwardWorktree brings a just-created existing-branch worktree up to its
// upstream tip (git pull --ff-only). "Start work on branch X" means X as it is NOW,
// not as of this copy's last fetch — without this the session silently begins on a
// stale checkout, which is the failure the whole existing-branch flow exists to avoid.
//
// Best-effort on purpose: --ff-only fails cleanly, and every way it fails (no upstream
// on a local-only branch, unpushed local commits, real divergence) is a state the user
// still wants a session in — resolving it is the session's job. A fast-forward that
// actually moved HEAD re-syncs submodules, whose pinned commits differ per commit.
func fastForwardWorktree(dir string) {
	before, _ := gitx.Run(dir, "rev-parse", "HEAD")
	ctx, cancel := context.WithTimeout(context.Background(), worktreeSyncTimeout)
	defer cancel()
	if out, err := gitx.CmdContext(ctx, dir, "pull", "--ff-only").CombinedOutput(); err != nil {
		// Not an error path — log it so a "why am I behind?" question is answerable.
		log.Printf("worktree %s: fast-forward skipped: %v: %s", filepath.Base(dir), err, strings.TrimSpace(string(out)))
		return
	}
	if after, _ := gitx.Run(dir, "rev-parse", "HEAD"); after != before {
		gitSubmodulesUpdate(dir)
	}
}

// realPath canonicalizes a path for identity comparison (git prints resolved,
// absolute worktree paths; our dirs can arrive with symlinks — /home vs a
// bind-mounted home — or as ".."-laden relatives). Falls back to a lexical clean
// when the path does not exist.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

// submoduleInsteadOfArgs builds `-c url.<https>.insteadOf=<ssh>` flags for every distinct
// host referenced by dir's top-level .gitmodules, so a recursive submodule fetch rewrites
// SSH URLs to HTTPS at ALL nesting levels (nested submodules are almost always on the same
// host, and the child git clones inherit these -c settings). Returns nil when there are no
// SSH-form submodule URLs, leaving the command untouched.
func submoduleInsteadOfArgs(dir string) []string {
	out, err := gitx.Run(dir, "config", "-f", ".gitmodules", "--get-regexp", `^submodule\..*\.url$`)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var args []string
	for _, line := range strings.Split(out, "\n") {
		_, url, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		host, ok := sshURLHost(url)
		if !ok || seen[host] {
			continue
		}
		seen[host] = true
		https := "https://" + host + "/"
		// Cover both SSH spellings: scp-like (git@host:) and ssh:// (ssh://git@host/).
		args = append(args,
			"-c", "url."+https+".insteadOf=git@"+host+":",
			"-c", "url."+https+".insteadOf=ssh://git@"+host+"/",
		)
	}
	return args
}

// sshURLHost returns the host of an SSH git URL (scp-like or ssh://), ok=false for anything
// else (HTTPS, local paths). Mirrors sshToHTTPS's matching so the two stay consistent.
func sshURLHost(url string) (string, bool) {
	u := strings.TrimSpace(url)
	if m := scpURLRe.FindStringSubmatch(u); m != nil {
		return m[1], true
	}
	if m := sshURLRe.FindStringSubmatch(u); m != nil {
		return m[1], true
	}
	return "", false
}

// rewriteSubmoduleSSHURLs replaces SSH-form submodule URLs in .git/config with their
// HTTPS equivalents (so the token credential helper applies). Operates on the URLs
// `submodule init` materialized; nested submodules are handled best-effort by the
// recursive update.
func rewriteSubmoduleSSHURLs(dir string) {
	out, err := gitx.Run(dir, "config", "--get-regexp", `^submodule\..*\.url$`)
	if err != nil {
		return // no submodules / no config
	}
	for _, line := range strings.Split(out, "\n") {
		key, url, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if https := sshToHTTPS(url); https != url {
			_ = gitx.Cmd(dir, "config", key, https).Run()
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
	out, err := gitx.Run(dir, "remote", "get-url", "origin")
	if err != nil {
		return "", false
	}
	return out, true
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
			// A reused clone of a still-empty remote sits on an unborn branch with no
			// ref to check out; when it's already the requested one there is nothing to do.
			if unbornHead(dir) != b {
				if out, err := gitx.Combined(dir, "checkout", b); err != nil {
					return "", fmt.Errorf("checkout %s: %v: %s", b, err, out)
				}
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
			// A previous launch may have left submodules unfetched or wedged (the sync is
			// time-boxed, and a killed clone does not resume by itself — git_submodule.go).
			// Nothing else on the relaunch path ever retries, so the session would keep
			// landing in the same broken checkout; retry here instead.
			if len(submoduleGaps(dir)) > 0 {
				gitSubmodulesEnsure(dir)
			}
			return dir, nil
		}
		return "", fmt.Errorf("path already exists and is not a worktree: %s", name)
	}
	// git worktree add [-b <newBranch>] <dir> [<base>]
	args := []string{"worktree", "add"}
	if newBranch != "" {
		args = append(args, "-b", newBranch, dir)
		if base != "" {
			args = append(args, base)
		}
	} else {
		args = append(args, dir, base)
	}
	if out, err := gitx.Combined(parentDir, args...); err != nil {
		return "", fmt.Errorf("worktree add: %v: %s", err, out)
	}
	applyGitIdentity(dir)    // commit identity for the worktree (config is shared, but explicit)
	gitSubmodulesEnsure(dir) // per-worktree submodule checkout; parent untouched (verified)
	// A new worktree starts without node_modules/target/.venv, which is exactly when
	// relocating them is free. Only on creation: an existing worktree may already hold
	// a populated tree on EFS, and moving that on a relaunch would stall the session.
	scratchAutoRelocate(dir)
	return dir, nil
}

func handleCloneRepo(w http.ResponseWriter, r *http.Request) {
	var req cloneReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	req.RemoteURL = strings.TrimSpace(req.RemoteURL)
	if req.RemoteURL == "" || strings.HasPrefix(req.RemoteURL, "-") {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_url", "remote_url is required and must not start with '-'")
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
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "name must start with a letter or number, may contain letters/numbers plus . _ @ -, and be at most 96 characters")
		return
	}
	if _, err := os.Stat(dir); err == nil {
		httpx.WriteErr(w, http.StatusConflict, "exists", "repo already exists: "+name)
		return
	}
	if err := gitClone(dir, req.RemoteURL, req.Branch, req.NewBranch); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "clone_failed", err.Error())
		return
	}
	st, _ := gitStatus(dir)
	httpx.WriteJSON(w, http.StatusCreated, Repo{
		Name: name, Path: dir, Branch: st.Branch, Dirty: st.Dirty, Ahead: st.Ahead, Behind: st.Behind,
	})
}

// repoDirFromPath validates {name} and ensures the working copy exists.
func repoDirFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir, ok := resolveRepoDir(r.PathValue("name"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid repo name")
		return "", false
	}
	if !isGitRepo(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such repo: "+r.PathValue("name"))
		return "", false
	}
	return dir, true
}

// repoAnyDirFromPath validates {name} and ensures it is a working copy of EITHER
// kind (git or svn). Used by the vcs-agnostic endpoints (delete); the git-only
// endpoints keep repoDirFromPath so they never run git on an svn folder.
func repoAnyDirFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	dir, ok := resolveRepoDir(r.PathValue("name"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid repo name")
		return "", false
	}
	if !isGitRepo(dir) && !isSvnRepo(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such repo: "+r.PathValue("name"))
		return "", false
	}
	return dir, true
}

func handleRepoStatus(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoViewDir(w, r)
	if !ok {
		return
	}
	st, err := gitStatus(dir)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "git_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// branchInfo describes one checkout target for the branch-switch modal.
type branchInfo struct {
	Name    string `json:"name"`    // checkout name (remote prefix stripped → DWIM tracking)
	Remote  bool   `json:"remote"`  // remote-only (no local branch of this name)
	Unix    int64  `json:"unix"`    // last-commit time, for newest-first sorting
	Date    string `json:"date"`    // last-commit ISO date (display/tooltip)
	Subject string `json:"subject"` // last-commit subject
	Current bool   `json:"current"` // currently checked out
	// WorktreePath is the OTHER working copy that already has this branch checked
	// out ("" = free). git permits a branch in one worktree only, so the pickers
	// disable these rows and offer to open that copy instead (see worktreeBranches).
	WorktreePath string `json:"worktree_path,omitempty"`
}

func handleRepoBranches(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	st, _ := gitStatus(dir)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"branches": gitBranchInfos(dir, st.Branch), "current": st.Branch})
}

// gitBranchInfos lists local branches plus remote-only branches (those without a
// local counterpart), each with its last-commit time and subject, sorted newest
// commit first. Remote-only entries use the short branch name (remote prefix
// stripped) so `git checkout <name>` creates a tracking branch (DWIM). Every local
// entry also carries the other worktree holding it, if any, so pickers can rule out
// a target git would refuse anyway (remote-only names have no local ref, so they are
// never occupied).
func gitBranchInfos(dir, current string) []branchInfo {
	occupied := worktreeBranches(dir)
	const sep = "\x1f" // unit separator: absent from ref names, dates, and subjects
	format := strings.Join([]string{
		"%(refname:short)", "%(committerdate:unix)", "%(committerdate:iso8601)", "%(contents:subject)",
	}, sep)
	infos := []branchInfo{}
	seen := map[string]bool{}
	// Local first so a local branch wins over its remote duplicate.
	for _, ns := range []string{"refs/heads", "refs/remotes"} {
		out, err := gitx.Run(dir, "for-each-ref", "--format="+format, ns)
		if err != nil {
			continue
		}
		isRemote := ns == "refs/remotes"
		for _, line := range strings.Split(out, "\n") {
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
				Current: name == current, WorktreePath: occupied[name],
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
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// A working copy with running sessions is pinned to its branch: switching it
	// out from under live agents corrupts them (see liveSessionsInDir). Refuse and
	// steer the user to open the branch as its own working copy instead. This blocks
	// the Console footgun; agent/manual `git checkout` inside a session still bypasses
	// it (no pre-checkout hook exists) and is caught by branch-drift detection.
	if running := liveSessionsInDir(dir); len(running) > 0 {
		httpx.WriteErr(w, http.StatusConflict, errCodeSessionsRunning,
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
			httpx.WriteErr(w, http.StatusBadRequest, "bad_ref", "branch name is required and must not start with '-'")
			return
		}
		args = []string{"checkout", "-b", name}
		if start := strings.TrimSpace(req.Ref); start != "" {
			if strings.HasPrefix(start, "-") {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_ref", "start ref must not start with '-'")
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
			httpx.WriteErr(w, http.StatusBadRequest, "bad_ref", "branch/ref is required and must not start with '-'")
			return
		}
		// git would refuse a branch already live in another worktree with a raw fatal.
		// Answer with the stable code instead, so the Console can point at the copy that
		// holds it rather than dead-ending on git's message. A sha (detached checkout)
		// never matches a branch name here, so that path is unaffected.
		if occ := worktreeBranches(dir)[ref]; occ != "" {
			writeBranchInUse(w, ref, occ)
			return
		}
		args = []string{"checkout", ref}
	}
	if out, err := gitx.Combined(dir, args...); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "checkout_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	gitSubmodulesUpdate(dir)
	st, _ := gitStatus(dir)
	httpx.WriteJSON(w, http.StatusOK, st)
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
	if out, err := gitx.Combined(dir, "pull", "--ff-only"); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "ff_failed", out)
		return
	}
	gitSubmodulesUpdate(dir)
	st, _ := gitStatus(dir)
	httpx.WriteJSON(w, http.StatusOK, st)
}

// fastForwardWorktreeFromParent brings a linked worktree up to its parent's HEAD.
// It accepts only a strict ancestor relationship, so it can never create a merge
// commit or resolve a divergence implicitly.
func fastForwardWorktreeFromParent(parent, dir string) error {
	if integration := gitWorktreeIntegration(parent, dir, ""); integration.Relation != "contained" {
		return fmt.Errorf("the worktree is not strictly behind its parent")
	}
	parentHead, err := gitx.Run(parent, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	if out, err := gitx.Combined(dir, "merge", "--ff-only", strings.TrimSpace(parentHead)); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	gitSubmodulesUpdate(dir)
	return nil
}

// handleRepoParentFF is the local-only counterpart to handleRepoFF: it brings a
// linked worktree up to its parent, without fetching or consulting origin. The
// relationship is re-checked server-side so an old Console row stays safe.
func handleRepoParentFF(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoDirFromPath(w, r)
	if !ok {
		return
	}
	if !isLinkedWorktree(dir) {
		httpx.WriteErr(w, http.StatusBadRequest, "not_worktree", "parent fast-forward is only available for linked worktrees")
		return
	}
	parent := worktreeParent(dir)
	if parent == "" || !isGitRepo(parent) {
		httpx.WriteErr(w, http.StatusNotFound, "parent_not_found", "cannot resolve the parent working copy")
		return
	}
	if err := fastForwardWorktreeFromParent(parent, dir); err != nil {
		httpx.WriteErr(w, http.StatusConflict, "parent_ff_not_possible", err.Error())
		return
	}
	st, _ := gitStatus(dir)
	httpx.WriteJSON(w, http.StatusOK, st)
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
	args := []string{"fetch"}
	if req.Prune {
		args = append(args, "--prune")
	}
	if out, err := gitx.Combined(dir, args...); err != nil {
		httpx.WriteErr(w, http.StatusBadGateway, "fetch_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	st, _ := gitStatus(dir)
	httpx.WriteJSON(w, http.StatusOK, st)
}

func handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	dir, ok := repoAnyDirFromPath(w, r)
	if !ok {
		return
	}
	// Deleting the working copy out from under live sessions is even worse than a
	// branch switch: their cwd vanishes mid-flight. Refuse while any session runs
	// there — the user must stop/archive them first (same guard as checkout).
	if running := liveSessionsInDir(dir); len(running) > 0 {
		httpx.WriteErr(w, http.StatusConflict, errCodeSessionsRunningDelete,
			fmt.Sprintf("%d session(s) are running in this working copy (%s); deleting it would break them. Stop those sessions first.",
				len(running), strings.Join(running, ", ")))
		return
	}
	// 削除ロック（docs/45）: 作業コピー自身のロックと、そこに住むロック済みセッションの
	// 巻き添え。どちらも force=true では越えられない — ロック解除が唯一の道。
	if repoLocked(dir) {
		httpx.WriteErr(w, http.StatusForbidden, errCodeLocked,
			"working copy is locked against deletion; unlock it first")
		return
	}
	if locked := lockedSessionsInDir(session.ListMetas(), dir); len(locked) > 0 {
		httpx.WriteErr(w, http.StatusForbidden, errCodeLockedSessions,
			fmt.Sprintf("%d locked session(s) live in this working copy (%s); deleting it would strand them. Unlock them first.",
				len(locked), strings.Join(locked, ", ")))
		return
	}
	// An SVN working copy has no worktree registry — just a folder with a .svn dir.
	// Remove it directly (after the session guard above); the git worktree logic below
	// applies only to git working copies.
	if !isGitRepo(dir) && isSvnRepo(dir) {
		if err := os.RemoveAll(dir); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		if pruneSessions(r) {
			forgetNonLiveMetasUnder(dir)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("name")})
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
				httpx.WriteErr(w, http.StatusConflict, errCodeWorktreeDirty,
					"worktree has uncommitted or unpushed changes; pass force=true to delete anyway")
				return
			}
		}
		parent := worktreeParent(dir)
		if parent == "" {
			httpx.WriteErr(w, http.StatusInternalServerError, "delete_failed", "cannot resolve worktree parent")
			return
		}
		if out, err := gitx.Combined(parent, "worktree", "remove", "--force", dir); err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, errCodeWorktreeRemoveFailed, out)
			return
		}
		_ = gitx.Cmd(parent, "worktree", "prune").Run()
		if pruneSessions(r) {
			forgetNonLiveMetasUnder(dir)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("name")})
		return
	}

	// A main working copy with linked worktrees must not be removed — os.RemoveAll would
	// break every worktree hanging off it. Refuse and let the user delete the worktrees
	// first.
	if n := linkedWorktreeCount(dir); n > 0 {
		httpx.WriteErr(w, http.StatusConflict, errCodeHasWorktrees,
			fmt.Sprintf("this working copy has %d worktree(s) branched off it; delete those first", n))
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	if pruneSessions(r) {
		forgetNonLiveMetasUnder(dir)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("name")})
}

// pruneSessions reports whether the delete should also forget the (non-live) session
// metas that lived in the removed dir. Opt-in via ?prune_sessions=1 so the Console's
// plain "delete working copy" keeps its existing behavior (metas untouched); only the
// cleanup path (MCP delete_worktree) sets it, completing the tidy-up so a stopped
// session isn't left pointing at a directory that no longer exists.
func pruneSessions(r *http.Request) bool {
	v := r.URL.Query().Get("prune_sessions")
	return v == "1" || v == "true"
}

// forgetNonLiveMetasUnder removes the metas of any NON-live session whose cwd is at or
// under dir. handleDeleteRepo has already refused when a LIVE session runs there, so the
// remaining metas are stopped/archived — unusable once dir is gone (resume would hit
// DirGoneErr). Belt-and-suspenders: re-check liveness here too. jsonl is left on disk
// (same as stop = forget meta, keep transcript).
func forgetNonLiveMetasUnder(dir string) {
	live := tmuxx.LiveSessionNames()
	for _, m := range session.ListMetas() {
		if m.Dir != dir && !strings.HasPrefix(m.Dir, dir+string(os.PathSeparator)) {
			continue
		}
		if live[m.Name] || (m.DriverKind() == session.DriverManaged && managedAlive(m)) {
			continue
		}
		if m.Locked {
			continue // 削除ロック（docs/45）— 掃除の巻き添えでも消さない
		}
		if m.Archived {
			// アーカイブは「棚」— WT を消しても会話は棚に残し、回収は棚側の削除
			// （delete_session の gz 退避付き）に任せる。ここで忘れると、①一括アーカイブ
			// →②WT削除 の段階を踏んだ人の棚が黙って消える。行は「フォルダ無し」表示になる。
			continue
		}
		finalizeSessionUsage(m) // 使用量台帳へ確定してから忘れる（docs/46 §3-b）
		session.RemoveMeta(m.Name)
	}
}
