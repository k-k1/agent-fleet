package spacex

// The record of spaces (issue #5231). MarkerFile sits inside the folder agents work in, so an
// agent tidying dotfiles, or the member, can delete it, and with it the space would silently
// leave the Spaces list, its sessions would move to Other, and the space delete could no longer
// find it. The Agent therefore keeps its own record of the spaces it made, outside every space,
// and a recorded folder whose marker is missing gets the marker written back instead of being
// dropped. The marker stays what decides; the record only restores it.
//
// Only a marker that is absent is restored. A directory, FIFO or symlink in its place is left
// alone and the folder stays no space, as before: something put it there, and replacing it would
// be the Agent deleting a file it did not write.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// recordMu serialises the record's read-modify-write. Lock order: lifeMu, then recordMu.
var recordMu sync.Mutex

// recordEntry is what the record keeps per space: enough to write the marker back.
type recordEntry struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// recordFile is the record's content.
type recordFile struct {
	Spaces map[string]recordEntry `json:"spaces"`
}

// recordPath is where the record lives: the state dir is in the home volume with ~/spaces, so
// the record and the folders it describes are lost or kept together.
func recordPath() string { return filepath.Join(paths.AgentStateDir(), "spaces.json") }

// recordMaxBytes bounds what readRecord reads. One entry is well under 1 KiB; this leaves room for
// thousands.
const recordMaxBytes = 4 << 20

// readRecord answers the record, empty when there is none. A record that cannot be read or does
// not parse is treated as empty too: it only ever restores markers, so losing it costs a repair,
// never a space.
func readRecord() recordFile {
	rec := recordFile{Spaces: map[string]recordEntry{}}
	f, err := os.Open(recordPath())
	if err != nil {
		return rec
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > recordMaxBytes {
		return rec
	}
	var got recordFile
	if json.NewDecoder(f).Decode(&got) != nil || got.Spaces == nil {
		return rec
	}
	return got
}

// writeRecord replaces the record through a temporary file and a rename.
func writeRecord(rec recordFile) error {
	dir := filepath.Dir(recordPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".spaces-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), recordPath()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// updateRecord applies change to the record and writes it back when change reports a change.
func updateRecord(change func(map[string]recordEntry) bool) error {
	recordMu.Lock()
	defer recordMu.Unlock()
	rec := readRecord()
	if !change(rec.Spaces) {
		return nil
	}
	return writeRecord(rec)
}

// remember records the space in folder. A failure is not the caller's: the marker is written
// already, and the next List records the space again.
func remember(folder string, e recordEntry) {
	_ = updateRecord(func(m map[string]recordEntry) bool {
		if m[folder] == e {
			return false
		}
		m[folder] = e
		return true
	})
}

// forget drops folder from the record, once the folder has left ~/spaces.
func forget(folder string) {
	_ = updateRecord(func(m map[string]recordEntry) bool {
		if _, ok := m[folder]; !ok {
			return false
		}
		delete(m, folder)
		return true
	})
}

// recorded answers folder's record entry.
func recorded(folder string) (recordEntry, bool) {
	recordMu.Lock()
	defer recordMu.Unlock()
	e, ok := readRecord().Spaces[folder]
	return e, ok
}

// rememberListed records every listed space the record does not hold yet, so a space made before
// the record existed (or whose record write failed) is protected from the first list on.
func rememberListed(spaces []Space) {
	_ = updateRecord(func(m map[string]recordEntry) bool {
		changed := false
		for _, sp := range spaces {
			if _, ok := m[sp.Folder]; ok {
				continue
			}
			m[sp.Folder] = recordEntry{Name: sp.Name, CreatedAt: sp.CreatedAt}
			changed = true
		}
		return changed
	})
}

// afterRepair is a test seam that runs once a repair has opened the space, just before it writes.
// Nil in production.
var afterRepair func()

// repairMarker writes the marker of the recorded space in folder back when it is missing, and
// reports whether the folder carries a marker afterwards. The folder has to be a real directory
// under Root (a symlink in its place is no space, as everywhere else), and only an absent marker
// is written: it is published with a hard link, which fails when anything appeared there
// meanwhile, so a marker written by someone else is never replaced.
func repairMarker(folder string) bool {
	if !ValidFolder(folder) {
		return false
	}
	e, ok := recorded(folder)
	if !ok {
		return false
	}
	// Shared with every other write inside a space: a delete moving the folder away waits for it,
	// and a folder already moved away is found missing below.
	lifeMu.RLock()
	defer lifeMu.RUnlock()
	// Go opens a Root without O_NONBLOCK, so a FIFO in the place of ~/spaces would block here.
	if fi, err := os.Stat(Root()); err != nil || !fi.IsDir() {
		return false
	}
	top, err := os.OpenRoot(Root())
	if err != nil {
		return false
	}
	defer top.Close()
	dirFi, err := top.Lstat(folder)
	if err != nil || !dirFi.IsDir() {
		return false
	}
	sp, err := top.OpenRoot(folder)
	if err != nil {
		return false
	}
	defer sp.Close()
	if openFi, err := sp.Stat("."); err != nil || !os.SameFile(dirFi, openFi) {
		return false
	}
	if afterRepair != nil {
		afterRepair()
	}
	if _, err := sp.Lstat(MarkerFile); !errors.Is(err, fs.ErrNotExist) {
		// Present (written meanwhile) counts; anything that is no regular file does not.
		return err == nil && markerPresent(filepath.Join(Root(), folder))
	}
	createdAt := e.CreatedAt
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(newMarker(e.Name, createdAt))
	if err != nil {
		return false
	}
	tmp, err := writeTempIn(sp, append(b, '\n'))
	if err != nil {
		return false
	}
	defer func() { _ = sp.Remove(tmp) }()
	if err := sp.Link(tmp, MarkerFile); err != nil && !errors.Is(err, fs.ErrExist) {
		return false
	}
	return markerPresent(filepath.Join(Root(), folder))
}

// Protected reports whether the file browser must refuse to delete or rename the entry at the
// absolute path p: ~/spaces itself, a space's folder, or a space's marker. Each of those is the
// space delete's job (the trash, the running-session refusal, the shelving), and a folder moved
// under another name breaks --resume for every session in it (see the package comment). Files
// inside a space stay the member's to change.
//
// The last element is judged as it is spelt, never followed: deleting or renaming a symlink
// touches the link, not its target. The folders above it are resolved, so an alias of ~/spaces
// (alias -> ~/spaces) cannot reach a space by another spelling. A folder counts when it carries a
// marker or is recorded, so a space whose marker was just removed is still protected.
func Protected(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	p = filepath.Clean(p)
	if isRoot(p) {
		return true
	}
	parent, base := filepath.Split(p)
	parent = filepath.Clean(parent)
	if real, err := filepath.EvalSymlinks(parent); err == nil {
		parent = real
	}
	if isRoot(filepath.Join(parent, base)) {
		return true
	}
	if isRoot(parent) {
		return isSpaceFolder(base)
	}
	if base != MarkerFile {
		return false
	}
	return isRoot(filepath.Dir(parent)) && isSpaceFolder(filepath.Base(parent))
}

// isRoot reports whether p (clean) is ~/spaces by either spelling.
func isRoot(p string) bool { return p == Root() || p == realRoot() }

// isSpaceFolder reports whether folder under Root is a space or is recorded as one.
func isSpaceFolder(folder string) bool {
	if !ValidFolder(folder) {
		return false
	}
	if markerPresent(filepath.Join(Root(), folder)) {
		return true
	}
	_, ok := recorded(folder)
	return ok
}
