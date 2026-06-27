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

func (d *dockerRuntime) agentBase() string {
	return fmt.Sprintf("http://%s:%s", d.agentHost, d.agentPort)
}

// state returns running | stopped | none.
func (d *dockerRuntime) state(ctx context.Context) string {
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

// start launches the Workspace container and waits for the Agent to be healthy.
func (d *dockerRuntime) start(ctx context.Context) error {
	if d.state(ctx) == "running" {
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

func (d *dockerRuntime) stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", d.name).CombinedOutput(); err != nil {
		return fmt.Errorf("docker rm: %v: %s", err, out)
	}
	// Best-effort: drop the now-empty per-user network (recreated on next start).
	if d.network != "" {
		_ = exec.CommandContext(ctx, "docker", "network", "rm", d.network).Run()
	}
	return nil
}

func (d *dockerRuntime) waitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", d.agentBase()+"/healthz", nil)
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

func (c config) rtFor(w http.ResponseWriter, r *http.Request) (*dockerRuntime, bool) {
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
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.name, "state": rt.state(r.Context())})
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
	_ = res.rt.stop(r.Context()) // best-effort: may not exist yet
	// Clear the working copies while the container is down. Targeted: we keep the
	// encrypted secrets store and everything else in home.
	_ = os.RemoveAll(filepath.Join(res.rt.dataDir, "home", "repos"))
	c.startResolved(w, r, res)
}

// startResolved applies the workspace quota then starts the container, writing
// the JSON result. Shared by start and recreate.
func (c config) startResolved(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt, ctx := res.rt, r.Context()
	// Quota (docs/16 P3-4): block a new running workspace once the tenant is at
	// its max_workspaces. 0/unset = unlimited. Counted authoritatively via docker.
	if rt.state(ctx) != "running" {
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
	if err := rt.start(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_ = c.mgr.store.SetWorkspaceState(ctx, res.ws.ID, "running")
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.name, "state": "running"})
}

func (c config) handleWorkspaceStop(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	if err := res.rt.stop(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_ = c.mgr.store.SetWorkspaceState(r.Context(), res.ws.ID, "stopped")
	writeJSON(w, http.StatusOK, map[string]any{"name": res.rt.name, "state": "stopped"})
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
	Alive     bool   `json:"alive"`
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
	if res.rt.state(ctx) == "running" {
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
			writeAPIErr(w, &apiError{http.StatusTooManyRequests, "quota_sessions",
				fmt.Sprintf("session limit reached (%d)", lim)})
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
