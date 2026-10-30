package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The shipped plugins are one module per file that opencode 1 (server()) and opencode 2
// (id + setup()) must both accept. This drives them with fake hosts of each major; the real
// binaries are exercised by the contract tests. testdata/plugin_modules.mjs holds the assertions.
func TestOpencodePluginModulesServeBothMajors(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		skipUnlessV2Required(t, "node is needed to load the plugin modules")
	}
	bin, home := t.TempDir(), t.TempDir()
	failPutEnv := filepath.Join(t.TempDir(), "fail-put-env")
	agentLog := filepath.Join(t.TempDir(), "agent.log")
	// rtk rewrite exits non-zero when it rewrites (measured); git commands stand in for that.
	writeScript(t, filepath.Join(bin, "rtk"), `#!/bin/sh
case "$2" in
  git*) echo "rtk $2"; exit 3 ;;
  *) echo "$2"; exit 1 ;;
esac
`)
	writeScript(t, filepath.Join(bin, "workspace-agent"), "#!/bin/sh\necho \"$*\" >> \""+agentLog+"\"\nif [ \"$1\" = opencode-engine-token ]; then echo \"tok-$2\"; fi\nif [ \"$1\" = opencode-claim ] && [ \"$3\" = ses_lose ]; then exit 3; fi\nif [ \"$1\" = opencode-put-env ]; then sleep 0.2; fi\nif [ \"$1\" = opencode-put-env ] && [ -e \""+failPutEnv+"\" ]; then exit 1; fi\n")

	pluginDir := filepath.Join("..", "..", "..", "..", "opencode-plugin")
	cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings",
		filepath.Join("testdata", "plugin_modules.mjs"), pluginDir, bin, agentLog)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home,
		"AF_SESSION_SID=sid-test",
		"AF_WORKSPACE_AGENT_BIN="+filepath.Join(bin, "workspace-agent"),
		// The shared-server plugins read their timing and marker path once, at load.
		"AF_TEST_PUT_ENV_FAIL_FILE="+failPutEnv,
		"AF_OPENCODE_TAG_WAIT_MS=700",
		"AF_OPENCODE_TUI_POLL_MS=25",
		"AF_OPENCODE_TUI_SERVER_POLL_MS=25",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plugin modules: %v\n%s", err, out)
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
