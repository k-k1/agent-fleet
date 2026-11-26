package spacex

// The record of spaces (issue #5231). MarkerFile sits inside the folder agents work in, so an
// agent tidying dotfiles, or the member, can delete it, and with it the space would silently
// leave the Spaces list, its sessions would move to Other, and the space delete could no longer
// find it. The Agent therefore keeps its own record of the spaces it knows, outside every space,
// and a recorded folder whose marker is missing gets the marker written back instead of being
// dropped. The marker stays what decides; the record only restores it.
//
// An entry names the directory itself (inode and birth time), not only its folder name: a space
// moved away by hand and replaced by an ordinary folder of the same name is a different directory,
// and that folder must neither get the old space's marker nor be protected as the space. The
// device number is left out because the kernel assigns it per mount (a home volume attached again
// may get another); the birth time is what tells a directory made later in a reused inode.
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

	"golang.org/x/sys/unix"

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
	Ino       uint64 `json:"ino"`
	// Born is the directory's birth time in Unix nanoseconds, 0 where the file system keeps none.
	Born int64 `json:"born,omitempty"`
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

// fileKey tells whether the record file changed since it was cached. The change time and mode are
// in it so a chmod (which keeps size and mtime) makes the next read find out the file is no longer
// readable, rather than answer from the cache.
type fileKey struct {
	dev, ino uint64
	size     int64
	mtime    time.Time
	ctime    syscall.Timespec
	mode     os.FileMode
}

// recordCache keeps the last record read. A session row asks FolderOf on every poll, and a row
// in a folder made by hand under ~/spaces asks the record each time; an lstat says whether the
// cached copy is still the file. rec is never changed in place: readers look entries up under
// recordMu, and updateRecord changes a copy.
var recordCache struct {
	key fileKey
	rec recordFile
	ok  bool
}

// errRecordUnreadable is a record that is there but could not be read: an update must not
// replace it with what little it knows.
var errRecordUnreadable = errors.New("the spaces record could not be read")

// readRecordLocked answers the record, which the caller must not change (updateRecord copies it).
// No record is an empty one.
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
		return recordCache.rec, nil
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
	return got, nil
}

func keyOf(fi os.FileInfo) fileKey {
	k := fileKey{size: fi.Size(), mtime: fi.ModTime(), mode: fi.Mode()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		k.dev, k.ino, k.ctime = uint64(st.Dev), st.Ino, st.Ctim
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
	cur, err := readRecordLocked()
	if err != nil {
		return err
	}
	rec := recordFile{Spaces: maps.Clone(cur.Spaces)}
	if !change(rec.Spaces) {
		return nil
	}
	return writeRecordLocked(rec)
}

// dirID is a directory's identity: its inode and birth time. The inode alone is not one: a folder
// made after the space was removed may get the same inode back.
type dirID struct {
	ino  uint64
	born int64
}

// idOf answers the identity of the entry at path, without following a link at its end, and false
// when there is none to be had: no statx, or a file system that keeps no birth time. Without one
// no directory is ever taken for a recorded space, so such a file system gets no repair and no
// protection for a space whose marker is gone, which is what it had before the record.
func idOf(path string) (dirID, bool) {
	return statxID(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW)
}

// idOfFile is idOf for a directory already open: the identity of what was opened, whatever its
// path names by now.
func idOfFile(f *os.File) (dirID, bool) {
	return statxID(int(f.Fd()), "", unix.AT_EMPTY_PATH)
}

func statxID(dirfd int, path string, flags int) (dirID, bool) {
	var st unix.Statx_t
	if err := unix.Statx(dirfd, path, flags, unix.STATX_INO|unix.STATX_BTIME, &st); err != nil {
		return dirID{}, false
	}
	if st.Mask&unix.STATX_BTIME == 0 || st.Mask&unix.STATX_INO == 0 {
		return dirID{}, false
	}
	born := st.Btime.Sec*1e9 + int64(st.Btime.Nsec)
	if born == 0 || st.Ino == 0 {
		return dirID{}, false
	}
	return dirID{ino: st.Ino, born: born}, true
}

// entryWith is the record entry for a directory of identity id (the zero dirID when it could not
// be had: remember then keeps the identity already recorded), under the marker's fields. The name
// is cleaned the way Lookup cleans it, so a repair writes back the name a reader showed, and one
// Lookup would drop is recorded as none.
func entryWith(id dirID, name, createdAt string) recordEntry {
	if n, ok := CleanName(name); ok {
		name = n
	} else {
		name = ""
	}
	return recordEntry{Name: name, CreatedAt: createdAt, Ino: id.ino, Born: id.born}
}

// sameDir reports whether the directory at dir is the one e was recorded for.
func sameDir(e recordEntry, dir string) bool {
	id, ok := idOf(dir)
	return ok && e.Ino != 0 && e.Born != 0 && id == dirID{ino: e.Ino, born: e.Born}
}

// inoOf answers fi's inode, 0 when it has none.
func inoOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// remember records the space in folder. A failure is not the caller's: the marker is written
// already, and the next List records the space again (syncRecord). An entry without an identity
// keeps the one already recorded, so a passing statx failure does not unprotect a space.
func remember(folder string, e recordEntry) {
	_ = updateRecord(func(m map[string]recordEntry) bool {
		old, ok := m[folder]
		if ok && e.Ino == 0 {
			e.Ino, e.Born = old.Ino, old.Born
		}
		if ok && old == e {
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
//
// It takes renameMu as well, so a rename cannot publish a new name and record it between this
// reading the old marker and writing the record back.
func syncRecord(spaces []Space) {
	renameMu.Lock()
	defer renameMu.Unlock()
	lifeMu.RLock()
	defer lifeMu.RUnlock()
	type seen struct {
		id     dirID
		m      marker
		parsed bool
	}
	now := map[string]seen{}
	for _, sp := range spaces {
		dir := filepath.Join(Root(), sp.Folder)
		// The identity is taken before and after the marker is read: a folder swapped for another
		// meanwhile (renameMu and lifeMu do not stop a shell) would otherwise tie this marker's name
		// to the other folder.
		before, ok := idOf(dir)
		if !ok || !markerPresent(dir) {
			continue
		}
		m, parsed := readMarkerParsed(dir)
		// An unparsable marker keeps the recorded name only for the directory it was recorded for.
		if after, ok := idOf(dir); !ok || after != before {
			continue
		}
		now[sp.Folder] = seen{id: before, m: m, parsed: parsed}
	}
	if len(now) == 0 {
		return
	}
	if afterSyncRead != nil {
		afterSyncRead()
	}
	_ = updateRecord(func(rec map[string]recordEntry) bool {
		changed := false
		for folder, s := range now {
			old, had := rec[folder]
			e := entryWith(s.id, s.m.Name, s.m.CreatedAt)
			if !s.parsed && had && old.Ino == s.id.ino && old.Born == s.id.born {
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

// afterSyncRead is a test seam between syncRecord's reading of the markers and its write. Nil in
// production.
var afterSyncRead func()

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
	// A row whose folder is gone (most rows that get here) costs one lstat, and one in a folder made
	// by hand a record lookup; neither takes lifeMu or opens anything.
	dir := filepath.Join(Root(), folder)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return false
	}
	if e, ok := recorded(folder); !ok || !sameDir(e, dir) {
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
	// Again under lifeMu: a delete may have moved the folder away and forgotten it since.
	e, ok := recorded(folder)
	if !ok || !sameDir(e, dir) || inoOf(dirFi) != e.Ino {
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
	name, nameOK := CleanName(e.Name)
	if !nameOK {
		name = ""
	}
	b, ok := repairedMarkerBytes(name, createdAt)
	if !ok {
		return false
	}
	tmp, err := writeTempIn(sp, b)
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
	return ok && sameDir(e, dir)
}

// repairedMarkerBytes is the marker a repair writes, within the bound every reader applies
// (markerMaxBytes): a marker past it would be published and then read as nameless for good. A
// creation time too long for the full form (copied from an old marker) drops the note first, then
// the creation time. The name is a cleaned one (at most NameMaxRunes runes, which JSON escaping
// keeps far below the bound), and an empty marker is the last resort, so a repair is never refused
// for its size.
func repairedMarkerBytes(name, createdAt string) ([]byte, bool) {
	for _, v := range []any{
		newMarker(name, createdAt),
		marker{Name: name, CreatedAt: createdAt},
		marker{Name: name},
		marker{},
	} {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		if b = append(b, '\n'); len(b) <= markerMaxBytes {
			return b, true
		}
	}
	return nil, false
}
