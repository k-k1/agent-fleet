package runtime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestNativeHelperAgent is not a test: it is the fake workspace-agent the native
// runtime tests spawn (helper-process pattern). The lifecycle test writes a
// bash launcher named "workspace-agent" that re-execs THIS test binary with
// GO_WANT_HELPER_PROCESS=1 and -test.run pinned here. It honors just enough of
// the agent contract for the adapter: binds AGENT_ADDR, answers /healthz 200,
// dumps its env to $HOME/env.dump, and exits 0 on SIGTERM (graceful stop).
func TestNativeHelperAgent(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if home := os.Getenv("HOME"); home != "" {
		_ = os.WriteFile(filepath.Join(home, "env.dump"), []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o644)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	go func() {
		<-sig
		if ms, _ := strconv.Atoi(os.Getenv("AF_TEST_TERM_DELAY_MS")); ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		os.Exit(0)
	}()
	if ms, _ := strconv.Atoi(os.Getenv("AF_TEST_HEALTH_DELAY_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := http.ListenAndServe(os.Getenv("AGENT_ADDR"), mux); err != nil {
		os.Exit(1)
	}
}

func newTraditionalNativeRuntime(t *testing.T, extraEnv ...string) *nativeRuntime {
	t.Helper()
	dataDir := t.TempDir()
	return &nativeRuntime{
		agentBin:  writeFakeAgent(t, t.TempDir()),
		name:      "af-native-fence-" + newTestID(),
		dataDir:   dataDir,
		agentPort: freeLoopbackPort(t),
		extraEnv:  extraEnv,
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pid := readPidFile(path); pid > 0 {
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pidfile was not written: %s", path)
	return 0
}

func TestNativeStartCancellationQuiescesExactSpawn(t *testing.T) {
	rt := newTraditionalNativeRuntime(t, "AF_TEST_HEALTH_DELAY_MS=1000")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Start(ctx) }()
	pid := waitForPIDFile(t, rt.pidFile())
	startID := nativeProcessStartID(pid)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Start returned nil after context cancellation")
	}
	if sameNativeProcess(pid, rt.agentBin, startID) {
		t.Fatalf("canceled Start left its exact process %d running", pid)
	}
	if pid := readPidFile(rt.pidFile()); pid != 0 {
		t.Fatalf("pidfile remains after canceled Start: %d", pid)
	}
	rt.spawnMu.Lock()
	spawn := rt.spawned
	rt.spawnMu.Unlock()
	if spawn.pid != 0 {
		t.Fatalf("uncommitted spawn remains after cancellation: %+v", spawn)
	}
}

func TestNativeStopCancellationWaitsForQuiescenceWithoutCleanup(t *testing.T) {
	rt := newTraditionalNativeRuntime(t, "AF_TEST_TERM_DELAY_MS=300")
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := waitForPIDFile(t, rt.pidFile())
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	started := time.Now()
	if err := rt.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Fatalf("Stop returned before the SIGTERM process quiesced: %v", elapsed)
	}
	if got := readPidFile(rt.pidFile()); got != pid {
		t.Fatalf("canceled Stop mutated pidfile: got %d, want %d", got, pid)
	}
	if sameNativeProcess(pid, rt.agentBin, "") {
		t.Fatalf("old process %d still live when canceled Stop returned", pid)
	}
}

func TestNativeOperationFenceWaitsForOldHolderQuiescence(t *testing.T) {
	rt := newTraditionalNativeRuntime(t)
	releaseA, err := rt.AcquireOperationFence(context.Background())
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	acquiredB := make(chan func(), 1)
	go func() {
		release, err := rt.AcquireOperationFence(context.Background())
		if err == nil {
			acquiredB <- release
		}
	}()
	select {
	case release := <-acquiredB:
		release()
		t.Fatal("second holder crossed the first native fence")
	case <-time.After(100 * time.Millisecond):
	}
	releaseA()
	select {
	case release := <-acquiredB:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("second holder did not acquire after old holder quiesced")
	}
}

// writeFakeAgent writes the bash launcher the factory will treat as the
// workspace-agent binary. `exec -a workspace-agent` keeps argv[0] stable so
// pidAlive's /proc cmdline check matches the recorded binary name.
func writeFakeAgent(t *testing.T, dir string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bin := filepath.Join(dir, "workspace-agent")
	script := "#!/bin/bash\nGO_WANT_HELPER_PROCESS=1 exec -a workspace-agent \"" + self + "\" -test.run='^TestNativeHelperAgent$'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	return bin
}

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// The native factory is single-user only (no container isolation): any auth
// mode but dev must fail at boot, and a missing agent binary must fail fast
// rather than surfacing at the first Start.
func TestNativeFactoryGates(t *testing.T) {
	bin := writeFakeAgent(t, t.TempDir())
	t.Setenv("AF_NATIVE_AGENT_BIN", bin)
	for _, mode := range []string{"proxy", "oauth"} {
		if _, err := NewFactory("native", Config{AuthMode: mode, RootDataDir: StaticRootDataDir(t.TempDir(), "")}); err == nil {
			t.Errorf("AUTH=%s: expected error, got nil", mode)
		}
	}
	t.Setenv("AF_NATIVE_AGENT_BIN", filepath.Join(t.TempDir(), "missing"))
	if _, err := NewFactory("wsl", Config{AuthMode: "dev", RootDataDir: StaticRootDataDir(t.TempDir(), "")}); err == nil {
		t.Error("missing agent binary: expected error, got nil")
	}
}

// writeFakeRootfs lays out the minimum tree the rootfs-mode factory gates check,
// with a known image-env manifest (the release builder injects the real one).
func writeFakeRootfs(t *testing.T) string {
	t.Helper()
	rootfs := t.TempDir()
	for _, rel := range []string{"usr/local/bin/workspace-agent", "usr/local/bin/entrypoint.sh"} {
		p := filepath.Join(rootfs, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	envPath := filepath.Join(rootfs, "usr/local/share/agent-fleet/image-env.json")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatal(err)
	}
	imageEnv := `["PATH=/home/dev/.local/bin:/usr/local/bin:/usr/bin:/bin","LANG=C.UTF-8","DISABLE_AUTOUPDATER=1"]`
	if err := os.WriteFile(envPath, []byte(imageEnv+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Bind mountpoints the factory verifies (baked by the workspace Dockerfile).
	for _, rel := range []string{"home/dev", "var/lib/af/claude", "usr/local/share/agent-fleet/docs"} {
		if err := os.MkdirAll(filepath.Join(rootfs, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return rootfs
}

// writeFakeBwrap writes a stand-in bwrap that records its argv and env into
// dumpDir, then execs the helper agent — so the lifecycle test exercises the
// real Start/Stop paths while pinning the frozen bwrap invocation.
func writeFakeBwrap(t *testing.T, dumpDir string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "bwrap")
	script := "#!/bin/bash\n" +
		"printf '%s\\n' \"$@\" > \"" + dumpDir + "/args.dump\"\n" +
		"env > \"" + dumpDir + "/env.dump\"\n" +
		// The helper dumps ITS env to $HOME/env.dump — give it a private subdir so
		// it neither writes to /home/dev nor clobbers the dump captured above.
		"mkdir -p \"" + dumpDir + "/helper-home\" && export HOME=\"" + dumpDir + "/helper-home\"\n" +
		"GO_WANT_HELPER_PROCESS=1 exec -a bwrap \"" + self + "\" -test.run='^TestNativeHelperAgent$'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bwrap: %v", err)
	}
	return bin
}

// Rootfs-mode factory gates: an incomplete rootfs (missing agent, entrypoint or
// image-env manifest) and a missing bwrap must all fail at boot, not at Start.
func TestNativeRootfsFactoryGates(t *testing.T) {
	dump := t.TempDir()
	bwrap := writeFakeBwrap(t, dump)

	t.Setenv("AF_NATIVE_ROOTFS", t.TempDir()) // empty dir: no agent/entrypoint/env
	t.Setenv("AF_NATIVE_BWRAP", bwrap)
	if _, err := NewFactory("native", Config{AuthMode: "dev", RootDataDir: StaticRootDataDir(t.TempDir(), "")}); err == nil {
		t.Error("incomplete rootfs: expected error, got nil")
	}

	rootfs := writeFakeRootfs(t)
	t.Setenv("AF_NATIVE_ROOTFS", rootfs)
	t.Setenv("AF_NATIVE_BWRAP", filepath.Join(t.TempDir(), "missing-bwrap"))
	if _, err := NewFactory("native", Config{AuthMode: "dev", RootDataDir: StaticRootDataDir(t.TempDir(), "")}); err == nil {
		t.Error("missing bwrap: expected error, got nil")
	}

	t.Setenv("AF_NATIVE_BWRAP", bwrap)
	if _, err := NewFactory("native", Config{AuthMode: "oauth", RootDataDir: StaticRootDataDir(t.TempDir(), "")}); err == nil {
		t.Error("AUTH=oauth: expected error, got nil")
	}
	if _, err := NewFactory("native", Config{AuthMode: "dev", RootDataDir: StaticRootDataDir(t.TempDir(), "")}); err != nil {
		t.Errorf("complete rootfs + bwrap: unexpected error: %v", err)
	}
}

// Rootfs-mode lifecycle: Start must invoke bwrap with the frozen argv (ro-bind
// rootfs at /, docker-layout binds, single-uid userns, pid unshare, entrypoint
// command) and an env rebuilt from the image manifest with container paths —
// and the State/Stop semantics must match the traditional mode.
func TestNativeRootfsLifecycle(t *testing.T) {
	dataRoot := t.TempDir()
	dump := t.TempDir()
	rootfs := writeFakeRootfs(t)
	t.Setenv("AF_NATIVE_ROOTFS", rootfs)
	t.Setenv("AF_NATIVE_BWRAP", writeFakeBwrap(t, dump))
	t.Setenv("WS_JVM_DIR", "") // keep the optional jvm bind out of the golden argv
	t.Setenv("AF_MASTER_KEY", "cp-deployment-secret")

	m := Config{AuthMode: "dev", RootDataDir: StaticRootDataDir(dataRoot, ""), ExtraEnv: []string{"GITHUB_OAUTH_CLIENT_ID=cid123"}}
	f, err := NewFactory("native", m)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ws := Workspace{
		ContainerName: "af-ws-rootfs",
		DataDir:       filepath.Join(dataRoot, "rootfs-ws"),
		AgentPort:     freeLoopbackPort(t),
		AgentToken:    "tok-rootfs",
	}
	rt := f.New(ws, "dek-rootfs", nil)
	ctx := context.Background()
	t.Cleanup(func() { _ = rt.Stop(ctx) })

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := rt.State(ctx); got != "running" {
		t.Fatalf("post-start State = %q, want running", got)
	}

	args, err := os.ReadFile(filepath.Join(dump, "args.dump"))
	if err != nil {
		t.Fatalf("read args.dump: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(args)), "\n")
	home := filepath.Join(ws.DataDir, "home")
	claudeCfg := filepath.Join(ws.DataDir, "claude-config")
	wantSeq := []string{
		"--ro-bind", rootfs, "/",
		"--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--tmpfs", "/run",
		"--bind", home, "/home/dev",
		"--bind", claudeCfg, "/var/lib/af/claude",
	}
	for i, want := range wantSeq {
		if i >= len(argv) {
			t.Fatalf("bwrap argv too short (%d items): %q", len(argv), argv)
		}
		if argv[i] != want {
			t.Fatalf("bwrap argv[%d] = %q, want %q (argv=%q)", i, argv[i], want, argv)
		}
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--unshare-user --uid 1000 --gid 1000",
		"--unshare-pid --die-with-parent",
		"--chdir /home/dev",
		"/usr/local/bin/entrypoint.sh workspace-agent",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("bwrap argv missing %q (argv=%q)", want, joined)
		}
	}

	envB, err := os.ReadFile(filepath.Join(dump, "env.dump"))
	if err != nil {
		t.Fatalf("read env.dump: %v", err)
	}
	env := string(envB)
	for _, want := range []string{
		"PATH=/home/dev/.local/bin:/usr/local/bin:/usr/bin:/bin\n", // from image-env.json
		"DISABLE_AUTOUPDATER=1\n",                                  // image config ENV survives export
		"HOME=/home/dev\n",                                         // container path, not the host data dir
		"CLAUDE_CONFIG_DIR=/var/lib/af/claude\n",
		"AGENT_TOKEN=tok-rootfs\n",
		"AF_SECRET_KEY=dek-rootfs\n",
		"AF_TMUX_SOCKET=af-ws-rootfs\n",
		"GITHUB_OAUTH_CLIENT_ID=cid123\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("bwrap env missing %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(env, "AF_MASTER_KEY") {
		t.Error("bwrap env leaked AF_MASTER_KEY from the CP environment")
	}
	if strings.Contains(env, "AGENT_DOCS_DIR") {
		t.Error("rootfs mode must not set AGENT_DOCS_DIR (docs ro-bind onto the default path)")
	}

	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := rt.State(ctx); got != "none" {
		t.Fatalf("post-stop State = %q, want none", got)
	}
}

// Full process lifecycle through the Runtime port: none → Start (spawn, pidfile,
// healthz) → running → Stop (SIGTERM, pidfile removed) → none; plus the crash
// path (SIGKILL from outside) reporting "stopped" off the stale pidfile.
func TestNativeRuntimeLifecycle(t *testing.T) {
	dataRoot := t.TempDir()
	bin := writeFakeAgent(t, t.TempDir())
	t.Setenv("AF_NATIVE_AGENT_BIN", bin)
	// The CP's own secrets must never reach the workspace process env.
	t.Setenv("AF_MASTER_KEY", "cp-deployment-secret")

	m := Config{AuthMode: "dev", RootDataDir: StaticRootDataDir(dataRoot, ""), ExtraEnv: []string{"GITHUB_OAUTH_CLIENT_ID=cid123"}}
	f, err := NewFactory("native", m)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ws := Workspace{
		ContainerName: "af-ws-testnat",
		DataDir:       filepath.Join(dataRoot, "testnat"),
		AgentPort:     freeLoopbackPort(t),
		AgentToken:    "tok-native",
	}
	rt := f.New(ws, "dek-native", []string{"AF_AGENT_SELF_UPDATE_ALLOWED=1"})
	ctx := context.Background()
	t.Cleanup(func() { _ = rt.Stop(ctx) })

	if got := rt.State(ctx); got != "none" {
		t.Fatalf("pre-start State = %q, want none", got)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := rt.State(ctx); got != "running" {
		t.Fatalf("post-start State = %q, want running", got)
	}
	// Idempotent Start while running (docker parity).
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	// The helper dumped its env: runtime vars in, CP deployment secrets out.
	dump, err := os.ReadFile(filepath.Join(ws.DataDir, "home", "env.dump"))
	if err != nil {
		t.Fatalf("read env.dump: %v", err)
	}
	env := string(dump)
	for _, want := range []string{
		"HOME=" + filepath.Join(ws.DataDir, "home") + "\n",
		"CLAUDE_CONFIG_DIR=" + filepath.Join(ws.DataDir, "claude-config") + "\n",
		"AGENT_TOKEN=tok-native\n",
		"AF_SECRET_KEY=dek-native\n",
		"AF_TMUX_SOCKET=af-ws-testnat\n",
		"GITHUB_OAUTH_CLIENT_ID=cid123\n",
		"AF_AGENT_SELF_UPDATE_ALLOWED=1\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("agent env missing %q", strings.TrimSpace(want))
		}
	}
	if strings.Contains(env, "AF_MASTER_KEY") {
		t.Error("agent env leaked AF_MASTER_KEY from the CP environment")
	}

	pid := readPidFile(filepath.Join(ws.DataDir, "agent.pid"))
	if pid <= 0 {
		t.Fatalf("pidfile not written")
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := rt.State(ctx); got != "none" {
		t.Fatalf("post-stop State = %q, want none (pidfile removed)", got)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("agent process %d still alive after Stop", pid)
	}

	// Crash path: an externally SIGKILLed agent leaves a stale pidfile → "stopped";
	// the next Start clears it and relaunches.
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	pid = readPidFile(filepath.Join(ws.DataDir, "agent.pid"))
	_ = syscall.Kill(pid, syscall.SIGKILL)
	for i := 0; i < 50 && rt.State(ctx) == "running"; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if got := rt.State(ctx); got != "stopped" {
		t.Fatalf("post-crash State = %q, want stopped (stale pidfile)", got)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start after crash: %v", err)
	}
	if got := rt.State(ctx); got != "running" {
		t.Fatalf("State after crash restart = %q, want running", got)
	}
}

// mirrorBootProgress publishes the latest [entrypoint] line (sans prefix) to the
// .boot-phase file for BootPhase(), and clears it when the boot ends (docs/log/35
// §35.9-9). The Console "starting" dialog reads this via GET /api/workspace.
func TestNativeBootPhase(t *testing.T) {
	dir := t.TempDir()
	n := &nativeRuntime{name: "af-ws-bp", dataDir: dir}

	if got := n.BootPhase(); got != "" {
		t.Fatalf("BootPhase with no file = %q, want empty", got)
	}

	// A log line without the marker must NOT publish a phase; the marker line must.
	logPath := filepath.Join(dir, "agent.log")
	if err := os.WriteFile(logPath,
		[]byte("some noise\n[entrypoint] boot-install (pinned): claude-code@2.1.215\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go n.mirrorBootProgress(ctx, 0)

	want := "boot-install (pinned): claude-code@2.1.215"
	deadline := time.Now().Add(3 * time.Second)
	for n.BootPhase() != want {
		if time.Now().After(deadline) {
			t.Fatalf("BootPhase never became %q (got %q)", want, n.BootPhase())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Ending the mirror (agent healthy) clears the phase so the dialog closes.
	cancel()
	deadline = time.Now().Add(3 * time.Second)
	for n.BootPhase() != "" {
		if time.Now().After(deadline) {
			t.Fatalf("BootPhase not cleared after mirror stopped (got %q)", n.BootPhase())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
