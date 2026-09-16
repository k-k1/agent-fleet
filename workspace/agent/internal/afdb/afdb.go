// Package afdb implements the af-db CLI (ADR 0086 P0): per-working-copy Postgres
// databases started on demand inside the Workspace container, no Docker, no root.
//
// One server per (engine, major) per Workspace; one database per working copy
// (or per explicit --db=<name>). Registry at
// ~/.config/agent-fleet/af-db/instances.json, locked under
// ~/.config/agent-fleet/af-db/lock (advisory flock, POSIX).
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
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// DefaultMajor is the default Postgres major version.
const DefaultMajor = 17

// Instance holds the live state for one (engine, major) server.
type Instance struct {
	Engine     string    `json:"engine"`
	Major      int       `json:"major"`
	Root       string    `json:"root"`
	Datadir    string    `json:"datadir"`
	Sockdir    string    `json:"sockdir"`
	Port       int       `json:"port"`
	PID        int       `json:"pid,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
	Persist    bool      `json:"persist,omitempty"`
	// Databases: db name → working copy dir. dir=="" means explicitly named (--db=<name>);
	// reconcile never drops those.
	Databases map[string]string `json:"databases,omitempty"`
}

// registry is the on-disk JSON structure.
type registry struct {
	Instances map[string]*Instance `json:"instances"`
}

// instanceKey is the registry map key for an engine+major.
func instanceKey(engine string, major int) string {
	return fmt.Sprintf("%s-%d", engine, major)
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
	return filepath.Join(paths.AgentConfigDir(), "af-db")
}

func registryPath() string { return filepath.Join(registryDir(), "instances.json") }
func lockPath() string     { return filepath.Join(registryDir(), "lock") }

// startLockPath is a per-(engine,major) lock that serializes concurrent starts.
func startLockPath(engine string, major int) string {
	return filepath.Join(registryDir(), fmt.Sprintf("%s-%d.start.lock", engine, major))
}

// homeStateBase is the home-rooted state base (persists across container stops).
func homeStateBase() string {
	return filepath.Join(homeDir(), ".local", "state", "af-db")
}

// scratchBase returns the task-local or home state base for datadirs.
// When AF_WS_SCRATCH is set the datadir is on the fast scratch disk (wiped on stop).
func scratchBase() string {
	if s := os.Getenv("AF_WS_SCRATCH"); s != "" {
		return filepath.Join(s, "af-db")
	}
	return homeStateBase()
}

// sockBase always uses home so socket paths stay well under 107 bytes.
func sockBase() string {
	return filepath.Join(homeDir(), ".local", "state", "af-db", "run")
}

// postgresRoot returns the install root for a given major.
// AF_DB_POSTGRES_ROOT overrides (used in tests against the retained dist).
func postgresRoot(major int) string {
	if r := os.Getenv("AF_DB_POSTGRES_ROOT"); r != "" {
		return r
	}
	return filepath.Join(paths.AgentDataDir(), "postgres", fmt.Sprintf("%d", major))
}

// passPath is where the generated password for a major is kept (mode 0600).
func passPath(major int) string {
	return filepath.Join(registryDir(), fmt.Sprintf("postgres-%d.pass", major))
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

// validateExplicitDB returns an errUsage if name is not a safe Postgres identifier.
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
func withStartLock(engine string, major int, fn func() error) error {
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
		return nil, fmt.Errorf("registry parse: %w", err)
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

// URLForDir returns the Postgres connection URL (socket form) if a running Postgres
// instance has a database for dir. Returns "" if not found or not running.
// Called from session_tmux.go to inject AF_DB_URL_POSTGRES; no side effects.
func URLForDir(dir string) string {
	// Return early if the registry doesn't exist yet to avoid creating lock files
	// on every tmux launch before af-db has ever been used.
	if _, err := os.Stat(registryPath()); os.IsNotExist(err) {
		return ""
	}
	var url string
	_ = withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return nil
		}
		for _, inst := range r.Instances {
			if inst.Engine != "postgres" {
				continue
			}
			if !isPGRunning(inst.PID, inst.Datadir) {
				continue
			}
			dbName := DBNameFor(dir)
			if _, ok := inst.Databases[dbName]; ok {
				pw := readPass(passPath(inst.Major))
				url = buildURL(inst, dbName, pw, false)
				return nil
			}
		}
		return nil
	})
	return url
}

// buildURL returns the connection URL for an instance and database.
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

// ---- process liveness ----

// isRunning returns true if pid > 0 and the process is alive.
func isRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// isPGRunning checks if the postgres instance is running by cross-checking with
// postmaster.pid in the datadir. This guards against stale registry pids after a
// container restart on a persistent home (pid reuse).
func isPGRunning(pid int, datadir string) bool {
	if datadir != "" {
		if authPID, err := readPostmasterPID(datadir); err == nil && authPID > 0 {
			return isRunning(authPID)
		}
		// postmaster.pid absent: server is not running, regardless of registry pid.
		return false
	}
	return isRunning(pid)
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
