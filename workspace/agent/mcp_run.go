package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// mcp-run is the credential-injecting launcher for external ops MCP servers
// (docs/25 Phase 1). An MCP config references `workspace-agent mcp-run <provider>`
// instead of embedding the provider's API key, so no secret is ever written into
// a claude/.claude.json MCP config. The wrapper loads the encrypted store at
// spawn, sets the provider's env vars into ONLY the child process, and execs the
// real server (uvx pagerduty-mcp). This mirrors the git cred-helper idiom: the
// secret lives solely in secrets.enc and is materialized on demand, never on disk
// as plaintext. If no credential is configured, it exits non-zero so claude simply
// reports the server as unavailable (the assistant just has no PagerDuty tools).

// runMCPRun handles `workspace-agent mcp-run <provider> [extra args...]`.
func runMCPRun(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "mcp-run: provider required (e.g. pagerduty)")
		os.Exit(2)
	}
	provider, extra := args[0], args[1:]
	switch provider {
	case "pagerduty":
		runPagerDutyMCP(extra)
	case "grafana":
		runGrafanaMCP(extra)
	default:
		fmt.Fprintf(os.Stderr, "mcp-run: unknown provider %q\n", provider)
		os.Exit(2)
	}
}

// runPagerDutyMCP execs `uvx pagerduty-mcp` with the stored API key injected as
// env. Read-only by default: --enable-write-tools is never passed here, so the
// server advertises only read tools regardless of the key's own scope.
func runPagerDutyMCP(extra []string) {
	s, err := secrets.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run pagerduty: load secrets: %v\n", err)
		os.Exit(1)
	}
	if s.PagerDuty == nil || s.PagerDuty.APIKey == "" {
		fmt.Fprintln(os.Stderr, "mcp-run pagerduty: no PagerDuty connection configured")
		os.Exit(1)
	}
	env := append(os.Environ(), "PAGERDUTY_USER_API_KEY="+s.PagerDuty.APIKey)
	if s.PagerDuty.Host != "" {
		env = append(env, "PAGERDUTY_API_HOST="+s.PagerDuty.Host)
	}
	uvx, err := uvxPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run pagerduty: %v\n", err)
		os.Exit(1)
	}
	argv := append([]string{uvx, "pagerduty-mcp"}, extra...)
	// exec replaces this process so stdio (JSON-RPC) is wired straight to the MCP
	// server; the injected key exists only in the exec'd child's env.
	if err := syscall.Exec(uvx, argv, env); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run pagerduty: exec %s: %v\n", uvx, err)
		os.Exit(1)
	}
}

// runGrafanaMCP execs the mcp-grafana binary (baked into the image) with the
// stored URL + service-account token injected as env. Read-only is enforced
// here, not in the MCP config: -disable-write -disable-admin are always
// prepended, so the config's wrapper reference alone can never yield write
// tools. Works unchanged for self-hosted / Grafana Cloud / Amazon Managed
// Grafana — AMG auth is the same Bearer service-account token (docs/25 AMG 検討).
func runGrafanaMCP(extra []string) {
	s, err := secrets.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run grafana: load secrets: %v\n", err)
		os.Exit(1)
	}
	if s.Grafana == nil || s.Grafana.URL == "" || s.Grafana.Token == "" {
		fmt.Fprintln(os.Stderr, "mcp-run grafana: no Grafana connection configured")
		os.Exit(1)
	}
	env := append(os.Environ(),
		"GRAFANA_URL="+s.Grafana.URL,
		"GRAFANA_SERVICE_ACCOUNT_TOKEN="+s.Grafana.Token,
	)
	bin, err := grafanaMCPPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run grafana: %v\n", err)
		os.Exit(1)
	}
	argv := append([]string{bin, "-disable-write", "-disable-admin"}, extra...)
	if err := syscall.Exec(bin, argv, env); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run grafana: exec %s: %v\n", bin, err)
		os.Exit(1)
	}
}

// grafanaMCPPath resolves the mcp-grafana binary: PATH (the image bakes it into
// /usr/local/bin) first, then the per-user Phase 0 install under ~/.local/bin.
func grafanaMCPPath() (string, error) {
	if p, err := exec.LookPath("mcp-grafana"); err == nil {
		return p, nil
	}
	if home := homeDir(); home != "" {
		p := filepath.Join(home, ".local", "bin", "mcp-grafana")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("mcp-grafana not found (baked into newer images; or install to ~/.local/bin)")
}

// uvxPath resolves the uvx launcher: PATH first, then the per-user install under
// ~/.local/bin (uv installed via `pip install --user uv` persists there across
// container recreation).
func uvxPath() (string, error) {
	if p, err := exec.LookPath("uvx"); err == nil {
		return p, nil
	}
	if home := homeDir(); home != "" {
		p := filepath.Join(home, ".local", "bin", "uvx")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("uvx not found (install with `pip install --user uv`)")
}
