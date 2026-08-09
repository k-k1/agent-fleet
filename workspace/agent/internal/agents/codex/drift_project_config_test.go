//go:build drift

// Does a TRUSTED project's own `.codex/config.toml` contribute MCP servers, and what
// happens to them when af sends a thread-scoped `mcp_servers` map?
//
// This matters because af sends a thread-scoped map to stamp the session name on its
// own MCP entry. If that map replaced the file layers, a trusted project's servers
// would vanish from managed sessions while still working in TUI ones. Measured: they
// merge — but that is a contract worth pinning, not an assumption.
//
// `codex mcp list` is NOT a valid probe here — it reports only user-level servers
// (openai/codex#13025), which is what made an earlier measurement conclude, wrongly,
// that codex has no project-scoped MCP at all. The runtime's own view
// (`mcpServerStatus/list {threadId}`) is the honest one, so that is what these use.
package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	driftGlobalSrv  = "af_drift_global_srv"
	driftProjectSrv = "af_drift_project_srv"
)

// seedProjectTrustedConfig writes a user-level server plus a trust entry for dir, and
// dir's own project-scoped config with a DIFFERENT server. Trust is the documented
// gate on project config, so without it the measurement would only prove "untrusted
// project config is ignored".
func seedProjectTrustedConfig(t *testing.T, dir string) func(home string) {
	t.Helper()
	projCfg := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(projCfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projCfg, "config.toml"),
		fmt.Appendf(nil, "[mcp_servers.%s]\ncommand = \"/bin/true\"\n", driftProjectSrv), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(home string) {
		codexHome := filepath.Join(home, ".codex")
		if err := os.MkdirAll(codexHome, 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("[mcp_servers.%s]\ncommand = \"/bin/true\"\n\n[projects.%q]\ntrust_level = \"trusted\"\n",
			driftGlobalSrv, dir)
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDriftCodexTrustedProjectConfigContributesMCPServers establishes the premise: a
// trusted project's .codex/config.toml really does add servers to a thread whose cwd
// is that project. If this ever stops holding, the sibling test below is measuring
// nothing and af's replacement has no layer to lose.
func TestDriftCodexTrustedProjectConfigContributesMCPServers(t *testing.T) {
	proj := driftProjectDir(t)
	cl := startDriftAppServerSeeded(t, seedProjectTrustedConfig(t, proj))

	tid := startDriftThreadInDir(t, cl, proj, nil)
	waitDriftMCPServer(t, cl, tid, driftGlobalSrv)
	if names := driftMCPServerNames(t, cl, tid); !names[driftProjectSrv] {
		t.Skipf("trusted project .codex/config.toml contributed nothing (saw %v) — either this "+
			"codex does not merge project config, or trust is expressed differently than "+
			"`[projects.<dir>] trust_level`; treat the sibling test's result as unproven", names)
	}
}

// TestDriftCodexThreadConfigKeepsProjectServers pins the property af depends on: a
// thread-scoped map ADDS to the project layer instead of replacing it, so a managed
// session keeps a trusted project's own MCP servers.
func TestDriftCodexThreadConfigKeepsProjectServers(t *testing.T) {
	proj := driftProjectDir(t)
	cl := startDriftAppServerSeeded(t, seedProjectTrustedConfig(t, proj))

	// Control: inherited, both layers visible.
	inherited := startDriftThreadInDir(t, cl, proj, nil)
	waitDriftMCPServer(t, cl, inherited, driftGlobalSrv)
	if names := driftMCPServerNames(t, cl, inherited); !names[driftProjectSrv] {
		t.Skipf("project layer not visible even when inheriting (saw %v) — nothing to lose here", names)
	}

	// af's thread-scoped map: only what af re-emits survives.
	scoped := startDriftThreadInDir(t, cl, proj, map[string]any{
		"mcp_servers": map[string]any{"af_only": map[string]any{"command": "/bin/true"}},
	})
	waitDriftMCPServer(t, cl, scoped, "af_only")
	driftSettle(t, cl, scoped)
	names := driftMCPServerNames(t, cl, scoped)
	if !names[driftProjectSrv] || !names[driftGlobalSrv] {
		t.Fatalf("a thread-scoped map now REPLACES the file layers (%v): managed codex sessions "+
			"just lost the trusted project's own MCP servers and every config.toml row af does "+
			"not own. mcpreg.CodexThreadServers restates only the af entry and must be changed "+
			"to re-emit the whole effective set", names)
	}
	t.Logf("thread-scoped map leaves %v: file layers merge through, so af only has to restate "+
		"its own entry", driftSorted(names))
}

// driftProjectDir returns a project directory codex will accept. t.TempDir() lives
// under /tmp, which codex treats specially, so keep projects beside the isolated home.
func driftProjectDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(base, ".cache"), "af-codex-proj-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startDriftThreadInDir(t *testing.T, cl *appClient, dir string, config map[string]any) string {
	t.Helper()
	params := map[string]any{"cwd": dir}
	if config != nil {
		params["config"] = config
	}
	res, err := cl.call("thread/start", params, 15*time.Second)
	if err != nil {
		t.Fatalf("thread/start in %s: %v", dir, err)
	}
	st, err := parseThreadResult(res)
	if err != nil || st.threadID == "" {
		t.Fatalf("thread/start returned no thread id: %v", err)
	}
	return st.threadID
}

func sortedNameList(names map[string]bool) []string {
	var out []string
	for n := range names {
		out = append(out, n)
	}
	return out
}
