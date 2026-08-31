// runtime_native.go — ネイティブ（コンテナレス）Runtime アダプタ。Docker が使えない
// 環境（素の WSL2 等）向けに、workspace-agent をホストの子プロセスとして直接起動する。
// docs/log/34-native-runtime.md 参照。
//
// 割り切り（docs/log/34 §制約）:
//   - コンテナ隔離が無い。ワークスペース分離は HOME/CLAUDE_CONFIG_DIR/tmux ソケットの
//     論理分離のみなので、単一ユーザー前提の AUTH=dev でしか構築させない（factory が拒否）。
//   - メモリ上限（WS_MEMORY / per-user MemBytes）は強制しない（cgroup を持たない）。
//   - 従来モードでは実行環境（tmux / git / claude 等の CLI / chromium）はホスト側に
//     導入済みであること（Dockerfile / entrypoint.sh 相当の初期化はしない）。rootfs
//     モード（AF_NATIVE_ROOTFS — docs/log/35 §35.7.2）は workspace イメージの rootfs を
//     bwrap で read-only 実行し、entrypoint 初期化・ピン止めが docker と同等に働く。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// nativeRuntime drives one per-workspace agent PROCESS instead of a container.
// The layout under dataDir mirrors the docker adapter exactly — home/ is the
// process HOME (the docker bind-mount source), claude-config/ is CLAUDE_CONFIG_DIR
// — so a workspace's data is portable between the two local runtimes, and
// cleanHome / stageWorkspaceDocs / dirDiskUsage work unchanged.
//
// Two launch modes (docs/log/35 §35.7.2):
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
	spawnMu    sync.Mutex
	spawned    nativeSpawn
}

type nativeSpawn struct {
	pid     int
	startID string
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
// The rootfs launch rebuilds the env from this file (docs/log/35 §35.7.2-2).
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

func (n *nativeRuntime) lifecycleLockFile() string {
	return filepath.Join(n.dataDir, "lifecycle.lock")
}

// AcquireOperationFence complements the renewable DB lease with a kernel-held
// host fence. If a CP is paused past lease expiry, the new DB holder still waits
// here until the old native Start/Stop has quiesced.
func (n *nativeRuntime) AcquireOperationFence(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(n.dataDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(n.lifecycleLockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					_ = f.Close()
				})
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// State mirrors the docker adapter's semantics: a live process is "running", a
// stale pidfile (crashed / SIGKILLed agent) is "stopped", and no pidfile at all
// is "none" — Stop removes the pidfile, so the normal stopped state is "none",
// which the Console relies on.
//
// A spawn is instant but READINESS is not: on the rootfs path the entrypoint runs
// the pinned boot-install (minutes) before the agent listens, and pid-alive would
// call that "running" (docs/log/35 §35.9-9). So the boot window is reported as
// "starting" while Start's marker is armed — same contract as the docker adapter.
func (n *nativeRuntime) State(ctx context.Context) string {
	pid := readPidFile(n.pidFile())
	if pid <= 0 {
		return "none"
	}
	if pidAlive(pid, n.agentBin) {
		if n.startingMarker().active(ctx, n.Endpoint()) {
			return "starting"
		}
		return "running"
	}
	return "stopped"
}

func (n *nativeRuntime) startingMarker() agentStartingMarker {
	return agentStartingMarkerIn(n.dataDir)
}

// Start launches the workspace-agent as a detached host process and waits for it
// to become healthy. The env is built EXPLICITLY rather than inherited: the CP's
// own environment carries deployment secrets (AF_MASTER_KEY, oauth secrets, DB
// URLs) that must never leak into a workspace process that runs arbitrary user
// sessions.
// mountsStagedDocs: native ro-binds (or points AGENT_DOCS_DIR at) <dataDir>/docs, so it
// needs the same per-start staging as docker (runtime.go runtimeDocsMounter).
func (n *nativeRuntime) mountsStagedDocs() {}

func (n *nativeRuntime) Start(ctx context.Context) (retErr error) {
	switch n.State(ctx) {
	case "running":
		return nil
	case "starting":
		// A boot is already converging (the agent process is alive, /healthz is not
		// answering yet). Re-spawning would kill a legitimate first start mid
		// boot-install; the marker is time-boxed so this cannot wedge.
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
	spawn := nativeSpawn{pid: pid, startID: nativeProcessStartID(pid)}
	n.spawnMu.Lock()
	n.spawned = spawn
	n.spawnMu.Unlock()
	// Reap from the moment Start succeeds, including pidfile-write failures.
	go func() { _ = cmd.Wait() }()
	defer func() {
		if retErr != nil {
			n.abortSpawn(spawn)
		}
	}()
	if err := os.WriteFile(n.pidFile(), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write pidfile: %w", err)
	}
	// Record what this process was spawned FROM, so Stale() can later tell that the
	// deployment moved on (af update / a rebuild) while this process still runs the
	// old code. Best-effort: a missing stamp just means "unknown" (never stale).
	_ = os.WriteFile(n.binStampPath(), []byte(n.spawnStamp()), 0o644)
	// Rootfs mode gets a much larger default budget: the FIRST start runs the
	// entrypoint's pinned boot-install (npm + GitHub downloads, minutes) before
	// the agent ever listens. Overridable either way (AF_AGENT_HEALTH_WAIT_SEC).
	healthWait := agentHealthWait(15 * time.Second)
	if n.rootfs != "" {
		healthWait = agentHealthWait(300 * time.Second)
		// The entrypoint's boot-install writes only to agent.log, which nothing
		// surfaces during Start — so a first-start operator sees a long, silent
		// wait and can wrongly conclude the CLIs were baked / no install ran
		// (docs/log/35 §35.9-9). Mirror the entrypoint's own [entrypoint] progress
		// lines (boot-install, install-go/jdk, …) to the CP log — visible in the
		// `af start` terminal — for the duration of this wait. Best-effort and
		// read-only; the deferred cancel stops it the moment the agent is healthy.
		log.Printf("[ws %s] starting (first start pins agent CLIs into ~/.local; this can take a few minutes) — progress in %s",
			n.name, filepath.Join(n.dataDir, "agent.log"))
		off := int64(0)
		if fi, statErr := os.Stat(filepath.Join(n.dataDir, "agent.log")); statErr == nil {
			off = fi.Size()
		}
		_ = os.Remove(n.bootPhasePath()) // drop any stale phase from a prior start
		tailCtx, stopTail := context.WithCancel(ctx)
		defer stopTail()
		go n.mirrorBootProgress(tailCtx, off)
	}
	// 到達待ちは起動の成否ではない（runtime_health.go 冒頭の契約）。印を先に立ててから
	// 待つので、待っている最中の State() は "starting" を返す。予算切れで失敗を返して
	// いた頃は、この関数の defer が **まだ boot-install 中のプロセスを kill** していた
	// ので、遅い初回起動ほど確実に殺していた。
	marker := n.startingMarker()
	marker.arm(time.Now().Add(maxDuration(agentBootBudget, healthWait)))
	waitErr := waitAgentHealthy(ctx, n.Endpoint(), healthWait)
	if waitErr == nil {
		marker.clear()
		return nil
	}
	if ctx.Err() != nil {
		// 呼び出し側が去った/lease を失った。ここは従来どおり失敗＝この spawn は
		// コミットされず、defer の abortSpawn が片付ける。
		return fmt.Errorf("%w (see %s)", waitErr, filepath.Join(n.dataDir, "agent.log"))
	}
	log.Printf("[ws %s] agent not answering yet after %s; still starting (budget %s, progress in %s)",
		n.name, healthWait, agentBootBudget, filepath.Join(n.dataDir, "agent.log"))
	return nil
}

// CommitStart marks the process as belonging to the durable running state. Until
// this point the lifecycle holder may still lose its DB lease after Start has
// returned, in which case AbortUncommittedStart removes only this exact spawn.
func (n *nativeRuntime) CommitStart() {
	n.spawnMu.Lock()
	n.spawned = nativeSpawn{}
	n.spawnMu.Unlock()
}

func (n *nativeRuntime) AbortUncommittedStart(ctx context.Context) error {
	_ = ctx // quiescence is mandatory; cancellation must not release the host fence early
	n.spawnMu.Lock()
	spawn := n.spawned
	n.spawnMu.Unlock()
	if spawn.pid <= 0 {
		return nil
	}
	n.abortSpawn(spawn)
	return nil
}

func (n *nativeRuntime) abortSpawn(spawn nativeSpawn) {
	if sameNativeProcess(spawn.pid, n.agentBin, spawn.startID) {
		_ = syscall.Kill(-spawn.pid, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Second)
		for sameNativeProcess(spawn.pid, n.agentBin, spawn.startID) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if sameNativeProcess(spawn.pid, n.agentBin, spawn.startID) {
			_ = syscall.Kill(-spawn.pid, syscall.SIGKILL)
			for sameNativeProcess(spawn.pid, n.agentBin, spawn.startID) {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	removePidFileIfPID(n.pidFile(), spawn.pid)
	n.spawnMu.Lock()
	if n.spawned == spawn {
		n.spawned = nativeSpawn{}
	}
	n.spawnMu.Unlock()
}

// binStampPath / spawnStamp — what the agent was actually launched FROM, recorded
// at Start so Stale() can notice the deployment moved on (workspace_stale.go).
//
// The identity differs by mode, and picking the wrong one is the whole trap:
//   - rootfs mode (the packaged native deployment): the agent binary lives INSIDE
//     the versioned rootfs, and agentBin is only bwrap — which is byte-identical
//     across releases. So the rootfs directory (…/shared/rootfs/<r>, version-keyed
//     by the launcher) is the identity; its .ok marker covers a same-version
//     re-extract. An `af update` that reuses the pinned rootfs (docs/log/35 §35.3
//     image-immutable release) leaves this unchanged — correctly, since restarting
//     the workspace would then run exactly the same code.
//   - plain mode (dev: AF_NATIVE_AGENT_BIN): the workspace-agent binary itself,
//     by mtime+size — a rebuild moves at least one.
func (n *nativeRuntime) binStampPath() string { return filepath.Join(n.dataDir, "agent.bin-stamp") }

func (n *nativeRuntime) spawnStamp() string {
	if n.rootfs != "" {
		return "rootfs:" + n.rootfs + ":" + fileStamp(filepath.Join(n.rootfs, ".ok"))
	}
	return "bin:" + fileStamp(n.agentBin)
}

func fileStamp(path string) string {
	fi, err := os.Stat(path) // follows the symlink — we want the real target's identity
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
}

// Stale reports whether a fresh Start would launch the agent from something other
// than what the running process was launched from. Unknown (no stamp from an older
// start, unreadable path) → false: never nag on a guess.
func (n *nativeRuntime) Stale(context.Context) bool {
	b, err := os.ReadFile(n.binStampPath())
	if err != nil {
		return false
	}
	was := strings.TrimSpace(string(b))
	now := n.spawnStamp()
	return was != "" && was != "bin:" && was != now
}

// bootPhasePath is the file mirrorBootProgress keeps the latest boot-install
// phase line in, so a separate GET /api/workspace request (possibly served by a
// different runtime instance) can read it back via BootPhase() without shared
// in-memory state. Absent = no boot in progress.
func (n *nativeRuntime) bootPhasePath() string { return filepath.Join(n.dataDir, ".boot-phase") }

// BootPhase returns the latest entrypoint boot-install phase (docs/log/35 §35.9-9),
// or "" when no boot is in progress. The Console's "starting" dialog polls
// GET /api/workspace and shows this while the agent boot-installs pinned CLIs.
func (n *nativeRuntime) BootPhase() string {
	b, err := os.ReadFile(n.bootPhasePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// mirrorBootProgress tails agent.log from startOff and echoes the entrypoint's
// own progress lines (the "[entrypoint] …" prefix — boot-install, install-go,
// install-jdk, claude repair, …) to the CP log so a first-start operator can see
// that pinned CLIs are being installed instead of a silent multi-minute wait
// (docs/log/35 §35.9-9). Best-effort: any error just ends the mirror. It stops when
// ctx is cancelled (the caller cancels the moment the agent is healthy).
func (n *nativeRuntime) mirrorBootProgress(ctx context.Context, startOff int64) {
	// The boot is over the moment this returns (agent healthy) — clear the phase
	// so GET /api/workspace stops reporting one and the Console dialog closes.
	defer os.Remove(n.bootPhasePath())
	f, err := os.Open(filepath.Join(n.dataDir, "agent.log"))
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(startOff, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReader(f)
	var pending string // trailing partial line not yet newline-terminated
	for {
		line, err := r.ReadString('\n')
		if err == nil { // a complete line
			s := strings.TrimRight(pending+line, "\r\n")
			pending = ""
			if i := strings.Index(s, "[entrypoint]"); i >= 0 {
				log.Printf("[ws %s] %s", n.name, strings.TrimSpace(s))
				// Publish the phase (sans prefix) for the Console dialog. Atomic
				// rename so a concurrent BootPhase() read never sees a torn write.
				phase := strings.TrimSpace(s[i+len("[entrypoint]"):])
				if phase != "" {
					tmp := n.bootPhasePath() + ".tmp"
					if os.WriteFile(tmp, []byte(phase), 0o644) == nil {
						_ = os.Rename(tmp, n.bootPhasePath())
					}
				}
			}
			continue
		}
		// EOF (or read error): hold any partial bytes and wait for more, unless
		// cancelled (the agent went healthy → boot phase is done).
		pending += line
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// bwrapArgs assembles the frozen bwrap invocation (docs/log/35 §35.7.2): the rootfs
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
	if err := ctx.Err(); err != nil {
		return err
	}
	n.startingMarker().clear() // 起動途中で止めた場合に古い印を残さない
	pid := readPidFile(n.pidFile())
	startID := nativeProcessStartID(pid)
	if pid > 0 && sameNativeProcess(pid, n.agentBin, startID) {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		deadline := time.Now().Add(time.Duration(stopGraceSec()) * time.Second)
		for sameNativeProcess(pid, n.agentBin, startID) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				// SIGTERM cannot be rolled back. Keep the host fence held until this
				// exact old process is quiescent, but never escalate or touch state
				// after lease ownership was lost.
				for sameNativeProcess(pid, n.agentBin, startID) {
					time.Sleep(25 * time.Millisecond)
				}
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sameNativeProcess(pid, n.agentBin, startID) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			for sameNativeProcess(pid, n.agentBin, startID) {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
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
	removePidFileIfPID(n.pidFile(), pid)
	return nil
}

// Destroy stops the agent process and removes the workspace's data directory (home,
// pid file, boot phase, the lifecycle lock). Nothing else on the host belongs to this
// workspace, so there is never a leftover to report.
//
// Stop must complete first: the lock file the operation fence uses lives under dataDir,
// and killing the process after unlinking its home is how you get a half-written home
// back on the next start.
func (n *nativeRuntime) Destroy(ctx context.Context) ([]string, error) {
	if err := n.Stop(ctx); err != nil {
		return nil, err
	}
	if n.dataDir == "" {
		return nil, nil
	}
	if err := os.RemoveAll(n.dataDir); err != nil {
		return nil, fmt.Errorf("remove data dir %s: %w", n.dataDir, err)
	}
	return nil, nil
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

// Linux process start time makes a PID identity stable across reuse. On hosts
// without /proc, the existing executable-name guard remains the best signal.
func nativeProcessStartID(pid int) string {
	if pid <= 0 {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return ""
	}
	fields := strings.Fields(string(b)[i+2:])
	if len(fields) <= 19 { // field 22 (starttime); slice begins at field 3
		return ""
	}
	return fields[19]
}

func sameNativeProcess(pid int, agentBin, startID string) bool {
	if !pidAlive(pid, agentBin) {
		return false
	}
	return startID == "" || nativeProcessStartID(pid) == startID
}

func removePidFileIfPID(path string, pid int) {
	if pid > 0 && readPidFile(path) == pid {
		_ = os.Remove(path)
	}
}
