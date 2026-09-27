package gitx

// What a worktree delete leaves behind, so the folder can be put back as it was (issue #1042).
//
// Removing a linked worktree keeps its branch but loses three things: which commit it was on
// (the branch may move or be deleted later, and unpushed commits then become unreachable and
// are collected by gc), its uncommitted and untracked work, and the fact that it existed at
// all. The delete now records a tombstone in the trash, pins the commit with a ref so gc
// cannot take it, and — when the tree is dirty — first folds the working tree into a commit
// of its own. Restoring from the trash, or recreating the folder from the archive, reads the
// tombstone back (RecreateDeleted).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeletedWorktreeRefPrefix is where the pins live. Outside refs/heads and refs/tags, so no
// branch list, push or fetch refspec ever shows them.
const DeletedWorktreeRefPrefix = "refs/af/deleted-worktrees/"

// WorktreeTombstone is the record of one deleted worktree.
type WorktreeTombstone struct {
	// Path is the worktree's absolute path; Name its folder name under ~/repos.
	Path string `json:"path"`
	Name string `json:"name"`
	// Parent is the main working copy's absolute path: the repository the ref lives in.
	Parent string `json:"parent"`
	// Branch is the branch it had checked out, "" when HEAD was detached.
	Branch string `json:"branch,omitempty"`
	// Head is the commit HEAD was on.
	Head string `json:"head"`
	// Snapshot is a commit whose parent is Head and whose tree is the working tree at the
	// delete, untracked files included and ignored files not; "" when the tree was clean.
	Snapshot string `json:"snapshot,omitempty"`
	// Ref pins Snapshot (else Head) against gc; the trash drops it when the entry is purged.
	Ref string `json:"ref,omitempty"`
	// Shelved names the stopped AI sessions the delete moved to the shelf; restoring the
	// entry from the trash takes them off it again.
	Shelved []string `json:"shelved,omitempty"`
}

// Target is the commit the pin points at: the snapshot when there is one, which keeps Head
// reachable as its parent.
func (t WorktreeTombstone) Target() string {
	if t.Snapshot != "" {
		return t.Snapshot
	}
	return t.Head
}

// snapshotIdentity makes commit-tree independent of the user's git config: a missing
// user.email must not be what stops a delete, and this commit is never pushed.
var snapshotIdentity = []string{
	"GIT_AUTHOR_NAME=Agent Fleet", "GIT_AUTHOR_EMAIL=agent-fleet@localhost",
	"GIT_COMMITTER_NAME=Agent Fleet", "GIT_COMMITTER_EMAIL=agent-fleet@localhost",
}

// PrepareTombstone reads what a delete of the linked worktree dir would lose, snapshotting a
// dirty working tree into a commit. ok is false for a worktree with no commit yet (an unborn
// branch has nothing to pin; its delete proceeds unrecorded).
//
// The snapshot is built in a temporary index, so the worktree's own index — and the shared
// stash stack, which belongs to every session using this repository — are never touched.
func PrepareTombstone(dir, parent string) (t WorktreeTombstone, ok bool, err error) {
	head, herr := Run(dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if herr != nil || head == "" {
		return t, false, nil
	}
	t = WorktreeTombstone{Path: dir, Name: filepath.Base(dir), Parent: parent, Head: head}
	if b := GitCurrentBranch(dir); b != "" && b != "(detached)" {
		t.Branch = b
	}
	dirty, err := Run(dir, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=dirty")
	if err != nil {
		return t, false, fmt.Errorf("status: %w", err)
	}
	if dirty == "" {
		return t, true, nil
	}
	snap, err := snapshotWorkingTree(dir, head, t.Name)
	if err != nil {
		return t, false, err
	}
	t.Snapshot = snap
	return t, true, nil
}

func snapshotWorkingTree(dir, head, name string) (string, error) {
	tmp, err := os.MkdirTemp("", "af-snapshot-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := append([]string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}, snapshotIdentity...)
	run := func(args ...string) (string, error) {
		cmd := Cmd(dir, args...)
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := run("read-tree", head); err != nil {
		return "", err
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	tree, err := run("write-tree")
	if err != nil {
		return "", err
	}
	return run("commit-tree", tree, "-p", head, "-m", "Uncommitted work of "+name+" when it was deleted")
}

// PinDeletedWorktree points ref at t's target in the parent repository.
func PinDeletedWorktree(t WorktreeTombstone) error {
	if !strings.HasPrefix(t.Ref, DeletedWorktreeRefPrefix) {
		return fmt.Errorf("not a deleted-worktree ref: %q", t.Ref)
	}
	if out, err := Combined(t.Parent, "update-ref", t.Ref, t.Target()); err != nil {
		return fmt.Errorf("update-ref: %v: %s", err, out)
	}
	return nil
}

// UnpinDeletedWorktree drops the pin. Best-effort: a parent that is gone took the ref with it.
func UnpinDeletedWorktree(t WorktreeTombstone) {
	if !strings.HasPrefix(t.Ref, DeletedWorktreeRefPrefix) || !IsGitRepo(t.Parent) {
		return
	}
	_ = Cmd(t.Parent, "update-ref", "-d", t.Ref).Run()
}
