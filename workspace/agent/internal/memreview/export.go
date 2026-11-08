package memreview

// The one-time export of ADR 0117's pending proposal queue (ADR 0130 decision 2). The review is
// gone, but the proposals it left unanswered are observations a later step may read, so they are
// written to one file under the memory store's work directory before the review's state goes.
//
// The order is the whole safety argument:
//
//  1. the export file is written durably (temp file, fsync, rename, fsync of the directory);
//  2. only then is the review's state directory removed, queue and rejected hashes included.
//
// A stop between the two leaves a valid export and the old state; the next start sees the export,
// does not write it again and finishes the removal. A stop before step 1 completes leaves no
// export file (the rename is atomic) and an untouched queue. With the state directory gone there
// is no run record left, so nothing can resume or re-dispatch an old run, and nothing here raises
// a notification.
//
// The export is versioned: a file of another version is rewritten from the queue while the queue
// still exists, and never read as "done".

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

const (
	// ExportVersion is the version of the export file's format.
	ExportVersion = 1
	// ExportFileName is the file under the memory store's work directory.
	ExportFileName = "review-queue-export-v1.json"
	exportSource   = "adr-0117-review-queue"
)

// ExportedProposal is one pending proposal as the review had queued it. Nothing here is an
// approval: a member never saw a decision made on it.
type ExportedProposal struct {
	ID           string    `json:"id"`
	RunID        string    `json:"runId"`
	Session      string    `json:"session"`
	Scope        string    `json:"scope"`
	ProjectID    string    `json:"projectId,omitempty"`
	BaseRevision int       `json:"baseRevision,omitempty"`
	Op           string    `json:"op"`
	Name         string    `json:"name"`
	Type         string    `json:"type,omitempty"`
	Description  string    `json:"description,omitempty"`
	Body         string    `json:"body,omitempty"`
	Reason       string    `json:"reason"`
	Evidence     []int     `json:"evidence"`
	Hash         string    `json:"hash"`
	Created      time.Time `json:"created"`
}

// ExportedRejection is a rejected proposal's hash: the member said no to exactly this idea.
type ExportedRejection struct {
	Hash      string    `json:"hash"`
	Scope     string    `json:"scope"`
	Name      string    `json:"name"`
	ProjectID string    `json:"projectId,omitempty"`
	At        time.Time `json:"at"`
}

// Export is the whole file.
type Export struct {
	Version    int                 `json:"version"`
	Source     string              `json:"source"`
	ExportedAt time.Time           `json:"exportedAt"`
	Proposals  []ExportedProposal  `json:"proposals"`
	Rejected   []ExportedRejection `json:"rejected"`
	// Unreadable counts queue files that could not be parsed: they are not in the lists above and
	// are removed with the rest, so the number is the only trace.
	Unreadable int `json:"unreadable,omitempty"`
}

// legacyProposal and legacyRejection read the files ADR 0117's queue wrote (the field names are
// that format's; the package that wrote them is gone).
type legacyProposal struct {
	ID        string `json:"id"`
	RunID     string `json:"runId"`
	Session   string `json:"session"`
	ProjectID string `json:"projectId,omitempty"`
	Op        struct {
		Op           string `json:"op"`
		Scope        string `json:"scope"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		Description  string `json:"description"`
		Body         string `json:"body"`
		Reason       string `json:"reason"`
		Evidence     []int  `json:"evidence"`
		BaseRevision int    `json:"baseRevision"`
	} `json:"op"`
	Hash    string    `json:"hash"`
	Created time.Time `json:"created"`
}

// stateRoot is the retired review's directory (pending/, rejected/, runs/, ...). It is resolved on
// every call: tests move CLAUDE_CONFIG_DIR.
func stateRoot() string { return filepath.Join(paths.ClaudeConfigDir(), "af-memory-review") }

// workDir is the memory store's work directory (memoryx.memoryWorkDir).
func workDir() string { return filepath.Join(paths.ClaudeConfigDir(), "af-memory.work") }

// ExportPath is where the export file is.
func ExportPath() string { return filepath.Join(workDir(), ExportFileName) }

// The calls that decide durability and the stop point between the two steps; tests replace them.
var (
	syncFile    = func(f *os.File) error { return f.Sync() }
	syncDir     = syncDirectory
	afterExport = func() error { return nil }
	removeAll   = os.RemoveAll
)

func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// MigrateQueue is the migration. It is safe to call on every start: with no review state directory
// it does nothing, and with an export already on disk it only finishes the removal.
//
// The whole migration runs under an exclusive lock file in the work directory: two Agents on one
// config directory (overlapping restarts) must not interleave, or the second could read a queue the
// first is already deleting and overwrite its complete export with a partial one.
func MigrateQueue() error {
	root := stateRoot()
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	unlock, err := lockMigration()
	if err != nil {
		return err
	}
	defer unlock()
	// Another Agent may have finished while this one waited for the lock.
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !exportDone() {
		if err := writeExport(readQueue(root)); err != nil {
			return err
		}
	}
	if err := afterExport(); err != nil {
		return err
	}
	if err := removeAll(root); err != nil {
		return err
	}
	return syncDir(filepath.Dir(root))
}

// lockMigration takes the exclusive lock (flock, released by the kernel if the process dies).
func lockMigration() (func(), error) {
	dir := workDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".review-queue-export.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// exportDone reports whether a complete export of the current version is on disk.
func exportDone() bool {
	b, err := os.ReadFile(ExportPath())
	if err != nil {
		return false
	}
	var e Export
	return json.Unmarshal(b, &e) == nil && e.Version == ExportVersion && e.Source == exportSource
}

func readQueue(root string) Export {
	e := Export{Version: ExportVersion, Source: exportSource, ExportedAt: time.Now().UTC(),
		Proposals: []ExportedProposal{}, Rejected: []ExportedRejection{}}
	for _, name := range jsonFiles(filepath.Join(root, "pending")) {
		var p legacyProposal
		if !readJSON(name, &p) || p.ID == "" {
			e.Unreadable++
			continue
		}
		ev := p.Op.Evidence
		if ev == nil {
			ev = []int{}
		}
		e.Proposals = append(e.Proposals, ExportedProposal{
			ID: p.ID, RunID: p.RunID, Session: p.Session, Scope: p.Op.Scope, ProjectID: p.ProjectID,
			BaseRevision: p.Op.BaseRevision, Op: p.Op.Op, Name: p.Op.Name, Type: p.Op.Type,
			Description: p.Op.Description, Body: p.Op.Body, Reason: p.Op.Reason, Evidence: ev,
			Hash: p.Hash, Created: p.Created,
		})
	}
	sort.SliceStable(e.Proposals, func(i, j int) bool {
		a, b := e.Proposals[i], e.Proposals[j]
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.ID < b.ID
	})
	for _, name := range jsonFiles(filepath.Join(root, "rejected")) {
		var r ExportedRejection
		if !readJSON(name, &r) || r.Hash == "" {
			e.Unreadable++
			continue
		}
		e.Rejected = append(e.Rejected, r)
	}
	sort.SliceStable(e.Rejected, func(i, j int) bool { return e.Rejected[i].Hash < e.Rejected[j].Hash })
	return e
}

// jsonFiles lists dir's regular *.json files (the staging subdirectory is not one).
func jsonFiles(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && len(b) > 0 && json.Unmarshal(b, v) == nil
}

// writeExport makes the export durable: when it returns nil the file is visible under its final
// name and the directory entry is synced, so a later deletion cannot outlive it.
func writeExport(e Export) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	dir := workDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// A writer killed before its rename leaves a temp file holding proposal text.
	stale, _ := filepath.Glob(filepath.Join(dir, ".review-queue-export-*.tmp"))
	for _, s := range stale {
		_ = os.Remove(s)
	}
	f, err := os.CreateTemp(dir, ".review-queue-export-*.tmp") // mode 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	if werr == nil {
		werr = syncFile(f)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, ExportPath())
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write export: %w", werr)
	}
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if err := syncDir(d); err != nil {
			return errors.Join(errors.New("sync export directory"), err)
		}
	}
	return nil
}
