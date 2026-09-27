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

func WriteMeta(m Meta) {
	if err := os.MkdirAll(MetaDir(), 0o700); err != nil {
		return
	}
	metaWriteMu.Lock()
	defer metaWriteMu.Unlock()
	if deletedMetas[MetaPath(m.Name)] {
		// Deleted (moved to the trash, ADR 0101) since the caller read it. Every writer works
		// from a snapshot it read earlier — the list's stopped stamp, a title suggestion that
		// took seconds, a restore from the shelf — and writing that back would bring the row
		// back with its transcript already in the trash. Names are never reused, so a deleted
		// name stays deleted until a restore from the trash (CreateMetaIfAbsent) clears it.
		return
	}
	if b, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(MetaPath(m.Name), b, 0o600)
	}
	rememberCWD(m)
}

// metaWriteMu orders WriteMeta against the removal of a meta, and deletedMetas remembers which
// meta files a person deleted in this process (keyed by path, so a test's temporary directory
// never shadows another's). Together they make "read a meta, work, write it back" safe against
// a delete landing in between, at every one of the writers at once.
var (
	metaWriteMu  sync.Mutex
	deletedMetas = map[string]bool{}
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
	if err := os.Link(tmp.Name(), MetaPath(m.Name)); err != nil {
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
// written. Never call it from anywhere else: that would be a delete skipping the trash. Erasure is
// best-effort and logged, never fatal: the meta is already gone by the time this runs, so
// failing the request over it would be strictly worse than a leftover lineage row.
func RemoveMetaAndLineage(name string) {
	metaWriteMu.Lock()
	deletedMetas[MetaPath(name)] = true
	RemoveMeta(name)
	metaWriteMu.Unlock()
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
