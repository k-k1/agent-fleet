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
	dataDir    string // host path; <dataDir>/home is bind-mounted to ~ in the container
	agentHost  string
	agentPort  string
	memory     string
	sessionCmd string
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

	args := []string{
		"run", "-d", "--name", d.name,
		"--memory", d.memory,
		"-p", fmt.Sprintf("127.0.0.1:%s:7700", d.agentPort),
		"-v", home + ":/home/node",
	}
	if d.sessionCmd != "" {
		args = append(args, "-e", "AGENT_SESSION_CMD="+d.sessionCmd)
	}
	args = append(args, d.image)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %v: %s", err, out)
	}
	return d.waitHealthy(ctx, 15*time.Second)
}

func (d *dockerRuntime) stop(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", d.name).CombinedOutput(); err != nil {
		return fmt.Errorf("docker rm: %v: %s", err, out)
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

func (c config) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": c.rt.name, "state": c.rt.state(r.Context())})
}

func (c config) handleWorkspaceStart(w http.ResponseWriter, r *http.Request) {
	if err := c.rt.start(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": c.rt.name, "state": "running"})
}

func (c config) handleWorkspaceStop(w http.ResponseWriter, r *http.Request) {
	if err := c.rt.stop(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": c.rt.name, "state": "stopped"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
