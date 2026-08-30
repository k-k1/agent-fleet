// runtime_docker.go — ローカル Docker アダプタ（dockerRuntime / dockerFactory）。
// runtime.go からの機械的分割（docs/log/23 P2-W1）。
package main

import (
	"context"
	"fmt"
	"log"
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
	// inspect overrides `docker inspect` (tests only); nil = the real docker CLI.
	inspect func(ctx context.Context, typ, ref, format string) string
	// stopFn overrides Stop (tests only; the CP's own test environment has no docker
	// CLI, and Destroy's contract — never unlink the home while the container can still
	// be writing to it — is exactly what needs asserting). nil = the real Stop.
	stopFn func(ctx context.Context) error
	// cpus is the per-workspace CPU cap as docker --cpus (fractional cores); "" = no
	// flag = every core. Native has no cgroup, so it ignores this axis entirely.
	cpus string
}

// dockerFactory is the `local` (compose) RuntimeFactory. It carries the template
// fields shared by every container plus rootDataDir, a closure that re-bases a
// workspace's stored data_dir onto the CURRENT dataRoot (docs/log/
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
	// Per-workspace CPU cap. The value is carried in Fargate CPU units (1024 = 1 vCPU)
	// because that is the axis the ECS adapter needs to be exact about; docker --cpus
	// takes fractional cores, so it is just units/1024. Unset (0) means no --cpus flag
	// at all, which is docker's "use every core" default — the pre-P1 behaviour.
	cpus := ""
	if ws.CPUUnits > 0 {
		cpus = strconv.FormatFloat(float64(ws.CPUUnits)/1024, 'f', -1, 64)
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
		cpus:       cpus,
		sessionCmd: f.sessionCmd,
		extraEnv:   env,
	}
}

func (d *dockerRuntime) Endpoint() string {
	return fmt.Sprintf("http://%s:%s", d.agentHost, d.agentPort)
}

func (d *dockerRuntime) Token() string { return d.token }
func (d *dockerRuntime) Name() string  { return d.name }

// State returns running | starting | stopped | none.
//
// `docker run -d` returns with the container already running, so the transient
// created/restarting statuses collapse into "stopped" as before — but a running
// CONTAINER is not a reachable AGENT. The entrypoint still has to finish (pinned
// CLI boot-install, the opt-in self-update: 約60 秒 実測, docs/log/38 ★6) before
// workspace-agent listens, and during that window every caller that gates on
// "running" (terminal WS, file proxy, browser, session create) used to be waved
// through to a socket nobody answers.
//
// So the boot window is a state now: while Start's marker is armed and /healthz
// has not answered yet, this reports "starting" — the value the whole codebase
// already knows how to treat ("wait", never re-Start, never idle-stop). The probe
// only runs while the marker exists, and clearing it there makes the state
// self-healing even if the CP that armed it is gone.
func (d *dockerRuntime) State(ctx context.Context) string {
	switch d.inspectOne(ctx, "container", d.name, "{{.State.Status}}") {
	case "":
		return "none" // docker inspect が引けない = そんなコンテナは無い
	case "running":
		if d.startingMarker().active(ctx, d.Endpoint()) {
			return "starting"
		}
		return "running"
	default:
		return "stopped"
	}
}

func (d *dockerRuntime) startingMarker() agentStartingMarker {
	return agentStartingMarkerIn(d.dataDir)
}

// imageStampPath / recordImageStamp — what this container was actually launched
// FROM, recorded at Start so Stale() can notice the tag moved (workspace_stale.go).
// Same shape as the native runtime's spawnStamp. Two traps decide both the stamp
// and WHAT is stamped; both were paid for on the dev fleet.
//
// ★ Never compare the running container's `docker inspect {{.Image}}` against the
//
//	tag's `docker image inspect {{.Id}}`. Under the containerd image store
//	(`docker info` → Driver=overlayfs, the default on newer engines) those are
//	digests of DIFFERENT objects — the platform config vs the manifest/index — so
//	they disagree even for a container started from exactly that image. Measured on
//	the dev fleet (2026-07-29):
//
//	    tag :dev             {{.Id}}    = sha256:ff2da9ec…   (built 22:57:53)
//	    container started 3 min later   {{.Image}} = sha256:02a946de…
//
//	The original two-sided check therefore reported 要再起動 permanently on that
//	host, whatever was (or was not) updated. (The container's digest is not even a
//	usable ref: `docker image inspect <that digest>` answers "No such image", so
//	there is no way to fingerprint the running image after the fact — the value has
//	to be recorded at Start.)
//
// ★ Stamping the tag's {{.Id}} is not enough either — the ID is a REPRESENTATION,
//
//	not the content, and it moves without the image changing. Measured 2026-08-16:
//	a fully cache-hit `docker build -t agent-fleet/workspace:dev` re-exported the
//	same content with a fresh provenance attestation, so the tag stopped resolving
//	to the plain config digest and started resolving to an index digest:
//
//	    stamp at container start 15:23   = sha256:97f63692…  (== container {{.Image}})
//	    tag {{.Id}} after 15:43 rebuild  = sha256:d109f019…  (Created still 14:46:47)
//	    image contents                   = byte-identical (sha256 of workspace-agent,
//	                                       entrypoint.sh, CLAUDE.md all unchanged)
//
//	i.e. 要再起動 for an image where stop→start changes nothing. So stamp the
//	CONTENT identity instead: the layer chain plus the config fields a Dockerfile
//	can change without producing a layer (ENV/CMD/ENTRYPOINT/USER/WORKDIR/LABEL).
//	Those are diffIDs and literal values — no digest representation involved.
func (d *dockerRuntime) imageStampPath() string {
	return filepath.Join(d.dataDir, "image.rootfs-stamp")
}

// legacyImageIDStampPath is the pre-2026-08-16 stamp, which held the tag's {{.Id}}.
// It is removed rather than read: an ID cannot be compared against a fingerprint,
// and treating it as "unknown" is the safe side of the contract (never nag on a
// guess) — the next Start writes a real fingerprint.
func (d *dockerRuntime) legacyImageIDStampPath() string {
	return filepath.Join(d.dataDir, "image.id-stamp")
}

// dockerImageFingerprint is the `docker inspect --type=image` format that yields the
// content identity described above. Fields are named one by one on purpose: dumping
// `{{.RootFS}}` or `{{json .Config}}` would fold the docker CLI's struct/JSON shape
// into the stamp, so a CLI upgrade that adds a field would look like a moved image —
// the very class of bug this comparison exists to avoid.
const dockerImageFingerprint = "{{range .RootFS.Layers}}{{.}} {{end}}|" +
	"{{.Config.User}}|{{.Config.WorkingDir}}|{{.Config.Entrypoint}}|" +
	"{{.Config.Cmd}}|{{.Config.Env}}|{{.Config.Labels}}"

// recordImageStamp stores the fingerprint of the image the tag resolves to right
// now. Called by Start, straight after `docker run`. Best-effort: an unwritable/
// unreadable value just means "unknown", and Stale then never nags. An empty result
// is written on purpose — leaving a previous container's stamp in place would be a lie.
func (d *dockerRuntime) recordImageStamp(ctx context.Context) {
	fp := d.imageFingerprint(ctx)
	// Prime the TTL cache with the value we just probed: a cached PRE-rebuild
	// fingerprint would otherwise make this freshly started container look stale for
	// up to a minute (Start is the one moment we know the truth).
	freshness.set("img:"+d.image, fp)
	_ = os.WriteFile(d.imageStampPath(), []byte(fp), 0o644)
	_ = os.Remove(d.legacyImageIDStampPath())
}

func (d *dockerRuntime) imageFingerprint(ctx context.Context) string {
	return d.inspectOne(ctx, "image", d.image, dockerImageFingerprint)
}

// Stale reports whether the tag now resolves to different image CONTENT than the one
// this container was started from — i.e. stop→start would swap in new backend code
// while the live container keeps the old. A rebuilt image is what a local
// `docker build` or a pull of a new release produces, so this catches dev rebuilds
// that carry no version stamp; a re-tag of identical content (cache-hit rebuild) is
// correctly silent. Unknown (no stamp — a start that predates this, or one that only
// left the legacy ID stamp; image not present locally — e.g. a registry-only tag)
// → false: never nag on a guess.
//
// The probe is cached (workspace_stale.go) because /api/workspace is polled every 4s
// per open Console; the fingerprint only moves on a build/pull.
func (d *dockerRuntime) Stale(ctx context.Context) bool {
	b, err := os.ReadFile(d.imageStampPath())
	if err != nil {
		return false
	}
	was := strings.TrimSpace(string(b))
	if was == "" {
		return false
	}
	now := freshness.get("img:"+d.image, 60*time.Second, func() string { return d.imageFingerprint(ctx) })
	return now != "" && now != was
}

// inspectOne runs a single-field `docker inspect` (overridable in tests).
func (d *dockerRuntime) inspectOne(ctx context.Context, typ, ref, format string) string {
	if d.inspect != nil {
		return d.inspect(ctx, typ, ref, format)
	}
	return dockerInspectOne(ctx, typ, ref, format)
}

// dockerInspectOne runs a single-field `docker inspect`, returning "" on any error
// (missing object, docker unavailable) so callers can treat it as "unknown".
func dockerInspectOne(ctx context.Context, typ, ref, format string) string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--type="+typ, "-f", format, ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Start launches the Workspace container and waits for the Agent to be healthy.
// mountsStagedDocs marks this adapter as one that bind-mounts <dataDir>/docs, so the
// start path stages it (runtime.go runtimeDocsMounter).
func (d *dockerRuntime) mountsStagedDocs() {}

func (d *dockerRuntime) Start(ctx context.Context) error {
	switch d.State(ctx) {
	case "running":
		return nil
	case "starting":
		// A boot is already in flight (this adapter reports it while the Agent has not
		// answered yet). Falling through would `docker rm -f` a container that is in the
		// middle of its entrypoint — killing a legitimate start and losing whatever the
		// boot-install had already downloaded. Let the poller observe the transition;
		// the marker is time-boxed (agentBootBudget) so this can never wedge.
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
		// Chromium's setuid sandbox creates PID/network namespaces. Docker's
		// default capability bounding set omits SYS_ADMIN, so the root-owned
		// helper cannot create those namespaces even though the container's
		// normal dev process has no effective capabilities. The image removes
		// setuid bits from every other executable, leaving chrome-sandbox as the
		// sole path that can acquire this bounded capability.
		"--cap-add=SYS_ADMIN",
		"-p", fmt.Sprintf("127.0.0.1:%s:7700", d.agentPort),
		"-v", home + ":/home/dev",
		"-v", claudeCfg + ":/var/lib/af/claude",
		"-e", "CLAUDE_CONFIG_DIR=/var/lib/af/claude",
		// Graceful-shutdown budget for the Agent's SIGTERM handler; see Stop.
		"-e", fmt.Sprintf("AGENT_STOP_GRACE_SEC=%d", agentStopGraceSec()),
	}
	if d.cpus != "" {
		args = append(args, "--cpus", d.cpus)
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
	// AGENT_TOKEN / AF_SECRET_KEY(DEK) は argv の `-e` だと /proc/<pid>/cmdline から
	// 可視になるため、0600 の一時 --env-file 経由で渡す(docker run 完了後に削除)。
	if d.token != "" || d.secretKey != "" {
		ef, err := d.writeSecretEnvFile()
		if err != nil {
			return err
		}
		defer os.Remove(ef)
		args = append(args, "--env-file", ef)
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
	// Record which image this container actually came from, so Stale() can later
	// tell that the tag moved on while it keeps running the old code.
	d.recordImageStamp(ctx)

	// ここから先は「待つ」だけで、もう起動は確定している。だから到達しなくても
	// **エラーにしない**（runtime_health.go 冒頭の契約）。印を先に立ててから待つので、
	// 待っている最中に別経路（Console の 4 秒ポーリング・ターミナル起動）が State() を
	// 引いても "starting" が返り、死んだソケットに繋ぎに行かない。
	grace := agentHealthWait(d.startHealthWait())
	marker := d.startingMarker()
	marker.arm(time.Now().Add(maxDuration(agentBootBudget, grace)))
	err := waitAgentHealthy(ctx, d.Endpoint(), grace)
	if err == nil {
		marker.clear()
		return nil
	}
	if ctx.Err() != nil {
		// 呼び出し側が去った（リクエスト打ち切り・lease 喪失）。コンテナはそのまま
		// 起き続けるので印は残し、エラーはそのまま返す（後段の checkpoint が判断する）。
		return err
	}
	// 予算切れ。失敗ではない — 状態として表に出し、ポーラーに収束を任せる。
	log.Printf("docker start: container %s is up but the Agent has not answered within %s; still starting (budget %s)",
		d.name, grace, agentBootBudget)
	return nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// writeSecretEnvFile writes the secret env entries to a 0600 temp file for
// `docker run --env-file`. The caller removes it once docker run has returned.
func (d *dockerRuntime) writeSecretEnvFile() (string, error) {
	f, err := os.CreateTemp("", "af-ws-env-*") // CreateTemp creates with 0600
	if err != nil {
		return "", fmt.Errorf("secret env file: %w", err)
	}
	var b strings.Builder
	if d.token != "" {
		b.WriteString("AGENT_TOKEN=" + d.token + "\n")
	}
	if d.secretKey != "" {
		b.WriteString("AF_SECRET_KEY=" + d.secretKey + "\n")
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("secret env file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("secret env file: %w", err)
	}
	return f.Name(), nil
}

// startHealthWait is how long Start BLOCKS waiting for /healthz after `docker run`.
// It is a courtesy, not a deadline: overrunning it means "answer starting and let the
// poller finish the job" (see Start), so this number only decides how often a start
// comes back already-running instead of already-starting.
//
// Which is why the old 15s / 300s split is gone. The 300s branch existed because a
// self-updating boot (the entrypoint runs the update SYNCHRONOUSLY before
// `exec workspace-agent`: npm @latest for the 4 CLIs ~35s cold, agy ~15s, cursor ~6s —
// 約60 秒 with everything updating, worse on a slow link or a network-backed home)
// would otherwise be FAILED at 15s. Nothing fails here now, and a 300s block is the
// thing the Runtime port forbids: Start runs inside an HTTP request, and a wait past
// the ingress idle timeout does not deliver a slower success, it deletes the response
// (docs/log/62 §62.5, measured as a 504 on ECS). So one budget, sized to sit inside that
// timeout, and the boot window is carried by State() == "starting" instead.
//
// AF_AGENT_HEALTH_WAIT_SEC still overrides it — a deployment that knows its ingress
// tolerates more (or has none) can buy a synchronous answer for a slower boot.
const dockerStartGrace = 45 * time.Second

func (d *dockerRuntime) startHealthWait() time.Duration {
	for _, kv := range d.extraEnv {
		// An unattended start (scheduler wake) skips the update block entirely and has
		// nobody waiting on the answer — the fire path polls the Agent itself, patiently
		// (AF_SCHEDULE_WAKE_TIMEOUT). Blocking the tick goroutine longer buys nothing.
		if kv == unattendedStartEnv {
			return 15 * time.Second
		}
	}
	return dockerStartGrace
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
	// 起動途中の停止（利用者が「起動中…」のまま止めた）でも印を残さない。State() は
	// コンテナが居なければ印を見ないので実害は無いが、次の Start が自分で立て直す
	// 印を古いまま置いておく理由も無い。
	d.startingMarker().clear()
	// 「No such container」は冪等成功扱い: 停止済み(=コンテナ無し)WSへの stop API を
	// 500 にしない。
	noSuch := func(out []byte) bool {
		return strings.Contains(strings.ToLower(string(out)), "no such container")
	}
	if out, err := exec.CommandContext(ctx, "docker", "stop", "-t", strconv.Itoa(stopGraceSec()), d.name).CombinedOutput(); err != nil && !noSuch(out) {
		if out2, err2 := exec.CommandContext(ctx, "docker", "rm", "-f", d.name).CombinedOutput(); err2 != nil && !noSuch(out2) {
			return fmt.Errorf("docker stop: %v: %s; docker rm -f: %v: %s",
				err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2)))
		}
	} else if err == nil {
		if out, err := exec.CommandContext(ctx, "docker", "rm", d.name).CombinedOutput(); err != nil && !noSuch(out) {
			return fmt.Errorf("docker rm: %v: %s", err, out)
		}
	}
	// Best-effort: drop the now-empty per-user network (recreated on next start).
	if d.network != "" {
		_ = exec.CommandContext(ctx, "docker", "network", "rm", d.network).Run()
	}
	return nil
}

// Destroy removes the container (Stop already does that, plus the per-user network) and
// then the host data directory that holds the home bind-mount. Unlike the cloud adapters
// there is nothing left over afterwards: the workspace's whole existence on this host is
// the container and <dataDir>.
//
// The dataDir is removed AFTER Stop so nothing is writing through the bind-mount while we
// unlink it. An already-absent directory is success, not an error — Destroy is retried by
// the caller when a partial teardown left the DB row behind.
func (d *dockerRuntime) Destroy(ctx context.Context) ([]string, error) {
	stop := d.Stop
	if d.stopFn != nil {
		stop = d.stopFn
	}
	if err := stop(ctx); err != nil {
		return nil, err
	}
	if d.dataDir == "" {
		return nil, nil
	}
	if err := os.RemoveAll(d.dataDir); err != nil {
		return nil, fmt.Errorf("remove data dir %s: %w", d.dataDir, err)
	}
	return nil, nil
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
	return cleanHomeContext(context.Background(), dataDir)
}

func cleanHomeContext(ctx context.Context, dataDir string) error {
	home := filepath.Join(dataDir, "home")
	entries, err := os.ReadDir(home)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if homeKeep[e.Name()] {
			continue
		}
		if err := removeAllContext(ctx, filepath.Join(home, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// removeAllContext is the cancellable lifecycle equivalent of os.RemoveAll.
// It never follows symlinks and checks the lease-derived context between entries,
// so a fenced holder stops deleting before another CP can start the workspace.
func removeAllContext(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Remove(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeAllContext(ctx, filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(path)
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

// agentHealthWait / waitAgentHealthy / agentStartingMarker は runtime_health.go へ
// 移した（docker と native の両方が使う共通部品なので）。
