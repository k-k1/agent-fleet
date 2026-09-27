package main

// The trash side of a worktree delete (issue #1042). gitx.PrepareTombstone reads what the delete
// would lose; this file pins it, files it in the cleanup archive, and takes it back out:
// restoring the entry puts the worktree back at its path with its uncommitted work and returns
// the sessions the delete shelved, and purging it drops the pin so gc may collect the commits.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// recordDeletedWorktree pins t's commit and writes its trash entry. It is gitx's
// RecordDeletedWorktree: the delete calls it before removing anything, and undo if the delete
// then stops.
func recordDeletedWorktree(t gitx.WorktreeTombstone) (func(), error) {
	now := nowUTC()
	// Named apart from the archive id, which a collision may still suffix while writing; the
	// nanoseconds keep two deletes of the same folder in one second apart.
	t.Ref = gitx.DeletedWorktreeRefPrefix + idSlug(t.Name) + "-" + now.Format("20060102-150405.000000000")
	if err := gitx.PinDeletedWorktree(t); err != nil {
		return nil, err
	}
	man := cleanupManifest{
		ID: newCleanupID(now, idSlug(t.Name)), At: now.Format(time.RFC3339),
		Reason: "delete_worktree", Worktree: &t,
	}
	if err := writeCleanupArchive(&man, nil); err != nil {
		gitx.UnpinDeletedWorktree(t)
		return nil, err
	}
	invalidateCleanupUsage()
	return func() {
		sessionx.WithCleanupLock(func() { _, _ = purgeCleanupArchive(man.ID) })
		gitx.UnpinDeletedWorktree(t)
		invalidateCleanupUsage()
	}, nil
}

// latestTombstone is the newest trash entry recording a delete of the worktree at dir, or nil.
func latestTombstone(dir string) *gitx.WorktreeTombstone {
	for _, m := range listCleanupArchives() { // newest first
		if m.Worktree != nil && filepath.Clean(m.Worktree.Path) == dir {
			t := *m.Worktree
			return &t
		}
	}
	return nil
}

// restoreConflict is a restore the archive itself cannot finish, with the stable code the
// Console localizes; the way forward is the archive's "Recreate working copy", which can ask
// for what is missing (a new branch name).
type restoreConflict struct{ code, msg string }

func (e *restoreConflict) Error() string { return e.msg }

// restoreDeletedWorktree puts a deleted worktree back from its tombstone, then takes the
// sessions its delete shelved off the shelf. A folder already back (recreated since) is left
// as it is, and only the sessions are restored.
func restoreDeletedWorktree(t gitx.WorktreeTombstone) error {
	dir, ok := gitx.ResolveRepoDir(t.Name)
	if !ok || dir != filepath.Clean(t.Path) {
		return fmt.Errorf("the worktree %s was not under the repositories folder", t.Name)
	}
	var err error
	sessionx.WithDeletionGate(func() { err = recreateFromTombstone(dir, t) })
	if err != nil {
		return err
	}
	for _, name := range t.Shelved {
		if m, ok := session.ReadMeta(name); ok && m.Archived && filepath.Clean(m.Dir) == dir {
			sessionx.RestoreSession(name)
		}
	}
	return nil
}

func recreateFromTombstone(dir string, t gitx.WorktreeTombstone) error {
	if _, err := os.Lstat(dir); err == nil {
		if gitx.IsGitRepo(dir) {
			return nil // recreated since; nothing to put back
		}
		return &restoreConflict{errCodeRecreatePathExists, "something that is not a working copy sits at " + t.Name}
	}
	if !gitx.IsGitRepo(t.Parent) {
		return &restoreConflict{errCodeRecreateParentMissing, "the working copy " + filepath.Base(t.Parent) + " this worktree came from is gone"}
	}
	cands := gitx.ResolveRecreate(t.Parent, t.Name, nil, nil, &t)
	if len(cands) == 0 || cands[0].Source != gitx.RecreateDeleted {
		return &restoreConflict{errCodeRecreateStale, "the commit this worktree was on is no longer in the repository"}
	}
	c := cands[0]
	switch {
	case c.InUse != "":
		return &restoreConflict{errCodeBranchInUse, fmt.Sprintf("branch %q is checked out in %s; recreate it from the archive on a new branch", c.Branch, c.InUse)}
	case c.NeedsNewBranch():
		return &restoreConflict{errCodeRecreateNeedsNewBranch, "the branch has moved since the delete, or there was none; recreate it from the archive on a new branch"}
	}
	if err := gitx.RecreateWorktreeAt(t.Parent, dir, c, ""); err != nil {
		return &restoreConflict{errCodeRecreateFailed, err.Error()}
	}
	return nil
}
