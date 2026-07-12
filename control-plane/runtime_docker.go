// runtime_docker.go — ローカル Docker アダプタ（dockerRuntime / dockerFactory）。
// runtime.go からの機械的分割（docs/23 P2-W1）。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// dockerRuntime is the `local` Runtime adapter (ports & adapters, docs/09).
// It drives one per-user Workspace container via the docker CLI. The AWS
// adapter (ECS) will implement the same lifecycle behind the same handlers.
type dockerRuntime struct {
	image      string
	name       string
	network    string // per-user docker network; isolates containers from each other
	dataDir    string // host path; <dataDir>/home is bind-mounted to ~ in the container
	agentHost  string
	agentPort  string
	token      string // CP↔Agent shared secret (injected as AGENT_TOKEN; docs/07 §7.5)
	secretKey  string // per-user at-rest key (injected as AF_SECRET_KEY; A3)
	memory     string
	sessionCmd string
	extraEnv   []string // KEY=VAL passed to the workspace container (e.g. CLAUDE_INSTALL=0)
}

// dockerFactory is the `local` (compose) RuntimeFactory. It carries the template
// fields shared by every container plus rootDataDir, a closure that re-bases a
// workspace's stored data_dir onto the CURRENT dataRoot (docs/history/
// p3-10-packaging.md §20.3) — kept as a closure so the factory need not know the
// manager's tenant/path internals.
type dockerFactory struct {
	image       string
	agentHost   string
	memory      string
	sessionCmd  string
	extraEnv    []string
	rootDataDir func(Workspace) string
}

func (f *dockerFactory) New(ws Workspace, secretKey string, extraEnv []string) Runtime {
	// Shared template env first, then the per-workspace extras (copied so we never
	// mutate the factory's slice).
	env := append(append([]string(nil), f.extraEnv...), extraEnv...)
	// Per-workspace RAM cap (resolveWorkspaceMemBytes) overrides the shared WS_MEMORY
	// default when set; docker --memory accepts a raw byte count. 0 => the default.
	memory := f.memory
	if ws.MemBytes > 0 {
		memory = strconv.FormatInt(ws.MemBytes, 10)
	}
	return &dockerRuntime{
		image:      f.image,
		name:       ws.ContainerName,
		network:    ws.Network,
		dataDir:    f.rootDataDir(ws),
		agentHost:  f.agentHost,
		agentPort:  ws.AgentPort,
		token:      ws.AgentToken,
		secretKey:  secretKey,
		memory:     memory,
		sessionCmd: f.sessionCmd,
		extraEnv:   env,
	}
}

func (d *dockerRuntime) Endpoint() string {
	return fmt.Sprintf("http://%s:%s", d.agentHost, d.agentPort)
}

func (d *dockerRuntime) Token() string { return d.token }
func (d *dockerRuntime) Name() string  { return d.name }

// State returns running | stopped | none. The docker adapter never reports
// "starting": `docker run -d` returns with the container already running, so the
// transient created/restarting statuses are collapsed into "stopped" as before.
func (d *dockerRuntime) State(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Status}}", d.name).Output()
	if err != nil {
		return "none"
	}
	switch strings.TrimSpace(string(out)) {
	case "running":
		return "running"
	default:
		return "stopped"
	}
}

// Start launches the Workspace container and waits for the Agent to be healthy.
func (d *dockerRuntime) Start(ctx context.Context) error {
	if d.State(ctx) == "running" {
		return nil
	}
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", d.name).Run() // clear any stopped remnant

	home := filepath.Join(d.dataDir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("mkdir data home: %w", err)
	}
	// Plaintext Claude state (CLAUDE_CONFIG_DIR) lives OUTSIDE the browsable home
	// so the Console file browser never exposes it (docs/17 P3-5 段2). Persisted
	// via its own mount; auth still works via the per-session env token.
	claudeCfg := filepath.Join(d.dataDir, "claude-config")
	if err := os.MkdirAll(claudeCfg, 0o700); err != nil {
		return fmt.Errorf("mkdir claude-config: %w", err)
	}

	// Each user's container sits alone on a dedicated network, so containers
	// cannot reach each other (相互不可視, docs/09 §9.7). The Agent is still
	// reached by the CP via the host-published 127.0.0.1 port; egress (git,
	// Claude API) works via the network's NAT.
	if err := d.ensureNetwork(ctx); err != nil {
		return err
	}

	args := []string{
		"run", "-d", "--name", d.name,
		// --init runs tini as PID 1 to reap orphaned children. workspace-agent is
		// otherwise PID 1 and Go does not reap, so every claude/tmux session exit
		// would leave a <defunct> zombie that lives for the container's lifetime.
		// tini also forwards docker stop's SIGTERM to the Agent (graceful stop).
		"--init",
		"--memory", d.memory,
		"-p", fmt.Sprintf("127.0.0.1:%s:7700", d.agentPort),
		"-v", home + ":/home/dev",
		"-v", claudeCfg + ":/var/lib/af/claude",
		"-e", "CLAUDE_CONFIG_DIR=/var/lib/af/claude",
		// Graceful-shutdown budget for the Agent's SIGTERM handler; see Stop.
		"-e", fmt.Sprintf("AGENT_STOP_GRACE_SEC=%d", agentStopGraceSec()),
	}
	// Shared Temurin JDKs: mounted read-only from one host dir into every
	// workspace (kept out of the image to stay slim). The entrypoint/agent pick
	// JAVA_HOME from /usr/lib/jvm. Opt-in via WS_JVM_DIR.
	if jvm := os.Getenv("WS_JVM_DIR"); jvm != "" {
		args = append(args, "-v", jvm+":/usr/lib/jvm:ro")
	}
	// Role-scoped agent-fleet docs, staged per-start by the CP into <dataDir>/docs
	// (stageWorkspaceDocs) — mounted read-only at the shared path the entrypoint
	// already uses for baked assets. Absent when nothing was staged (no baked docs,
	// or a role/deploy without docs), so the mount is conditional.
	if docs := filepath.Join(d.dataDir, "docs"); isDirPath(docs) {
		args = append(args, "-v", docs+":/usr/local/share/agent-fleet/docs:ro")
	}
	if d.network != "" {
		args = append(args, "--network", d.network)
	}
	if d.token != "" {
		args = append(args, "-e", "AGENT_TOKEN="+d.token)
	}
	if d.secretKey != "" {
		args = append(args, "-e", "AF_SECRET_KEY="+d.secretKey)
	}
	if d.sessionCmd != "" {
		args = append(args, "-e", "AGENT_SESSION_CMD="+d.sessionCmd)
	}
	for _, e := range d.extraEnv {
		args = append(args, "-e", e)
	}
	args = append(args, d.image)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %v: %s", err, out)
	}
	return d.waitHealthy(ctx, 15*time.Second)
}

// ensureNetwork creates the per-user network if it does not already exist.
func (d *dockerRuntime) ensureNetwork(ctx context.Context) error {
	if d.network == "" {
		return nil
	}
	if exec.CommandContext(ctx, "docker", "network", "inspect", d.network).Run() == nil {
		return nil // already exists
	}
	if out, err := exec.CommandContext(ctx, "docker", "network", "create", d.network).CombinedOutput(); err != nil {
		return fmt.Errorf("docker network create %s: %v: %s", d.network, err, out)
	}
	return nil
}

// stopGraceSec is the workspace stop grace (seconds) before the runtime's hard
// kill — `docker stop -t` locally, the container stopTimeout on ECS. One knob
// (AF_STOP_GRACE_SEC, default 30) drives both adapters. Clamped to Fargate's
// stopTimeout ceiling (120s) so one value stays valid everywhere.
func stopGraceSec() int {
	n := envInt("AF_STOP_GRACE_SEC", 30)
	if n < 1 {
		n = 1
	}
	if n > 120 {
		n = 120
	}
	return n
}

// agentStopGraceSec is the budget injected into the container as
// AGENT_STOP_GRACE_SEC: the runtime grace minus a safety margin, so the Agent's
// graceful shutdown (Ctrl-C panes → wait → tmux kill-server → exit) always
// finishes before the runtime's SIGKILL lands.
func agentStopGraceSec() int {
	if n := stopGraceSec() - 5; n >= 5 {
		return n
	}
	return 5
}

// Stop is a two-stage graceful stop (previously a bare `docker rm -f`, i.e. an
// instant SIGKILL to everything inside): `docker stop -t` delivers SIGTERM to
// tini → the Agent, whose shutdown handler Ctrl-C's every live pane so claude /
// git / builds land in a consistent state before exiting; past the grace, docker
// itself SIGKILLs — the built-in second stage. The follow-up rm keeps the
// "normal stopped state is none" semantics the Console relies on. If stop itself
// errors (missing container, wedged daemon) fall back to the old hard remove so
// Stop still converges.
func (d *dockerRuntime) Stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "docker", "stop", "-t", strconv.Itoa(stopGraceSec()), d.name).CombinedOutput(); err != nil {
		if out2, err2 := exec.CommandContext(ctx, "docker", "rm", "-f", d.name).CombinedOutput(); err2 != nil {
			return fmt.Errorf("docker stop: %v: %s; docker rm -f: %v: %s",
				err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2)))
		}
	} else if out, err := exec.CommandContext(ctx, "docker", "rm", d.name).CombinedOutput(); err != nil {
		return fmt.Errorf("docker rm: %v: %s", err, out)
	}
	// Best-effort: drop the now-empty per-user network (recreated on next start).
	if d.network != "" {
		_ = exec.CommandContext(ctx, "docker", "network", "rm", d.network).Run()
	}
	return nil
}

// homeKeep are the top-level ~ entries preserved by an admin "home 掃除": connection
// secrets and auth/identity. Everything else under home (repos, caches, dotfiles)
// is removed. Claude login also survives because it lives outside home (a separate
// claude-config mount, docs/17 P3-5).
var homeKeep = map[string]bool{
	".config":          true, // agent-fleet encrypted secrets store (git/agent connections)
	".ssh":             true, // git over SSH
	".git-credentials": true, // git over HTTPS
	".gitconfig":       true, // git identity
	".claude":          true, // Claude CLI state
	".claude.json":     true,
	".codex":           true, // Codex CLI auth
}

// cleanHome removes everything under <dataDir>/home except the auth/connection
// entries in homeKeep. The caller MUST stop the container first — we mutate the host
// bind-mount source, and deleting under a live mount risks inconsistency.
func cleanHome(dataDir string) error {
	home := filepath.Join(dataDir, "home")
	entries, err := os.ReadDir(home)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if homeKeep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(home, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// dockerInspectOut runs `docker <args...>` and returns its stdout.
// テスト用シーム（gitBackendServe と同型）。
var dockerInspectOut = func(args ...string) ([]byte, error) {
	return exec.Command("docker", args...).Output()
}

// dockerPublishedPort returns the host port mapped to the container's 7700/tcp.
func dockerPublishedPort(name string) string {
	out, err := dockerInspectOut("inspect", "-f",
		`{{with index .NetworkSettings.Ports "7700/tcp"}}{{(index . 0).HostPort}}{{end}}`, name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dockerEnvValue returns the value of an env var baked into a container's config.
func dockerEnvValue(name, key string) string {
	out, err := dockerInspectOut("inspect", "-f",
		`{{range .Config.Env}}{{println .}}{{end}}`, name)
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func (d *dockerRuntime) waitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", d.Endpoint()+"/healthz", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("agent did not become healthy within %s", timeout)
}
