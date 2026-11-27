package session

// Persistence of the session Meta: the directory it lives in, read/write, enumeration and
// updating the starting branch.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MetaDir lives in the home volume (persists across Stop→Start) under the denylisted
// .local/state/agent-fleet, so stopped sessions survive a Workspace restart.
//
// It used to say the same sentence about .config/agent-fleet, and on the ecs-ec2 runtime
// that was not true: ~/.config is one of AF_WS_KEEP_DIRS, so the ledger was on EFS and
// ListMetas below — one ReadDir plus one ReadFile per session, 5M+4 file syscalls, so 1,039
// at 207 sessions — ran over NFS once every four seconds per open Console tab (ADR 0087
// source A). (The 836 this comment used to quote was measured with a trace set that left out
// fstat, which os.ReadFile's f.Stat() emits once per file on linux/amd64; re-measured
// 2026-09-18.)
// paths.AgentStateDir is the home-volume root that keeps the sentence true.
func MetaDir() string {
	if v := os.Getenv("AF_SESSIONS_DIR"); v != "" {
		return v
	}
	return filepath.Join(paths.AgentStateDir(), "sessions")
}

func MetaPath(name string) string { return filepath.Join(MetaDir(), name+".json") }

// metaWriteGap is a test seam: WriteMeta calls it once its temporary file is complete and before
// the rename, so a test can hold a writer there and read the meta meanwhile.
var metaWriteGap func()

// WriteMeta stores m best effort and reports nothing. The file is replaced by a rename from a
// temporary file in MetaDir (the rename is atomic only inside one directory), so a concurrent
// reader sees the old meta or the new one, never a truncated or half-written file: ReadMeta
// would answer "not found" for that and ListMetas would drop the session, and some readers
// decide on that answer (retire, an automatic start, the person-act record). No fsync: this is
// the hot path (the session list's stopped stamp among them) and a lost write is rewritten by
// the next one; WriteMetaChecked is the durable variant.
func WriteMeta(m Meta) {
	if err := os.MkdirAll(MetaDir(), 0o700); err != nil {
		return
	}
	var tmp string
	if b, err := json.Marshal(m); err == nil {
		tmp, _ = writeMetaTemp(b, false)
	}
	if tmp != "" {
		defer os.Remove(tmp) // a no-op after a successful rename
	}
	if metaWriteGap != nil {
		metaWriteGap()
	}
	metaWriteMu.Lock()
	defer metaWriteMu.Unlock()
	if deletedMetas[MetaPath(m.Name)] {
		// Deleted (moved to the trash, ADR 0101) since the caller read it. Every writer works
		// from a snapshot it read earlier — the list's stopped stamp, a title suggestion that
		// took seconds, a restore from the shelf — and writing that back would bring the row
		// back with its transcript already in the trash. Names are never reused, so a deleted
		// name stays deleted until a restore from the trash (CreateMetaIfAbsent) clears it.
		// The check is made here, after the temporary file, because a delete can land in between.
		return
	}
	if tmp != "" {
		_ = os.Rename(tmp, MetaPath(m.Name))
	}
	rememberCWD(m)
}

// writeMetaTemp writes b to a new temporary file in MetaDir (mode 0600) and returns its path;
// nothing is left behind on an error. The name starts with a dot and ends in .tmp, which every
// walker of the directory skips (they take only *.json).
func writeMetaTemp(b []byte, durable bool) (string, error) {
	tmp, err := os.CreateTemp(MetaDir(), ".meta-*.tmp")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(b)
	if werr == nil && durable {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return "", werr
	}
	return tmp.Name(), nil
}

// ErrMetaDeleted is WriteMetaChecked's answer for a meta deleted since the caller read it.
var ErrMetaDeleted = errors.New("the session's meta was deleted")

// WriteMetaChecked is WriteMeta for a writer that must know the meta reached the disk: a move's
// moving / movedTo is the only record of a closed source after a restart, and success there
// must mean it is on disk. It fsyncs the temporary file before the rename and returns every
// error.
func WriteMetaChecked(m Meta) error {
	if err := os.MkdirAll(MetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := writeMetaTemp(b, true)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	metaWriteMu.Lock()
	defer metaWriteMu.Unlock()
	if deletedMetas[MetaPath(m.Name)] {
		return ErrMetaDeleted
	}
	if err := os.Rename(tmp, MetaPath(m.Name)); err != nil {
		return err
	}
	rememberCWD(m)
	return nil
}

// metaWriteMu orders WriteMeta against the removal of a meta, and deletedMetas remembers which
// meta files a person deleted in this process (keyed by path, so a test's temporary directory
// never shadows another's). Together they make "read a meta, work, write it back" safe against
// a delete landing in between, at every one of the writers at once.
var (
	metaWriteMu     sync.Mutex
	deletedMetas    = map[string]bool{}
	trashInProgress = map[string]bool{}
)

// CreateMetaIfAbsent writes m only when no meta of that name exists, and reports whether it
// did. For the cleanup restore: an archived meta is a snapshot, and a live meta of the same
// name — a session already restored, then locked, renamed or run since — is newer and must
// not be rolled back to it. The meta is written to a temporary name first and hard-linked
// into place, which fails if the name exists: atomic, and never a half-written meta on disk.
func CreateMetaIfAbsent(m Meta) (created bool, err error) {
	if err := os.MkdirAll(MetaDir(), 0o700); err != nil {
		return false, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(MetaDir(), ".meta-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		return false, errors.Join(werr, cerr)
	}
	metaWriteMu.Lock()
	defer metaWriteMu.Unlock()
	path := MetaPath(m.Name)
	if trashInProgress[path] {
		return false, errors.New("the session is being moved to the trash")
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	// A restore from the trash is the one way a deleted name comes back.
	delete(deletedMetas, MetaPath(m.Name))
	rememberCWD(m)
	return true, nil
}

// MetaState is what ReadMetaState found.
type MetaState int

const (
	// MetaAbsent: there is no meta file of that name (os reported "does not exist").
	MetaAbsent MetaState = iota
	// MetaUnreadable: a meta file may be there but could not be read or parsed (empty, cut,
	// not JSON, a permission or I/O error). It is no evidence of absence: a caller that decides
	// something on the meta treats it as in doubt.
	MetaUnreadable
	// MetaOK: the meta was read.
	MetaOK
)

// ReadMetaState is ReadMeta that tells "absent" from "present but unreadable". For the readers
// that decide something (retire, an automatic start, the person-act record); a reader that only
// displays keeps ReadMeta.
func ReadMetaState(name string) (Meta, MetaState) {
	var m Meta
	b, err := os.ReadFile(MetaPath(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return m, MetaAbsent
		}
		return m, MetaUnreadable
	}
	if json.Unmarshal(b, &m) != nil {
		return Meta{}, MetaUnreadable
	}
	rememberCWD(m)
	return m, MetaOK
}

func ReadMeta(name string) (Meta, bool) {
	var m Meta
	b, err := os.ReadFile(MetaPath(name))
	if err != nil {
		return m, false
	}
	if json.Unmarshal(b, &m) != nil {
		return m, false
	}
	rememberCWD(m)
	return m, true
}

// RemoveMeta forgets the meta and NOTHING else — no fleet-graph lineage change. It has
// exactly ONE production caller: the stopped-session TTL auto-prune in
// HandleListSessions, which is the one case ADR 0096 decision 6 says must NOT erase the
// lineage row (prune already costs the session's card; costing its line in the graph too
// is the exact regression the ADR's ledger exists to undo — see docs/log/94). Every other
// place a session's meta is forgotten is a PERSON asking for it to be gone, and belongs on
// RemoveMetaAndLineage instead. meta_remove_sites_test.go counts callers of both so a new
// call site cannot silently pick the wrong one.
func RemoveMeta(name string) { _ = os.Remove(MetaPath(name)) }

// RemoveMetaAndLineage is RemoveMeta plus erasing the session's fleet-graph lineage row
// (ADR 0096 decision 6: an explicit, person-initiated forgetting of a session means
// "deleted", not "still has a line in the graph"). Its one caller is main's trashSession
// (ADR 0101 decision 1), which every delete goes through — DELETE /sessions/{name}, its old
// name /stop, and the shell / ssm of a deleted working copy — after the gz archive is
// written. The caller releases the name after cleanup, so a restore from another archive
// cannot bring the meta back while the trash is still removing files under that name.
func RemoveMetaAndLineage(name string) (release func(), err error) {
	metaWriteMu.Lock()
	path := MetaPath(name)
	err = os.Remove(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		deletedMetas[path] = true
		trashInProgress[path] = true
	}
	metaWriteMu.Unlock()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	eraseLineage(name)
	return func() { releaseMetaTrash(path) }, nil
}

// StagedMeta retains the original file until the folder operation has succeeded.
type StagedMeta struct {
	name, path, backup string
}

// StageMetaRemoval hides a meta after its trash archive is complete. Renaming it back can
// roll back the operation without writing the meta again, even on a full disk.
func StageMetaRemoval(name string) (StagedMeta, error) {
	path := MetaPath(name)
	f, err := os.CreateTemp(filepath.Dir(path), ".trash-meta-*.tmp")
	if err != nil {
		return StagedMeta{}, err
	}
	backup := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(backup)
		return StagedMeta{}, err
	}
	metaWriteMu.Lock()
	defer metaWriteMu.Unlock()
	if err := os.Rename(path, backup); err != nil {
		_ = os.Remove(backup)
		return StagedMeta{}, err
	}
	trashInProgress[path] = true
	deletedMetas[path] = true
	return StagedMeta{name, path, backup}, nil
}

// Rollback restores the original meta. If it is blocked, leave the trash archive available
// for a later restore and keep the original file as a backup.
func (s StagedMeta) Rollback() error {
	metaWriteMu.Lock()
	defer metaWriteMu.Unlock()
	defer delete(trashInProgress, s.path)
	if _, err := os.Lstat(s.path); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return err
		}
		return fs.ErrExist
	}
	if err := os.Rename(s.backup, s.path); err != nil {
		return err
	}
	delete(deletedMetas, s.path)
	return nil
}

// Commit erases lineage while the staged name is still blocked against restore.
// Release must follow the other per-session cleanup, which still owns files under the name.
func (s StagedMeta) Commit() {
	eraseLineage(s.name)
	if err := os.Remove(s.backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("trash %s: could not remove staged meta: %v", s.name, err)
	}
}

func (s StagedMeta) Release() {
	releaseMetaTrash(s.path)
}

func releaseMetaTrash(path string) {
	metaWriteMu.Lock()
	defer metaWriteMu.Unlock()
	delete(trashInProgress, path)
}

func eraseLineage(name string) {
	if err := fleetgraph.EraseLineage(name); err != nil {
		log.Printf("fleet-graph: erase lineage for %s: %v", name, err)
	}
	// Drop the in-process dedup state (last-observed live state / conv id) too — a deleted
	// name is never reused (session-slug-immutable-managed-no-env), so without this it
	// would just sit unused in the map for the rest of the process's life.
	fleetgraph.ForgetSession(name)
}

func ListMetas() []Meta {
	ents, err := os.ReadDir(MetaDir())
	if err != nil {
		return nil
	}
	var out []Meta
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if m, ok := ReadMeta(strings.TrimSuffix(e.Name(), ".json")); ok {
			out = append(out, m)
		}
	}
	return out
}

// ListMetasChecked is ListMetas that also names the sessions whose meta file exists but could not
// be read, and reports whether the directory itself could be listed (an absent directory is an
// empty list, not a failure). A caller that decides on the list must not treat a name in
// unreadable, or listErr != nil, as "no such session".
func ListMetasChecked() (metas []Meta, unreadable []string, listErr error) {
	ents, err := os.ReadDir(MetaDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		switch m, st := ReadMetaState(name); st {
		case MetaOK:
			metas = append(metas, m)
		case MetaUnreadable:
			unreadable = append(unreadable, name)
		}
	}
	return metas, unreadable, nil
}
