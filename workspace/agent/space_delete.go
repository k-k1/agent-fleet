package main

// Deleting a space (ADR 0142 decision 2): DELETE /spaces/{folder} moves the folder to the trash
// and takes its sessions with it the way deleting a working copy does (ADR 0101 decision 4), and
// restoring the trash entry puts both back. Deleting a session never reaches here: a session's
// delete keeps its space.
//
// The trash entry is the usual archive (a manifest, no payload) plus the folder itself, moved
// beside it as <id>.space. A rename keeps every file, its mode and its time, whatever the
// folder's size; a tarball would hold the whole folder in memory (writeCleanupArchive) and would
// have to cap what a restore reads (maxCleanupEntryBytes), neither of which fits a member's
// documents. The price is that the trash and ~/spaces must share a file system, which is checked
// before anything is changed.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// Error codes of a space's delete and restore. Worded in the Console's own table, not err.*: the
// shared codes it would otherwise reuse say "working copy".
const (
	errCodeSpaceNotFound            = "space_not_found"
	errCodeSpaceSessionsRunning     = "space_sessions_running"
	errCodeSpaceLockedSessions      = "space_locked_sessions"
	errCodeSpaceSessionsTrashFailed = "space_sessions_trash_failed"
	// The delete could not tell which sessions live in the space (a meta it cannot read, tmux not
	// answering), and removed nothing.
	errCodeSpaceSessionsUnobservable = "space_sessions_unobservable"
	// The space holds sessions the delete request's expect list did not name; the answer lists them
	// under "sessions".
	errCodeSpaceSessionsChanged      = "space_sessions_changed"
	errCodeSpaceSessionsSettleFailed = "space_sessions_settle_failed"
	errCodeSpaceTrashFailed          = "space_trash_failed"
	errCodeSpaceTrashFS              = "space_trash_other_filesystem"
	errCodeSpaceRestoreExists        = "space_restore_exists"
	errCodeSpaceRestoreMissing       = "space_restore_missing"
)

// spaceTombstone is what a deleted space's trash entry records. The folder itself sits in the
// trash directory as <id>.space.
type spaceTombstone struct {
	// Folder is the space's identity; a restore puts it back under this name, so the sessions'
	// ids (derived from their directory) and Claude's transcript paths stay valid.
	Folder string `json:"folder"`
	// Name is the display name at the time of the delete, for the trash row.
	Name string `json:"name,omitempty"`
	// Path is the folder's absolute path when it was deleted.
	Path string `json:"path"`
	// Shelved lists the AI sessions the delete moved to the shelf; a restore takes them off it.
	// Shell and ssm sessions go to the trash as entries of their own, and are restored from there.
	Shelved []string `json:"shelved,omitempty"`
}

// spaceTrashName is the folder for this archive, relative to the opened trash directory.
func spaceTrashName(id string) string { return id + ".space" }

// newSpaceArchiveID is a trash entry id for the space in folder: the usual time-sortable prefix,
// then the folder's start and a random token, so two entries never share an id (a purge of one
// and a later delete of another in the same second would otherwise be told apart by nothing).
var newSpaceArchiveID = func(now time.Time, folder string) string {
	slug := idSlug("space-" + folder)
	if len(slug) > 17 {
		slug = slug[:17]
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err) // crypto/rand does not fail on Linux; an id must never repeat silently
	}
	return newCleanupID(now, slug+"-"+hex.EncodeToString(raw[:]))
}

// entryIDFree reports whether no file of a trash entry with this id exists.
func entryIDFree(trash *os.Root, id string) bool {
	if !validArchiveID(id) {
		return false
	}
	for _, name := range []string{id + ".json", id + ".tar.gz", spaceTrashName(id), restoringMarkerName(id), spaceRestoreRecordName(id), worktreeRestoreRecordName(id)} {
		if _, err := trash.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// removeOwnEntry discards a failed delete's own archive files. It leaves any folder in the trash.
func removeOwnEntry(trash *os.Root, id string) {
	_ = trash.Remove(id + ".json")
	_ = trash.Remove(id + ".tar.gz")
}

// deleteSpaceResponse is DELETE /spaces/{folder}'s answer: the folder, and the id of the trash entry
// that holds it.
type deleteSpaceResponse struct {
	Deleted string `json:"deleted"`
	Archive string `json:"archive"`
}

// spaceRestoreStart is a test seam where the space part of a restore begins, after the generic
// restore's own check that the entry exists. Nil in production.
var spaceRestoreStart func()

// spaceBeforeMoveIn is a test seam inside a restore's lock, just before the folder moves back.
// Nil in production.
var spaceBeforeMoveIn func()

// spaceAfterManifest is a test seam between publishing the trash entry and moving the folder,
// where a purge of that entry would otherwise win. Nil in production.
var spaceAfterManifest func(id string)

// handleDeleteSpace is DELETE /spaces/{folder}. The order is the safety, as for a working copy:
// refuse while anything runs or is locked, trash the shell / ssm sessions (a failure stops the
// delete with nothing removed), record the entry, move the folder, and only then shelve the AI
// sessions, whose conversations stay readable.
func handleDeleteSpace(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	sp, ok := spacex.Lookup(folder)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, errCodeSpaceNotFound, "no such space")
		return
	}
	dir := sp.Dir
	// Before anything is changed: a trash on another file system could only copy the folder, and
	// a rename that fails after the shells were trashed would leave them trashed with the space
	// still there.
	store := cleanupStoreDir()
	if err := os.MkdirAll(store, 0o700); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceTrashFailed,
			fmt.Sprintf("could not open the trash, so the space was not deleted: %v", err))
		return
	}
	trash, err := os.OpenRoot(store)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceTrashFailed,
			fmt.Sprintf("could not open the trash, so the space was not deleted: %v", err))
		return
	}
	defer trash.Close()
	if same, err := spacex.SameDevice(dir, trash); err != nil || !same {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceTrashFS,
			"the trash is on another file system than ~/spaces, so the space cannot be moved to it; nothing was deleted")
		return
	}

	// Only the decision runs inside the deletion gate, as for a working copy: a lock request
	// either lands before the guards read it or meets the mark and is refused.
	var end func()
	sessionx.WithDeletionGate(func() { end = decideDeleteSpace(w, folder, dir) })
	if end == nil {
		return
	}
	defer end()

	// The sessions this delete settles, read once with the mark held (gitx.SessionLivesIn): the
	// shells it trashes, the AI sessions the entry records and the ones shelved after the move.
	settle, err := gitx.SessionsToSettleUnder(dir)
	if err != nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, errCodeSpaceSessionsUnobservable,
			"could not tell which sessions live in this space, so it was left as it is: "+err.Error())
		return
	}
	// A client that showed the member the sessions it would settle (expect) is held to that list:
	// one the Agent finds in the space and the client did not name (a path through a link the
	// Console cannot resolve) is not settled unseen. The answer names them, so the dialog can show
	// them and ask again.
	if r.URL.Query().Has("expect") {
		if extra := unexpectedSessions(settle, r.URL.Query().Get("expect")); len(extra) > 0 {
			var body spaceSessionsChanged
			body.Error.Code = errCodeSpaceSessionsChanged
			body.Error.Message = fmt.Sprintf("the space holds session(s) the request did not list (%s); review them and try again", strings.Join(extra, ", "))
			body.Error.Sessions = extra
			httpx.WriteJSON(w, http.StatusConflict, body)
			return
		}
	}
	var shelved []string
	for _, m := range settle {
		if m.Kind != session.KindShell && m.Kind != session.KindSSM {
			shelved = append(shelved, m.Name)
		}
	}

	now := nowUTC()
	man := cleanupManifest{
		At:     now.Format(time.RFC3339),
		Reason: "delete_space",
		Space:  &spaceTombstone{Folder: folder, Name: sp.Name, Path: dir, Shelved: shelved},
	}
	// The entry is published and the folder moved under the cleanup lock a purge takes, so a purge
	// pressed on the new row in another tab cannot remove the manifest before the folder is in the
	// trash (which would leave the files with nothing to name them).
	failed, moveErr := gitx.RemoveWithShellTrash(dir, settle, func() error {
		var err error
		sessionx.WithCleanupLock(func() { err = moveSpaceToTrash(trash, folder, now, &man) })
		return err
	})
	if moveErr != nil {
		invalidateCleanupUsage()
		if failed != "" {
			httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceSessionsTrashFailed,
				fmt.Sprintf("could not move session %s to the trash, so the space was left as it is: %v", failed, moveErr))
			return
		}
		if errors.Is(moveErr, spacex.ErrNotFound) {
			httpx.WriteErr(w, http.StatusNotFound, errCodeSpaceNotFound, moveErr.Error())
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceTrashFailed,
			fmt.Sprintf("could not move the space to the trash: %v", moveErr))
		return
	}
	invalidateCleanupUsage()
	if err := gitx.ShelveSessionsUnder(dir, settle, r.URL.Query().Has("expect")); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceSessionsSettleFailed, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, deleteSpaceResponse{Deleted: folder, Archive: man.ID})
}

func moveSpaceToTrash(trash *os.Root, folder string, now time.Time, man *cleanupManifest) error {
	// An id none of whose files exists yet, checked under the lock that space deletes and
	// purges take (the other archive writers claim their id with O_EXCL on the tarball
	// instead, and their ids have another shape, so they cannot draw this one): whatever is
	// found under it afterwards is this attempt's own, and a clean-up of a failed attempt
	// removes nothing that was there before.
	for attempt := 0; man.ID == "" && attempt < 4; attempt++ {
		if id := newSpaceArchiveID(now, folder); entryIDFree(trash, id) {
			man.ID = id
		}
	}
	if man.ID == "" {
		return errors.New("could not find a free trash id")
	}
	if err := writeCleanupBundleIn(trash, man, nil, false); err != nil {
		return fmt.Errorf("could not record the space in the trash: %w", err)
	}
	if spaceAfterManifest != nil {
		spaceAfterManifest(man.ID)
	}
	// The list reads the sidecar alone; one that could not be written (missing, or cut short)
	// would hide a row whose folder is in the trash. Refused here, with nothing moved, rather
	// than found out later.
	if _, err := readArchiveSidecar(trash, man.ID); err != nil {
		removeOwnEntry(trash, man.ID)
		return errors.New("could not list the space in the trash")
	}
	if err := spacex.MoveOut(folder, trash, spaceTrashName(man.ID)); err != nil {
		// Only the files this attempt wrote go, never a .space directory: none of ours exists.
		removeOwnEntry(trash, man.ID)
		return err
	}
	return nil
}

// spaceSessionsChanged is the 409 space_sessions_changed body: the usual error, and the names of
// the sessions the request's expect list left out.
type spaceSessionsChanged struct {
	Error struct {
		Code     string   `json:"code"`
		Message  string   `json:"message"`
		Sessions []string `json:"sessions"`
	} `json:"error"`
}

// unexpectedSessions is the names in settle that expect (comma-separated session names) leaves out,
// sorted.
func unexpectedSessions(settle []session.Meta, expect string) []string {
	named := map[string]bool{}
	for _, n := range strings.Split(expect, ",") {
		named[strings.TrimSpace(n)] = true
	}
	var out []string
	for _, m := range settle {
		if !named[m.Name] {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

// decideDeleteSpace is the part of a delete that runs inside the gate. It returns the function
// that clears the "being deleted" mark, or nil after answering the refusal itself.
func decideDeleteSpace(w http.ResponseWriter, folder, dir string) (end func()) {
	// The running sessions are read outside the launch gate, the mark is taken inside it, and a
	// session started in between is seen either way (gitx.MarkAgainstLaunches). One read decides
	// the guards and the names the mark records.
	var release func()
	for reread := 0; release == nil; reread++ {
		epoch := gitx.LaunchEpoch()
		sessions, err := gitx.ReadDirSessions(dir)
		if err != nil {
			httpx.WriteErr(w, http.StatusServiceUnavailable, errCodeSpaceSessionsUnobservable,
				"could not tell which sessions live in this space, so it was left as it is: "+err.Error())
			return nil
		}
		if running := sessions.Running(); len(running) > 0 {
			httpx.WriteErr(w, http.StatusConflict, errCodeSpaceSessionsRunning,
				fmt.Sprintf("%d session(s) are running in this space (%s); stop them first", len(running), strings.Join(running, ", ")))
			return nil
		}
		if locked := sessions.Locked(); len(locked) > 0 {
			httpx.WriteErr(w, http.StatusForbidden, errCodeSpaceLockedSessions,
				fmt.Sprintf("%d locked session(s) live in this space (%s); unlock them first", len(locked), strings.Join(locked, ", ")))
			return nil
		}
		end, out, why := gitx.MarkAgainstLaunches(dir, "", epoch, sessions.Names())
		switch {
		case out == gitx.MarkReread && reread < gitx.MaxLaunchRereads:
			continue
		case out == gitx.MarkLaunching:
			httpx.WriteErr(w, http.StatusConflict, errCodeSpaceSessionsRunning,
				"a session is starting in this space; stop it first")
			return nil
		case out == gitx.MarkReread:
			httpx.WriteErr(w, http.StatusConflict, errCodeSpaceSessionsRunning,
				"sessions kept starting while the delete checked this space; try again in a moment")
			return nil
		case out == gitx.MarkBusy:
			httpx.WriteErr(w, http.StatusConflict, gitx.ErrCodeDeleteInProgress(), why)
			return nil
		}
		release = end
	}
	// Existence is read again with the mark held: a delete of this same space that finished
	// before the mark was taken has taken the folder with it, and this one answers 404.
	if _, ok := spacex.Lookup(folder); !ok {
		release()
		httpx.WriteErr(w, http.StatusNotFound, errCodeSpaceNotFound, "no such space")
		return nil
	}
	return release
}

// A record written before moving the directory proves which directory can resume an
// interrupted restore. Shelf revisions keep the retries from undoing a later shelf action.
type spaceRestoreRecord struct {
	ID       string               `json:"id"`
	Folder   string               `json:"folder"`
	Identity spacex.TrashIdentity `json:"identity"`
	Shelved  map[string]string    `json:"shelved"`
	Complete bool                 `json:"complete,omitempty"`
}

func spaceRestoreRecordName(id string) string { return id + ".space-restore" }

// Tests pause or fail completion after the folder and sessions are back.
var spaceBeforeRestoreComplete func()

func restoreDeletedSpace(trash *os.Root, id string, archive os.FileInfo, t spaceTombstone) error {
	if !spacex.ValidFolder(t.Folder) {
		return &restoreConflict{errCodeSpaceRestoreMissing, "the trash entry names no space folder"}
	}
	if spaceRestoreStart != nil {
		spaceRestoreStart()
	}
	dir := filepath.Join(spacex.Root(), t.Folder)
	var restoreErr error
	// The delete holds its mark through shelving. Hold the gate through our own shelf work
	// so a new delete cannot take a mark after the move but before the last session is back.
	// Order: deletion gate, cleanup lock, space lifecycle or session meta lock.
	sessionx.WithDeletionGate(func() {
		if gitx.UnderDeletion(dir) {
			restoreErr = &restoreConflict{gitx.ErrCodeDeleteInProgress(), "a delete of this space is still in progress; try again in a moment"}
			return
		}
		sessionx.WithCleanupLock(func() {
			if err := checkRestoreArchive(trash, id, archive); err != nil {
				restoreErr = err
				return
			}
			restoreErr = resumeSpaceRestore(trash, id, dir, t)
		})
	})
	return restoreErr
}

func resumeSpaceRestore(trash *os.Root, id, dir string, t spaceTombstone) error {
	var record spaceRestoreRecord
	name := spaceRestoreRecordName(id)
	data, err := readArchiveFile(trash, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		identity, err := spacex.ReadTrashIdentity(trash, spaceTrashName(id))
		if err != nil {
			return spaceRestoreMoveError(err, t.Folder)
		}
		shelved, unread, err := shelfRevisionsOf(id, t.Shelved)
		if err != nil {
			return err
		}
		if len(unread) > 0 {
			return fmt.Errorf("%w: could not read session %s", errRestoreStopped, unread[0])
		}
		record = spaceRestoreRecord{ID: id, Folder: t.Folder, Identity: identity, Shelved: shelved}
		if err := writeRestoreRecord(trash, name, record); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("%w: could not read space restore progress: %w", errRestoreStopped, err)
	default:
		if err := json.Unmarshal(data, &record); err != nil || record.ID != id ||
			record.Folder != t.Folder || record.Identity.Inode == 0 || record.Shelved == nil {
			return fmt.Errorf("%w: invalid space restore progress for %s", errRestoreStopped, id)
		}
		if record.Complete {
			return nil
		}
	}

	root := spacex.Root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	spaces, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer spaces.Close()
	entry := spaceTrashName(id)
	if _, err := trash.Lstat(entry); errors.Is(err, fs.ErrNotExist) {
		id, err := spacex.ReadTrashIdentity(spaces, t.Folder)
		if err != nil {
			return spaceRestoreMoveError(err, t.Folder)
		}
		if id != record.Identity {
			return &restoreConflict{errCodeSpaceRestoreExists, "could not confirm that ~/spaces/" + t.Folder + " is the folder moved by this restore"}
		}
	} else {
		if err != nil {
			return err
		}
		if spaceBeforeMoveIn != nil {
			spaceBeforeMoveIn()
		}
		if err := spacex.MoveIn(trash, entry, spaces, t.Folder, record.Identity); err != nil {
			return spaceRestoreMoveError(err, t.Folder)
		}
	}
	if err := restoreShelvedRevisions(dir, t.Shelved, record.Shelved); err != nil {
		return err
	}
	if spaceBeforeRestoreComplete != nil {
		spaceBeforeRestoreComplete()
	}
	record.Complete = true
	return writeRestoreRecord(trash, name, record)
}

// writeRestoreRecord stores a space or worktree restore's progress record next to its archive.
func writeRestoreRecord(trash *os.Root, name string, record any) error {
	data, err := json.Marshal(record)
	if err == nil {
		err = writeArchiveFile(trash, name, data)
	}
	if err != nil {
		return fmt.Errorf("%w: could not record restore progress: %w", errRestoreStopped, err)
	}
	return nil
}

func spaceRestoreMoveError(err error, folder string) error {
	switch {
	case errors.Is(err, spacex.ErrExists):
		return &restoreConflict{errCodeSpaceRestoreExists, "something already exists at ~/spaces/" + folder}
	case errors.Is(err, spacex.ErrNotFound):
		return &restoreConflict{errCodeSpaceRestoreMissing, "the space's original folder is missing from its restore location"}
	default:
		return err
	}
}
