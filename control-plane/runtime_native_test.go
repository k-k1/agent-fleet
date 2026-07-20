package main

import (
	"context"
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
	go func() { <-sig; os.Exit(0) }()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := http.ListenAndServe(os.Getenv("AGENT_ADDR"), mux); err != nil {
		os.Exit(1)
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
		if _, err := newRuntimeFactory("native", &manager{authMode: mode, dataRoot: t.TempDir()}); err == nil {
			t.Errorf("AUTH=%s: expected error, got nil", mode)
		}
	}
	t.Setenv("AF_NATIVE_AGENT_BIN", filepath.Join(t.TempDir(), "missing"))
	if _, err := newRuntimeFactory("wsl", &manager{authMode: "dev", dataRoot: t.TempDir()}); err == nil {
		t.Error("missing agent binary: expected error, got nil")
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

	m := &manager{authMode: "dev", dataRoot: dataRoot, extraEnv: []string{"GITHUB_OAUTH_CLIENT_ID=cid123"}}
	f, err := newRuntimeFactory("native", m)
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
