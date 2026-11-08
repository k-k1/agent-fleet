package memreview

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	oldSync, oldSyncDir, oldAfter, oldRemove := syncFile, syncDir, afterExport, removeAll
	t.Cleanup(func() { syncFile, syncDir, afterExport, removeAll = oldSync, oldSyncDir, oldAfter, oldRemove })
}

func put(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(stateRoot(), rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const proposalA = `{"id":"aaaaaaaaaaaaaaaaaaaa","runId":"rrrrrrrrrrrrrrrrrrrr","index":0,"session":"s1","projectId":"proj1",
"op":{"op":"update","scope":"project","name":"build-notes","type":"lesson","description":"d","body":"the body",
"reason":"because","evidence":[3,5],"baseRevision":7},"hash":"h1","created":"2026-11-01T10:00:00Z","stale":"changed"}`

const proposalB = `{"id":"bbbbbbbbbbbbbbbbbbbb","runId":"qqqqqqqqqqqqqqqqqqqq","index":1,"session":"s2",
"op":{"op":"create","scope":"user","name":"tone","body":"b2","reason":"r2","evidence":null},"hash":"h2","created":"2026-11-02T10:00:00Z"}`

func readExport(t *testing.T) Export {
	t.Helper()
	b, err := os.ReadFile(ExportPath())
	if err != nil {
		t.Fatal(err)
	}
	var e Export
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func seedQueue(t *testing.T) {
	put(t, "pending/aaaaaaaaaaaaaaaaaaaa.json", proposalA)
	put(t, "pending/bbbbbbbbbbbbbbbbbbbb.json", proposalB)
	put(t, "rejected/h9.json", `{"hash":"h9","scope":"user","name":"old","at":"2026-10-30T10:00:00Z"}`)
	put(t, "runs/rrrrrrrrrrrrrrrrrrrr.json", `{"id":"rrrrrrrrrrrrrrrrrrrr","state":"dispatched","ops":[{"op":"create","body":"unqueued"}]}`)
	put(t, "watermark/x.json", `{}`)
	put(t, "daily.json", `{}`)
}

func TestMigrateExportsEveryProposalAndTheRejectedHashesThenRemovesTheState(t *testing.T) {
	setup(t)
	seedQueue(t)
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	e := readExport(t)
	if e.Version != ExportVersion || len(e.Proposals) != 2 || len(e.Rejected) != 1 || e.Unreadable != 0 {
		t.Fatalf("export = %+v", e)
	}
	a := e.Proposals[0] // oldest first
	want := ExportedProposal{ID: "aaaaaaaaaaaaaaaaaaaa", RunID: "rrrrrrrrrrrrrrrrrrrr", Session: "s1", Scope: "project",
		ProjectID: "proj1", BaseRevision: 7, Op: "update", Name: "build-notes", Type: "lesson", Description: "d",
		Body: "the body", Reason: "because", Evidence: []int{3, 5}, Hash: "h1", Created: a.Created}
	if a.ID != want.ID || a.RunID != want.RunID || a.Session != want.Session || a.Scope != want.Scope ||
		a.ProjectID != want.ProjectID || a.BaseRevision != 7 || a.Op != want.Op || a.Name != want.Name ||
		a.Type != want.Type || a.Description != want.Description || a.Body != want.Body ||
		a.Reason != want.Reason || len(a.Evidence) != 2 || a.Hash != want.Hash {
		t.Fatalf("proposal = %+v, want %+v", a, want)
	}
	if b := e.Proposals[1]; b.ID != "bbbbbbbbbbbbbbbbbbbb" || b.Evidence == nil || len(b.Evidence) != 0 {
		t.Fatalf("second proposal = %+v", b)
	}
	if r := e.Rejected[0]; r.Hash != "h9" || r.Scope != "user" || r.Name != "old" {
		t.Fatalf("rejection = %+v", r)
	}
	if exists(stateRoot()) {
		t.Fatal("the review state (queue, runs) is still there after the export")
	}
	if fi, _ := os.Stat(ExportPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("export mode = %v", fi.Mode())
	}
}

// A stop between the export and the deletion: the export is complete, the queue still there; the
// next start must not write the export again (so what it holds is what was first written) and
// finishes the removal.
func TestMigrateCrashBetweenWriteAndDeleteIsFinishedByTheNextRun(t *testing.T) {
	setup(t)
	seedQueue(t)
	afterExport = func() error { return errors.New("stopped") }
	if err := MigrateQueue(); err == nil {
		t.Fatal("want the stop to surface")
	}
	if !exists(stateRoot()) || !exists(filepath.Join(stateRoot(), "pending", "aaaaaaaaaaaaaaaaaaaa.json")) {
		t.Fatal("the queue went before the stop point")
	}
	first, _ := os.ReadFile(ExportPath())
	if len(readExport(t).Proposals) != 2 {
		t.Fatal("the export was not complete at the stop point")
	}
	afterExport = func() error { return nil }
	// What the second run would write differs if it re-exported: drop a proposal first.
	if err := os.Remove(filepath.Join(stateRoot(), "pending", "bbbbbbbbbbbbbbbbbbbb.json")); err != nil {
		t.Fatal(err)
	}
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	if exists(stateRoot()) {
		t.Fatal("the state was not removed on the rerun")
	}
	if again, _ := os.ReadFile(ExportPath()); string(again) != string(first) {
		t.Fatal("the rerun rewrote the export")
	}
}

// If the export cannot be made durable the queue must stay exactly as it is.
func TestMigrateKeepsTheQueueWhenTheExportCannotBeSynced(t *testing.T) {
	setup(t)
	seedQueue(t)
	syncFile = func(*os.File) error { return errors.New("disk full") }
	if err := MigrateQueue(); err == nil {
		t.Fatal("want an error")
	}
	if exists(ExportPath()) {
		t.Fatal("a failed export is visible under its final name")
	}
	if tmp, _ := filepath.Glob(filepath.Join(workDir(), ".review-queue-export-*")); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
	if !exists(filepath.Join(stateRoot(), "pending", "aaaaaaaaaaaaaaaaaaaa.json")) {
		t.Fatal("the queue was deleted without an export")
	}
	syncFile = func(f *os.File) error { return f.Sync() }
	if err := MigrateQueue(); err != nil || len(readExport(t).Proposals) != 2 || exists(stateRoot()) {
		t.Fatalf("retry: err=%v", err)
	}
}

// The export is synced before the first deletion.
func TestMigrateSyncsTheExportBeforeDeleting(t *testing.T) {
	setup(t)
	seedQueue(t)
	var log []string
	syncFile = func(f *os.File) error { log = append(log, "file"); return f.Sync() }
	syncDir = func(d string) error { log = append(log, "dir"); return syncDirectory(d) }
	removeAll = func(p string) error { log = append(log, "remove"); return os.RemoveAll(p) }
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(log, ","); !strings.HasPrefix(got, "file,dir,dir,remove") {
		t.Fatalf("order = %s", got)
	}
}

func TestMigrateEmptyQueueWritesAnEmptyExportAndIsDone(t *testing.T) {
	setup(t)
	if err := os.MkdirAll(filepath.Join(stateRoot(), "pending"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	e := readExport(t)
	if len(e.Proposals) != 0 || len(e.Rejected) != 0 || e.Proposals == nil || e.Rejected == nil {
		t.Fatalf("export = %+v", e)
	}
	if exists(stateRoot()) {
		t.Fatal("state left")
	}
}

func TestMigrateWithoutAnyReviewStateDoesNothing(t *testing.T) {
	setup(t)
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	if exists(ExportPath()) || exists(workDir()) {
		t.Fatal("a workspace that never ran the review got an export")
	}
}

// Rejected hashes with no pending proposal still travel.
func TestMigrateExportsRejectedHashesOfAnOtherwiseEmptyQueue(t *testing.T) {
	setup(t)
	put(t, "rejected/h1.json", `{"hash":"h1","scope":"project","name":"n","projectId":"p","at":"2026-10-30T10:00:00Z"}`)
	put(t, "rejected/h2.json", `{"hash":"h2","scope":"user","name":"m","at":"2026-10-31T10:00:00Z"}`)
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	e := readExport(t)
	if len(e.Proposals) != 0 || len(e.Rejected) != 2 || e.Rejected[0].Hash != "h1" || e.Rejected[0].ProjectID != "p" {
		t.Fatalf("export = %+v", e)
	}
}

func TestMigrateCountsUnreadableFilesAndCompletes(t *testing.T) {
	setup(t)
	put(t, "pending/aaaaaaaaaaaaaaaaaaaa.json", proposalA)
	put(t, "pending/broken.json", `{not json`)
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	if e := readExport(t); len(e.Proposals) != 1 || e.Unreadable != 1 {
		t.Fatalf("export = %+v", e)
	}
}

// A file of another version is not "done": while the queue exists it is replaced.
func TestMigrateRewritesAnExportOfAnotherVersion(t *testing.T) {
	setup(t)
	if err := os.MkdirAll(workDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ExportPath(), []byte(`{"version":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	put(t, "pending/aaaaaaaaaaaaaaaaaaaa.json", proposalA)
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	if e := readExport(t); e.Version != ExportVersion || len(e.Proposals) != 1 {
		t.Fatalf("export = %+v", e)
	}
}

// A second start after a finished migration is a no-op and keeps the export.
func TestMigrateRunsOnceAndAStartAfterwardsChangesNothing(t *testing.T) {
	setup(t)
	seedQueue(t)
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(ExportPath())
	if err := MigrateQueue(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(ExportPath()); string(after) != string(before) {
		t.Fatal("export changed")
	}
}

// Two migrations started together (overlapping Agent restarts) end with the complete export: the
// loser waits for the lock, finds the state gone and leaves the first export alone.
func TestMigrateConcurrentRunsKeepTheCompleteExport(t *testing.T) {
	setup(t)
	seedQueue(t)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { errs <- MigrateQueue() }()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if e := readExport(t); len(e.Proposals) != 2 || len(e.Rejected) != 1 {
		t.Fatalf("export = %+v", e)
	}
	if exists(stateRoot()) {
		t.Fatal("state left")
	}
}
