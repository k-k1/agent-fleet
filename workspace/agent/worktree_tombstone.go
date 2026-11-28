package main

// The trash side of a worktree delete (issue #1042). gitx.PrepareTombstone reads what the delete
// would lose; this file pins it, files it in the cleanup archive, and takes it back out:
// restoring the entry puts the worktree back at its path with its uncommitted work and returns
// the sessions the delete shelved, and purging it drops the pin so gc may collect the commits.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// recordDeletedWorktree pins t's commit and writes its trash entry. It is gitx's
// RecordDeletedWorktree: keep the entry protected until the delete has finished with it.
func recordDeletedWorktree(t gitx.WorktreeTombstone) (func(keep bool), error) {
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
	if err := writeCleanupBundle(&man, nil, true); err != nil {
		gitx.UnpinDeletedWorktree(t)
		return nil, err
	}
	return func(keep bool) {
		if !keep {
			discardPreparedTrash(man.ID)
			gitx.UnpinDeletedWorktree(t)
		}
		releaseTrash(man.ID)
		invalidateCleanupUsage()
	}, nil
}

// latestTombstone is the newest trash entry recording a delete of the worktree at dir, or nil.
func latestTombstone(dir string) *gitx.WorktreeTombstone {
	for _, m := range listCleanupArchives() { // newest first
		if m.Worktree != nil && !trashPending(m.ID) && (filepath.Clean(m.Worktree.Path) == dir || gitx.IsReposEntryPath(m.Worktree.Path, filepath.Base(dir))) {
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

// worktreeRestoreChecked is a test seam between a restore's check of the tombstone's path and the
// steps that use it. Nil in production.
var worktreeRestoreChecked func()

// worktreeRestoreRecord is a worktree restore's progress, kept next to its archive from the first
// attempt on. Shelved holds the shelf revision of each session still on the shelf when the
// first attempt began, so a retry after the member shelved one again leaves it on the shelf.
// Unread names the sessions whose meta could not be read yet: each gets its revision on the first
// attempt that can read it, and the restore is not complete until none is left. Ref is the
// tombstone's pin, unique per delete: a record a purge failed to remove can never stand in for
// a later entry that reuses the id.
type worktreeRestoreRecord struct {
	ID       string            `json:"id"`
	Ref      string            `json:"ref"`
	Shelved  map[string]string `json:"shelved"`
	Unread   []string          `json:"unread,omitempty"`
	Complete bool              `json:"complete,omitempty"`
}

func worktreeRestoreRecordName(id string) string { return id + ".worktree-restore" }

// Test seams: after the folder is back and before its sessions are, and after the sessions are
// back and before the record says so. Nil in production.
var worktreeRestoreRecreated, worktreeBeforeRestoreComplete func()

// restoreDeletedWorktree puts a deleted worktree back from its tombstone, then takes the
// sessions its delete shelved off the shelf. A folder already back (recreated since) is left
// as it is, and only the sessions are restored. trash, id and archive name the entry t came
// from; its progress record lives there.
func restoreDeletedWorktree(trash *os.Root, id string, archive os.FileInfo, t gitx.WorktreeTombstone) error {
	if _, ok := gitx.ResolveRepoDir(t.Name); !ok || !gitx.IsReposEntryPath(t.Path, t.Name) {
		return fmt.Errorf("the worktree %s was not under the repositories folder", t.Name)
	}
	// The path checked just now is the one every step uses: a tombstone a delete through a link
	// wrote holds the physical path, and resolving ~/repos again later could name another disk.
	dir := filepath.Clean(t.Path)
	if worktreeRestoreChecked != nil {
		worktreeRestoreChecked()
	}
	var err error
	// Order: deletion gate, cleanup lock, session meta lock. The gate is held through the shelf
	// work so a new delete cannot shelve again between the folder coming back and its sessions.
	sessionx.WithDeletionGate(func() {
		var record worktreeRestoreRecord
		sessionx.WithCleanupLock(func() {
			if err = checkRestoreArchive(trash, id, archive); err == nil {
				record, err = loadWorktreeRestoreRecord(trash, id, t)
			}
		})
		if err != nil {
			return
		}
		if err = recreateFromTombstone(dir, t); err != nil || record.Complete {
			return
		}
		if worktreeRestoreRecreated != nil {
			worktreeRestoreRecreated()
		}
		sessionx.WithCleanupLock(func() {
			// A purge that won since the first check has taken the record with the archive. The
			// folder is back by now, so its sessions still come back as the record read above
			// says; only the progress has nowhere left to go.
			purged := checkRestoreArchive(trash, id, archive) != nil
			if err = restoreShelvedRevisions(dir, t.Shelved, record.Shelved); err != nil {
				return
			}
			if len(record.Unread) > 0 {
				err = fmt.Errorf("%w: could not read session %s", errRestoreStopped, strings.Join(record.Unread, ", "))
				return
			}
			if purged {
				return
			}
			if worktreeBeforeRestoreComplete != nil {
				worktreeBeforeRestoreComplete()
			}
			record.Complete = true
			err = writeRestoreRecord(trash, worktreeRestoreRecordName(id), record)
		})
	})
	return err
}

// loadWorktreeRestoreRecord reads the progress an earlier attempt left, or records the shelf
// revisions now when this is the first attempt.
func loadWorktreeRestoreRecord(trash *os.Root, id string, t gitx.WorktreeTombstone) (worktreeRestoreRecord, error) {
	var record worktreeRestoreRecord
	name := worktreeRestoreRecordName(id)
	data, err := readArchiveFile(trash, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// An unreadable meta does not hold the folder back: it stays unread in the record and
		// the restore reports it after putting back everything else.
		shelved, unread, err := shelfRevisionsOf(id, t.Shelved)
		if err != nil {
			return record, err
		}
		record = worktreeRestoreRecord{ID: id, Ref: t.Ref, Shelved: shelved, Unread: unread}
		return record, writeRestoreRecord(trash, name, record)
	case err != nil:
		return record, fmt.Errorf("%w: could not read worktree restore progress: %w", errRestoreStopped, err)
	}
	if err := json.Unmarshal(data, &record); err != nil || record.ID != id || record.Shelved == nil {
		return record, fmt.Errorf("%w: invalid worktree restore progress for %s", errRestoreStopped, id)
	}
	if record.Ref != t.Ref {
		// Left by a purged entry that had this id: not this entry's progress.
		if err := trash.Remove(name); err != nil {
			return record, fmt.Errorf("%w: could not drop stale worktree restore progress: %w", errRestoreStopped, err)
		}
		return loadWorktreeRestoreRecord(trash, id, t)
	}
	if len(record.Unread) > 0 {
		for _, n := range record.Unread {
			if !slices.Contains(t.Shelved, n) {
				return record, fmt.Errorf("%w: invalid worktree restore progress for %s", errRestoreStopped, id)
			}
		}
		shelved, unread, err := shelfRevisionsOf(id, record.Unread)
		if err != nil {
			return record, err
		}
		if len(unread) < len(record.Unread) {
			maps.Copy(record.Shelved, shelved)
			record.Unread = unread
			if err := writeRestoreRecord(trash, name, record); err != nil {
				return record, err
			}
		}
	}
	return record, nil
}

// shelfRevisionsOf is the shelf revision of each named session still on the shelf, and the names
// whose meta could not be read. A session that is gone or already off the shelf is left out of
// both, and so never restored by a retry.
func shelfRevisionsOf(id string, names []string) (shelved map[string]string, unread []string, err error) {
	shelved = map[string]string{}
	for _, name := range names {
		if !session.ValidName(name) {
			return nil, nil, fmt.Errorf("archive %s has an invalid shelved session name", id)
		}
		m, state := session.ReadMetaState(name)
		switch {
		case state == session.MetaUnreadable:
			unread = append(unread, name)
		case state == session.MetaOK && m.Archived:
			shelved[name] = m.ShelfRevision
		}
	}
	return shelved, unread, nil
}

// restoreShelvedRevisions takes off the shelf each named session that revisions tracks, still
// carries the tracked revision and still lives in dir. A session shelved again since has a new
// revision and stays; one already back or now in another folder is left alone.
func restoreShelvedRevisions(dir string, names []string, revisions map[string]string) error {
	var restoreErr error
	for _, name := range names {
		revision, tracked := revisions[name]
		if !tracked {
			continue
		}
		if err := sessionx.RestoreShelvedSession(name, func(m session.Meta) bool {
			return m.ShelfRevision == revision && gitx.SessionLivesIn(m, dir)
		}); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("could not restore session %s: %w", name, err))
		}
	}
	if restoreErr != nil {
		return fmt.Errorf("%w: %w", errRestoreStopped, restoreErr)
	}
	return nil
}

func recreateFromTombstone(dir string, t gitx.WorktreeTombstone) error {
	if gitx.DeleteTouches(dir, t.Parent) {
		return &restoreConflict{gitx.ErrCodeDeleteInProgress(), "a delete of this working copy or of its base clone is still in progress; try again in a moment"}
	}
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
	if err := gitx.RestoreWorktreeCreator(dir, t.Creator); err != nil {
		return &restoreConflict{errCodeRecreateFailed, "the working copy is back but its creator record could not be written: " + err.Error()}
	}
	return nil
}
