package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// Runtime is the port that abstracts where a Workspace container runs and how the
// CP reaches its Agent. dockerRuntime is the local/compose adapter (docker CLI);
// the AWS adapter (ECS, P3-7) implements the same port so the handlers, manager
// and reaper stay backend-agnostic. Endpoint() hides the reachability difference
// (host-published 127.0.0.1:port locally vs Service Connect on ECS).
type Runtime interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	State(ctx context.Context) string // running | stopped | none
	Endpoint() string                 // http base URL for CP→Agent REST
	Token() string                    // Bearer secret for CP→Agent (may be "")
	Name() string                     // container / display name
}

// dockerRuntime is the first Runtime adapter; the ECS adapter (P3-7) must satisfy
// the same contract. This assertion fails the build if either drifts.
var _ Runtime = (*dockerRuntime)(nil)

// RuntimeFactory is the single construction seam for the Runtime port. Every call
// site (handlers, manager, reaper, admin, mcp) builds its Runtime through the
// factory rather than instantiating a concrete adapter, so swapping the local
// Docker adapter for the ECS adapter (P3-7) is a one-line profile switch in
// main.go — no concrete type ever leaks into the backend-agnostic core.
//
// secretKey is the per-workspace at-rest DEK (injected as AF_SECRET_KEY on Start).
// Pass "" for state/stop/read-only calls that never touch secrets.
type RuntimeFactory interface {
	New(ws Workspace, secretKey string) Runtime
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

func (f *dockerFactory) New(ws Workspace, secretKey string) Runtime {
	return &dockerRuntime{
		image:      f.image,
		name:       ws.ContainerName,
		network:    ws.Network,
		dataDir:    f.rootDataDir(ws),
		agentHost:  f.agentHost,
		agentPort:  ws.AgentPort,
		token:      ws.AgentToken,
		secretKey:  secretKey,
		memory:     f.memory,
		sessionCmd: f.sessionCmd,
		extraEnv:   f.extraEnv,
	}
}

var _ RuntimeFactory = (*dockerFactory)(nil)

// newRuntimeFactory selects the Runtime adapter by deployment profile (AF_RUNTIME):
// "" / "local" / "docker" → Docker Engine (compose, the on-prem default); "ecs" /
// "aws" → AWS ECS (P3-7). Unknown profiles fail fast at boot rather than silently
// defaulting to Docker. The docker factory captures the manager's template fields
// by value, so it MUST be built after those fields are finalized (e.g. extraEnv
// appends in main.go).
func newRuntimeFactory(profile string, m *manager) (RuntimeFactory, error) {
	switch profile {
	case "", "local", "docker":
		return &dockerFactory{
			image:       m.image,
			agentHost:   m.agentHost,
			memory:      m.memory,
			sessionCmd:  m.sessionCmd,
			extraEnv:    m.extraEnv,
			rootDataDir: m.rootedDataDir,
		}, nil
	case "ecs", "aws":
		return newECSFactory(m)
	default:
		return nil, fmt.Errorf("unknown AF_RUNTIME profile %q (want local|ecs)", profile)
	}
}

func (d *dockerRuntime) Endpoint() string {
	return fmt.Sprintf("http://%s:%s", d.agentHost, d.agentPort)
}

func (d *dockerRuntime) Token() string { return d.token }
func (d *dockerRuntime) Name() string  { return d.name }

// State returns running | stopped | none.
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
		"--init",
		"--memory", d.memory,
		"-p", fmt.Sprintf("127.0.0.1:%s:7700", d.agentPort),
		"-v", home + ":/home/dev",
		"-v", claudeCfg + ":/var/lib/af/claude",
		"-e", "CLAUDE_CONFIG_DIR=/var/lib/af/claude",
	}
	// Shared Temurin JDKs: mounted read-only from one host dir into every
	// workspace (kept out of the image to stay slim). The entrypoint/agent pick
	// JAVA_HOME from /usr/lib/jvm. Opt-in via WS_JVM_DIR.
	if jvm := os.Getenv("WS_JVM_DIR"); jvm != "" {
		args = append(args, "-v", jvm+":/usr/lib/jvm:ro")
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

func (d *dockerRuntime) Stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", d.name).CombinedOutput(); err != nil {
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

// --- HTTP handlers ---

// rtFor resolves the request's user (AuthGateway) and returns its runtime. When
// the gateway provides no identity (proxy mode, missing header) it writes 401
// and returns ok=false; callers must stop.
// resolvedFor returns the full per-request resolution (runtime + workspace +
// identity + membership). Tenant selection: header for REST; query param for the
// terminal WebSocket (browsers can't set custom headers on a WS handshake).
func (c config) resolvedFor(w http.ResponseWriter, r *http.Request) (*resolved, bool) {
	id := c.mgr.resolveIdentity(r)
	if id.key == "" {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"})
		return nil, false
	}
	tenantSel := r.Header.Get("X-AF-Tenant")
	if tenantSel == "" {
		tenantSel = r.URL.Query().Get("tenant")
	}
	res, aerr := c.mgr.resolveFull(r.Context(), id.key, id.email, tenantSel)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return nil, false
	}
	return res, true
}

func (c config) rtFor(w http.ResponseWriter, r *http.Request) (Runtime, bool) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return nil, false
	}
	return res.rt, true
}

// handleWhoami reports how the AuthGateway resolved this request, plus the raw
// gateway headers — used to verify the funnel -> oauth2-proxy -> Caddy -> CP
// chain actually delivers the authenticated email. In dev mode resolved_user is
// the fixed id, but the email/sanitized fields still show what proxy mode would
// pick, so the chain can be verified without flipping AUTH globally.
func (c config) handleWhoami(w http.ResponseWriter, r *http.Request) {
	email := r.Header.Get(c.mgr.emailHeader)
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode":          c.mgr.authMode,
		"resolved_user":      c.mgr.resolveUser(r),
		"email_header":       c.mgr.emailHeader,
		"email":              email,
		"sanitized_user":     sanitizeUser(email),
		"x_forwarded_user":   r.Header.Get("X-Forwarded-User"),
		"preferred_username": r.Header.Get("X-Forwarded-Preferred-Username"),
	})
}

func (c config) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	rt, ok := c.rtFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.Name(), "state": rt.State(r.Context())})
}

func (c config) handleWorkspaceStart(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	c.startResolved(w, r, res)
}

// handleWorkspaceRecreate tears the container down and starts a fresh one from
// the current image — a clean rebuild (pick up a new build / clear a wedged
// container). Login (a separate CLAUDE_CONFIG_DIR mount) and connections (the
// encrypted store under ~/.config) persist; the cloned working copies under
// ~/repos are wiped (the user accepts losing them, incl. uncommitted work) and
// running sessions are lost — so the Console guards this behind a warning dialog.
func (c config) handleWorkspaceRecreate(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	_ = res.rt.Stop(r.Context()) // best-effort: may not exist yet
	// Clear the working copies while the container is down. Targeted: we keep the
	// encrypted secrets store and everything else in home.
	_ = os.RemoveAll(filepath.Join(c.mgr.rootedDataDir(res.ws), "home", "repos"))
	c.startResolved(w, r, res)
}

// startResolved applies the workspace quota then starts the container, writing
// the JSON result. Shared by start and recreate.
func (c config) startResolved(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt, ctx := res.rt, r.Context()
	// Quota (docs/16 P3-4): block a new running workspace once the tenant is at
	// its max_workspaces. 0/unset = unlimited. Counted authoritatively via docker.
	if rt.State(ctx) != "running" {
		t, err := c.mgr.store.GetTenant(ctx, res.ws.TenantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if lim := parseLimits(t.Limits); lim.MaxWorkspaces > 0 {
			n, err := c.mgr.countRunningInTenant(ctx, res.ws.TenantID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if n >= lim.MaxWorkspaces {
				writeAPIErr(w, &apiError{http.StatusTooManyRequests, "quota_workspaces",
					fmt.Sprintf("tenant workspace limit reached (%d)", lim.MaxWorkspaces)})
				return
			}
		}
	}
	if err := rt.Start(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_ = c.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.Name(), "state": "running"})
}

func (c config) handleWorkspaceStop(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	if err := res.rt.Stop(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_ = c.mgr.store.SetWorkspaceState(r.Context(), res.ws.ID, "stopped")
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.Name(), "state": "stopped"})
}

// sessionWire is the session shape exchanged with the Agent and the Console. The
// CP mirrors it into the DB so it can be re-served while the container is down.
type sessionWire struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Dir       string `json:"dir"`
	Repo      string `json:"repo"`
	Label     string `json:"label"`
	Started   string `json:"started"`
	CreatedAt string `json:"createdAt"`
	RemoteUrl string `json:"remoteUrl"`
	State     string `json:"state"`
	Alive     bool   `json:"alive"`
	Resumable bool   `json:"resumable"`
}

func fmtStarted(createdAt string) string {
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		return t.Local().Format("01/02 15:04")
	}
	return ""
}

// handleSessionsList serves GET /api/sessions. While the Workspace runs the Agent
// is authoritative: fetch its list and mirror it into the DB. While it is stopped
// (or the Agent is briefly unreachable) serve the last mirrored list from the DB —
// as stopped — so the user still sees, and can resume, their sessions.
func (c config) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if res.rt.State(ctx) == "running" {
		if list, err := c.mgr.agentSessions(ctx, res.rt); err == nil {
			rows := make([]SessionRow, 0, len(list))
			for _, s := range list {
				state := "stopped"
				if s.Alive {
					state = "running"
				}
				rows = append(rows, SessionRow{
					Name: s.Name, Kind: s.Kind, Dir: s.Dir, Repo: s.Repo,
					Label: s.Label, CreatedAt: s.CreatedAt, State: state,
				})
			}
			_ = c.mgr.store.ReplaceSessions(ctx, res.ws.ID, rows)
			writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
			return
		}
		// Agent unreachable (e.g. mid-start): fall through to the DB mirror.
	}
	rows, err := c.mgr.store.ListSessions(ctx, res.ws.ID)
	if err != nil {
		rows = nil
	}
	out := make([]sessionWire, 0, len(rows))
	for _, r0 := range rows {
		out = append(out, sessionWire{
			Name: r0.Name, Kind: r0.Kind, Dir: r0.Dir, Repo: r0.Repo, Label: r0.Label,
			Started: fmtStarted(r0.CreatedAt), CreatedAt: r0.CreatedAt, Alive: false,
			// Container is down: we can't check the dir, so assume resumable; the
			// Agent re-checks and refuses on actual attach if the dir is gone.
			Resumable: true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleSessionCreate enforces the per-user session quota (docs/16 P3-4) then
// proxies POST /api/sessions to the Agent.
func (c config) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	lim := 0
	if ul, ok, _ := c.mgr.store.GetUserLimit(ctx, res.mv.MembershipID); ok && ul.MaxSessions > 0 {
		lim = ul.MaxSessions
	} else if t, err := c.mgr.store.GetTenant(ctx, res.ws.TenantID); err == nil {
		lim = parseLimits(t.Limits).MaxSessions
	}
	if lim > 0 {
		// If the workspace isn't reachable, skip the check; the proxy will report it.
		if n, err := c.mgr.countSessions(ctx, res.rt); err == nil && n >= lim {
			// Developer-facing fallback; the Console localizes by `code` (quota_sessions).
			writeAPIErr(w, &apiError{http.StatusTooManyRequests, "quota_sessions",
				fmt.Sprintf("concurrent session limit reached (%d running, max %d)", n, lim)})
			return
		}
	}
	c.proxyAgentREST(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
