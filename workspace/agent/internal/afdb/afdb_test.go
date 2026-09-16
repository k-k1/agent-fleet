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
	// reconcile on a stopped instance (PID=0, no datadir) must be a no-op.
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

func TestValidateExplicitDB(t *testing.T) {
	good := []string{"mydb", "af_test_01", "a", "_underscore", strings.Repeat("a", 63)}
	for _, n := range good {
		if err := validateExplicitDB(n); err != nil {
			t.Errorf("validateExplicitDB(%q) unexpected error: %v", n, err)
		}
	}
	bad := []string{"", "MyDB", "my-db", "1starts_with_digit", strings.Repeat("a", 64)}
	for _, n := range bad {
		if err := validateExplicitDB(n); err == nil {
			t.Errorf("validateExplicitDB(%q) expected error, got nil", n)
		}
	}
}

func TestParseMajorFromArgs(t *testing.T) {
	cases := []struct {
		args       []string
		wantMajor  int
		wantRest   []string
		wantErrNil bool
	}{
		{nil, 17, nil, true},
		{[]string{"--major=16"}, 16, nil, true},
		{[]string{"--major", "18"}, 18, nil, true},
		{[]string{"--persist", "--major=16"}, 16, []string{"--persist"}, true},
		{[]string{"--major"}, 0, nil, false},   // missing value
		{[]string{"--major=x"}, 0, nil, false}, // non-number
	}
	for _, c := range cases {
		major, rest, err := parseMajorFromArgs(c.args)
		if c.wantErrNil && err != nil {
			t.Errorf("parseMajorFromArgs(%v) unexpected error: %v", c.args, err)
			continue
		}
		if !c.wantErrNil && err == nil {
			t.Errorf("parseMajorFromArgs(%v) expected error, got nil", c.args)
			continue
		}
		if err != nil {
			continue
		}
		if major != c.wantMajor {
			t.Errorf("parseMajorFromArgs(%v) major=%d, want %d", c.args, major, c.wantMajor)
		}
		if len(rest) != len(c.wantRest) {
			t.Errorf("parseMajorFromArgs(%v) rest=%v, want %v", c.args, rest, c.wantRest)
		}
	}
}

// ---- integration tests (require AF_DB_POSTGRES_ROOT) ----

// skipIfNoBinary skips the test when AF_DB_POSTGRES_ROOT is unset or the binary is absent.
func skipIfNoBinary(t *testing.T) string {
	t.Helper()
	root := os.Getenv("AF_DB_POSTGRES_ROOT")
	if root == "" {
		t.Skip("AF_DB_POSTGRES_ROOT not set; skipping Postgres integration test")
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "postgres")); err != nil {
		t.Skipf("postgres binary not found at %s/bin/postgres: %v", root, err)
	}
	return root
}

// TestEnsureUpURLStop exercises the full ensureUp → urlFor → stopInstance path.
// This is the path the acceptance criterion exercises end to end.
func TestEnsureUpURLStop(t *testing.T) {
	root := skipIfNoBinary(t)

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("AF_DB_POSTGRES_ROOT", root)
	// Ensure AF_WS_SCRATCH is not set so the datadir lands in home (the temp dir).
	t.Setenv("AF_WS_SCRATCH", "")

	inst, err := ensureUp("postgres", 17, false)
	if err != nil {
		t.Fatalf("ensureUp: %v", err)
	}
	t.Cleanup(func() {
		// stopInstance with purge removes the datadir AFTER pg_ctl stop.
		if err := stopInstance("postgres", 17, true); err != nil {
			t.Logf("stopInstance cleanup: %v", err)
		}
	})

	if !isPGRunning(inst.PID, inst.Datadir) {
		t.Fatal("server is not running after ensureUp")
	}

	// urlFor creates a database and returns a usable URL.
	url, err := urlFor("postgres", 17, "", false)
	if err != nil {
		t.Fatalf("urlFor: %v", err)
	}
	if url == "" {
		t.Fatal("urlFor returned empty URL")
	}

	// Connect to the database and run a basic query.
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
		conn.Close(ctx)
		t.Fatalf("SELECT 1: %v", err)
	}
	conn.Close(ctx)

	// Second call to urlFor must be idempotent (same URL, database already exists).
	url2, err := urlFor("postgres", 17, "", false)
	if err != nil {
		t.Fatalf("urlFor idempotent: %v", err)
	}
	if url2 != url {
		t.Errorf("urlFor not idempotent: first=%q second=%q", url, url2)
	}

	// CountClientBackends must not count its own connection.
	n2 := CountClientBackends(inst)
	if n2 != 0 {
		t.Errorf("CountClientBackends with no real clients: got %d, want 0", n2)
	}

	// Concurrent ensureUp calls must not race (both should return cleanly).
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := ensureUp("postgres", 17, false)
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent ensureUp: %v", err)
		}
	}
}

// TestPostgresIntegration exercises initdb + start + database operations + reconcile
// with more granular assertions.
func TestPostgresIntegration(t *testing.T) {
	root := skipIfNoBinary(t)
	binDir := filepath.Join(root, "bin")

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("AF_DB_POSTGRES_ROOT", root)
	t.Setenv("AF_WS_SCRATCH", "")

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

	// initdb with scram-sha-256.
	initArgs := []string{
		"--auth=scram-sha-256", "--auth-local=scram-sha-256",
		"-U", "postgres",
		fmt.Sprintf("--pwfile=%s", pp),
		"-D", datadir,
	}
	if out, err := runCmd(filepath.Join(binDir, "initdb"), initArgs...); err != nil {
		t.Fatalf("initdb: %v\n%s", err, out)
	}

	port, err := pickPort()
	if err != nil {
		t.Fatalf("pickPort: %v", err)
	}

	serverFlags := fmt.Sprintf(
		"-k %s -h 127.0.0.1 -p %d -c shared_buffers=32MB -c max_connections=50 -c fsync=off",
		sockdir, port)
	startArgs := []string{"-w", "start", "-D", datadir, "-l", logFile, "-o", serverFlags}
	if out, err := runCmd(filepath.Join(binDir, "pg_ctl"), startArgs...); err != nil {
		logContents, _ := os.ReadFile(logFile)
		t.Fatalf("pg_ctl start: %v\n%s\nlog:\n%s", err, out, logContents)
	}

	pid, err := readPostmasterPID(datadir)
	if err != nil {
		t.Fatalf("readPostmasterPID: %v", err)
	}

	t.Cleanup(func() {
		runCmd(filepath.Join(binDir, "pg_ctl"), "stop", "-m", "fast", "-D", datadir) //nolint:errcheck
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

	// ensureDatabase idempotency.
	testDB := fmt.Sprintf("af_inttest_%d", port%10000)
	if err := ensureDatabase(connStr, testDB); err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if err := ensureDatabase(connStr, testDB); err != nil {
		t.Fatalf("ensureDatabase idempotent: %v", err)
	}

	// Connect to the new database.
	dbURL := fmt.Sprintf("postgres://postgres:%s@/%s?host=%s&port=%d&sslmode=disable",
		pw, testDB, sockdir, port)
	dbConn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to %s: %v", testDB, err)
	}
	var one int
	if err := dbConn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
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
		Datadir: datadir,
		Databases: map[string]string{
			testDB:               existDir,
			goneDB:               goneDir, // does not exist → should be dropped
			"af_shared_explicit": "",      // explicitly named → never dropped
		},
	}

	_ = withLock(func() error {
		r := &registry{Instances: map[string]*Instance{instanceKey("postgres", major): inst}}
		return writeRegistry(r)
	})

	if err := reconcile(inst, pw); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// goneDB should be removed from the registry.
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

	// Verify goneDB is actually gone from Postgres.
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

// ---- helpers ----

func runCmd(bin string, args ...string) ([]byte, error) {
	return exec.Command(bin, args...).CombinedOutput()
}
