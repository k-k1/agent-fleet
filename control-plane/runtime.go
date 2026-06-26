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

	// Each user's container sits alone on a dedicated network, so containers
	// cannot reach each other (相互不可視, docs/09 §9.7). The Agent is still
	// reached by the CP via the host-published 127.0.0.1 port; egress (git,
	// Claude API) works via the network's NAT.
	if err := d.ensureNetwork(ctx); err != nil {
		return err
	}

	args := []string{
		"run", "-d", "--name", d.name,
		"--memory", d.memory,
		"-p", fmt.Sprintf("127.0.0.1:%s:7700", d.agentPort),
		"-v", home + ":/home/node",
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
func (c config) rtFor(w http.ResponseWriter, r *http.Request) (*dockerRuntime, bool) {
	id := c.mgr.resolveIdentity(r)
	if id.key == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{"code": "unauthenticated", "message": "no gateway identity"},
		})
		return nil, false
	}
	rt, err := c.mgr.forUser(r.Context(), id.key, id.email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return nil, false
	}
	return rt, true
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
	rt, ok := c.rtFor(w, r)
	if !ok {
		return
	}
	if err := rt.start(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.name, "state": "running"})
}

func (c config) handleWorkspaceStop(w http.ResponseWriter, r *http.Request) {
	rt, ok := c.rtFor(w, r)
	if !ok {
		return
	}
	if err := rt.stop(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.name, "state": "stopped"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
