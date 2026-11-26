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
	errCodeSpaceTrashFailed         = "space_trash_failed"
	errCodeSpaceTrashFS             = "space_trash_other_filesystem"
	errCodeSpaceRestoreExists       = "space_restore_exists"
	errCodeSpaceRestoreMissing      = "space_restore_missing"
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

// spaceTrashDir is where the folder of archive id waits.
func spaceTrashDir(id string) string { return filepath.Join(cleanupStoreDir(), id+".space") }

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
func entryIDFree(id string) bool {
	for _, p := range []string{id + ".json", id + ".tar.gz", id + ".space", id + ".restoring"} {
		if _, err := os.Lstat(filepath.Join(cleanupStoreDir(), p)); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// sidecarReadable reports whether the listing's file for id exists and parses to id.
func sidecarReadable(id string) bool {
	b, err := os.ReadFile(filepath.Join(cleanupStoreDir(), id+".json"))
	if err != nil {
		return false
	}
	var m cleanupManifest
	return json.Unmarshal(b, &m) == nil && m.ID == id
}

// removeOwnEntry removes the manifest files a delete that stopped wrote itself. Unlike a purge it
// never touches a folder waiting in the trash.
func removeOwnEntry(id string) {
	_ = os.Remove(filepath.Join(cleanupStoreDir(), id+".json"))
	_ = os.Remove(filepath.Join(cleanupStoreDir(), id+".tar.gz"))
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
	if same, err := spacex.SameDevice(dir, store); err != nil || !same {
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

	// Shell / ssm sessions first: the trash can fail (a full disk), and removing the folder first
	// would leave them listed with no folder.
	var shelved []string
	for _, m := range gitx.SessionsToSettleUnder(dir) {
		if m.Kind == session.KindShell || m.Kind == session.KindSSM {
			if err := trashStoppedSession(m); err != nil {
				httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceSessionsTrashFailed,
					fmt.Sprintf("could not move session %s to the trash, so the space was left as it is: %v", m.Name, err))
				return
			}
			continue
		}
		shelved = append(shelved, m.Name)
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
	var moveErr error
	sessionx.WithCleanupLock(func() {
		// An id none of whose files exists yet, checked under the lock that space deletes and
		// purges take (the other archive writers claim their id with O_EXCL on the tarball
		// instead, and their ids have another shape, so they cannot draw this one): whatever is
		// found under it afterwards is this attempt's own, and a clean-up of a failed attempt
		// removes nothing that was there before.
		for attempt := 0; man.ID == "" && attempt < 4; attempt++ {
			if id := newSpaceArchiveID(now, folder); entryIDFree(id) {
				man.ID = id
			}
		}
		if man.ID == "" {
			moveErr = errors.New("could not find a free trash id, so the space was left as it is")
			return
		}
		if err := writeCleanupArchive(&man, nil); err != nil {
			moveErr = fmt.Errorf("could not record the space in the trash, so it was left as it is: %w", err)
			return
		}
		if spaceAfterManifest != nil {
			spaceAfterManifest(man.ID)
		}
		// The list reads the sidecar alone; one that could not be written (missing, or cut short)
		// would hide a row whose folder is in the trash. Refused here, with nothing moved, rather
		// than found out later.
		if !sidecarReadable(man.ID) {
			removeOwnEntry(man.ID)
			moveErr = errors.New("could not list the space in the trash, so it was left as it is")
			return
		}
		if err := spacex.MoveOut(folder, spaceTrashDir(man.ID)); err != nil {
			// Only the files this attempt wrote go, never a .space directory: none of ours exists.
			removeOwnEntry(man.ID)
			moveErr = err
		}
	})
	if moveErr != nil {
		invalidateCleanupUsage()
		if errors.Is(moveErr, spacex.ErrNotFound) {
			httpx.WriteErr(w, http.StatusNotFound, errCodeSpaceNotFound, "no such space")
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, errCodeSpaceTrashFailed,
			fmt.Sprintf("could not move the space to the trash, so it was left as it is: %v", moveErr))
		return
	}
	gitx.ShelveSessionsUnder(dir)
	invalidateCleanupUsage()
	httpx.WriteJSON(w, http.StatusOK, deleteSpaceResponse{Deleted: folder, Archive: man.ID})
}

// decideDeleteSpace is the part of a delete that runs inside the gate. It returns the function
// that clears the "being deleted" mark, or nil after answering the refusal itself.
func decideDeleteSpace(w http.ResponseWriter, folder, dir string) (end func()) {
	if running := sessionx.LiveSessionsInDir(dir); len(running) > 0 {
		httpx.WriteErr(w, http.StatusConflict, errCodeSpaceSessionsRunning,
			fmt.Sprintf("%d session(s) are running in this space (%s); stop them first", len(running), strings.Join(running, ", ")))
		return nil
	}
	// A session being started here is one the guard above cannot see yet (gitx.ReserveLaunch).
	if gitx.LaunchingUnder(dir) {
		httpx.WriteErr(w, http.StatusConflict, errCodeSpaceSessionsRunning,
			"a session is starting in this space; stop it first")
		return nil
	}
	if locked := sessionx.LockedSessionsInDir(session.ListMetas(), dir); len(locked) > 0 {
		httpx.WriteErr(w, http.StatusForbidden, errCodeSpaceLockedSessions,
			fmt.Sprintf("%d locked session(s) live in this space (%s); unlock them first", len(locked), strings.Join(locked, ", ")))
		return nil
	}
	release, ok, why := gitx.BeginDelete(dir, "", gitx.SessionNamesUnder(dir)...)
	if !ok {
		httpx.WriteErr(w, http.StatusConflict, gitx.ErrCodeDeleteInProgress(), why)
		return nil
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

// restoreDeletedSpace puts a deleted space back from the trash entry id, then takes the sessions
// its delete shelved off the shelf. A space that is already back (the entry was restored before,
// or the folder was recreated) is left as it is and only the sessions are restored, which makes
// restoring twice safe.
func restoreDeletedSpace(id string, t spaceTombstone) error {
	if !spacex.ValidFolder(t.Folder) {
		return &restoreConflict{errCodeSpaceRestoreMissing, "the trash entry names no space folder"}
	}
	src := spaceTrashDir(id)
	if spaceRestoreStart != nil {
		spaceRestoreStart()
	}
	var err error
	sessionx.WithDeletionGate(func() {
		if gitx.UnderDeletion(filepath.Join(spacex.Root(), t.Folder)) {
			err = &restoreConflict{gitx.ErrCodeDeleteInProgress(), "a delete of this space is still in progress; try again in a moment"}
			return
		}
		_, statErr := os.Lstat(src)
		_, back := spacex.Lookup(t.Folder)
		switch {
		case statErr != nil && back:
			// Moved back by an earlier restore.
		case statErr != nil:
			err = &restoreConflict{errCodeSpaceRestoreMissing, "the space's folder is no longer in the trash"}
		default:
			// Under the cleanup lock a purge takes, and only while the entry still exists: a
			// purge that won removes the folder, and one that is removing it must not run
			// alongside the move back.
			var merr error
			sessionx.WithCleanupLock(func() {
				if _, err := os.Stat(filepath.Join(cleanupStoreDir(), id+".tar.gz")); err != nil {
					merr = spacex.ErrNotFound
					return
				}
				if spaceBeforeMoveIn != nil {
					spaceBeforeMoveIn()
				}
				merr = spacex.MoveIn(src, t.Folder)
			})
			switch {
			case errors.Is(merr, spacex.ErrExists):
				err = &restoreConflict{errCodeSpaceRestoreExists, "something already exists at ~/spaces/" + t.Folder}
			case errors.Is(merr, spacex.ErrNotFound):
				err = &restoreConflict{errCodeSpaceRestoreMissing, "the space's folder in the trash is not a space any more"}
			case merr != nil:
				err = merr
			}
		}
	})
	if err != nil {
		return err
	}
	for _, name := range t.Shelved {
		if m, ok := session.ReadMeta(name); ok && m.Archived && underDir(filepath.Clean(m.Dir), filepath.Clean(t.Path)) {
			sessionx.RestoreSession(name)
		}
	}
	return nil
}
