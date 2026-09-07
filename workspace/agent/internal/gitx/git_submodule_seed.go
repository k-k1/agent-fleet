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

// maxSeedDepth bounds the descent into nested submodules. In practice the parent's own store
// bounds it already — each level has to exist there as a real directory — so this is only here
// so that a symlink inside that store cannot spin.
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
	for _, sm := range submoduleSpecs(dir) {
		src := filepath.Join(store, filepath.FromSlash(sm.Name))
		if !isGitDir(src) {
			continue // the parent never checked this one out; there is nothing to seed from
		}
		sub := filepath.Join(dir, filepath.FromSlash(sm.Path))
		if submodulePathEmpty(sub) { // never clone over a working tree
			if err := seedSubmodule(dir, sm, src); err != nil {
				log.Printf("submodules %s: seeding %s from the parent failed: %v", dir, sm.Path, err)
				continue
			}
		}
		seedSubmodulesFrom(sub, filepath.Join(src, "modules"), depth+1)
	}
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
	if out, err := Combined(dir,
		"-c", "protocol.file.allow=always",
		"-c", "submodule."+sm.Name+".url="+src,
		"submodule", "update", "--", sm.Path); err != nil {
		return fmt.Errorf("%v: %s", err, out)
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
	out, err := Run(dir, "config", "-f", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		return nil
	}
	var specs []submoduleSpec
	for _, line := range strings.Split(out, "\n") {
		key, path, ok := strings.Cut(line, " ")
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
