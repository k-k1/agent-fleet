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
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

	root := repoRoot(t)
	tmp := t.TempDir() // CP binary and logs; no container touches it, so auto-removal is safe
	cpBin := buildCP(t, root, tmp)

	// Workspace data is written by uid 1000 inside the container, so it cannot go through
	// t.TempDir, whose removal failure fails the test. Reclaim it best-effort instead.
	dataDir, err := os.MkdirTemp("", "af-e2e-data-")
	if err != nil {
		t.Fatal(err)
	}
	// Register the reclaim immediately after creating it, so a Fatal before cp.Start
	// still leaves nothing behind in /tmp. Cleanup is LIFO, so the teardown registered
	// later (stop CP, reclaim container) runs first, which is the order required here.
	t.Cleanup(func() { cleanupDataDir(image, dataDir) })
	// Where the host uid differs from the container's dev user (uid 1000) — GitHub
	// runners, for one — home and claude-config created 0755 by the CP are not writable
	// from inside the container, the entrypoint dies under set -e, and the Agent never
	// reaches healthz. Pre-create the mount points 0777; the CP's MkdirAll passes over an
	// existing dir, so this is inert on a uid-1000 host. The path shape is
	// manager.rootedDataDir's default tenant layout <WS_DATA>/<user>/{home,claude-config}.
	for _, d := range []string{"home", "claude-config"} {
		p := filepath.Join(dataDir, user, d)
		if err := os.MkdirAll(p, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o777); err != nil { // umask trims what MkdirAll asked for
			t.Fatal(err)
		}
	}

	cpAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	agentPort := freePort(t)
	logPath := filepath.Join(tmp, "cp.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() }) // close the fd on every exit path

	cp := exec.Command(cpBin)
	cp.Dir = tmp
	cp.Stdout = logFile
	cp.Stderr = logFile
	cp.Env = append(append(os.Environ(),
		"CP_ADDR="+cpAddr,
		"WS_IMAGE="+image,
		"WS_DATA="+dataDir,
		"DEV_USER="+user,
		fmt.Sprintf("WS_AGENT_PORT=%d", agentPort),
		"CONSOLE_DIR="+tmp, // static serving is out of scope; point at a dir that exists
	), extraEnv...)
	if err := cp.Start(); err != nil {
		t.Fatalf("start CP: %v", err)
	}
	t.Cleanup(func() { teardown(t, cp, logPath, user) })

	base := "http://" + cpAddr
	waitFor(t, 15*time.Second, "CP /healthz", func() (bool, string) {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			return false, err.Error()
		}
		resp.Body.Close()
		return resp.StatusCode == 200, resp.Status
	})

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

func buildCP(t *testing.T, root, tmp string) string {
	t.Helper()
	bin := filepath.Join(tmp, "af-cp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join(root, "control-plane")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build control-plane: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
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

// teardown reclaims the container, the network and the CP process, and dumps the tail of
// the CP log when the test failed. All of it is best-effort: what it leaves behind must
// stay within what the next run or CI's disposable environment can absorb. Reclaiming
// dataDir belongs to cleanupDataDir, registered as soon as the directory exists.
func teardown(t *testing.T, cp *exec.Cmd, logPath, user string) {
	t.Helper()
	// Stop the CP first, so the reaper cannot reach back for the container mid-teardown.
	if cp.Process != nil {
		_ = cp.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = cp.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cp.Process.Kill()
		}
	}
	_ = exec.Command("docker", "rm", "-f", "af-ws-"+user).Run()
	_ = exec.Command("docker", "network", "rm", "af-net-"+user).Run()
	if t.Failed() {
		if b, err := os.ReadFile(logPath); err == nil {
			if len(b) > 8000 {
				b = b[len(b)-8000:]
			}
			t.Logf("--- control-plane log (tail) ---\n%s", b)
		}
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
