package opencode

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// seedDB writes a database the way opencode leaves it: v1 with one session, or (kvDone) already
// imported by opencode 2, which records {"phase":"completed"} under kv.migration.v1-v2.
func seedDB(t *testing.T, path string, sessions int, kvDone bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{`create table session (id text primary key)`}
	for i := 0; i < sessions; i++ {
		stmts = append(stmts, `insert into session values ('ses_`+string(rune('a'+i))+`')`)
	}
	if kvDone {
		stmts = append(stmts, `create table kv (key text primary key, value text, a int, b int)`,
			`insert into kv values ('migration.v1-v2','{"phase":"completed"}',1,1)`)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeMigration answers the migration status like opencode 2.0.29: a script of statuses, then
// "completed". Completing writes kv.migration.v1-v2 into the database, as the real import does.
type fakeMigration struct {
	script  []string // statuses answered before "completed", one per poll
	final   string   // replaces "completed" when set
	polls   atomic.Int32
	started atomic.Int32
	stopped atomic.Int32
	done    atomic.Bool
}

func (f *fakeMigration) recordImport(dbp string) error {
	db, err := sql.Open("sqlite", "file:"+dbp+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`create table if not exists kv (key text primary key, value text, a int, b int)`); err != nil {
		return err
	}
	_, err = db.Exec(`insert or replace into kv values ('migration.v1-v2','{"phase":"completed"}',1,1)`)
	return err
}

func useFakeMigration(t *testing.T, dbp string, f *fakeMigration) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pw, ok := r.BasicAuth(); !ok || pw != "pw" || r.URL.Path != "/api/experimental/migration/v1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		n := int(f.polls.Add(1)) - 1
		if n < len(f.script) {
			_, _ = w.Write([]byte(f.script[n]))
			return
		}
		if f.final != "" {
			_, _ = w.Write([]byte(f.final))
			return
		}
		// Record the import before answering "completed", as the real one does. The write waits
		// out a reader (needsMigration of another caller holds a shared lock for a moment) and a
		// failure fails the request: a dropped write left the database reading as not imported,
		// so the next caller started a second import (SQLITE_BUSY, measured under -race).
		if err := f.recordImport(dbp); err != nil {
			t.Errorf("fake import could not record completion: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.done.Store(true)
		_, _ = w.Write([]byte(`{"status":"completed"}`))
	}))
	t.Cleanup(srv.Close)
	old := startMigrationServe
	startMigrationServe = func(LaunchSpec) (string, string, <-chan struct{}, func(), error) {
		f.started.Add(1)
		return srv.URL, "pw", nil, func() { f.stopped.Add(1) }, nil
	}
	t.Cleanup(func() { startMigrationServe = old })
}

const running = `{"status":"running","progress":{"label":"Migrating sessions","numerator":1,"denominator":50}}`

var spec2 = LaunchSpec{Bin: "opencode"}

// The defect: a resume asked an opencode 2 whose import was still running, got a 404 for a
// conversation that exists and started a new one. EnsureMigrated must not return before the
// status says "completed".
func TestEnsureMigratedWaitsForTheImport(t *testing.T) {
	isolatedHome(t)
	db := dbPath()
	seedDB(t, db, 2, false)
	f := &fakeMigration{script: []string{"{\"status\":\"required\"}", running, running}}
	useFakeMigration(t, db, f)
	if err := EnsureMigrated(spec2); err != nil {
		t.Fatal(err)
	}
	if !f.done.Load() || f.started.Load() != 1 || f.stopped.Load() != 1 {
		t.Fatalf("done %v started %d stopped %d", f.done.Load(), f.started.Load(), f.stopped.Load())
	}
	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 1 {
		t.Fatalf("second call: err %v started %d; the database says it is imported", err, f.started.Load())
	}
}

// Only "completed" is success. "error" is an import that failed, "required" an import that has
// not started (it keeps waiting, and times out), and an empty or unknown status is a server this
// code does not understand: none of them may let a caller open the database.
func TestEnsureMigratedOnlyCompletedIsSuccess(t *testing.T) {
	for _, tc := range []struct{ name, status, want string }{
		{"error", `{"status":"error","message":"disk full"}`, "disk full"},
		{"empty", `{}`, `status ""`},
		{"unknown", `{"status":"weird"}`, "weird"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedHome(t)
			db := dbPath()
			seedDB(t, db, 1, false)
			useFakeMigration(t, db, &fakeMigration{final: tc.status})
			err := EnsureMigrated(spec2)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want a failure naming %q", err, tc.want)
			}
			if !needsMigration(db) {
				t.Fatal("the database reads as imported after a failed import")
			}
		})
	}
	t.Run("required never starts", func(t *testing.T) {
		isolatedHome(t)
		db := dbPath()
		seedDB(t, db, 1, false)
		useFakeMigration(t, db, &fakeMigration{final: `{"status":"required"}`})
		old := migrationWait
		migrationWait = 1200 * time.Millisecond
		t.Cleanup(func() { migrationWait = old })
		if err := EnsureMigrated(spec2); err == nil {
			t.Fatal("a migration that is only ever 'required' returned nil")
		}
	})
}

// The database decides, not a record of an earlier look at its path: restoring a pre-upgrade copy
// over an imported one (the documented rollback) makes the next start import it again.
func TestEnsureMigratedFollowsTheDatabaseContent(t *testing.T) {
	isolatedHome(t)
	db := dbPath()
	f := &fakeMigration{}
	useFakeMigration(t, db, f)

	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 0 {
		t.Fatalf("no database: err %v started %d, want nothing to do", err, f.started.Load())
	}
	seedDB(t, db, 0, false)
	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 0 {
		t.Fatalf("a database with no sessions: err %v started %d, want nothing to do", err, f.started.Load())
	}
	seedDB(t, db, 2, false)
	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 1 {
		t.Fatalf("1.x sessions: err %v started %d, want one import", err, f.started.Load())
	}
	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 1 {
		t.Fatalf("imported: err %v started %d, want none", err, f.started.Load())
	}
	seedDB(t, db, 2, false) // the pre-upgrade copy is put back
	f.polls.Store(0)
	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 2 {
		t.Fatalf("restored 1.x copy: err %v started %d, want a second import", err, f.started.Load())
	}
	seedDB(t, db, 2, true)
	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 2 {
		t.Fatalf("kv says completed: err %v started %d", err, f.started.Load())
	}
}

// An opencode 1 on PATH is refused before anything starts: no import, and the error names it.
func TestPrepareStartRefusesOpencode1(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 1)
	db := dbPath()
	seedDB(t, db, 2, false)
	f := &fakeMigration{}
	useFakeMigration(t, db, f)
	err := PrepareStart(LaunchSpec{Bin: "opencode"})
	if err == nil || !strings.Contains(err.Error(), "1.19.0") {
		t.Fatalf("err = %v, want a refusal naming 1.19.0", err)
	}
	if f.started.Load() != 0 {
		t.Fatal("the import started for an opencode af refuses")
	}
}

// A binary whose major cannot be read (a wrapper without its manifest, no pin) is let through.
func TestPrepareStartLetsAnUnknownMajorThrough(t *testing.T) {
	isolatedHome(t)
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	useOpencode(t, bin)
	if err := PrepareStart(LaunchSpec{Bin: "opencode"}); err != nil {
		t.Fatalf("err = %v, want nil for an unknown major", err)
	}
}

// Concurrent first starts (the serve, a chat run, a models listing) share one import.
func TestEnsureMigratedSerialisesConcurrentCallers(t *testing.T) {
	isolatedHome(t)
	db := dbPath()
	seedDB(t, db, 2, false)
	f := &fakeMigration{script: []string{running, running, running, running}}
	useFakeMigration(t, db, f)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := EnsureMigrated(spec2); err != nil {
				t.Error(err)
			} else if !f.done.Load() {
				t.Error("a caller returned before the import finished")
			}
		}()
	}
	wg.Wait()
	if f.started.Load() != 1 {
		t.Fatalf("%d throwaway servers started for five callers, want 1", f.started.Load())
	}
}

func TestEnsureMigratedTimeoutFails(t *testing.T) {
	isolatedHome(t)
	db := dbPath()
	seedDB(t, db, 1, false)
	useFakeMigration(t, db, &fakeMigration{final: running})
	old := migrationWait
	migrationWait = 1500 * time.Millisecond
	t.Cleanup(func() { migrationWait = old })
	if err := EnsureMigrated(spec2); err == nil {
		t.Fatal("a migration that never completes returned nil")
	}
}

// A throwaway server that dies (a broken install, a port taken) fails the caller at once instead
// of holding it for the whole wait.
func TestEnsureMigratedFailsFastWhenTheServerExits(t *testing.T) {
	isolatedHome(t)
	seedDB(t, dbPath(), 1, false)
	old := startMigrationServe
	t.Cleanup(func() { startMigrationServe = old })
	startMigrationServe = func(LaunchSpec) (string, string, <-chan struct{}, func(), error) {
		c := make(chan struct{})
		close(c)
		return "http://127.0.0.1:1", "pw", c, func() {}, nil
	}
	start := time.Now()
	if err := EnsureMigrated(spec2); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %v, want an immediate failure", err, time.Since(start))
	}
}

// A reader on the database while the import records its completion (another caller's
// needsMigration, for instance) must delay the record, not drop it: a dropped record read as
// "not imported" and started a second import.
func TestEnsureMigratedRecordsCompletionDespiteAReader(t *testing.T) {
	isolatedHome(t)
	db := dbPath()
	seedDB(t, db, 1, false)
	rd, err := sql.Open("sqlite", "file:"+db)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	tx, err := rd.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`select count(*) from session`).Scan(&n); err != nil { // takes the shared lock
		t.Fatal(err)
	}
	go func() { time.Sleep(700 * time.Millisecond); _ = tx.Rollback() }()
	f := &fakeMigration{}
	useFakeMigration(t, db, f)
	if err := EnsureMigrated(spec2); err != nil {
		t.Fatal(err)
	}
	if err := EnsureMigrated(spec2); err != nil || f.started.Load() != 1 {
		t.Fatalf("second call: err %v started %d; the completion must have been recorded", err, f.started.Load())
	}
}
