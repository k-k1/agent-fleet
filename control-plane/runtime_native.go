// runtime_native.go — ネイティブ（コンテナレス）Runtime アダプタ。Docker が使えない
// 環境（素の WSL2 等）向けに、workspace-agent をホストの子プロセスとして直接起動する。
// docs/34-native-runtime.md 参照。
//
// 割り切り（docs/34 §制約）:
//   - コンテナ隔離が無い。ワークスペース分離は HOME/CLAUDE_CONFIG_DIR/tmux ソケットの
//     論理分離のみなので、単一ユーザー前提の AUTH=dev でしか構築させない（factory が拒否）。
//   - メモリ上限（WS_MEMORY / per-user MemBytes）は強制しない（cgroup を持たない）。
//   - 実行環境（tmux / git / claude 等の CLI / chromium）はホスト側に導入済みであること。
//     Dockerfile / entrypoint.sh 相当の初期化はしない。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// nativeRuntime drives one per-workspace agent PROCESS instead of a container.
// The layout under dataDir mirrors the docker adapter exactly — home/ is the
// process HOME (the docker bind-mount source), claude-config/ is CLAUDE_CONFIG_DIR
// — so a workspace's data is portable between the two local runtimes, and
// cleanHome / stageWorkspaceDocs / dirDiskUsage work unchanged.
type nativeRuntime struct {
	agentBin   string
	name       string // workspace name (af-ws-<key>); doubles as the tmux socket name
	dataDir    string
	agentPort  string
	token      string
	secretKey  string
	sessionCmd string
	extraEnv   []string
}

var _ Runtime = (*nativeRuntime)(nil)

type nativeFactory struct {
	agentBin    string
	sessionCmd  string
	extraEnv    []string
	rootDataDir func(Workspace) string
}

var _ RuntimeFactory = (*nativeFactory)(nil)

// newNativeFactory builds the containerless adapter. Two fail-fast gates:
// the workspace-agent binary must exist on the host (AF_NATIVE_AGENT_BIN or
// PATH), and the deployment must be single-user (AUTH=dev) — without container
// isolation every workspace runs as the same OS user, which is only acceptable
// when that user is the sole operator of the deployment.
func newNativeFactory(m *manager) (RuntimeFactory, error) {
	if m.authMode != "dev" {
		return nil, fmt.Errorf("AF_RUNTIME=native is single-user only (no container isolation); it requires AUTH=dev, got AUTH=%s", m.authMode)
	}
	bin := os.Getenv("AF_NATIVE_AGENT_BIN")
	if bin == "" {
		p, err := exec.LookPath("workspace-agent")
		if err != nil {
			return nil, fmt.Errorf("AF_RUNTIME=native: workspace-agent binary not found (set AF_NATIVE_AGENT_BIN or put workspace-agent on PATH; build it with `go build ./workspace/agent`)")
		}
		bin = p
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return nil, fmt.Errorf("AF_RUNTIME=native: resolve agent binary %q: %w", bin, err)
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return nil, fmt.Errorf("AF_RUNTIME=native: agent binary %q is not an executable file", abs)
	}
	return &nativeFactory{
		agentBin:    abs,
		sessionCmd:  m.sessionCmd,
		extraEnv:    m.extraEnv,
		rootDataDir: m.rootedDataDir,
	}, nil
}

func (f *nativeFactory) New(ws Workspace, secretKey string, extraEnv []string) Runtime {
	env := append(append([]string(nil), f.extraEnv...), extraEnv...)
	return &nativeRuntime{
		agentBin:   f.agentBin,
		name:       ws.ContainerName,
		dataDir:    f.rootDataDir(ws),
		agentPort:  ws.AgentPort,
		token:      ws.AgentToken,
		secretKey:  secretKey,
		sessionCmd: f.sessionCmd,
		extraEnv:   env,
	}
}

// The agent is told to bind loopback-only (AGENT_ADDR below), so the endpoint is
// always the loopback the CP shares with it — never a routable interface.
func (n *nativeRuntime) Endpoint() string { return "http://127.0.0.1:" + n.agentPort }
func (n *nativeRuntime) Token() string    { return n.token }
func (n *nativeRuntime) Name() string     { return n.name }

func (n *nativeRuntime) pidFile() string { return filepath.Join(n.dataDir, "agent.pid") }

// State mirrors the docker adapter's semantics: a live process is "running", a
// stale pidfile (crashed / SIGKILLed agent) is "stopped", and no pidfile at all
// is "none" — Stop removes the pidfile, so the normal stopped state is "none",
// which the Console relies on. Never "starting" (a process spawn is instant).
func (n *nativeRuntime) State(ctx context.Context) string {
	pid := readPidFile(n.pidFile())
	if pid <= 0 {
		return "none"
	}
	if pidAlive(pid, n.agentBin) {
		return "running"
	}
	return "stopped"
}

// Start launches the workspace-agent as a detached host process and waits for it
// to become healthy. The env is built EXPLICITLY rather than inherited: the CP's
// own environment carries deployment secrets (AF_MASTER_KEY, oauth secrets, DB
// URLs) that must never leak into a workspace process that runs arbitrary user
// sessions.
func (n *nativeRuntime) Start(ctx context.Context) error {
	if n.State(ctx) == "running" {
		return nil
	}
	_ = os.Remove(n.pidFile()) // clear a stale (crashed) remnant, like docker rm -f

	home := filepath.Join(n.dataDir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("mkdir data home: %w", err)
	}
	// Same split as the docker adapter: plaintext Claude state lives OUTSIDE the
	// browsable home (docs/17 P3-5 段2).
	claudeCfg := filepath.Join(n.dataDir, "claude-config")
	if err := os.MkdirAll(claudeCfg, 0o700); err != nil {
		return fmt.Errorf("mkdir claude-config: %w", err)
	}

	logf, err := os.OpenFile(filepath.Join(n.dataDir, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open agent.log: %w", err)
	}
	defer logf.Close()

	cmd := exec.Command(n.agentBin)
	cmd.Dir = home
	cmd.Env = n.processEnv(home, claudeCfg)
	cmd.Stdout = logf
	cmd.Stderr = logf
	// New session/process group: the agent must outlive this CP request (and the
	// CP itself — parity with a docker container surviving a CP restart), and
	// Stop kills the whole group so direct children die with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start workspace-agent: %w", err)
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(n.pidFile(), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("write pidfile: %w", err)
	}
	// Reap the child when it exits so it never lingers as a zombie under the CP.
	go func() { _ = cmd.Wait() }()

	if err := waitAgentHealthy(ctx, n.Endpoint(), 15*time.Second); err != nil {
		return fmt.Errorf("%w (see %s)", err, filepath.Join(n.dataDir, "agent.log"))
	}
	return nil
}

// processEnv builds the agent process environment from scratch. Base runtime
// vars first, then the workspace extraEnv (template + per-workspace) so a
// deployment override (e.g. proxy env, GITHUB_OAUTH_CLIENT_ID) wins — flattened
// through a map so no key ever appears twice.
func (n *nativeRuntime) processEnv(home, claudeCfg string) []string {
	env := map[string]string{
		"HOME": home,
		// entrypoint.sh parity: user installs (claude 等) under ~/.local/bin take
		// precedence; the rest of the PATH is the CP host's (tmux/git/node live there).
		"PATH":              filepath.Join(home, ".local", "bin") + ":" + os.Getenv("PATH"),
		"TERM":              "xterm-256color",
		"LANG":              envOr("LANG", "C.UTF-8"),
		"AGENT_ADDR":        "127.0.0.1:" + n.agentPort, // loopback-only bind; docker publishes the same way
		"CLAUDE_CONFIG_DIR": claudeCfg,
		// Scope every tmux invocation to a per-workspace socket so the agent can
		// never touch the host user's own tmux server (tmuxx.Cmd seam).
		"AF_TMUX_SOCKET":       n.name,
		"AGENT_STOP_GRACE_SEC": strconv.Itoa(agentStopGraceSec()),
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		env["USER"] = u.Username
		env["LOGNAME"] = u.Username
	}
	if tz := os.Getenv("TZ"); tz != "" {
		env["TZ"] = tz
	}
	// Role-scoped docs staged by the CP (stageWorkspaceDocs); the agent's fixed
	// container path is overridden via AGENT_DOCS_DIR (agent fs.go — NOT the
	// CP-side AF_DOCS_DIR, which names the staging source).
	if docs := filepath.Join(n.dataDir, "docs"); isDirPath(docs) {
		env["AGENT_DOCS_DIR"] = docs
	}
	if n.token != "" {
		env["AGENT_TOKEN"] = n.token
	}
	if n.secretKey != "" {
		env["AF_SECRET_KEY"] = n.secretKey
	}
	if n.sessionCmd != "" {
		env["AGENT_SESSION_CMD"] = n.sessionCmd
	}
	for _, kv := range n.extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Stop is the same two-stage graceful stop as the docker adapter, hand-rolled:
// SIGTERM to the agent's process group (its shutdown handler Ctrl-C's every live
// pane within AGENT_STOP_GRACE_SEC), SIGKILL past the runtime grace. The tmux
// server daemonizes OUT of the agent's process group, so its scoped socket is
// killed explicitly as a fallback for the SIGKILL path (a graceful agent exit
// has already done this itself).
func (n *nativeRuntime) Stop(ctx context.Context) error {
	pid := readPidFile(n.pidFile())
	if pid > 0 && pidAlive(pid, n.agentBin) {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		deadline := time.Now().Add(time.Duration(stopGraceSec()) * time.Second)
		for pidAlive(pid, n.agentBin) && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		if pidAlive(pid, n.agentBin) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
	// Best-effort: reap a tmux server left behind by a non-graceful agent death.
	// Scoped to this workspace's socket, so other workspaces (and the host user's
	// own tmux) are untouchable by construction.
	_ = exec.CommandContext(ctx, "tmux", "-L", n.name, "kill-server").Run()
	// "normal stopped state is none" — same semantics as docker stop + rm.
	_ = os.Remove(n.pidFile())
	return nil
}

// readPidFile returns the recorded pid, or 0 when absent/garbled.
func readPidFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// pidAlive reports whether pid is a live process AND still our agent binary —
// the /proc cmdline check closes the classic pidfile hazard where the pid was
// recycled by an unrelated process after a crash or host reboot.
func pidAlive(pid int, agentBin string) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// No /proc (e.g. darwin dev host): the liveness signal alone has to do.
		return true
	}
	argv0 := string(b)
	if i := strings.IndexByte(argv0, 0); i >= 0 {
		argv0 = argv0[:i]
	}
	return filepath.Base(argv0) == filepath.Base(agentBin)
}
