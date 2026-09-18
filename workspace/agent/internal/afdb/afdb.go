// Package afdb implements the af-db CLI (ADR 0086 P0/P1): per-working-copy Postgres and MySQL
// databases started on demand inside the Workspace container, no Docker, no root.
//
// One server per (engine, major) per Workspace; one database per working copy
// (or per explicit --db=<name>). Registry at
// ~/.local/state/agent-fleet/af-db/instances.json, locked under
// ~/.local/state/agent-fleet/af-db/lock (advisory flock, POSIX) — the same home volume the
// datadirs are on, which is what the PIDs and sockets it records are true of anyway.
package afdb

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// DefaultMajor is the default Postgres major version.
const DefaultMajor = "17"

// DefaultMySQLMajor is the default MySQL major version.
const DefaultMySQLMajor = "8.4"

// Instance holds the live state for one (engine, major) server.
type Instance struct {
	Engine  string `json:"engine"`
	Major   string `json:"major"`
	Root    string `json:"root"`
	Datadir string `json:"datadir"`
	Sockdir string `json:"sockdir"`
	Port    int    `json:"port"`
	// PreferredPort is the port this instance wants next time. Unlike Port it is
	// NOT cleared on stop — that is the whole point: a clean stop zeroes Port, so
	// without this the next start would re-run the search and could land somewhere
	// else if the base port happened to be busy the first time.
	PreferredPort int `json:"preferredPort,omitempty"`
	PID           int `json:"pid,omitempty"`
	// Autostart brings this engine up when the Agent boots, i.e. when the
	// workspace starts. Off by default: a database costs memory on a shared,
	// memory-constrained host, and starting one nobody asked for is not a default
	// this project gets to choose for a member.
	Autostart  bool      `json:"autostart,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
	// Durable turns fsync (Postgres) / innodb-flush-log-at-trx-commit (MySQL) back
	// on. The JSON key stays `persist` because that is what `--persist` wrote into
	// every registry built before decision 4″: back then the flag meant "home
	// datadir AND fsync on", and the half that survives the new default is this
	// one. Renaming the key would silently drop the setting on upgrade.
	Durable bool `json:"persist,omitempty"`
	// Ephemeral puts the datadir on the task-local scratch disk, which is wiped
	// when the workspace stops. Opt-in (decision 4″); sticky across restarts so a
	// plain `af-db up` does not quietly move the datadir back to home.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// Databases: db name → working copy dir. dir=="" means explicitly named (--db=<name>);
	// reconcile never drops those.
	Databases map[string]string `json:"databases,omitempty"`
}

// UnmarshalJSON accepts `major` either as the string this build writes ("17",
// "8.4") or as the number the P0 build wrote (17). Without this, a registry left
// by an older agent fails to parse and every af-db verb — including the ones that
// would repair it — stops with "registry parse".
func (i *Instance) UnmarshalJSON(b []byte) error {
	type plain Instance
	aux := struct {
		*plain
		Major json.RawMessage `json:"major"`
	}{plain: (*plain)(i)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	i.Major = ""
	switch {
	case len(aux.Major) == 0 || string(aux.Major) == "null":
	case aux.Major[0] == '"':
		var s string
		if err := json.Unmarshal(aux.Major, &s); err != nil {
			return err
		}
		i.Major = s
	default:
		var n json.Number
		if err := json.Unmarshal(aux.Major, &n); err != nil {
			return err
		}
		i.Major = n.String()
	}
	return nil
}

// tailWriter keeps the tail of a subprocess's output so that a failure can carry
// the reason with it: "install-mysql 8.4 failed: exit status 3" tells a member
// reading the Console card nothing, while the installer's own last line names the
// library, the URL or the sha that went wrong.
type tailWriter struct {
	buf []byte
}

// tailWriterMax bounds what is kept; installers are chatty and only the end matters.
const tailWriterMax = 4096

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > tailWriterMax {
		w.buf = w.buf[len(w.buf)-tailWriterMax:]
	}
	return len(p), nil
}

// reason returns the last few non-empty lines, ready to append to an error
// message, or "" when the subprocess said nothing.
func (w *tailWriter) reason() string {
	var lines []string
	for _, l := range strings.Split(string(w.buf), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return ": " + strings.Join(lines, " / ")
}

// registry is the on-disk JSON structure.
type registry struct {
	Instances map[string]*Instance `json:"instances"`
}

// instanceKey is the registry map key for an engine+major.
func instanceKey(engine, major string) string {
	return engine + "-" + major
}

// ---- path helpers ----

func homeDir() string {
	if h, _ := os.UserHomeDir(); h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// registryDir returns the af-db config directory.
func registryDir() string {
	return filepath.Join(paths.AgentStateDir(), "af-db")
}

func registryPath() string { return filepath.Join(registryDir(), "instances.json") }
func lockPath() string     { return filepath.Join(registryDir(), "lock") }

// startLockPath is a per-(engine,major) lock that serializes concurrent starts.
func startLockPath(engine, major string) string {
	return filepath.Join(registryDir(), fmt.Sprintf("%s-%s.start.lock", engine, major))
}

// homeStateBase is the home-rooted state base (persists across container stops).
func homeStateBase() string {
	return filepath.Join(homeDir(), ".local", "state", "af-db")
}

// datadirBase returns the base directory datadirs are built under.
//
// Home is the default and the only place that survives a workspace stop
// (decision 4″). A database that disappears when the workspace stops is not a
// behaviour a member can plan around: they cannot tell a fixture they meant to
// throw away from the seed data they spent an afternoon on, and nothing in the
// Console says which one they have. ephemeral=true opts in to the task-local
// scratch disk, which is faster and is wiped on stop — and when no scratch disk
// is injected (AF_WS_SCRATCH unset, which is every deployment today) it has
// nowhere to put one, so it falls back to home rather than inventing a path.
// Returns onScratch=false when the fallback was taken, so the caller records
// what it actually did instead of what it was asked for: an instance that
// remembered ephemeral=true from a workspace without a scratch disk would move
// its datadir — and initdb an empty one — the first time a deployment injected
// one.
func datadirBase(ephemeral bool) (base string, onScratch bool) {
	if ephemeral {
		if s := os.Getenv("AF_WS_SCRATCH"); s != "" {
			return filepath.Join(s, "af-db"), true
		}
	}
	return homeStateBase(), false
}

// sockBase always uses home so socket paths stay well under 107 bytes.
func sockBase() string {
	return filepath.Join(homeDir(), ".local", "state", "af-db", "run")
}

// postgresRoot returns the install root for a given major.
// AF_DB_POSTGRES_ROOT overrides (used in tests against the retained dist).
func postgresRoot(major string) string {
	if r := os.Getenv("AF_DB_POSTGRES_ROOT"); r != "" {
		return r
	}
	return filepath.Join(paths.AgentDataDir(), "postgres", major)
}

// mysqlRoot returns the install root for MySQL.
// AF_DB_MYSQL_ROOT overrides (used in tests).
func mysqlRoot(major string) string {
	if r := os.Getenv("AF_DB_MYSQL_ROOT"); r != "" {
		return r
	}
	return filepath.Join(paths.AgentDataDir(), "mysql", major)
}

// passPath is where the generated password for (engine, major) is kept (mode 0600).
//
// This is a plaintext password under the STATE directory, which rule ① of the split in
// paths.AgentStateDir ("a credential, or a file that can carry one") would otherwise send to
// the keep volume. The exception is deliberate: it authenticates nothing outside this
// Workspace. It is generated here (generatePass), reaches only a loopback server whose
// datadir sits on the same volume, and is worthless without it — losing the volume loses the
// database the password is for. A copy on the keep volume would outlive the thing it opens.
func passPath(engine, major string) string {
	return filepath.Join(registryDir(), fmt.Sprintf("%s-%s.pass", engine, major))
}

// ---- database name derivation ----

var unsafeRe = regexp.MustCompile(`[^a-z0-9]+`)

// DBNameFor derives the database name for a working copy directory.
// Format: af_<sanitised-basename>_<6-hex-of-sha256(dir)>
func DBNameFor(dir string) string {
	base := strings.ToLower(filepath.Base(dir))
	base = unsafeRe.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_")
	if len(base) > 40 {
		base = base[:40]
	}
	if base == "" {
		base = "db"
	}
	h := sha256.Sum256([]byte(dir))
	return "af_" + base + "_" + hex.EncodeToString(h[:])[:6]
}

// validExplicitDBRe validates an explicit --db name (user-supplied).
var validExplicitDBRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// validateExplicitDB returns an errUsage if name is not a safe database identifier.
func validateExplicitDB(name string) error {
	if !validExplicitDBRe.MatchString(name) {
		return errUsage(fmt.Sprintf("--db name must match ^[a-z_][a-z0-9_]{0,62}$, got %q", name))
	}
	return nil
}

// ---- working copy resolution ----

// ResolveDir returns the working copy directory for the current call context:
//  1. AF_SESSION_NAME env → session.ReadMeta → Meta.Dir
//  2. git rev-parse --show-toplevel of cwd
//  3. cwd
func ResolveDir() string {
	if name := os.Getenv("AF_SESSION_NAME"); name != "" {
		if m, ok := session.ReadMeta(name); ok && m.Dir != "" {
			return m.Dir
		}
	}
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			return d
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// ---- TCP port allocation ----

// pickPort allocates a free port on 127.0.0.1 by binding :0 and closing.
func pickPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// basePortFor is where the search for a stable port starts: the engine's own
// well-known number. A member's application config, their ORM's default, and
// every tutorial they will copy from all say 5432 / 3306, so landing there means
// the connection string they already have is the right one.
func basePortFor(engine string) int {
	if engine == "mysql" {
		return 3306
	}
	return 5432
}

// portScanRange is how far above the base to look before giving up on a stable
// number. Small on purpose: past this, something else in the container owns that
// neighbourhood and an ephemeral port is the honest answer.
const portScanRange = 64

// portFree reports whether 127.0.0.1:port can be bound right now.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// stablePort picks the port a server should listen on, preferring the one it
// used last time.
//
// The old behaviour — a fresh 127.0.0.1:0 on every start — gave a different
// number after every restart, and the idle-stop of decision 7 makes restarts
// routine rather than rare. That is not something a member can build against:
// they would have to rewrite the connection string in the application they are
// developing every time the server came back. For Postgres it is worse than a
// number changing, because the unix socket is named .s.PGSQL.<port>, so the
// socket path moves too.
//
// Order: the recorded port, then the engine's well-known port and the 64 above
// it, then an ephemeral one. The recorded port comes first so a server that
// already landed somewhere keeps its place; the well-known base comes next
// because a first start should be predictable rather than random.
//
// Note that 127.0.0.1:0 hands out ports from 32768-60999, the SAME range the
// kernel assigns to outbound sockets — so a remembered ephemeral port has a real
// chance of being taken by some unrelated connection while the server is down.
// Starting from 5432 / 3306 puts the server outside that traffic entirely.
//
// Stability is preferred, never promised: this container is shared with the
// member's other sessions, so the port can still move if something took it.
// Whatever it ends up as is recorded, and `af-db connect` and the Console read
// it live.
func stablePort(engine string, recorded int) (int, error) {
	if recorded > 0 && portFree(recorded) {
		return recorded, nil
	}
	base := basePortFor(engine)
	for p := base; p < base+portScanRange; p++ {
		if portFree(p) {
			return p, nil
		}
	}
	return pickPort()
}

// ---- password helpers ----

// generatePass creates a random 32-hex-char password and writes it to path (mode 0600).
func generatePass(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(b)
	if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}

// generatePassInMemory generates a random 32-hex-char password without writing it to disk.
func generatePassInMemory() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// writePassFile writes pw to path (mode 0600), creating parent dirs as needed.
func writePassFile(path, pw string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(pw+"\n"), 0o600)
}

// readPass reads the password from path, returning "" on any error.
func readPass(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- registry locking / read-modify-write ----

// withLock executes fn with the registry lock held (POSIX advisory flock, exclusive).
func withLock(fn func() error) error {
	if err := os.MkdirAll(registryDir(), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("registry lock: %w", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// withStartLock serializes concurrent startServer calls for one (engine, major).
func withStartLock(engine, major string, fn func() error) error {
	if err := os.MkdirAll(registryDir(), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(startLockPath(engine, major), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("start lock: %w", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// readRegistry reads the registry. Caller must hold the lock.
func readRegistry() (*registry, error) {
	r := &registry{Instances: make(map[string]*Instance)}
	b, err := os.ReadFile(registryPath())
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		// Name the file: a registry this build cannot read is repaired by moving
		// it aside, and the caller sees this text on the Console card.
		return nil, fmt.Errorf("registry parse (%s): %w", registryPath(), err)
	}
	if r.Instances == nil {
		r.Instances = make(map[string]*Instance)
	}
	return r, nil
}

// writeRegistry atomically writes the registry. Caller must hold the lock.
func writeRegistry(r *registry) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := registryPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, registryPath())
}

// ensureClientTools makes the engine's command-line client reachable as a bare
// name on PATH. Never fatal: a member whose server is up but whose client could
// not be placed still has a working database, and failing `af-db up` over it
// would be the wrong trade.
//
// Two different gaps, one per engine:
//
//   - MySQL ships its own `mysql` client inside the server tarball, so it is
//     already on disk — just three directories deep, with nothing on PATH
//     pointing at it. `command -v mysql` answered nothing on a workspace that
//     had been running MySQL for a day.
//   - Postgres' client is a separate download (install-pg-client) that nothing
//     ever ran on its own, so `psql` was missing until a member found the
//     command in the guide.
func ensureClientTools(engine, major, root string) {
	binDir := filepath.Join(homeDir(), ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return
	}
	if engine == "mysql" {
		// Unlike Postgres, the MySQL client has NO environment variable for the user
		// name: it falls back to the OS user, and `dev` is not a MySQL account, so a
		// bare `mysql` answers "Access denied for user 'dev'@'localhost'" even with
		// the socket and password in the environment. An option file is the only
		// place to say it. Command-line -u still wins over this.
		if err := writeMySQLClientCnf(root); err != nil {
			fmt.Fprintf(os.Stderr, "af-db: could not write the mysql client defaults: %v\n", err)
		}
		for _, bin := range []string{"mysql", "mysqldump"} {
			if err := writeClientWrapper(binDir, root, bin); err != nil {
				fmt.Fprintf(os.Stderr, "af-db: could not put %s on PATH: %v\n", bin, err)
			}
		}
		return
	}
	// Postgres: install-pg-client writes the wrappers itself. Only run it when the
	// wrapper is absent — it is a network download, and every `af-db up` paying
	// for it would be a tax on the common case.
	if fileExists(filepath.Join(binDir, "psql")) {
		return
	}
	fmt.Fprintln(os.Stderr, "af-db: psql not found; installing the client...")
	if err := installPgClientFn(major); err != nil {
		// Egress may be restricted, or the Debian index may not carry this major.
		// The server is up either way; say what is missing and move on.
		fmt.Fprintf(os.Stderr,
			"af-db: psql could not be installed (%v); the server is running — retry with 'workspace-agent install-pg-client %s'\n",
			err, major)
	}
}

// installPgClientFn re-execs the Agent to install the Postgres client. It is a
// seam because os.Executable() under `go test` is the TEST BINARY: called for
// real from a test, it re-runs the whole suite as a subprocess, which is how
// TestEnsureUpURLStop first hung for 264 s instead of failing. Tests replace it.
var installPgClientFn = func(major string) error {
	self, err := os.Executable()
	if err != nil {
		self = "/usr/local/bin/workspace-agent"
	}
	cmd := exec.Command(self, "install-pg-client", major)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// mysqlClientCnfPath is the option file the mysql wrappers point at. It lives in
// the install root rather than in ~/.my.cnf: that file belongs to the member, and
// silently taking it over would break the day they connect to something else.
func mysqlClientCnfPath(root string) string {
	return filepath.Join(root, "af-client.cnf")
}

func writeMySQLClientCnf(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	// user only. The password is not written here — it belongs to the instance, it
	// is regenerated when the datadir is, and a stale copy in a second file is a
	// bug waiting to be filed. It reaches the client through MYSQL_PWD (ShellEnv)
	// or through `af-db connect`, both of which read the live one.
	return os.WriteFile(mysqlClientCnfPath(root), []byte("[client]\nuser=root\n"), 0o644)
}

// writeClientWrapper writes ~/.local/bin/<bin> pointing at the engine's own copy.
//
// A wrapper rather than a symlink: MySQL's binaries find their libraries through
// a RUNPATH of $ORIGIN/../lib/private, and exec'ing the absolute path keeps
// $ORIGIN pointing inside the install root whatever the caller's cwd is.
// LD_LIBRARY_PATH is set as well so the binary still resolves if a future
// tarball ships without that RUNPATH.
func writeClientWrapper(binDir, root, bin string) error {
	target := filepath.Join(root, "bin", bin)
	if !fileExists(target) {
		return nil // this tarball does not carry it; not an error
	}
	// --defaults-extra-file must come first, and is still overridable: the client
	// reads option files before the command line, so a later -u or --defaults-file
	// from the member wins.
	var defaults string
	if cnf := mysqlClientCnfPath(root); fileExists(cnf) {
		defaults = fmt.Sprintf("--defaults-extra-file=%q ", cnf)
	}
	content := fmt.Sprintf("#!/bin/sh\nexec env LD_LIBRARY_PATH=%q\"${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}\" %q %s\"$@\"\n",
		filepath.Join(root, "lib", "private"), target, defaults)
	dest := filepath.Join(binDir, bin)
	tmp := dest + ".part"
	if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ShellEnv returns the environment a freshly launched session needs for `psql`
// and `mysql` to connect with no arguments at all.
//
// The CLIs being on PATH is only half of "a member can query their database":
// unconfigured, `psql` tries a unix socket in /var/run under the member's own
// user name and fails with a sentence about a socket nobody has. So the launch
// injects the standard client variables — the same ones a human would export by
// hand — pointing at this workspace's server and at the calling working copy's
// database.
//
// dir is the working copy the session starts in; "" (or a directory with no
// database yet) still yields the server-level variables, so `psql -l` and
// `mysql` work and only PGDATABASE is missing.
//
// Passwords ride here for the reason passPath gives: they authenticate nothing
// outside this workspace, and the environment of a process is readable only by
// the uid that already owns the datadir.
func ShellEnv(dir string) []string {
	// Return early if the registry doesn't exist yet to avoid creating lock files
	// on every tmux launch before af-db has ever been used.
	if _, err := os.Stat(registryPath()); os.IsNotExist(err) {
		return nil
	}
	var env []string
	_ = withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return nil
		}
		for _, inst := range r.Instances {
			if !isInstanceRunning(inst) {
				continue
			}
			pw := readPass(passPath(inst.Engine, inst.Major))
			dbName := DBNameFor(dir)
			_, hasDB := inst.Databases[dbName]
			if inst.Engine == "mysql" {
				// MYSQL_UNIX_PORT is the socket; the client has no variable for the
				// default database, so `mysql` lands on the server and `mysql <db>`
				// picks one. The Connect button supplies the name.
				env = append(env,
					"MYSQL_UNIX_PORT="+mysqlSockFile(inst),
					"MYSQL_TCP_PORT="+strconv.Itoa(inst.Port),
					"MYSQL_PWD="+pw,
				)
				continue
			}
			env = append(env,
				"PGHOST="+inst.Sockdir,
				"PGPORT="+strconv.Itoa(inst.Port),
				"PGUSER=postgres",
				"PGPASSWORD="+pw,
			)
			if hasDB && dir != "" {
				env = append(env, "PGDATABASE="+dbName)
				// Kept for compatibility: decision 8′ named this variable, and
				// projects already read it.
				env = append(env, "AF_DB_URL_POSTGRES="+buildURL(inst, dbName, pw, false))
			}
		}
		return nil
	})
	return env
}

// buildURL returns the Postgres connection URL for an instance and database.
// tcp=true uses the TCP listener; tcp=false uses the unix socket.
// Socket connections include port= so pgx finds .s.PGSQL.<port> not .s.PGSQL.5432.
func buildURL(inst *Instance, dbName, pw string, tcp bool) string {
	if tcp {
		return fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/%s?sslmode=disable",
			pw, inst.Port, dbName)
	}
	return fmt.Sprintf("postgres://postgres:%s@/%s?host=%s&port=%d&sslmode=disable",
		pw, dbName, inst.Sockdir, inst.Port)
}

// mysqlSockFile returns the MySQL socket file path from an instance's sockdir.
func mysqlSockFile(inst *Instance) string {
	return filepath.Join(inst.Sockdir, "mysql.sock")
}

// buildMySQLURL returns the MySQL connection URL for an instance and database.
func buildMySQLURL(inst *Instance, dbName, pw string, tcp bool) string {
	if tcp {
		return fmt.Sprintf("mysql://root:%s@127.0.0.1:%d/%s", pw, inst.Port, dbName)
	}
	return fmt.Sprintf("mysql://root:%s@localhost/%s?socket=%s", pw, dbName, mysqlSockFile(inst))
}

// buildMySQLGoDSN returns the go-sql-driver DSN for a MySQL instance and database.
func buildMySQLGoDSN(inst *Instance, dbName, pw string, tcp bool) string {
	if tcp {
		return fmt.Sprintf("root:%s@tcp(127.0.0.1:%d)/%s", pw, inst.Port, dbName)
	}
	return fmt.Sprintf("root:%s@unix(%s)/%s", pw, mysqlSockFile(inst), dbName)
}

// ---- process liveness ----

// isRunning returns true if pid > 0 and the process is alive and not a zombie.
// A zombie passes Signal(0) on some kernels; we check /proc explicitly.
func isRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.SplitN(string(data), "\n", 10) {
			if strings.HasPrefix(line, "State:") {
				// "State:\tZ (zombie)" — process has exited but not been reaped.
				// The separator is a tab, so split on whitespace rather than matching a literal space.
				f := strings.Fields(line)
				return !(len(f) >= 2 && f[1] == "Z")
			}
		}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// procCmdline returns /proc/<pid>/cmdline with its NUL separators turned into
// spaces, or "" when it cannot be read (the process is gone, or this is not Linux).
func procCmdline(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

// pidIsServerFor reports whether pid is alive AND is the engine's own server for
// this datadir, by matching /proc/<pid>/cmdline.
//
// "A process with this pid exists" is not the same claim. A workspace that is
// stopped as a container leaves its pid files behind; on the next boot the same
// numbers belong to whatever started first. Reading the command line is what
// separates "our server is up" from "someone else inherited the number", and the
// answer decides whether a datadir may be removed or a pid file cleared.
//
// When /proc says nothing (cmdline unreadable), the caller's conservative
// fallback applies: we do not claim the process is ours, and we do not claim it
// is gone either.
func pidIsServerFor(pid int, exeName, datadir string) bool {
	if pid <= 0 || !isRunning(pid) {
		return false
	}
	cmd := procCmdline(pid)
	if cmd == "" {
		return false
	}
	return strings.Contains(cmd, exeName) && (datadir == "" || strings.Contains(cmd, datadir))
}

// isPGRunning checks if the Postgres instance is running by cross-checking with
// postmaster.pid in the datadir. When the pid file is absent (e.g. the file was
// removed while the server is still up), it falls back to the registry pid so that
// a running server is never silently treated as stopped (which would allow unsafe
// datadir removal).
func isPGRunning(pid int, datadir string) bool {
	if datadir != "" {
		if authPID, err := readPostmasterPID(datadir); err == nil && authPID > 0 {
			// A pid file that survived a container stop names a number that now
			// belongs to someone else; only a postgres running THIS datadir counts.
			return pidIsServerFor(authPID, "postgres", datadir)
		}
		// pid file absent — fall back to registry pid as a safety net.
		return isRunning(pid)
	}
	return isRunning(pid)
}

// clearStalePIDFile removes a pid file left behind by a server that is no longer
// there, and reports whether it removed one.
//
// The case is ordinary: the workspace is stopped as a container, so nothing runs
// the shutdown path, and `postmaster.pid` / `mysqld.pid` stay in the datadir. The
// next start then meets pg_ctl's "another server might be running; trying to
// start server anyway" — which is a warning here and a refusal on the day the old
// number belongs to a live process.
//
// It removes the file ONLY when the pid it names is provably not this engine's
// server for this datadir. An unreadable /proc, or a live matching server, leaves
// the file alone: two postmasters on one datadir is worse than a warning.
func clearStalePIDFile(pidFile, exeName, datadir string) bool {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	line := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		// Unparsable pid file: nothing can be running under it.
		pid = 0
	}
	if pidIsServerFor(pid, exeName, datadir) {
		return false
	}
	if pid > 0 && isRunning(pid) && procCmdline(pid) == "" {
		// Alive but unidentifiable — do not touch it.
		return false
	}
	if err := os.Remove(pidFile); err != nil {
		return false
	}
	fmt.Fprintf(os.Stderr, "af-db: removed stale %s (pid %d is not %s for this datadir)\n",
		filepath.Base(pidFile), pid, exeName)
	return true
}

// readMySQLPID reads the MySQL pid file (one number per line).
func readMySQLPID(pidFile string) (int, error) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	line := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
	return strconv.Atoi(strings.TrimSpace(line))
}

// isMySQLRunning checks if the MySQL instance is running via its pid file.
// When the pid file is absent it falls back to the registry pid, matching the
// same safety-net behaviour as isPGRunning.
func isMySQLRunning(pid int, datadir string) bool {
	if datadir != "" {
		pidFile := filepath.Join(filepath.Dir(datadir), "mysqld.pid")
		if p, err := readMySQLPID(pidFile); err == nil && p > 0 {
			return pidIsServerFor(p, "mysqld", datadir)
		}
		// pid file absent — fall back to registry pid.
		return isRunning(pid)
	}
	return isRunning(pid)
}

// isInstanceRunning returns whether the instance is currently running,
// dispatching to the engine-specific liveness check.
func isInstanceRunning(inst *Instance) bool {
	if inst.Engine == "postgres" {
		return isPGRunning(inst.PID, inst.Datadir)
	}
	return isMySQLRunning(inst.PID, inst.Datadir)
}

// waitPIDGone polls until pid is no longer alive or timeout elapses.
func waitPIDGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isRunning(pid) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return !isRunning(pid)
}

// ---- RSS helpers ----

// rssForPID returns the resident set size (VmRSS) in bytes for a process.
// Returns 0 on any error.
func rssForPID(pid int) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				n, _ := strconv.ParseInt(fields[1], 10, 64)
				return n * 1024 // kB → bytes
			}
		}
	}
	return 0
}

// rssForPGInstance returns the combined RSS of the postmaster and all its children.
func rssForPGInstance(inst *Instance) int64 {
	authPID, err := readPostmasterPID(inst.Datadir)
	if err != nil || authPID <= 0 {
		authPID = inst.PID
	}
	if authPID <= 0 {
		return 0
	}
	total := rssForPID(authPID)

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return total
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == authPID {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if ppid, _ := strconv.Atoi(fields[1]); ppid == authPID {
						total += rssForPID(pid)
					}
				}
				break
			}
		}
	}
	return total
}

// rssForInstance returns the RSS in bytes for a running instance.
func rssForInstance(inst *Instance) int64 {
	if inst.Engine == "postgres" {
		return rssForPGInstance(inst)
	}
	// For MySQL, use the live pid from the pid file.
	pidFile := filepath.Join(filepath.Dir(inst.Datadir), "mysqld.pid")
	pid, err := readMySQLPID(pidFile)
	if err != nil || pid <= 0 {
		return rssForPID(inst.PID)
	}
	return rssForPID(pid)
}
