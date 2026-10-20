package memreview

// The review's file store. Every record the review relies on after a stop - the attempt written
// before a call, the daily count, a queued proposal - must survive not only the process ending but
// the host or the storage failing right after the write returned. A rename of an unsynced temp file
// can come back after a power cut empty or missing, and a directory entry that was never synced can
// vanish, so a write here is: write a temp file inside the store, fsync it, rename it into place,
// fsync the directory it left and the one it entered. Removal syncs the directory too and reports
// its error: a text that could not be deleted is not deleted.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// fsOps are the calls that decide durability; tests replace them to see the order and to fail.
var fsOps = struct {
	syncFile func(*os.File) error
	syncDir  func(string) error
	rename   func(oldpath, newpath string) error
}{
	syncFile: func(f *os.File) error { return f.Sync() },
	syncDir: func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	},
	rename: os.Rename,
}

const stagingDir = ".staging"

// store is one directory of JSON records, <root>/<sub>/<key>.json.
type store[T any] struct{ sub string }

func (s store[T]) Dir() string { return filepath.Join(stateRoot(), s.sub) }

func (s store[T]) path(key string) string { return filepath.Join(s.Dir(), key+".json") }

func (s store[T]) staging() string { return filepath.Join(s.Dir(), stagingDir) }

// Write stores v under key durably (see the file comment).
func (s store[T]) Write(key string, v T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := s.Dir()
	if err := mkdirAllSynced(s.staging()); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.staging(), key+".*") // mode 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	if werr == nil {
		werr = fsOps.syncFile(f)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = fsOps.rename(tmp, s.path(key))
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	for _, d := range []string{dir, s.staging()} {
		if err := fsOps.syncDir(d); err != nil {
			return err
		}
	}
	return nil
}

// Read returns the record, ok=false when it is missing or unreadable.
func (s store[T]) Read(key string) (T, bool) {
	var v T
	b, err := os.ReadFile(s.path(key))
	if err != nil || len(b) == 0 || json.Unmarshal(b, &v) != nil {
		var zero T
		return zero, false
	}
	return v, true
}

// Remove deletes the record and syncs the directory. A record that is already gone is not an
// error; anything else is, so a caller never believes a text is deleted when it is not. Temp files
// the store staged for the key are removed too.
func (s store[T]) Remove(key string) error {
	err := os.Remove(s.path(key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.removeStaged(key); err != nil {
		return err
	}
	if serr := fsOps.syncDir(s.Dir()); serr != nil && !os.IsNotExist(serr) {
		return serr
	}
	return nil
}

// removeStaged deletes the staged copies of key (a writer killed between the create and the
// rename leaves one, holding the whole record) and syncs the staging directory.
func (s store[T]) removeStaged(key string) error {
	stale, _ := filepath.Glob(filepath.Join(s.staging(), key+".*"))
	if len(stale) == 0 {
		return nil
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := fsOps.syncDir(s.staging()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// keys lists the stored keys.
func (s store[T]) keys() []string {
	ents, err := os.ReadDir(s.Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && e.Type().IsRegular() {
			out = append(out, name)
		}
	}
	return out
}

// sweepStaging removes staged files older than age. A writer killed between the create and the
// rename leaves one behind, and what it holds may be a proposal's text.
func (s store[T]) sweepStaging(age time.Duration, now time.Time) {
	ents, err := os.ReadDir(s.staging())
	if err != nil {
		return
	}
	removed := false
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > age {
			if os.Remove(filepath.Join(s.staging(), e.Name())) == nil {
				removed = true
			}
		}
	}
	if removed {
		_ = fsOps.syncDir(s.staging())
	}
}

// unsyncedDirs are directories this process created, or is responsible for, whose entries are not
// yet known to be durable. A sync that fails leaves them here, so the next write retries them
// instead of finding the directories already visible and assuming they were synced.
var (
	syncMu       sync.Mutex
	unsyncedDirs = map[string]bool{}
	chainQueued  = map[string]bool{}
)

// mkdirAllSynced creates dir and every missing ancestor and makes the new directory entries
// durable: every directory it created, the existing one the chain hangs from, and, once per
// process for each store directory, the whole chain this package is responsible for (the store's
// directories, the state root, the config directory and its parent) - a directory that is already
// visible may owe its sync to an earlier failure or an earlier process.
func mkdirAllSynced(dir string) error {
	var created []string
	for d := dir; ; d = filepath.Dir(d) {
		_, err := os.Stat(d)
		created = append(created, d) // the last element is the first directory that already existed
		if err == nil || filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	syncMu.Lock()
	defer syncMu.Unlock()
	if len(created) > 1 {
		for _, d := range created {
			unsyncedDirs[d] = true
		}
	}
	if !chainQueued[dir] {
		chainQueued[dir] = true
		cfg := paths.ClaudeConfigDir()
		for d := dir; ; d = filepath.Dir(d) {
			unsyncedDirs[d] = true
			if d == cfg || filepath.Dir(d) == d {
				unsyncedDirs[filepath.Dir(d)] = true
				break
			}
		}
	}
	// Deepest first, so a parent is synced after the entries it lists.
	pending := make([]string, 0, len(unsyncedDirs))
	for d := range unsyncedDirs {
		pending = append(pending, d)
	}
	sort.Slice(pending, func(i, j int) bool { return len(pending[i]) > len(pending[j]) })
	for _, d := range pending {
		if err := fsOps.syncDir(d); err != nil {
			if os.IsNotExist(err) {
				delete(unsyncedDirs, d)
				continue
			}
			return err
		}
		delete(unsyncedDirs, d)
	}
	return nil
}
