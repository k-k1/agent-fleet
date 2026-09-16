package afdb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- unit tests (no Postgres required) ----

func TestDBNameFor(t *testing.T) {
	cases := []string{
		"/home/dev/repos/agent-fleet",
		"/home/dev/MyRepo",
		"/home/dev/aaaaaaaaaa-bbbbbbbbbb-cccccccccc-dddddddddd-eeeee",
	}
	for _, dir := range cases {
		name := DBNameFor(dir)
		if !strings.HasPrefix(name, "af_") {
			t.Errorf("DBNameFor(%q) = %q: missing af_ prefix", dir, name)
		}
		if len(name) > 50 {
			t.Errorf("DBNameFor(%q) = %q: too long (%d > 50)", dir, name, len(name))
		}
		for _, ch := range name {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_') {
				t.Errorf("DBNameFor(%q) = %q: illegal char %q", dir, name, ch)
			}
		}
		if DBNameFor(dir) != name {
			t.Errorf("DBNameFor(%q) not deterministic", dir)
		}
	}
	n1 := DBNameFor("/home/dev/repo1")
	n2 := DBNameFor("/home/dev/repo2")
	if n1 == n2 {
		t.Errorf("DBNameFor collision: repo1 and repo2 both give %q", n1)
	}
}

func TestResolveDirFallback(t *testing.T) {
	dir := ResolveDir()
	if dir == "" {
		t.Error("ResolveDir returned empty string")
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	inst := &Instance{
		Engine:    "postgres",
		Major:     17,
		Root:      filepath.Join(tmp, "root"),
		Datadir:   filepath.Join(tmp, "data"),
		Sockdir:   filepath.Join(tmp, "sock"),
		Port:      5555,
		PID:       0,
		StartedAt: time.Now().Truncate(time.Second),
		Databases: map[string]string{"af_test_aabbcc": "/some/dir"},
	}

	if err := withLock(func() error {
		r := &registry{Instances: map[string]*Instance{instanceKey("postgres", 17): inst}}
		return writeRegistry(r)
	}); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	var got *Instance
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		got = r.Instances[instanceKey("postgres", 17)]
		return nil
	}); err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if got == nil {
		t.Fatal("instance not found after write")
	}
	if got.Port != inst.Port {
		t.Errorf("Port: got %d, want %d", got.Port, inst.Port)
	}
	if got.Databases["af_test_aabbcc"] != "/some/dir" {
		t.Errorf("Databases: got %v", got.Databases)
	}
}

func TestReconcileStoppedInstanceNoOp(t *testing.T) {
	// reconcile on a stopped instance (PID=0) must be a no-op.
	inst := &Instance{
		Engine: "postgres",
		Major:  17,
		PID:    0,
		Databases: map[string]string{
			"af_existing_aa": t.TempDir(),
			"af_gone_bb":     filepath.Join(t.TempDir(), "nonexistent"),
			"af_shared_cc":   "",
		},
	}
	if err := reconcile(inst, "pw"); err != nil {
		t.Fatalf("reconcile on stopped instance: %v", err)
	}
	if len(inst.Databases) != 3 {
		t.Errorf("reconcile changed stopped instance databases: %v", inst.Databases)
	}
}

// ---- integration test (requires AF_DB_POSTGRES_ROOT) ----

func TestPostgresIntegration(t *testing.T) {
	root := os.Getenv("AF_DB_POSTGRES_ROOT")
	if root == "" {
		t.Skip("AF_DB_POSTGRES_ROOT not set; skipping Postgres integration test")
	}
	binDir := filepath.Join(root, "bin")
	if _, err := os.Stat(filepath.Join(binDir, "postgres")); err != nil {
		t.Skipf("postgres binary not found at %s: %v", binDir, err)
	}

	// Redirect home so we don't touch the real registry.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("AF_DB_POSTGRES_ROOT", root)

	_ = os.MkdirAll(filepath.Join(tmp, ".config", "agent-fleet", "af-db"), 0o700)

	major := 17
	pp := passPath(major)
	pw, err := generatePass(pp)
	if err != nil {
		t.Fatalf("generatePass: %v", err)
	}

	datadir := filepath.Join(tmp, "pgdata")
	sockdir := filepath.Join(tmp, "pgsock")
	logFile := filepath.Join(tmp, "pg.log")
	_ = os.MkdirAll(datadir, 0o700)
	_ = os.MkdirAll(sockdir, 0o700)

	// Run initdb with scram-sha-256.
	initCmd := exec.Command(filepath.Join(binDir, "initdb"),
		"--auth=scram-sha-256", "--auth-local=scram-sha-256",
		"-U", "postgres", fmt.Sprintf("--pwfile=%s", pp), "-D", datadir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("initdb failed: %v\n%s", err, out)
	}

	port, err := pickPort()
	if err != nil {
		t.Fatalf("pickPort: %v", err)
	}

	// Start postgres.
	serverFlags := fmt.Sprintf(
		"-k %s -h 127.0.0.1 -p %d -c shared_buffers=32MB -c max_connections=50 -c fsync=off",
		sockdir, port)
	startCmd := exec.Command(filepath.Join(binDir, "pg_ctl"),
		"-w", "start", "-D", datadir, "-l", logFile, "-o", serverFlags)
	if out, err := startCmd.CombinedOutput(); err != nil {
		logContents, _ := os.ReadFile(logFile)
		t.Fatalf("pg_ctl start: %v\n%s\nlog:\n%s", err, out, logContents)
	}

	// Read postmaster PID.
	pid, err := readPostmasterPID(datadir)
	if err != nil {
		t.Fatalf("readPostmasterPID: %v", err)
	}

	// Always stop in cleanup, BEFORE any datadir mutations.
	t.Cleanup(func() {
		stopCmd := exec.Command(filepath.Join(binDir, "pg_ctl"),
			"stop", "-m", "fast", "-D", datadir)
		stopCmd.CombinedOutput() //nolint:errcheck
		waitPIDGone(pid, 10*time.Second)
	})

	connStr := fmt.Sprintf("postgres://postgres:%s@/postgres?host=%s&port=%d&sslmode=disable",
		pw, sockdir, port)

	// Basic connectivity.
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		logContents, _ := os.ReadFile(logFile)
		t.Fatalf("connect: %v\nlog:\n%s", err, logContents)
	}
	conn.Close(ctx)

	// CREATE DATABASE round-trip (idempotent).
	testDB := "af_inttest_" + fmt.Sprintf("%d", port%10000)
	if err := ensureDatabase(connStr, testDB); err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if err := ensureDatabase(connStr, testDB); err != nil {
		t.Fatalf("ensureDatabase idempotent: %v", err)
	}

	// Connect to the new database and run a query.
	dbURL := fmt.Sprintf("postgres://postgres:%s@/%s?host=%s&port=%d&sslmode=disable", pw, testDB, sockdir, port)
	dbConn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to %s: %v", testDB, err)
	}
	var n int
	if err := dbConn.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
		dbConn.Close(ctx)
		t.Fatalf("SELECT 1: %v", err)
	}
	dbConn.Close(ctx)

	// DBNameFor: verify format.
	dirName := DBNameFor(tmp)
	if !strings.HasPrefix(dirName, "af_") {
		t.Errorf("DBNameFor(%q) = %q: missing prefix", tmp, dirName)
	}

	// Reconcile: one database with an existing dir (stays), one with a gone dir (dropped).
	existDir := t.TempDir()
	goneDir := filepath.Join(tmp, "nonexistent-repo-zz99")

	// Create both databases so DROP won't error.
	goneDB := "af_gone_zz99aa"
	if err := ensureDatabase(connStr, testDB); err != nil {
		t.Fatalf("pre-reconcile create testDB: %v", err)
	}
	if err := ensureDatabase(connStr, goneDB); err != nil {
		t.Fatalf("pre-reconcile create goneDB: %v", err)
	}

	inst := &Instance{
		Engine:  "postgres",
		Major:   major,
		Root:    root,
		Sockdir: sockdir,
		Port:    port,
		PID:     pid,
		Databases: map[string]string{
			testDB:               existDir,
			goneDB:               goneDir, // directory does not exist → should be dropped
			"af_shared_explicit": "",      // explicitly named → never dropped
		},
	}
	// Write to temp registry.
	_ = withLock(func() error {
		r := &registry{Instances: map[string]*Instance{instanceKey("postgres", major): inst}}
		return writeRegistry(r)
	})

	if err := reconcile(inst, pw); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// goneDB should be gone from registry.
	_ = withLock(func() error {
		r, _ := readRegistry()
		if i, ok := r.Instances[instanceKey("postgres", major)]; ok {
			if _, found := i.Databases[goneDB]; found {
				t.Errorf("reconcile did not remove %q from registry", goneDB)
			}
			if _, found := i.Databases[testDB]; !found {
				t.Errorf("reconcile incorrectly removed %q (dir exists)", testDB)
			}
			if _, found := i.Databases["af_shared_explicit"]; !found {
				t.Errorf("reconcile incorrectly removed explicitly-named database")
			}
		}
		return nil
	})

	// Verify goneDB is actually gone from postgres too.
	conn2, err := pgx.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect for verification: %v", err)
	}
	defer conn2.Close(ctx)
	var dbExists bool
	_ = conn2.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", goneDB,
	).Scan(&dbExists)
	if dbExists {
		t.Errorf("goneDB %q still exists in Postgres after reconcile", goneDB)
	}
}
