// runtime_native.go — ネイティブ（コンテナレス）Runtime アダプタ。Docker が使えない
// 環境（素の WSL2 等）向けに、workspace-agent をホストの子プロセスとして直接起動する。
// docs/34-native-runtime.md 参照。
//
// 割り切り（docs/34 §制約）:
//   - コンテナ隔離が無い。ワークスペース分離は HOME/CLAUDE_CONFIG_DIR/tmux ソケットの
//     論理分離のみなので、単一ユーザー前提の AUTH=dev でしか構築させない（factory が拒否）。
//   - メモリ上限（WS_MEMORY / per-user MemBytes）は強制しない（cgroup を持たない）。
//   - 従来モードでは実行環境（tmux / git / claude 等の CLI / chromium）はホスト側に
//     導入済みであること（Dockerfile / entrypoint.sh 相当の初期化はしない）。rootfs
//     モード（AF_NATIVE_ROOTFS — docs/35 §35.7.2）は workspace イメージの rootfs を
//     bwrap で read-only 実行し、entrypoint 初期化・ピン止めが docker と同等に働く。
package main

import (
	"context"
	"encoding/json"
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
//
// Two launch modes (docs/35 §35.7.2):
//   - traditional (AF_NATIVE_AGENT_BIN): the host-built agent runs directly with
//     HOME pointed at the data dir. Dev workflow (run-dev.sh native).
//   - rootfs (AF_NATIVE_ROOTFS): the agent from an extracted workspace-image
//     rootfs runs under bubblewrap with the rootfs read-only at /, reproducing
//     the docker layout (bind home → /home/dev etc.) without a container engine.
type nativeRuntime struct {
	agentBin   string // traditional: workspace-agent; rootfs mode: the bwrap binary
	rootfs     string // extracted rootfs dir ("" = traditional mode)
	jvmDir     string // optional WS_JVM_DIR → /usr/lib/jvm ro-bind (rootfs mode)
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
	rootfs      string
	jvmDir      string
	sessionCmd  string
	extraEnv    []string
	rootDataDir func(Workspace) string
}

var _ RuntimeFactory = (*nativeFactory)(nil)

// rootfsImageEnvPath is the image ENV manifest the release builder injects into
// the rootfs tar: docker image ENV (PATH, LANG, DISABLE_AUTOUPDATER, …) lives in
// the image CONFIG, not its filesystem, so a plain rootfs export would lose it.
// The rootfs launch rebuilds the env from this file (docs/35 §35.7.2-2).
const rootfsImageEnvPath = "usr/local/share/agent-fleet/image-env.json"

// newNativeFactory builds the containerless adapter. Fail-fast gates: the
// deployment must be single-user (AUTH=dev) — without container isolation every
// workspace runs as the same OS user, which is only acceptable when that user is
// the sole operator — and the launch prerequisites must exist: in rootfs mode a
// complete extracted rootfs plus a bwrap binary, otherwise a host workspace-agent
// (AF_NATIVE_AGENT_BIN or PATH).
func newNativeFactory(m *manager) (RuntimeFactory, error) {
	if m.authMode != "dev" {
		return nil, fmt.Errorf("AF_RUNTIME=native is single-user only (no container isolation); it requires AUTH=dev, got AUTH=%s", m.authMode)
	}
	if rootfs := os.Getenv("AF_NATIVE_ROOTFS"); rootfs != "" {
		abs, err := filepath.Abs(rootfs)
		if err != nil {
			return nil, fmt.Errorf("AF_NATIVE_ROOTFS: resolve %q: %w", rootfs, err)
		}
		for _, rel := range []string{
			"usr/local/bin/workspace-agent",
			"usr/local/bin/entrypoint.sh",
			rootfsImageEnvPath,
		} {
			if fi, err := os.Stat(filepath.Join(abs, rel)); err != nil || fi.IsDir() {
				return nil, fmt.Errorf("AF_NATIVE_ROOTFS: %s missing under %s (incomplete rootfs extraction?)", rel, abs)
			}
		}
		// Bind mountpoints must be baked into the rootfs: the / ro-bind makes the
		// tree read-only, so bwrap cannot mkdir them at launch (docker created
		// them implicitly for -v). A rootfs from an image predating the mkdir
		// would fail inside bwrap with a cryptic error — fail fast here instead.
		for _, rel := range []string{"home/dev", "var/lib/af/claude", "usr/local/share/agent-fleet/docs"} {
			if fi, err := os.Stat(filepath.Join(abs, rel)); err != nil || !fi.IsDir() {
				return nil, fmt.Errorf("AF_NATIVE_ROOTFS: bind mountpoint %s missing under %s (rootfs built from an old workspace image?)", rel, abs)
			}
		}
		bwrap := os.Getenv("AF_NATIVE_BWRAP")
		if bwrap == "" {
			p, err := exec.LookPath("bwrap")
			if err != nil {
				return nil, fmt.Errorf("AF_NATIVE_ROOTFS: bwrap not found (set AF_NATIVE_BWRAP or install bubblewrap)")
			}
			bwrap = p
		}
		bwrapAbs, err := filepath.Abs(bwrap)
		if err != nil {
			return nil, fmt.Errorf("AF_NATIVE_BWRAP: resolve %q: %w", bwrap, err)
		}
		if fi, err := os.Stat(bwrapAbs); err != nil || fi.IsDir() {
			return nil, fmt.Errorf("AF_NATIVE_BWRAP: %q is not an executable file", bwrapAbs)
		}
		return &nativeFactory{
			agentBin:    bwrapAbs,
			rootfs:      abs,
			jvmDir:      os.Getenv("WS_JVM_DIR"),
			sessionCmd:  m.sessionCmd,
			extraEnv:    m.extraEnv,
			rootDataDir: m.rootedDataDir,
		}, nil
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
		rootfs:     f.rootfs,
		jvmDir:     f.jvmDir,
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

	var cmd *exec.Cmd
	if n.rootfs != "" {
		env, err := n.rootfsEnv()
		if err != nil {
			return err
		}
		cmd = exec.Command(n.agentBin, n.bwrapArgs(home, claudeCfg)...)
		cmd.Env = env
	} else {
		cmd = exec.Command(n.agentBin)
		cmd.Dir = home
		cmd.Env = n.processEnv(home, claudeCfg)
	}
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

	// Rootfs mode gets a much larger default budget: the FIRST start runs the
	// entrypoint's pinned boot-install (npm + GitHub downloads, minutes) before
	// the agent ever listens. Overridable either way (AF_AGENT_HEALTH_WAIT_SEC).
	healthWait := agentHealthWait(15 * time.Second)
	if n.rootfs != "" {
		healthWait = agentHealthWait(300 * time.Second)
	}
	if err := waitAgentHealthy(ctx, n.Endpoint(), healthWait); err != nil {
		return fmt.Errorf("%w (see %s)", err, filepath.Join(n.dataDir, "agent.log"))
	}
	return nil
}

// bwrapArgs assembles the frozen bwrap invocation (docs/35 §35.7.2): the rootfs
// read-only at /, the docker bind-mount layout reproduced onto container paths,
// a single-uid userns mapping to the container's dev uid, and an unshared pid
// namespace so a group SIGKILL of bwrap tears down every descendant (tmux
// included) with the namespace. net/uts/ipc stay SHARED — the agent must bind
// the host loopback the CP connects to.
func (n *nativeRuntime) bwrapArgs(home, claudeCfg string) []string {
	args := []string{
		"--ro-bind", n.rootfs, "/",
		"--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--tmpfs", "/run",
		"--bind", home, "/home/dev",
		"--bind", claudeCfg, "/var/lib/af/claude",
	}
	// Role-scoped docs staged by the CP land on the agent's DEFAULT container
	// path, so no AGENT_DOCS_DIR override is needed in this mode.
	if docs := filepath.Join(n.dataDir, "docs"); isDirPath(docs) {
		args = append(args, "--ro-bind", docs, "/usr/local/share/agent-fleet/docs")
	}
	// Shared host JDKs (docker parity: WS_JVM_DIR → /usr/lib/jvm read-only).
	if n.jvmDir != "" && isDirPath(n.jvmDir) {
		args = append(args, "--ro-bind", n.jvmDir, "/usr/lib/jvm")
	}
	// Host DNS/hosts pass through read-only (docker copies them into containers).
	for _, f := range []string{"/etc/resolv.conf", "/etc/hosts"} {
		if _, err := os.Stat(f); err == nil {
			args = append(args, "--ro-bind", f, f)
		}
	}
	return append(args,
		"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--die-with-parent",
		"--chdir", "/home/dev",
		"/usr/local/bin/entrypoint.sh", "workspace-agent",
	)
}

// rootfsEnv builds the agent environment for the rootfs launch: the image ENV
// manifest (baked into the rootfs by the release builder — docker keeps ENV in
// the image config, which a filesystem export loses) as the base, the runtime
// vars valued with CONTAINER paths on top, then the workspace extraEnv.
func (n *nativeRuntime) rootfsEnv() ([]string, error) {
	b, err := os.ReadFile(filepath.Join(n.rootfs, rootfsImageEnvPath))
	if err != nil {
		return nil, fmt.Errorf("read rootfs image env: %w", err)
	}
	var imageEnv []string
	if err := json.Unmarshal(b, &imageEnv); err != nil {
		return nil, fmt.Errorf("parse rootfs image env: %w", err)
	}
	env := map[string]string{}
	for _, kv := range imageEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	env["HOME"] = "/home/dev"
	env["USER"] = "dev"
	env["LOGNAME"] = "dev"
	env["TERM"] = "xterm-256color"
	env["AGENT_ADDR"] = "127.0.0.1:" + n.agentPort // loopback-only bind, shared net ns
	env["CLAUDE_CONFIG_DIR"] = "/var/lib/af/claude"
	env["AF_TMUX_SOCKET"] = n.name
	env["AGENT_STOP_GRACE_SEC"] = strconv.Itoa(agentStopGraceSec())
	n.overlayWorkspaceEnv(env)
	return flattenEnv(env), nil
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
	n.overlayWorkspaceEnv(env)
	return flattenEnv(env)
}

// overlayWorkspaceEnv adds the per-workspace vars (token, DEK, session command)
// and finally the deployment/workspace extraEnv, which wins on key collisions.
func (n *nativeRuntime) overlayWorkspaceEnv(env map[string]string) {
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
}

// flattenEnv renders the env map as a sorted KEY=VALUE list (deterministic, no
// duplicate keys — getenv would keep the first/stale one otherwise).
func flattenEnv(env map[string]string) []string {
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
	// own tmux) are untouchable by construction. Rootfs mode needs none of this:
	// the socket lives on the namespace-private /tmp, and killing bwrap (the pid-
	// namespace owner) already took the tmux server down with the namespace.
	if n.rootfs == "" {
		_ = exec.CommandContext(ctx, "tmux", "-L", n.name, "kill-server").Run()
	}
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
