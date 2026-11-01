package opencode

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The first opencode 2 process to open a database that still holds 1.x data migrates it in place
// and imports the 1.x sessions. /api/info answers within a second, but the import keeps running
// (measured on 2.0.29: 50 conversations x 1000 messages, 253 MB, ~25 s): until it completes,
// GET /api/session/{id} answers 404 for a conversation that exists, and a create succeeds. A
// resume that trusts that 404 starts a fresh conversation and overwrites the slot's recorded
// session id. So no af-started opencode 2 process touches a database before EnsureMigrated has
// seen the migration complete.

// migrationWait bounds the wait; a var so tests can shorten it.
var migrationWait = 30 * time.Minute

// startMigrationServe starts a throwaway `opencode serve` on a free loopback port against the
// workspace's database, and returns its address, the basic-auth password, a channel that
// closes when the process exits and a stop func. A var so tests can stand in for the binary.
var startMigrationServe = func(spec LaunchSpec) (addr, password string, exited <-chan struct{}, stop func(), err error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, nil, err
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	password, err = newServePassword()
	if err != nil {
		return "", "", nil, nil, err
	}
	cmd := exec.Command(spec.Bin, "serve", "--hostname", "127.0.0.1", "--port", port)
	cmd.Env = append(withoutEnv(os.Environ(), "AF_SESSION_SID", "AF_SESSION_NAME"),
		"OPENCODE_SERVER_USERNAME="+serveUser, "OPENCODE_SERVER_PASSWORD="+password)
	cmd.Dir = paths.HomeDir()
	// The import outlives nothing: if the Agent dies mid-import the server goes too, instead of
	// running on with no owner while the next Agent starts a second import of the same database.
	// The signal follows the thread that started it, which EnsureMigrated keeps locked.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return "", "", nil, nil, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	stop = func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	return "http://127.0.0.1:" + port, password, done, stop, nil
}

func migrationLock() string {
	return filepath.Join(paths.AgentStateDir(), "opencode-v2-migration.lock")
}

// needsMigration reads the database itself, so a restored or replaced file is judged on what it
// holds and not on what an earlier look at the path found. opencode 2 records the import in
// kv.migration.v1-v2 ({"phase":"completed"}, measured on 2.0.29). Without it, a database that has
// 1.x sessions still has to be imported; a missing file, a database without a 1.x session table
// or with no session rows has nothing to import. Anything unreadable counts as "needs", and the
// throwaway server then decides.
func needsMigration(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return true
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`select count(*) from sqlite_master where type='table' and name='kv'`).Scan(&n); err != nil {
		return true
	}
	if n > 0 {
		var v string
		if err := db.QueryRow(`select value from kv where key='migration.v1-v2'`).Scan(&v); err == nil {
			var st struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal([]byte(v), &st) == nil && st.Phase == "completed" {
				return false
			}
		}
	}
	if err := db.QueryRow(`select count(*) from sqlite_master where type='table' and name='session'`).Scan(&n); err != nil {
		return true
	}
	if n == 0 {
		return false
	}
	if err := db.QueryRow(`select count(*) from (select 1 from session limit 1)`).Scan(&n); err != nil {
		return true
	}
	return n > 0
}

// MigrateInBackground imports a database that still holds 1.x data, off the boot path. History
// is read from the opencode 2 tables only, so a home whose last opencode was 1.x shows none of
// its conversations until an opencode 2 process has opened the database; doing it at Agent start
// instead of at the first launch is what keeps that window to the import itself. A database with
// nothing to import, no opencode on PATH or an opencode af refuses starts nothing.
func MigrateInBackground() {
	if !Available() || checkSupported() != nil || !needsMigration(dbPath()) {
		return
	}
	go func() {
		if err := EnsureMigrated(CurrentLaunch()); err != nil {
			log.Printf("opencode: importing the 1.x database at start: %v (the next opencode start retries)", err)
			return
		}
		log.Printf("opencode: imported the 1.x database")
	}()
}

// PrepareStart is the gate every opencode process af starts passes first: it refuses an opencode
// af does not support, then waits for the database import (EnsureMigrated).
func PrepareStart(spec LaunchSpec) error {
	if err := checkSupported(); err != nil {
		return err
	}
	return EnsureMigrated(spec)
}

// EnsureMigrated returns once the database holds no 1.x data that is still to be imported
// (immediately for a database that needs nothing). Concurrent callers, across processes, queue on
// one lock, so the import runs once and nobody opens the database while it does. A failure is
// retried by the next caller.
func EnsureMigrated(spec LaunchSpec) error {
	db := dbPath()
	if !needsMigration(db) {
		return nil
	}
	if err := os.MkdirAll(paths.AgentStateDir(), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(migrationLock(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(migrationWait)
	for {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("opencode 2 is still migrating the database (another process holds %s)", migrationLock())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !needsMigration(db) {
		return nil
	}
	// Pdeathsig (startMigrationServe) follows the OS thread that started the server, not the
	// goroutine: stay on that thread until the server is stopped, or a runtime thread exit would
	// kill the import early.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	addr, password, exited, stop, err := startMigrationServe(spec)
	if err != nil {
		return fmt.Errorf("opencode 2 database migration: %w", err)
	}
	defer stop()
	return waitMigrated(addr, password, exited, deadline)
}

// waitMigrated polls GET /api/experimental/migration/v1. The upstream status is one of required,
// running, completed or error: "completed" ends the wait, "required" and "running" keep it, and
// "error" and anything else (an empty or unknown status included) fail it, so a failed or
// unstarted import never reads as a finished one. Measured shapes on 2.0.29:
// {"status":"running","progress":{…}} and {"status":"completed"}.
func waitMigrated(addr, password string, exited <-chan struct{}, deadline time.Time) error {
	c := &http.Client{Timeout: 5 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return fmt.Errorf("opencode 2 exited during its database migration (last status %q)", last)
		default:
		}
		req, err := http.NewRequest("GET", addr+"/api/experimental/migration/v1", nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth(serveUser, password)
		if res, err := c.Do(req); err == nil {
			body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			res.Body.Close()
			if res.StatusCode == 200 {
				var st struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(body, &st)
				last = st.Status
				switch st.Status {
				case "completed":
					return nil
				case "required", "running":
				default:
					return fmt.Errorf("opencode 2 database migration did not complete: status %q (%s)", st.Status, strings.TrimSpace(string(body)))
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("opencode 2 database migration did not finish in time (last status %q)", last)
}
