// Package projcfg is the shared ground floor for "project-scoped" tools (docs/57):
// tools that read/edit files INSIDE one working copy on the "management axis" (as
// opposed to internal/mcpreg's "distribution axis", which af writes automatically
// into each CLI's user/global config — docs/57 §0). The first tool built on it is
// internal/mcpproj (docs/56, project-scope MCP servers).
//
// This file holds the part every such tool needs before it can read a single file:
// telling git from svn from neither, whether the working copy is a linked worktree,
// and whether a path inside it is tracked / ignored. It takes an already-resolved,
// already-existing directory — repo NAME resolution and existence stay in package
// main (resolveRepoDir / repoAnyDirFromPath), which an internal package cannot
// import without a cycle, and which already has the traversal-safe name charset.
package projcfg

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// VCS identifies the version control (if any) managing a working copy.
const (
	VCSGit  = "git"
	VCSSVN  = "svn"
	VCSNone = "none"
)

// DetectVCS reports which VCS (if any) manages dir. Mirrors git.go's isGitRepo /
// svn.go's isSvnRepo (duplicated rather than imported — package main cannot be
// imported from here, and each check is two lines).
func DetectVCS(dir string) string {
	if isGitRepo(dir) {
		return VCSGit
	}
	if isSvnRepo(dir) {
		return VCSSVN
	}
	return VCSNone
}

func isGitRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular()) // file form: worktree/submodule gitlink
}

func isSvnRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".svn"))
	return err == nil && fi.IsDir()
}

// IsWorktree reports whether dir is a linked git worktree (`git worktree add`)
// rather than a normal clone — docs/56 §4.4 requires the caller to say so, because
// a write here would not be visible to the repo's other working copies until
// committed and pulled there. Mirrors git.go's isLinkedWorktree.
func IsWorktree(dir string) bool {
	out, err := gitx.Run(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false
	}
	return strings.Contains(filepath.ToSlash(out), "/.git/worktrees/")
}

// TrackState is what the VCS knows about one repo-relative path, for the secret
// warnings in docs/56 §7.2 / docs/57 憲章6. Only git can actually answer; svn and a
// missing VCS come back Uncertain rather than a guessed false — "判定不可" is itself
// the fact to display, never silently downgraded to "not tracked".
type TrackState struct {
	Tracked   bool
	Ignored   bool
	Uncertain bool
}

// Track answers TrackState for rel (repo-relative, forward slashes) under dir/vcs.
// vcs is the caller's own DetectVCS(dir) result, passed in rather than re-detected,
// so a caller juggling several paths in the same working copy pays the git/svn probe
// once.
func Track(dir, vcs, rel string) TrackState {
	if vcs != VCSGit {
		return TrackState{Uncertain: true}
	}
	tracked := gitx.OK(dir, "ls-files", "--error-unmatch", "--", rel)
	// check-ignore exits 0 when rel IS ignored, 1 when it is not, and >1 on a real
	// error — gitx.OK folds every non-zero into false, which is the safe direction
	// here (a probe error can only under-report "ignored", never falsely claim it).
	ignored := gitx.OK(dir, "check-ignore", "-q", "--", rel)
	return TrackState{Tracked: tracked, Ignored: ignored}
}
