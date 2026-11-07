package gitx

// Seeding a new worktree's submodules from the parent working copy's own object store.
//
// MEASURED (git 2.47): a worktree's submodule gitdir is
// <parent>/.git/worktrees/<wt>/modules/<name>, a store entirely separate from the parent's
// <parent>/.git/modules/<name>. Nothing links the two, so `git submodule update` in a fresh
// worktree re-clones every submodule from the remote in full — with the submodule's remote made
// unreachable the update fails outright ("repository does not exist") even though the parent
// holds every object on the same disk. For the 1.4 GB submodule behind git_submodule.go's
// header that is a 1.4 GB transfer per worktree, and 1.4 GB of disk again per worktree.
//
// Cloning from the parent's store instead makes it a local operation: measured 0.23 s for a
// 41 MB submodule with the remote OFFLINE, and git hardlinks the objects (same inode), so the
// worktree's store cost 168 KB rather than 41 MB. Only then does the normal update run, to
// fill in whatever the parent did not have.
//
// Nesting works the same way one level down — a nested submodule's objects are at
// <store>/<name>/modules/<nested name> — but it cannot be done in one pass: a nested submodule
// is declared only inside its parent submodule, which does not exist until that one is cloned.
// Hence the descent in seedSubmodulesFrom.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// submoduleSpec is one .gitmodules entry. Both .git/config and .git/modules are keyed by the
// entry's NAME, which is only conventionally the same string as its path.
type submoduleSpec struct{ Name, Path string }

// maxSeedDepth bounds the descent into nested submodules. Nothing here can loop forever even
// without it: the store path gains a segment per level, so a cyclic store is eventually refused
// by the filesystem — MEASURED, a store whose `modules` entry is a symlink back to itself
// unwinds at depth 41, where Linux gives up after 40 symlink traversals. But that is ~3 s of
// pointless git invocations to arrive at, and a kernel limit nobody chose is not a bound to
// lean on, so the descent stops at a depth no real repository reaches.
const maxSeedDepth = 8

// seedSubmodulesFromParent populates dir's not-yet-checked-out submodules by cloning them from
// parentDir's own module store instead of from the network. Best-effort and silent about the
// ordinary cases (no submodules, a submodule the parent never checked out): every one it skips
// is simply left to the normal update that follows.
func seedSubmodulesFromParent(dir, parentDir string) {
	seedSubmodulesFrom(dir, parentModuleStore(parentDir), 0)
}

// seedSubmodulesFrom seeds dir's submodules out of store — a .git/modules directory holding one
// gitdir per submodule NAME — and then descends into each of them. A nested submodule's objects
// sit at <store>/<name>/modules/<nested name> (measured), so the same trick works all the way
// down; and it has to, because a nested submodule is only declared inside its parent submodule
// and so cannot be seeded before that one exists.
func seedSubmodulesFrom(dir, store string, depth int) {
	if store == "" || !hasSubmodules(dir) {
		return
	}
	if depth >= maxSeedDepth {
		log.Printf("submodules %s: nested deeper than %d, leaving the rest to the update", dir, maxSeedDepth)
		return
	}
	// The url override below is read from config, so init has to have expanded .gitmodules
	// first, and the SSH→HTTPS rewrite has to happen before we copy a url onto a seeded
	// clone's origin — otherwise the submodule is left pointing at a host this workspace has
	// no key for. Both are idempotent; gitSubmodulesUpdate runs them again afterwards.
	if out, err := Combined(dir, "submodule", "init"); err != nil {
		log.Printf("submodules %s: init failed: %v: %s", dir, err, out)
		return
	}
	rewriteSubmoduleSSHURLs(dir)
	specs := submoduleSpecs(dir)
	// git resolves a path to the LAST entry declaring it, so with two entries on one path the
	// `-c submodule.<name>.url` override below can land on a different entry than the one
	// checked here, leaving the update to clone a .gitmodules url with the file protocol open.
	// An ambiguous path or name is never seeded.
	pathCount, nameCount := map[string]int{}, map[string]int{}
	for _, sm := range specs {
		pathCount[sm.Path]++
		nameCount[sm.Name]++
	}
	for _, sm := range specs {
		if pathCount[sm.Path] > 1 || nameCount[sm.Name] > 1 {
			log.Printf("submodules %s: not seeding a submodule declared more than once", dir)
			continue
		}
		// The name comes from the branch's .gitmodules, i.e. from whoever can push to it.
		// Seeding is only an optimisation, so an unusable name is left to the normal update.
		if !validSubmoduleName(sm.Name) {
			log.Printf("submodules %s: not seeding submodule with an unsupported name", dir)
			continue
		}
		// The path reaches git as a pathspec and the filesystem below, so it is held to the
		// same standard as the name and must be a gitlink in this very worktree.
		if !validSubmodulePath(sm.Path) || !isGitlink(dir, sm.Path) {
			log.Printf("submodules %s: not seeding submodule with an unsupported path", dir)
			continue
		}
		src := filepath.Join(store, filepath.FromSlash(sm.Name))
		if !withinDir(store, src) || !isGitDir(src) {
			continue // the parent never checked this one out; there is nothing to seed from
		}
		sub := filepath.Join(dir, filepath.FromSlash(sm.Path))
		if !withinDir(dir, sub) {
			continue
		}
		if submodulePathEmpty(sub) { // never clone over a working tree
			if err := seedSubmodule(dir, sm, src); err != nil {
				log.Printf("submodules %s: seeding %s from the parent failed: %v", dir, sm.Path, err)
				continue
			}
		}
		seedSubmodulesFrom(sub, filepath.Join(src, "modules"), depth+1)
	}
}

// validSubmoduleName reports whether name is safe to use as a directory under the module store
// and inside a `-c submodule.<name>.url=` argument. git splits a -c argument at the first `=`,
// so a name containing one would set a different key; a `..` element would leave the store.
// When in doubt, answer false: the normal update still handles the submodule.
func validSubmoduleName(name string) bool {
	if name == "" || name[0] == '-' || name[0] == '/' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '/', c == '-':
		default:
			return false
		}
	}
	for _, el := range strings.Split(name, "/") {
		if el == ".." || el == "." {
			return false
		}
	}
	return true
}

// validSubmodulePath reports whether path is a plain relative path with no `.`/`..` element,
// no leading `-`, `/` or `:`, and no glob or control characters.
func validSubmodulePath(path string) bool {
	if path == "" || strings.ContainsAny(path[:1], "-/:") || strings.ContainsAny(path, "*?[]\\") {
		return false
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, el := range strings.Split(path, "/") {
		if el == "" || el == "." || el == ".." {
			return false
		}
	}
	return true
}

// isGitlink reports whether path is a gitlink (mode 160000) in dir's index.
func isGitlink(dir, path string) bool {
	cmd := Cmd(dir, "ls-files", "--stage", "-z", "--", path)
	cmd.Env = append(cmd.Env, "GIT_LITERAL_PATHSPECS=1")
	out, err := cmd.Output()
	return err == nil && strings.HasPrefix(string(out), "160000 ")
}

// withinDir reports whether path is strictly inside root, lexically.
func withinDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// seedSubmodule clones one submodule out of the parent's store and then points it back at its
// real remote, so everything afterwards (fetch, the wedge repair, the user's own pushes) is
// unaffected by how the objects got here.
func seedSubmodule(dir string, sm submoduleSpec, src string) error {
	// protocol.file.allow is off by default (CVE-2022-39253, submodule URLs pointing at local
	// paths) and is re-enabled for THIS invocation only, for a path computed from parentDir —
	// never for a url out of .gitmodules, which is where that CVE lives.
	//
	// -c rather than `git config`: .git/config is shared with the parent and every sibling
	// worktree, so writing the local path there would redirect their fetches to it too.
	//
	// GIT_LITERAL_PATHSPECS: the path is a pathspec to git, and a glob in it would widen this
	// file-protocol-enabled update to submodules whose urls came straight from .gitmodules.
	cmd := Cmd(dir,
		"-c", "protocol.file.allow=always",
		"-c", "submodule."+sm.Name+".url="+src,
		"submodule", "update", "--", sm.Path)
	cmd.Env = append(cmd.Env, "GIT_LITERAL_PATHSPECS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	// The clone recorded the parent's store as its origin. `git submodule sync` would fix that
	// too, but it re-reads .gitmodules and would undo the SSH→HTTPS rewrite above, so take the
	// url from config instead.
	url, err := Run(dir, "config", "--get", "submodule."+sm.Name+".url")
	if err != nil || url == "" {
		return fmt.Errorf("no configured url for %q", sm.Name)
	}
	sub := filepath.Join(dir, filepath.FromSlash(sm.Path))
	if out, err := Combined(sub, "remote", "set-url", "origin", url); err != nil {
		return fmt.Errorf("remote set-url: %v: %s", err, out)
	}
	return nil
}

// isGitDir reports whether path is a git directory itself rather than a working copy holding
// one — which is what .git/modules/<name> is, so IsGitRepo (it looks for a `.git` entry) says
// no to every one of them.
func isGitDir(path string) bool {
	if fi, err := os.Stat(filepath.Join(path, "objects")); err != nil || !fi.IsDir() {
		return false
	}
	_, err := os.Stat(filepath.Join(path, "HEAD"))
	return err == nil
}

// parentModuleStore is parentDir's .git/modules, where git keeps one full object store per
// submodule name. Empty when there is none — a parent that never checked a submodule out has
// nothing to lend.
func parentModuleStore(parentDir string) string {
	gitDir, err := Run(parentDir, "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(parentDir, gitDir)
	}
	store := filepath.Join(gitDir, "modules")
	if fi, err := os.Stat(store); err != nil || !fi.IsDir() {
		return ""
	}
	return store
}

// submoduleSpecs reads dir's .gitmodules. Nested submodules are absent by design: they are
// only declared inside their parent submodule, which does not exist yet at seeding time.
func submoduleSpecs(dir string) []submoduleSpec {
	// -z: entries are NUL-separated and key/value newline-separated, so a space inside a name
	// cannot be mistaken for the separator.
	cmd := Cmd(dir, "config", "-z", "-f", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	var specs []submoduleSpec
	for _, line := range strings.Split(string(raw), "\x00") {
		key, path, ok := strings.Cut(line, "\n")
		if !ok || strings.TrimSpace(path) == "" {
			continue
		}
		name, ok := strings.CutPrefix(key, "submodule.")
		if !ok {
			continue
		}
		name, ok = strings.CutSuffix(name, ".path")
		if !ok || name == "" {
			continue
		}
		specs = append(specs, submoduleSpec{Name: name, Path: strings.TrimSpace(path)})
	}
	return specs
}
