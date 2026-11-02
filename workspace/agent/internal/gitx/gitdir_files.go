package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
)

// spawned counts the git processes this package has started. It is a test seam: the
// session list must stay at zero spawns per poll (each git reads the global config,
// which lives on an EFS mount in production).
var spawned atomic.Int64

// SpawnCount reports how many git processes Cmd / CmdContext have built so far.
func SpawnCount() int64 { return spawned.Load() }

// gitDirInfoFromFiles answers GitDirInfo by reading the repository's files instead of
// running `git rev-parse --abbrev-ref HEAD --absolute-git-dir`. ok=false means the case
// cannot be answered safely from files and the caller must ask git; a definite answer
// (including "not a repo": "", false) is ok=true.
//
// Anything that could make git's answer differ from the files' falls back: git env
// overrides, bare repos, repos owned by someone else (safe.directory), a HEAD that is
// not `ref: refs/heads/<n>` or a full SHA (reftable, other refs), and a branch name
// another ref namespace also resolves (git then prints "heads/<n>").
func gitDirInfoFromFiles(dir string) (branch string, worktree, ok bool) {
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM"} {
		if os.Getenv(k) != "" {
			return "", false, false
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false, false
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", false, true // git -C on a missing dir fails
	}
	// git discovers the repo by walking up from the logical cwd.
	for cur := abs; ; {
		dotgit := filepath.Join(cur, ".git")
		if fi, err := os.Lstat(dotgit); err == nil {
			return readDotGit(cur, dotgit, fi)
		} else if !os.IsNotExist(err) {
			return "", false, false
		}
		if looksLikeGitDir(cur) {
			return "", false, false // bare repo (or a dir inside .git)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false, true
		}
		cur = parent
	}
}

func looksLikeGitDir(d string) bool {
	if _, err := os.Stat(filepath.Join(d, "HEAD")); err != nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(d, "objects"))
	return err == nil && fi.IsDir()
}

func readDotGit(top, dotgit string, fi os.FileInfo) (string, bool, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return "", false, false
	}
	gitdir := dotgit
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", false, false
	}
	if fi.Mode().IsRegular() {
		b, err := os.ReadFile(dotgit)
		if err != nil {
			return "", false, false
		}
		line, _, _ := strings.Cut(string(b), "\n")
		p, found := strings.CutPrefix(strings.TrimRight(line, "\r"), "gitdir: ")
		if !found || p == "" {
			return "", false, false
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(top, p)
		}
		gitdir = p
	} else if !fi.IsDir() {
		return "", false, false
	}
	real, err := filepath.EvalSymlinks(gitdir) // git prints the resolved path
	if err != nil {
		return "", false, false
	}
	gitdir = filepath.Clean(real)
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return "", false, false
	}
	worktree := strings.Contains(filepath.ToSlash(gitdir), "/.git/worktrees/")
	h := strings.TrimSpace(string(head))
	if isFullSHA(h) {
		return "(detached)", worktree, true
	}
	name, found := strings.CutPrefix(h, "ref: refs/heads/")
	if !found || name == "" {
		return "", false, false
	}
	common := gitdir
	if b, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	}
	exists, ok := branchRefState(common, name)
	switch {
	case !ok:
		return "", false, false
	case !exists:
		return "", false, true // rev-parse HEAD fails on an unborn branch
	}
	return name, worktree, true
}

func isFullSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// branchRefState reports whether refs/heads/<name> exists (loose or packed) and
// ok=false when another ref could make git abbreviate it differently.
func branchRefState(common, name string) (exists, ok bool) {
	packed := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(common, "packed-refs")); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			if f := strings.Fields(ln); len(f) == 2 && !strings.HasPrefix(ln, "#") && !strings.HasPrefix(ln, "^") {
				packed[f[1]] = true
			}
		}
	} else if !os.IsNotExist(err) {
		return false, false
	}
	has := func(ref string) bool {
		if packed[ref] {
			return true
		}
		_, err := os.Lstat(filepath.Join(common, filepath.FromSlash(ref)))
		return err == nil
	}
	for _, other := range []string{"refs/" + name, "refs/tags/" + name, "refs/remotes/" + name, "refs/remotes/" + name + "/HEAD"} {
		if has(other) {
			return false, false
		}
	}
	if _, err := os.Lstat(filepath.Join(common, filepath.FromSlash(name))); err == nil {
		return false, false
	}
	if has("refs/heads/" + name) {
		return true, true
	}
	return false, true
}
