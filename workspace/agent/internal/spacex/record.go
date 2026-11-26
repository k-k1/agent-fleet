package spacex

// The record of spaces (issue #5231). MarkerFile sits inside the folder agents work in, so an
// agent tidying dotfiles, or the member, can delete it, and with it the space would silently
// leave the Spaces list, its sessions would move to Other, and the space delete could no longer
// find it. The Agent therefore keeps its own record of the spaces it knows, outside every space,
// and a recorded folder whose marker is missing gets the marker written back instead of being
// dropped. The marker stays what decides; the record only restores it.
//
// An entry names the directory itself (device and inode), not only its folder name: a space moved
// away by hand and replaced by an ordinary folder of the same name is a different directory, and
// that folder must neither get the old space's marker nor be protected as the space.
//
// Only a marker that is absent is restored. A directory, FIFO or symlink in its place is left
// alone and the folder stays no space, as before: something put it there, and replacing it would
// be the Agent deleting a file it did not write.

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// recordMu serialises the record's read-modify-write and guards recordCache. Lock order:
// renameMu, lifeMu, then recordMu.
var recordMu sync.Mutex

// recordEntry is what the record keeps per space: enough to write the marker back, and the
// identity of the directory it belongs to.
type recordEntry struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt,omitempty"`
	Dev       uint64 `json:"dev"`
	Ino       uint64 `json:"ino"`
}

// recordFile is the record's content.
type recordFile struct {
	Spaces map[string]recordEntry `json:"spaces"`
}

// recordPath is where the record lives: the state dir is in the home volume with ~/spaces, so
// the record and the folders it describes are lost or kept together.
func recordPath() string { return filepath.Join(paths.AgentStateDir(), "spaces.json") }

// recordMaxBytes bounds the record both ways: writeRecord refuses to grow past it, so readRecord
// never meets a record of its own it cannot read. One entry is about 150 bytes.
const recordMaxBytes = 4 << 20

// fileKey tells whether the record file changed since it was cached.
type fileKey struct {
	dev, ino uint64
	size     int64
	mtime    time.Time
}

// recordCache keeps the last record read. A session row asks FolderOf on every poll, and a row
// in a folder made by hand under ~/spaces asks the record each time; an lstat says whether the
// cached copy is still the file.
var recordCache struct {
	key fileKey
	rec recordFile
	ok  bool
}

// errRecordUnreadable is a record that is there but could not be read: an update must not
// replace it with what little it knows.
var errRecordUnreadable = errors.New("the spaces record could not be read")

// readRecordLocked answers the record (a copy the caller may change). No record is an empty one.
// One that is no regular file (a FIFO, a link: never opened, so nothing blocks), is over the size
// bound or does not parse is treated as empty too, and the next write replaces it: it only ever
// restores markers, and List records every marked space again. A read that fails (permission,
// I/O) is an error, so a passing fault does not wipe every entry. recordMu must be held.
func readRecordLocked() (recordFile, error) {
	empty := recordFile{Spaces: map[string]recordEntry{}}
	fi, err := os.Lstat(recordPath())
	if errors.Is(err, fs.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return recordFile{}, errRecordUnreadable
	}
	if !fi.Mode().IsRegular() || fi.Size() > recordMaxBytes {
		return empty, nil
	}
	key := keyOf(fi)
	if recordCache.ok && recordCache.key == key {
		return recordFile{Spaces: maps.Clone(recordCache.rec.Spaces)}, nil
	}
	f, err := os.OpenFile(recordPath(), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
		return empty, nil
	}
	if err != nil {
		return recordFile{}, errRecordUnreadable
	}
	defer f.Close()
	ofi, err := f.Stat()
	if err != nil {
		return recordFile{}, errRecordUnreadable
	}
	if !ofi.Mode().IsRegular() {
		return empty, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, recordMaxBytes+1))
	if err != nil {
		return recordFile{}, errRecordUnreadable
	}
	var got recordFile
	if len(b) > recordMaxBytes || json.Unmarshal(b, &got) != nil || got.Spaces == nil {
		return empty, nil
	}
	recordCache.key, recordCache.rec, recordCache.ok = keyOf(ofi), got, true
	return recordFile{Spaces: maps.Clone(got.Spaces)}, nil
}

func keyOf(fi os.FileInfo) fileKey {
	k := fileKey{size: fi.Size(), mtime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		k.dev, k.ino = uint64(st.Dev), st.Ino
	}
	return k
}

// writeRecordLocked replaces the record through a temporary file and a rename. recordMu must be
// held.
func writeRecordLocked(rec recordFile) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > recordMaxBytes {
		return errors.New("the spaces record would grow past its size bound")
	}
	dir := filepath.Dir(recordPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	recordCache.ok = false
	tmp, err := os.CreateTemp(dir, ".spaces-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
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

// updateRecord applies change to the record and writes it back when change reports a change. A
// record that could not be read is left as it is.
func updateRecord(change func(map[string]recordEntry) bool) error {
	recordMu.Lock()
	defer recordMu.Unlock()
	rec, err := readRecordLocked()
	if err != nil {
		return err
	}
	if !change(rec.Spaces) {
		return nil
	}
	return writeRecordLocked(rec)
}

// entryFor is the record entry of the directory dirFi describes, under the given marker fields.
func entryFor(dirFi os.FileInfo, name, createdAt string) recordEntry {
	e := recordEntry{Name: name, CreatedAt: createdAt}
	if st, ok := dirFi.Sys().(*syscall.Stat_t); ok {
		e.Dev, e.Ino = uint64(st.Dev), st.Ino
	}
	return e
}

// sameDir reports whether dirFi is the directory e was recorded for.
func sameDir(e recordEntry, dirFi os.FileInfo) bool {
	st, ok := dirFi.Sys().(*syscall.Stat_t)
	return ok && e.Ino != 0 && uint64(st.Dev) == e.Dev && st.Ino == e.Ino
}

// remember records the space in folder. A failure is not the caller's: the marker is written
// already, and the next List records the space again (syncRecord).
func remember(folder string, e recordEntry) {
	_ = updateRecord(func(m map[string]recordEntry) bool {
		if old, ok := m[folder]; ok && old == e {
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

// recorded answers folder's record entry. A record that cannot be read records nothing.
func recorded(folder string) (recordEntry, bool) {
	recordMu.Lock()
	defer recordMu.Unlock()
	rec, err := readRecordLocked()
	if err != nil {
		return recordEntry{}, false
	}
	e, ok := rec.Spaces[folder]
	return e, ok
}

// syncRecord brings the record in line with the listed spaces, from what is on disk now: a space
// made before the record existed is added, and an entry whose marker says otherwise (a rename
// whose record write failed, a folder replaced by a restore) is corrected. It holds lifeMu, so a
// delete that moved a listed folder away since the list was read is seen as gone and not
// recorded again. A marker that does not parse keeps the recorded name and creation time.
func syncRecord(spaces []Space) {
	lifeMu.RLock()
	defer lifeMu.RUnlock()
	type seen struct {
		dirFi  os.FileInfo
		m      marker
		parsed bool
	}
	now := map[string]seen{}
	for _, sp := range spaces {
		dir := filepath.Join(Root(), sp.Folder)
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || !markerPresent(dir) {
			continue
		}
		m, parsed := readMarkerParsed(dir)
		now[sp.Folder] = seen{dirFi: fi, m: m, parsed: parsed}
	}
	if len(now) == 0 {
		return
	}
	_ = updateRecord(func(rec map[string]recordEntry) bool {
		changed := false
		for folder, s := range now {
			old, had := rec[folder]
			e := entryFor(s.dirFi, s.m.Name, s.m.CreatedAt)
			if !s.parsed && had {
				e.Name, e.CreatedAt = old.Name, old.CreatedAt
			}
			if had && old == e {
				continue
			}
			rec[folder] = e
			changed = true
		}
		return changed
	})
}

// afterRepair is a test seam that runs once a repair has found the marker missing and written its
// temporary file, just before the final checks and the publish. Nil in production.
var afterRepair func()

// repairMarker writes the marker of the recorded space in folder back when it is missing, and
// reports whether the folder carries a marker afterwards. The folder has to be the real directory
// that was recorded (a symlink in its place is no space, as everywhere else, and another
// directory under the same name is not this space), and only an absent marker is written: it is
// published with a hard link, which fails when anything appeared there meanwhile, so a marker
// written by someone else is never replaced. Just before that, ~/spaces and the folder are checked
// to be the directories that were opened, so a folder moved away meanwhile gets nothing written
// into it at its new place.
func repairMarker(folder string) bool {
	if !ValidFolder(folder) {
		return false
	}
	// A row whose folder is gone (most rows that get here) costs one lstat, and no lock.
	if fi, err := os.Lstat(filepath.Join(Root(), folder)); err != nil || !fi.IsDir() {
		return false
	}
	// Shared with every other write inside a space: a delete moving the folder away waits for it,
	// and one that already moved it has also forgotten it, which the record read below sees.
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
	topFi, err := top.Stat(".")
	if err != nil {
		return false
	}
	dirFi, err := top.Lstat(folder)
	if err != nil || !dirFi.IsDir() {
		return false
	}
	e, ok := recorded(folder)
	if !ok || !sameDir(e, dirFi) {
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
	if afterRepair != nil {
		afterRepair()
	}
	if now, err := os.Stat(Root()); err != nil || !os.SameFile(topFi, now) {
		return false
	}
	if cur, err := top.Lstat(folder); err != nil || !os.SameFile(dirFi, cur) {
		return false
	}
	if err := sp.Link(tmp, MarkerFile); err != nil && !errors.Is(err, fs.ErrExist) {
		return false
	}
	return markerPresent(filepath.Join(Root(), folder))
}

// Protected reports whether the file browser must refuse to delete or rename the entry at the
// absolute path p, or to rename something onto it: ~/spaces itself, a space's folder, or a
// space's marker. Each of those is the space delete's job (the trash, the running-session
// refusal, the shelving), and a folder moved under another name breaks --resume for every
// session in it (see the package comment). Files inside a space stay the member's to change.
//
// The last element is judged as it is spelt, never followed: deleting or renaming a symlink
// touches the link, not its target. The folders above it are resolved, and so is ~/spaces itself,
// afresh (not through realRoot's cache, which a ~/spaces moved while the Agent runs would leave
// stale), so an alias of ~/spaces cannot reach a space by another spelling. A folder counts when
// it carries a marker or is the directory recorded under its name, so a space whose marker was
// just removed is still protected, and one replaced by another entry is not.
func Protected(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	roots := []string{Root()}
	if real, err := filepath.EvalSymlinks(Root()); err == nil && real != Root() {
		roots = append(roots, real)
	}
	isRoot := func(q string) bool { return q == roots[0] || (len(roots) > 1 && q == roots[1]) }
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

// isSpaceFolder reports whether folder under Root is a real directory that is a space or is the
// directory recorded as one.
func isSpaceFolder(folder string) bool {
	if !ValidFolder(folder) {
		return false
	}
	dir := filepath.Join(Root(), folder)
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	if markerPresent(dir) {
		return true
	}
	e, ok := recorded(folder)
	return ok && sameDir(e, fi)
}
