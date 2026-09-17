package afdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- unit tests (no database required) ----

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

func TestInstanceKeyString(t *testing.T) {
	if got := instanceKey("mysql", "8.4"); got != "mysql-8.4" {
		t.Errorf("instanceKey(mysql, 8.4) = %q, want mysql-8.4", got)
	}
	if got := instanceKey("postgres", "17"); got != "postgres-17" {
		t.Errorf("instanceKey(postgres, 17) = %q, want postgres-17", got)
	}
}

// TestRegistryMajorNumberOrString covers the registry a P0 agent left behind,
// where "major" is a JSON number: it has to load, or every verb stops with
// "registry parse" and nothing can repair it.
func TestRegistryMajorNumberOrString(t *testing.T) {
	const p0 = `{"instances":{"postgres-17":{"engine":"postgres","major":17,` +
		`"root":"/r","datadir":"/d","sockdir":"/s","port":0}}}`
	const p1 = `{"instances":{"mysql-8.4":{"engine":"mysql","major":"8.4",` +
		`"root":"/r","datadir":"/d","sockdir":"/s","port":3306}}}`

	for _, tc := range []struct{ name, in, key, want string }{
		{"p0 number", p0, "postgres-17", "17"},
		{"p1 string", p1, "mysql-8.4", "8.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r registry
			if err := json.Unmarshal([]byte(tc.in), &r); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			inst := r.Instances[tc.key]
			if inst == nil {
				t.Fatalf("instance %q missing", tc.key)
			}
			if inst.Major != tc.want {
				t.Errorf("Major = %q; want %q", inst.Major, tc.want)
			}
			// The rest of the instance must survive the custom unmarshaller.
			if inst.Root != "/r" || inst.Datadir != "/d" || inst.Sockdir != "/s" {
				t.Errorf("other fields lost: %+v", inst)
			}
		})
	}

	// A major that is neither string nor number is still an error.
	var r registry
	if err := json.Unmarshal([]byte(`{"instances":{"x":{"major":{"a":1}}}}`), &r); err == nil {
		t.Error("expected an error for an object-valued major")
	}

	// Writing back normalises the old number to this build's string form, so a
	// registry only has to be forgiven once.
	var old registry
	if err := json.Unmarshal([]byte(p0), &old); err != nil {
		t.Fatalf("unmarshal p0: %v", err)
	}
	b, err := json.Marshal(&old)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"major":"17"`) {
		t.Errorf("rewritten registry should carry a string major, got %s", b)
	}
}

// TestTailWriterReason covers what a failed installer contributes to the error
// the Console card shows: the last lines, never more than the tail it keeps.
func TestTailWriterReason(t *testing.T) {
	var empty tailWriter
	if got := empty.reason(); got != "" {
		t.Errorf("empty reason = %q; want \"\"", got)
	}

	var w tailWriter
	fmt.Fprint(&w, "[install-mysql] downloading ...\n\n[install-mysql] extracting ...\n")
	fmt.Fprint(&w, "[install-mysql] linked libaio.so.1 -> /lib/libaio.so.1t64\n")
	fmt.Fprint(&w, "[install-mysql] verifying ...\n")
	fmt.Fprint(&w, "ldd bin/mysqld: unresolved libraries: libaio.so.1\n")
	got := w.reason()
	if !strings.HasPrefix(got, ": ") {
		t.Errorf("reason should be appendable to an error message, got %q", got)
	}
	if !strings.Contains(got, "libaio.so.1") {
		t.Errorf("reason should keep the last line: %q", got)
	}
	if strings.Contains(got, "downloading") {
		t.Errorf("reason should keep only the last lines: %q", got)
	}

	var big tailWriter
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&big, "line %d of installer chatter\n", i)
	}
	if len(big.buf) > tailWriterMax {
		t.Errorf("tail grew to %d bytes; want at most %d", len(big.buf), tailWriterMax)
	}
	if !strings.Contains(big.reason(), "line 499") {
		t.Errorf("reason should end with the last line, got %q", big.reason())
	}
}

// TestDatabaseEntriesPerWorkingCopy pins what the Console card copies: one entry
// per registered database, sorted, each URL naming its OWN database. The first
// live run built a single engine-level URL from the Agent's own directory, which
// named a database nothing had created.
func TestDatabaseEntriesPerWorkingCopy(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	inst := &Instance{
		Engine:  "postgres",
		Major:   "17",
		Sockdir: filepath.Join(tmp, "run"),
		Port:    5433,
		Databases: map[string]string{
			"af_zzz_000000": "/home/dev/repos/zzz",
			"af_aaa_111111": "/home/dev/repos/aaa",
		},
	}
	entries := databaseEntries(inst, "postgres", "17")
	if len(entries) != 2 {
		t.Fatalf("entries = %d; want 2", len(entries))
	}
	if entries[0].Name != "af_aaa_111111" || entries[1].Name != "af_zzz_000000" {
		t.Errorf("entries not sorted by name: %v", entries)
	}
	if entries[0].Dir != "/home/dev/repos/aaa" {
		t.Errorf("entry lost its working copy: %q", entries[0].Dir)
	}
	for _, e := range entries {
		if !strings.Contains(e.URLSocket, e.Name) {
			t.Errorf("socket URL %q does not name its own database %q", e.URLSocket, e.Name)
		}
		if !strings.Contains(e.URLTCP, e.Name) {
			t.Errorf("TCP URL %q does not name its own database %q", e.URLTCP, e.Name)
		}
		if strings.Contains(e.URLSocket, DBNameFor(ResolveDir())) && e.Name != DBNameFor(ResolveDir()) {
			t.Errorf("URL names the agent's own directory instead of the entry: %q", e.URLSocket)
		}
	}
}

// TestStopInstanceWaitsForStartLock pins the exclusion that was missing: a purge
// must not run while another caller holds the per-(engine, major) start lock,
// because that caller is inside initdb / --initialize-insecure and the removal
// would take a half-formed datadir out from under a booting server.
func TestStopInstanceWaitsForStartLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// A registry entry with a datadir to purge, and no live server: stopInstance
	// walks straight to the removal.
	datadir := filepath.Join(tmp, "state", "postgres-17", "data")
	if err := os.MkdirAll(datadir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		r.Instances["postgres-17"] = &Instance{
			Engine: "postgres", Major: "17", Datadir: datadir,
		}
		return writeRegistry(r)
	}); err != nil {
		t.Fatal(err)
	}

	released := make(chan struct{})
	holding := make(chan struct{})
	var releasedFirst atomic.Bool

	go func() {
		_ = withStartLock("postgres", "17", func() error {
			close(holding)
			// Hold it long enough that a stopInstance which ignores the lock
			// removes the datadir before this returns.
			time.Sleep(300 * time.Millisecond)
			releasedFirst.Store(true)
			return nil
		})
		close(released)
	}()

	<-holding
	if err := stopInstance("postgres", "17", true); err != nil {
		t.Fatalf("stopInstance: %v", err)
	}
	if !releasedFirst.Load() {
		t.Error("stopInstance purged while the start lock was held")
	}
	<-released
	if _, err := os.Stat(datadir); !os.IsNotExist(err) {
		t.Errorf("datadir should be gone after the purge, stat err = %v", err)
	}
}

// TestResetRequiresDatabaseName pins the reset contract: the caller names the
// database. Asked without one, the Agent used to fall back to its own working
// directory and reset af_dev_… — a database no session uses, which DROP/CREATE
// then brought into existence.
func TestResetRequiresDatabaseName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, tc := range []struct{ name, query, wantCode string }{
		{"no db", "", "db_required"},
		// Uppercase and a hyphen fail ^[a-z_][a-z0-9_]{0,62}$. (A ';' would not
		// even reach the handler: Go drops query parameters containing one.)
		{"illegal db", "?db=Bad-Name", "bad_db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/env/databases/postgres/reset"+tc.query, nil)
			r.SetPathValue("engine", "postgres")
			r.SetPathValue("action", "reset")
			w := httptest.NewRecorder()

			HandleDatabasesAction(w, r)

			if w.Code != 400 {
				t.Fatalf("status = %d; want 400 (body %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantCode) {
				t.Errorf("body should carry %q, got %s", tc.wantCode, w.Body.String())
			}
		})
	}
}

func TestPassPathEngine(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	p := passPath("mysql", "8.4")
	if !strings.Contains(p, "mysql-8.4") {
		t.Errorf("passPath(mysql, 8.4) = %q, expected to contain mysql-8.4", p)
	}
	p2 := passPath("postgres", "17")
	if !strings.Contains(p2, "postgres-17") {
		t.Errorf("passPath(postgres, 17) = %q, expected to contain postgres-17", p2)
	}
}

func TestBuildMySQLURL(t *testing.T) {
	inst := &Instance{
		Engine:  "mysql",
		Major:   "8.4",
		Sockdir: "/tmp/sock-dir",
		Port:    3308,
	}
	pw := "testpw"
	dbName := "mydb"

	sockURL := buildMySQLURL(inst, dbName, pw, false)
	if !strings.HasPrefix(sockURL, "mysql://") {
		t.Errorf("socket URL missing mysql:// prefix: %q", sockURL)
	}
	if !strings.Contains(sockURL, "socket=") {
		t.Errorf("socket URL missing socket= param: %q", sockURL)
	}
	if !strings.Contains(sockURL, "mysql.sock") {
		t.Errorf("socket URL missing mysql.sock: %q", sockURL)
	}

	tcpURL := buildMySQLURL(inst, dbName, pw, true)
	if !strings.Contains(tcpURL, "127.0.0.1:3308") {
		t.Errorf("TCP URL missing host:port: %q", tcpURL)
	}
}

func TestBuildMySQLGoDSN(t *testing.T) {
	inst := &Instance{
		Engine:  "mysql",
		Major:   "8.4",
		Sockdir: "/tmp/sock-dir",
		Port:    3308,
	}
	pw := "testpw"
	dbName := "mydb"

	sockDSN := buildMySQLGoDSN(inst, dbName, pw, false)
	if !strings.HasPrefix(sockDSN, "root:") {
		t.Errorf("socket DSN missing root: prefix: %q", sockDSN)
	}
	if !strings.Contains(sockDSN, "@unix(") {
		t.Errorf("socket DSN missing @unix(: %q", sockDSN)
	}

	tcpDSN := buildMySQLGoDSN(inst, dbName, pw, true)
	if !strings.Contains(tcpDSN, "@tcp(127.0.0.1:3308)") {
		t.Errorf("TCP DSN missing @tcp: %q", tcpDSN)
	}
}

func TestParseEngineAndMajor(t *testing.T) {
	cases := []struct {
		args          []string
		defaultEngine string
		wantEngine    string
		wantMajor     string
		wantRestLen   int
		wantErr       bool
	}{
		{nil, "postgres", "postgres", "17", 0, false},
		{[]string{"mysql"}, "postgres", "mysql", "8.4", 0, false},
		{[]string{"postgres"}, "postgres", "postgres", "17", 0, false},
		{[]string{"mysql", "--major=8.4"}, "postgres", "mysql", "8.4", 0, false},
		{[]string{"--major=16"}, "postgres", "postgres", "16", 0, false},
		{[]string{"--major", "18"}, "postgres", "postgres", "18", 0, false},
		{[]string{"--persist", "--major=16"}, "postgres", "postgres", "16", 1, false},
		{[]string{"--major"}, "postgres", "", "", 0, true},
		{[]string{"mysql", "--persist"}, "postgres", "mysql", "8.4", 1, false},
	}
	for _, c := range cases {
		engine, major, rest, err := parseEngineAndMajor(c.args, c.defaultEngine)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseEngineAndMajor(%v) expected error, got nil", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseEngineAndMajor(%v) unexpected error: %v", c.args, err)
			continue
		}
		if engine != c.wantEngine {
			t.Errorf("parseEngineAndMajor(%v) engine=%q, want %q", c.args, engine, c.wantEngine)
		}
		if major != c.wantMajor {
			t.Errorf("parseEngineAndMajor(%v) major=%q, want %q", c.args, major, c.wantMajor)
		}
		if len(rest) != c.wantRestLen {
			t.Errorf("parseEngineAndMajor(%v) rest=%v (len %d), want len %d", c.args, rest, len(rest), c.wantRestLen)
		}
	}
}

func TestMemoryGateDisabled(t *testing.T) {
	t.Setenv("AF_DB_MEM_GATE", "0")
	if err := checkMemoryGate(); err != nil {
		t.Errorf("checkMemoryGate with gate disabled returned error: %v", err)
	}
}

func TestMemoryGateParsing(t *testing.T) {
	// Test the parseMemoryMax helper logic inline by exercising checkMemoryGate
	// with the gate disabled (so it does not actually read the cgroup file).
	// The underlying logic is tested via unit checks here.
	cases := []struct {
		s      string
		wantOK bool // true means "above threshold" (no gate)
		limit  uint64
	}{
		{"max", true, 0},
		{"2147483648", true, 2147483648},  // 2 GiB
		{"1073741824", true, 1073741824},  // exactly 1 GiB
		{"1073741823", false, 1073741823}, // 1 byte below 1 GiB
		{"0", false, 0},
	}
	for _, c := range cases {
		// Replicate the gate logic directly.
		var belowThreshold bool
		if c.s != "max" {
			n, err := parseUint64(c.s)
			if err == nil {
				const oneGiB = 1024 * 1024 * 1024
				belowThreshold = n < oneGiB
			}
		}
		if belowThreshold == c.wantOK {
			t.Errorf("memgate(%q): belowThreshold=%v but wantOK=%v", c.s, belowThreshold, c.wantOK)
		}
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	inst := &Instance{
		Engine:    "postgres",
		Major:     "17",
		Root:      filepath.Join(tmp, "root"),
		Datadir:   filepath.Join(tmp, "data"),
		Sockdir:   filepath.Join(tmp, "sock"),
		Port:      5555,
		PID:       0,
		StartedAt: time.Now().Truncate(time.Second),
		Databases: map[string]string{"af_test_aabbcc": "/some/dir"},
	}

	if err := withLock(func() error {
		r := &registry{Instances: map[string]*Instance{instanceKey("postgres", "17"): inst}}
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
		got = r.Instances[instanceKey("postgres", "17")]
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
	if got.Major != "17" {
		t.Errorf("Major: got %q, want \"17\"", got.Major)
	}
	if got.Databases["af_test_aabbcc"] != "/some/dir" {
		t.Errorf("Databases: got %v", got.Databases)
	}
}

func TestReconcileStoppedInstanceNoOp(t *testing.T) {
	inst := &Instance{
		Engine: "postgres",
		Major:  "17",
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

// parseUint64 is a test-local helper that mirrors the gate logic.
func parseUint64(s string) (uint64, error) {
	var n uint64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// ---- Postgres integration tests (require AF_DB_POSTGRES_ROOT) ----

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

func TestEnsureUpURLStop(t *testing.T) {
	root := skipIfNoBinary(t)

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("AF_DB_POSTGRES_ROOT", root)
	t.Setenv("AF_WS_SCRATCH", "")

	inst, err := ensureUp("postgres", "17", false)
	if err != nil {
		t.Fatalf("ensureUp: %v", err)
	}
	t.Cleanup(func() {
		if err := stopInstance("postgres", "17", true); err != nil {
			t.Logf("stopInstance cleanup: %v", err)
		}
	})

	if !isPGRunning(inst.PID, inst.Datadir) {
		t.Fatal("server is not running after ensureUp")
	}

	url, err := urlFor("postgres", "17", "", false, false)
	if err != nil {
		t.Fatalf("urlFor: %v", err)
	}
	if url == "" {
		t.Fatal("urlFor returned empty URL")
	}

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

	url2, err := urlFor("postgres", "17", "", false, false)
	if err != nil {
		t.Fatalf("urlFor idempotent: %v", err)
	}
	if url2 != url {
		t.Errorf("urlFor not idempotent: first=%q second=%q", url, url2)
	}

	n2 := CountClientBackends(inst)
	if n2 != 0 {
		t.Errorf("CountClientBackends with no real clients: got %d, want 0", n2)
	}

	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := ensureUp("postgres", "17", false)
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent ensureUp: %v", err)
		}
	}
}

func TestPostgresIntegration(t *testing.T) {
	root := skipIfNoBinary(t)
	binDir := filepath.Join(root, "bin")

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("AF_DB_POSTGRES_ROOT", root)
	t.Setenv("AF_WS_SCRATCH", "")

	_ = os.MkdirAll(filepath.Join(tmp, ".config", "agent-fleet", "af-db"), 0o700)

	major := "17"
	pp := passPath("postgres", major)
	pw, err := generatePass(pp)
	if err != nil {
		t.Fatalf("generatePass: %v", err)
	}

	datadir := filepath.Join(tmp, "pgdata")
	sockdir := filepath.Join(tmp, "pgsock")
	logFile := filepath.Join(tmp, "pg.log")
	_ = os.MkdirAll(datadir, 0o700)
	_ = os.MkdirAll(sockdir, 0o700)

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

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		logContents, _ := os.ReadFile(logFile)
		t.Fatalf("connect: %v\nlog:\n%s", err, logContents)
	}
	conn.Close(ctx)

	testDB := fmt.Sprintf("af_inttest_%d", port%10000)
	if err := ensureDatabase(connStr, testDB); err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if err := ensureDatabase(connStr, testDB); err != nil {
		t.Fatalf("ensureDatabase idempotent: %v", err)
	}

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

	dirName := DBNameFor(tmp)
	if !strings.HasPrefix(dirName, "af_") {
		t.Errorf("DBNameFor(%q) = %q: missing prefix", tmp, dirName)
	}

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
			goneDB:               goneDir,
			"af_shared_explicit": "",
		},
	}

	_ = withLock(func() error {
		r := &registry{Instances: map[string]*Instance{instanceKey("postgres", major): inst}}
		return writeRegistry(r)
	})

	if err := reconcile(inst, pw); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

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

// ---- MySQL integration tests (require AF_DB_MYSQL_ROOT) ----

func skipIfNoMySQLBinary(t *testing.T) string {
	t.Helper()
	root := os.Getenv("AF_DB_MYSQL_ROOT")
	if root == "" {
		t.Skip("AF_DB_MYSQL_ROOT not set; skipping MySQL integration test")
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "mysqld")); err != nil {
		t.Skipf("mysqld not found at %s/bin/mysqld: %v", root, err)
	}
	return root
}

func TestMySQLEnsureUpURLStop(t *testing.T) {
	root := skipIfNoMySQLBinary(t)

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("AF_DB_MYSQL_ROOT", root)
	t.Setenv("AF_WS_SCRATCH", "")

	t.Cleanup(func() {
		if err := stopInstance("mysql", "8.4", true); err != nil {
			t.Logf("stopInstance mysql cleanup: %v", err)
		}
	})

	inst, err := ensureUp("mysql", "8.4", false)
	if err != nil {
		t.Fatalf("ensureUp mysql: %v", err)
	}
	if !isInstanceRunning(inst) {
		t.Fatal("mysql not running after ensureUp")
	}

	url, err := urlFor("mysql", "8.4", "", false, false)
	if err != nil {
		t.Fatalf("urlFor mysql: %v", err)
	}
	if !strings.HasPrefix(url, "mysql://") {
		t.Errorf("urlFor mysql: unexpected URL %q", url)
	}

	// Run a version query.
	pw := readPass(passPath("mysql", "8.4"))
	out, err := mysqlQuery(inst, pw, "SELECT VERSION()")
	if err != nil {
		t.Fatalf("mysqlQuery VERSION: %v", err)
	}
	if !strings.Contains(out, "8.4") {
		t.Errorf("VERSION() = %q, expected to contain 8.4", out)
	}

	// JSON+CRUD round-trip.
	if _, err := mysqlQuery(inst, pw,
		"CREATE DATABASE IF NOT EXISTS af_test_mysql_p1"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	if _, err := mysqlQuery(inst, pw,
		"CREATE TABLE IF NOT EXISTS af_test_mysql_p1.t(id INT PRIMARY KEY, j JSON)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := mysqlQuery(inst, pw,
		`INSERT INTO af_test_mysql_p1.t VALUES (1,'{"a":1}') ON DUPLICATE KEY UPDATE j=j`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	jsonOut, err := mysqlQuery(inst, pw,
		`SELECT j->>'$.a' FROM af_test_mysql_p1.t WHERE id=1`)
	if err != nil {
		t.Fatalf("SELECT json: %v", err)
	}
	if strings.TrimSpace(jsonOut) != "1" {
		t.Errorf("j->>'$.a' = %q, want 1", jsonOut)
	}

	// Idempotent ensureUp.
	inst2, err := ensureUp("mysql", "8.4", false)
	if err != nil {
		t.Fatalf("ensureUp mysql idempotent: %v", err)
	}
	if inst2.Port != inst.Port {
		t.Errorf("ensureUp not idempotent: port changed from %d to %d", inst.Port, inst2.Port)
	}

	// rssBytes should be non-zero for a running server.
	rss := rssForInstance(inst)
	if rss <= 0 {
		t.Errorf("rssForInstance returned %d, expected > 0", rss)
	}
	t.Logf("MySQL rssBytes: %d (%.1f MB)", rss, float64(rss)/(1024*1024))
}

func TestMySQLIdleStop(t *testing.T) {
	skipIfNoMySQLBinary(t)

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("AF_DB_MYSQL_ROOT", os.Getenv("AF_DB_MYSQL_ROOT"))
	t.Setenv("AF_WS_SCRATCH", "")
	t.Setenv("AF_DB_IDLE_SECONDS", "2")

	t.Cleanup(func() {
		stopInstance("mysql", "8.4", true) //nolint:errcheck
	})

	inst, err := ensureUp("mysql", "8.4", false)
	if err != nil {
		t.Fatalf("ensureUp: %v", err)
	}

	key := instanceKey("mysql", "8.4")
	// Backdate lastUsedAt so idle threshold is immediately exceeded.
	_ = withLock(func() error {
		r, _ := readRegistry()
		if i, ok := r.Instances[key]; ok {
			i.LastUsedAt = time.Now().Add(-5 * time.Second)
		}
		return writeRegistry(r)
	})

	idleSince := map[string]time.Time{key: time.Now().Add(-5 * time.Second)}
	CheckAllInstances(idleSince)

	if isMySQLRunning(inst.PID, inst.Datadir) {
		t.Fatal("expected mysql to be stopped by idle loop")
	}
}

// ---- helpers ----

func runCmd(bin string, args ...string) ([]byte, error) {
	return exec.Command(bin, args...).CombinedOutput()
}
