package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	case "cloudwatch":
		runCloudWatchMCP(extra)
	case "aws":
		runAWSMCP(extra)
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

// runCloudWatchMCP execs the awslabs CloudWatch MCP server with the stored AWS
// profile/region set as env. No secret is injected — the server reads the AWS
// credential chain (the user's SSO login, same as ssm sessions), so an expired
// SSO session surfaces as per-tool errors and the fix is `aws sso login`. The
// server is read-only by design (all tools are read/analyze; verified v0.1.4),
// so no disable flags are needed.
func runCloudWatchMCP(extra []string) {
	s, err := secrets.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run cloudwatch: load secrets: %v\n", err)
		os.Exit(1)
	}
	if s.CloudWatch == nil || s.CloudWatch.Profile == "" {
		fmt.Fprintln(os.Stderr, "mcp-run cloudwatch: no CloudWatch connection configured")
		os.Exit(1)
	}
	env := append(os.Environ(), "AWS_PROFILE="+s.CloudWatch.Profile)
	if s.CloudWatch.Region != "" {
		env = append(env, "AWS_REGION="+s.CloudWatch.Region, "AWS_DEFAULT_REGION="+s.CloudWatch.Region)
	}
	// SSM-linked profiles live in per-session isolated configs, not ~/.aws/config
	// (session_ssm.go), so regenerate a durable ops config from the stored SSO
	// meta and point boto3 at it. Idempotent per spawn — survives clean-home.
	if s.CloudWatch.StartURL != "" {
		if err := writeOpsAWSConfig("cloudwatch", s.CloudWatch.AWSProfileRef); err != nil {
			fmt.Fprintf(os.Stderr, "mcp-run cloudwatch: write aws config: %v\n", err)
			os.Exit(1)
		}
		env = append(env, "AWS_CONFIG_FILE="+opsAWSConfigPath("cloudwatch"))
	}
	if os.Getenv("FASTMCP_LOG_LEVEL") == "" {
		env = append(env, "FASTMCP_LOG_LEVEL=ERROR")
	}
	// Baked entrypoint first (uv tool install in the image); uvx as the dev /
	// old-image fallback (fetches from PyPI on first run).
	if p, err := exec.LookPath("awslabs.cloudwatch-mcp-server"); err == nil {
		argv := append([]string{p}, extra...)
		if err := syscall.Exec(p, argv, env); err != nil {
			fmt.Fprintf(os.Stderr, "mcp-run cloudwatch: exec %s: %v\n", p, err)
			os.Exit(1)
		}
	}
	uvx, err := uvxPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run cloudwatch: %v\n", err)
		os.Exit(1)
	}
	// Pin the PyPI fetch to the verified version (versions.json) — on a lean
	// rootfs this fallback IS the normal path, and an unpinned uvx would pull
	// whatever latest is (docs/35 §35.7.2-6). No pin (dev build) = latest.
	pkg := "awslabs.cloudwatch-mcp-server"
	if pin := readBuildPins()["cloudwatch_mcp"]; pin != "" {
		pkg += "==" + pin
	}
	argv := append([]string{uvx, pkg}, extra...)
	if err := syscall.Exec(uvx, argv, env); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run cloudwatch: exec %s: %v\n", uvx, err)
		os.Exit(1)
	}
}

// opsAWSConfigPath is the durable aws config generated for an ops integration that
// rides an SSO profile (id = the integration id: "cloudwatch" / "aws") — a sibling
// of the per-session ~/.aws/af-sessions/*.config files, but persistent and
// session-independent. The SSO token cache stays in the shared ~/.aws/sso/cache, so
// a login done in an SSM session (or via `AWS_CONFIG_FILE=<this> aws sso login`) is
// reused here, and by the other integrations too.
func opsAWSConfigPath(id string) string {
	return filepath.Join(homeDir(), ".aws", "af-ops", id+".config")
}

// writeOpsAWSConfig (re)generates the durable ops aws config from the stored SSO
// meta. Idempotent; called at connect time (connections.go) and at every mcp-run
// spawn so it self-heals after a clean-home.
func writeOpsAWSConfig(id string, p secrets.AWSProfileRef) error {
	return writeSSMConfig(opsAWSConfigPath(id), session.SSMMeta{
		Profile:   p.Profile,
		StartURL:  p.StartURL,
		SSORegion: p.SSORegion,
		AccountID: p.AccountID,
		RoleName:  p.RoleName,
		Region:    p.Region,
	})
}

// AWS MCP (Agent Toolkit for AWS) — the server is AWS-operated and remote, reached
// over Streamable HTTP with SigV4 auth. The registry's own remote transport can't
// express that (it only carries static headers, docs/48 §3.1), so the integration is
// the official stdio proxy: mcp-proxy-for-aws signs each call with the local
// credential chain. That also keeps the "credential never lands in an MCP config"
// property of the other builtins for free — there is no credential to write down.
const (
	awsMCPPackage         = "mcp-proxy-for-aws"
	awsMCPDefaultEndpoint = "us-east-1" // the other published region is eu-central-1
)

// awsMCPEndpointURL builds the SigV4 MCP endpoint for a service region. Endpoint
// region ≠ resource region: this is where the MCP service itself runs.
func awsMCPEndpointURL(region string) string {
	if region == "" {
		region = awsMCPDefaultEndpoint
	}
	return "https://aws-mcp." + region + ".api.aws/mcp"
}

// awsMCPArgs builds the proxy's argv tail from the stored connection.
//
//   - --read-only unless the member opted into writes. Without it the server offers
//     call_aws (any of ~15,000 AWS API actions) and run_script (arbitrary code), and
//     the container is untrusted by design (reference/security.md §4.1-4.3).
//   - --retries: the first connect to the endpoint has been seen to fail once with a
//     transport read error and then succeed. From inside a CLI that reads as "the MCP
//     server is broken", so let the proxy retry rather than the human.
//   - --metadata AWS_REGION: the member's RESOURCE region, which the server uses to
//     scope calls. Distinct from the signing region (env, = the endpoint region).
func awsMCPArgs(c *secrets.AWSConn, extra []string) []string {
	args := []string{awsMCPEndpointURL(c.Endpoint), "--retries", "3"}
	if !c.Write {
		args = append(args, "--read-only")
	}
	if c.Region != "" {
		args = append(args, "--metadata", "AWS_REGION="+c.Region)
	}
	return append(args, extra...)
}

// runAWSMCP execs the AWS MCP proxy against the stored profile. Same no-secret story
// as CloudWatch: SigV4 comes off the credential chain, so an expired SSO login
// surfaces as per-tool errors and the fix is `aws sso login`. The flags are in
// awsMCPArgs above.
func runAWSMCP(extra []string) {
	s, err := secrets.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run aws: load secrets: %v\n", err)
		os.Exit(1)
	}
	if s.AWS == nil || s.AWS.Profile == "" {
		fmt.Fprintln(os.Stderr, "mcp-run aws: no AWS connection configured")
		os.Exit(1)
	}
	env := append(os.Environ(), "AWS_PROFILE="+s.AWS.Profile)
	// AWS_REGION here is the SIGNING region and must match the endpoint, not the
	// member's resource region — that one rides along as request metadata below.
	endpoint := s.AWS.Endpoint
	if endpoint == "" {
		endpoint = awsMCPDefaultEndpoint
	}
	env = append(env, "AWS_REGION="+endpoint, "AWS_DEFAULT_REGION="+endpoint)
	if s.AWS.StartURL != "" {
		if err := writeOpsAWSConfig("aws", s.AWS.AWSProfileRef); err != nil {
			fmt.Fprintf(os.Stderr, "mcp-run aws: write aws config: %v\n", err)
			os.Exit(1)
		}
		env = append(env, "AWS_CONFIG_FILE="+opsAWSConfigPath("aws"))
	}
	args := awsMCPArgs(s.AWS, extra)
	// Baked entrypoint first (uv tool install in the image); uvx as the dev / lean
	// rootfs fallback, pinned to the verified version (docs/35 §35.7.2-6).
	if p, err := exec.LookPath(awsMCPPackage); err == nil {
		argv := append([]string{p}, args...)
		if err := syscall.Exec(p, argv, env); err != nil {
			fmt.Fprintf(os.Stderr, "mcp-run aws: exec %s: %v\n", p, err)
			os.Exit(1)
		}
	}
	uvx, err := uvxPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run aws: %v\n", err)
		os.Exit(1)
	}
	pkg := awsMCPPackage
	if pin := readBuildPins()["aws_mcp_proxy"]; pin != "" {
		pkg += "==" + pin
	}
	argv := append([]string{uvx, pkg}, args...)
	if err := syscall.Exec(uvx, argv, env); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-run aws: exec %s: %v\n", uvx, err)
		os.Exit(1)
	}
}

// grafanaMCPPath resolves the mcp-grafana binary: PATH (the image bakes it into
// /usr/local/bin) first, the per-user installs next (~/.local/bin from Phase 0,
// ~/.local/share/agent-fleet/bin from the on-demand installer), and as the last
// resort it downloads the pinned release on the spot (lean rootfs — the bake is
// absent by design; docs/35 §35.7.2-6).
func grafanaMCPPath() (string, error) {
	if p, err := exec.LookPath("mcp-grafana"); err == nil {
		return p, nil
	}
	if home := homeDir(); home != "" {
		for _, p := range []string{
			filepath.Join(home, ".local", "bin", "mcp-grafana"),
			filepath.Join(agentFleetShareDir(), "bin", "mcp-grafana"),
		} {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
		}
	}
	if p, err := installGrafanaMCP(readBuildPins()["mcp_grafana"]); err == nil {
		return p, nil
	} else {
		return "", fmt.Errorf("mcp-grafana not found and on-demand install failed: %v", err)
	}
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
