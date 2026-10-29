//go:build e2e

// Fleet E2E (L2): starts the control plane headless (AUTH=dev by default) and walks a
// user's shortest path against real containers using only the public API — start the
// workspace, create a shell session, send input (echo), read the file back through the
// fs API, stop. kind=shell means no LLM credential is needed anywhere.
//
// Requires docker and a built Workspace image (WS_IMAGE, default
// agent-fleet/workspace:dev); without them the test skips, and CI promotes that skip to
// a failure with E2E_REQUIRE=1.
// Run: cd e2e && go test -v -tags e2e -timeout 15m
//
// Each test gets its own DEV_USER so the container (af-ws-<user>) and network
// (af-net-<user>) names cannot collide, and the CP and Agent listen ports are allocated
// dynamically — this has to stay safe to run on a dev host where a real fleet is
// already up. Teardown reclaims container, network and temporary data best-effort.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestFleet(t *testing.T) {
	base := startFleet(t, "e2e")

	// --- create a shell session (no LLM credential required) ---
	created := postJSON(t, base+"/api/sessions", map[string]any{"kind": "shell", "title": "e2e"}, 201)
	name, _ := created["name"].(string)
	if name == "" {
		t.Fatalf("session create returned no name: %v", created)
	}
	t.Logf("session created: %s (kind=%v)", name, created["kind"])

	waitFor(t, 20*time.Second, "session alive in list", func() (bool, string) {
		// Poll conditions must use the non-fatal tryGet: a Fatal on a transient error
		// would kill the retry loop that is the whole point of polling.
		code, body := tryGet(base + "/api/sessions")
		if code != 200 {
			return false, fmt.Sprintf("%d %s", code, truncate([]byte(body)))
		}
		var list map[string]any
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			return false, "bad JSON: " + string(truncate([]byte(body)))
		}
		sessions, _ := list["sessions"].([]any)
		for _, s := range sessions {
			m, _ := s.(map[string]any)
			if m["name"] == name {
				alive, _ := m["alive"].(bool)
				return alive, fmt.Sprintf("alive=%v", alive)
			}
		}
		return false, "not in list"
	})

	// --- type an echo into the pane, read it back through the fs API (drive I/O) ---
	nonce := fmt.Sprintf("e2e-ok-%d", os.Getpid())
	marker := "e2e-marker.txt" // the session's cwd is home, and the fs API reads home-relative too
	sendPrompt(t, base, name, fmt.Sprintf("echo %s > %s", nonce, marker))
	waitFileContains(t, base, marker, nonce, 60*time.Second)

	// --- status and teardown APIs ---
	status := getJSON(t, base+"/api/sessions/"+name+"/status")
	if alive, _ := status["alive"].(bool); !alive {
		t.Fatalf("session status alive=false: %v", status)
	}
	postJSON(t, base+"/api/sessions/"+name+"/stop", nil, 200)
	stop := postJSON(t, base+"/api/workspace/stop", nil, 200)
	if stop["state"] != "stopped" {
		t.Fatalf("workspace/stop state = %v, want stopped", stop["state"])
	}
}

// --- shared harness -------------------------------------------------------

// startFleet checks the prerequisites, builds and starts the CP (AUTH=dev,
// DEV_USER=user), waits for the workspace to reach running, and returns the CP base URL.
// Teardown of the CP, container, network and temporary data is already registered with
// t.Cleanup. extraEnv adds KEY=VAL entries to the CP process environment.
func startFleet(t *testing.T, user string, extraEnv ...string) string {
	t.Helper()
	image := envOr("WS_IMAGE", "agent-fleet/workspace:dev")
	requireDockerAndImage(t, image)

	dataDir := newDataDir(t, image)
	// The path shape is manager.rootedDataDir's default tenant layout <WS_DATA>/<user>.
	prepareWorkspaceDirs(t, filepath.Join(dataDir, user))

	env := append([]string{
		"DEV_USER=" + user,
		fmt.Sprintf("WS_AGENT_PORT=%d", freePort(t)),
	}, extraEnv...)
	base := launchCP(t, image, dataDir, func() []string { return []string{user} }, env...)

	// --- identity: dev auth resolves to a fixed user ---
	who := getJSON(t, base+"/api/whoami")
	if who["resolved_user"] != user {
		t.Fatalf("whoami resolved_user = %v, want %q", who["resolved_user"], user)
	}

	// --- start the workspace (docker run, synchronous through Agent healthz) ---
	// Start returns 500 whenever the 15s Agent-healthz wait is missed even once, but it
	// is idempotent, so retry until it takes and only then assert running.
	waitFor(t, 120*time.Second, "workspace/start accepted", func() (bool, string) {
		code, body := tryPost(base+"/api/workspace/start", nil)
		return code == 200, fmt.Sprintf("%d %s", code, body)
	})
	waitFor(t, 60*time.Second, "workspace running", func() (bool, string) {
		// Non-fatal tryGet again here: getJSON would Fatal on any non-200 and end the poll.
		code, body := tryGet(base + "/api/workspace")
		if code != 200 {
			return false, fmt.Sprintf("%d %s", code, truncate([]byte(body)))
		}
		var ws map[string]any
		if err := json.Unmarshal([]byte(body), &ws); err != nil {
			return false, "bad JSON: " + string(truncate([]byte(body)))
		}
		return ws["state"] == "running", fmt.Sprint(ws["state"])
	})
	return base
}

// newDataDir creates the CP's WS_DATA and registers its best-effort reclaim.
//
// Workspace data is written by uid 1000 inside the container, so it cannot go through
// t.TempDir, whose removal failure fails the test. The reclaim is registered before
// anything else, so a Fatal before the CP starts still leaves nothing behind in /tmp;
// cleanup is LIFO, so the CP and container teardown registered later runs first, which
// is the order required here.
func newDataDir(t *testing.T, image string) string {
	t.Helper()
	dataDir, err := os.MkdirTemp("", "af-e2e-data-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupDataDir(image, dataDir) })
	return dataDir
}

// prepareWorkspaceDirs pre-creates a workspace's bind-mount sources 0777 under wsDir
// (the workspace's data dir, as manager.workspaceNames derives it).
//
// Where the host uid differs from the container's dev user (uid 1000) — GitHub runners,
// for one — home and claude-config created 0755 by the CP are not writable from inside
// the container, the entrypoint dies under set -e, and the Agent never reaches healthz.
// The CP's MkdirAll passes over an existing dir, so this is inert on a uid-1000 host.
func prepareWorkspaceDirs(t *testing.T, wsDir string) {
	t.Helper()
	for _, d := range []string{"home", "claude-config"} {
		p := filepath.Join(wsDir, d)
		if err := os.MkdirAll(p, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o777); err != nil { // umask trims what MkdirAll asked for
			t.Fatal(err)
		}
	}
}

// launchCP builds and starts the CP against dataDir, waits for /healthz and returns its
// base URL. env adds KEY=VAL entries to the CP environment. workspaces names the
// af-ws-/af-net- suffixes teardown reclaims; it is called at cleanup time, so a harness
// can register workspaces after the CP is up.
func launchCP(t *testing.T, image, dataDir string, workspaces func() []string, env ...string) string {
	t.Helper()
	return startCP(t, image, dataDir, workspaces, env...).base
}

// cpProcess is a running test CP. restart stops it and starts the same binary with the
// same environment on the same address, for what the CP only does at start-up.
type cpProcess struct {
	t       *testing.T
	bin     string
	dir     string
	env     []string
	base    string
	logPath string
	cmd     *exec.Cmd
}

// startCP is launchCP returning the process, for a harness that restarts it.
func startCP(t *testing.T, image, dataDir string, workspaces func() []string, env ...string) *cpProcess {
	t.Helper()
	root := repoRoot(t)
	tmp := t.TempDir() // the CP's logs; no container touches it, so auto-removal is safe
	cpAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	p := &cpProcess{t: t, bin: buildCP(t, root), dir: tmp, base: "http://" + cpAddr,
		logPath: filepath.Join(tmp, "cp.log")}
	p.env = append(append(cpEnviron(os.Environ()),
		"AF_RUNTIME=local",
		"AF_DB="+filepath.Join(dataDir, "e2e-cp.db"),
		"CP_ADDR="+cpAddr,
		"WS_IMAGE="+image,
		"WS_DATA="+dataDir,
		"CONSOLE_DIR="+tmp, // static serving is out of scope; point at a dir that exists
	), env...)
	t.Cleanup(func() { teardown(t, p.cmd, p.logPath, workspaces()) })
	p.start()
	return p
}

// start runs the CP and waits for /healthz. Every run appends to the one log, so the tail
// teardown prints spans a restart.
func (p *cpProcess) start() {
	p.t.Helper()
	logFile, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { _ = logFile.Close() }) // close the fd on every exit path
	p.cmd = exec.Command(p.bin)
	p.cmd.Dir = p.dir
	p.cmd.Stdout = logFile
	p.cmd.Stderr = logFile
	p.cmd.Env = p.env
	if err := p.cmd.Start(); err != nil {
		p.t.Fatalf("start CP: %v", err)
	}
	waitFor(p.t, 15*time.Second, "CP /healthz", func() (bool, string) {
		resp, err := http.Get(p.base + "/healthz")
		if err != nil {
			return false, err.Error()
		}
		resp.Body.Close()
		return resp.StatusCode == 200, resp.Status
	})
}

// restart stops the CP (SIGTERM, then kill) and starts it again.
func (p *cpProcess) restart() {
	p.t.Helper()
	p.stop()
	p.start()
}

// stop stops the CP; start brings it back.
func (p *cpProcess) stop() {
	stopCP(p.cmd)
	p.cmd = nil
}

func (p *cpProcess) running() bool { return p.cmd != nil }

// sendPrompt types a prompt into the session's pane, absorbing the 409 that tmux
// returns for a moment right after it starts.
func sendPrompt(t *testing.T, base, name, prompt string) {
	t.Helper()
	waitFor(t, 20*time.Second, "session input accepted", func() (bool, string) {
		code, body := tryPost(base+"/api/sessions/"+name+"/input", map[string]any{"prompt": prompt})
		return code == 200, fmt.Sprintf("%d %s", code, body)
	})
}

// waitFileContains polls the fs API until the home-relative file contains want.
func waitFileContains(t *testing.T, base, relPath, want string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, relPath+" readable via fs API", func() (bool, string) {
		resp, err := http.Get(base + "/api/fs/file?path=" + relPath)
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return false, fmt.Sprintf("%d %s", resp.StatusCode, truncate(b))
		}
		var out struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(b, &out)
		return strings.Contains(out.Content, want), string(truncate(b))
	})
}

// --- helpers -------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// prereqMissing skips on an unmet prerequisite; CI promotes that to a failure with
// E2E_REQUIRE=1.
func prereqMissing(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv("E2E_REQUIRE") == "1" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

func requireDockerAndImage(t *testing.T, image string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		prereqMissing(t, "docker not on PATH")
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		prereqMissing(t, "workspace image not built: "+image+" (docker build -t "+image+" workspace/)")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file)) // the parent of e2e/ is the repository root
}

// cpBuild is the one CP binary every test of this run starts: scenarios run in parallel,
// and a build per test would compile the CP that many times at once on a small runner.
var cpBuild struct {
	once sync.Once
	dir  string
	bin  string
	err  error
	out  []byte
}

// TestMain removes the shared CP binary after the run.
func TestMain(m *testing.M) {
	code := m.Run()
	if cpBuild.dir != "" {
		_ = os.RemoveAll(cpBuild.dir)
	}
	os.Exit(code)
}

func buildCP(t *testing.T, root string) string {
	t.Helper()
	cpBuild.once.Do(func() {
		if cpBuild.dir, cpBuild.err = os.MkdirTemp("", "af-e2e-cp-"); cpBuild.err != nil {
			return
		}
		cpBuild.bin = filepath.Join(cpBuild.dir, "af-cp")
		cmd := exec.Command("go", "build", "-o", cpBuild.bin, ".")
		cmd.Dir = filepath.Join(root, "control-plane")
		cpBuild.out, cpBuild.err = cmd.CombinedOutput()
	})
	if cpBuild.err != nil {
		t.Fatalf("build control-plane: %v\n%s", cpBuild.err, cpBuild.out)
	}
	return cpBuild.bin
}

// freePort returns a 127.0.0.1 port that is free right now; see freePortRange for where it
// is taken from.
func freePort(t *testing.T) int {
	t.Helper()
	return freePortRange(t, 1)
}

// waitFor polls cond until it is true, and on timeout fails with the last value observed.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, obs := cond()
		if ok {
			t.Logf("ok: %s", desc)
			return
		}
		last = obs
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout (%s) waiting for %s; last: %s", timeout, desc, last)
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, truncate(b))
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("GET %s: bad JSON: %v: %s", url, err, truncate(b))
	}
	return m
}

func postJSON(t *testing.T, url string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	code, b := tryPost(url, body)
	if code != wantStatus {
		t.Fatalf("POST %s: %d (want %d) %s", url, code, wantStatus, b)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(b), &m)
	return m
}

// tryGet returns (status, body) without failing, for retry loops. The body is complete
// so it can be parsed as JSON; pass it through truncate before displaying it.
func tryGet(url string) (int, string) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// tryPost returns (status, body) without failing, for retry loops.
func tryPost(url string, body map[string]any) (int, string) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	resp, err := http.Post(url, "application/json", rd)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(truncate(b))
}

func truncate(b []byte) []byte {
	if len(b) > 400 {
		return append(b[:400:400], []byte("...")...)
	}
	return b
}

// cpEnvAllow is what the test CP may inherit from the shell that runs the tests: what it
// needs to find and reach docker, and locale. Everything else is dropped, because the CP
// reads its deployment from the environment — a developer shell with AF_DATABASE_URL,
// AF_DB_HOST or AF_DB exported would otherwise have the test migrate that database and
// write its tenants and super_admin into it, and AF_RUNTIME, the key custodian or the
// internal listener would point the test at real infrastructure.
var cpEnvAllow = []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "LANG", "XDG_RUNTIME_DIR",
	"SSL_CERT_FILE", "SSL_CERT_DIR"}

// cpEnvAllowPrefix extends cpEnvAllow by prefix (DOCKER_HOST, DOCKER_CONTEXT, LC_ALL, …).
var cpEnvAllowPrefix = []string{"DOCKER_", "LC_"}

// cpEnviron filters an environment (os.Environ's KEY=VAL form) down to cpEnvAllow.
func cpEnviron(parent []string) []string {
	var out []string
	for _, kv := range parent {
		k, _, _ := strings.Cut(kv, "=")
		ok := false
		for _, a := range cpEnvAllow {
			ok = ok || k == a
		}
		for _, p := range cpEnvAllowPrefix {
			ok = ok || strings.HasPrefix(k, p)
		}
		if ok {
			out = append(out, kv)
		}
	}
	return out
}

// teardown reclaims the containers, the networks and the CP process, and dumps the tail of
// the CP log when the test failed. All of it is best-effort: what it leaves behind must
// stay within what the next run or CI's disposable environment can absorb. Reclaiming
// dataDir belongs to cleanupDataDir, registered as soon as the directory exists.
func teardown(t *testing.T, cp *exec.Cmd, logPath string, workspaces []string) {
	t.Helper()
	// Stop the CP first, so the reaper cannot reach back for the container mid-teardown.
	stopCP(cp)
	for _, w := range workspaces {
		_ = exec.Command("docker", "rm", "-f", "af-ws-"+w).Run()
		_ = exec.Command("docker", "network", "rm", "af-net-"+w).Run()
	}
	if t.Failed() {
		if b, err := os.ReadFile(logPath); err == nil {
			if len(b) > 8000 {
				b = b[len(b)-8000:]
			}
			t.Logf("--- control-plane log (tail) ---\n%s", b)
		}
	}
}

// stopCP sends SIGTERM and kills the CP if it has not exited within 10 s.
func stopCP(cp *exec.Cmd) {
	if cp == nil || cp.Process == nil {
		return
	}
	_ = cp.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cp.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cp.Process.Kill()
		<-done
	}
}

// cleanupDataDir reclaims the workspace data dir best-effort. home contains files owned
// by uid 1000 from inside the container, which the runner's uid may be unable to remove,
// so on failure run the image itself as root to rm the contents, then RemoveAll again.
func cleanupDataDir(image, dataDir string) {
	if err := os.RemoveAll(dataDir); err != nil {
		_ = exec.Command("docker", "run", "--rm", "--user", "0",
			"-v", dataDir+":/clean", "--entrypoint", "/bin/sh", image,
			"-c", "rm -rf /clean/* /clean/.[!.]* 2>/dev/null || true").Run()
		_ = os.RemoveAll(dataDir)
	}
}
