//go:build drift

// Drift detection for materialize (docs/log/48 §13). These tests run against the REAL agent CLI
// binaries and are kept out of an ordinary `go test ./...` by the build tag `drift`.
//
// Why they are needed: materialize stands on a contract we believe unilaterally — "this CLI's
// config file has this shape". When a CLI changes shape, af's unit tests stay green (they only
// read back what af itself wrote) and the breakage surfaces when a user says "I registered it
// but the tool never shows up". That is the same shape as claude's TUI string contract breaking
// from version to version (false-idle), so this layer exists to go red in CI first.
//
// How the check is built: the expected values are never copied out by hand. The CLI's own
// `mcp add` runs in an isolated HOME, and the config it generates is compared structurally
// against the config af materialized from the same definition. A hand-written expectation would
// be a tautology, af's tests agreeing with af.
//
// There are two exceptions. cursor has no `mcp add`, so the reference runs the other way round
// (cursor reads the file af wrote). Every one of kiro's `mcp` subcommands demands a login, so it
// is skipped where nobody is logged in. Nothing else needs authentication — `mcp add` /
// `mcp list` only read and write config files. agy cannot start on this host (no RDRAND), which
// makes it the one kind with no drift-detection layer.

package mcpreg

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func cliBin(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatalf("%s not on PATH and E2E_REQUIRE=1: %v", name, err)
		}
		t.Skipf("%s not on PATH (set E2E_REQUIRE=1 to make this fatal): %v", name, err)
	}
	return p
}

func runCLI(t *testing.T, env []string, bin string, args ...string) []byte {
	t.Helper()
	return runCLIIn(t, "", env, bin, args...)
}

// runCLIIn is runCLI with a working directory — kiro's only login-free write scope is
// the one under the CWD (see TestDriftKiroMatchesMCPAdd).
func runCLIIn(t *testing.T, dir string, env []string, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
	return out
}

// serverEntry pulls one server out of a CLI's JSON config: root[key][name].
func serverEntry(t *testing.T, path, key, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	root := map[string]any{}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	m, _ := root[key].(map[string]any)
	e, ok := m[name].(map[string]any)
	if !ok {
		t.Fatalf("%s: %s has no %q: %v", path, key, name, m)
	}
	return e
}

// requireSameKeys compares what the CLI wrote with what af wrote, allowing af the
// extra members named in afExtra (keys af sets on purpose that the CLI's own `mcp add`
// leaves to a default). Anything else — a renamed key, a dropped key, a changed value —
// is the drift this file exists to catch.
func requireSameKeys(t *testing.T, kind string, got, want map[string]any, afExtra ...string) {
	t.Helper()
	for k, wv := range want {
		if gv, ok := got[k]; !ok || !reflect.DeepEqual(gv, wv) {
			gj, _ := json.MarshalIndent(got, "", "  ")
			wj, _ := json.MarshalIndent(want, "", "  ")
			t.Fatalf("%s config shape changed (docs/log/48 §8.1 needs updating): %q\n--- af\n%s\n--- %s mcp add\n%s",
				kind, k, gj, kind, wj)
		}
	}
	allowed := map[string]bool{}
	for _, k := range afExtra {
		allowed[k] = true
	}
	for k := range got {
		if _, ok := want[k]; !ok && !allowed[k] {
			t.Fatalf("%s: key %q is written by af only (either the CLI dropped it, or af writes too much)", kind, k)
		}
	}
}

// --- claude: mcpServers.<name> in $CLAUDE_CONFIG_DIR/.claude.json ----------------

func claudeServerEntry(t *testing.T, dir, name string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var root struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("parse .claude.json: %v", err)
	}
	e, ok := root.MCPServers[name]
	if !ok {
		t.Fatalf("mcpServers has no %q: %v", name, root.MCPServers)
	}
	return e
}

func TestDriftClaudeMatchesMCPAdd(t *testing.T) {
	bin := cliBin(t, "claude")
	cases := []struct {
		name string
		def  ServerDef
		add  []string
	}{
		{
			name: "stdio",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportStdio,
				Command: "/bin/echo", Args: []string{"a", "b"}, Env: map[string]string{"K": "v"}}),
			add: []string{"mcp", "add", "-s", "user", "afdrift", "-e", "K=v", "--", "/bin/echo", "a", "b"},
		},
		{
			name: "remote",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportHTTP,
				URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t", "X-Team": "sre"}}),
			add: []string{"mcp", "add", "-s", "user", "-t", "http", "afdrift", "https://mcp.example.com/mcp",
				"-H", "Authorization: Bearer t", "-H", "X-Team: sre"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The side the CLI was made to write.
			cliDir := t.TempDir()
			runCLI(t, []string{"CLAUDE_CONFIG_DIR=" + cliDir}, bin, tc.add...)
			want := claudeServerEntry(t, cliDir, "afdrift")

			// The side af materialized.
			afHome := t.TempDir()
			t.Setenv("HOME", afHome)
			afDir := filepath.Join(afHome, "claude-cfg")
			t.Setenv("CLAUDE_CONFIG_DIR", afDir)
			if _, _, _, err := materializeClaude([]ServerDef{tc.def}, nil); err != nil {
				t.Fatalf("materializeClaude: %v", err)
			}
			got := claudeServerEntry(t, afDir, "afdrift")

			if !reflect.DeepEqual(got, want) {
				gj, _ := json.MarshalIndent(got, "", "  ")
				wj, _ := json.MarshalIndent(want, "", "  ")
				t.Fatalf("claude's user-scope config shape changed (docs/log/48 §8.1 needs updating)\n--- af\n%s\n--- claude mcp add\n%s", gj, wj)
			}
		})
	}
}

// --- codex: [mcp_servers.<name>] in $CODEX_HOME/config.toml ----------------------

// codexListed returns what `codex mcp list --json` makes of the config in home —
// i.e. codex's OWN reading of what af wrote, not af's reading of it.
func codexListed(t *testing.T, bin, home, name string) map[string]any {
	t.Helper()
	out := runCLI(t, []string{"CODEX_HOME=" + home}, bin, "mcp", "list", "--json")
	// The CLI prefixes a PATH-alias warning on a tempdir CODEX_HOME; the JSON array
	// is the tail of the output.
	i := 0
	for i < len(out) && out[i] != '[' {
		i++
	}
	var servers []map[string]any
	if err := json.Unmarshal(out[i:], &servers); err != nil {
		t.Fatalf("parse `codex mcp list --json`: %v\n%s", err, out)
	}
	for _, s := range servers {
		if s["name"] == name {
			return s
		}
	}
	t.Fatalf("codex did not recognise %q (it cannot read the config.toml af wrote):\n%s", name, out)
	return nil
}

func TestDriftCodexMatchesMCPAdd(t *testing.T) {
	bin := cliBin(t, "codex")
	def := sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportStdio,
		Command: "/bin/echo", Args: []string{"a", "b"}, Env: map[string]string{"K": "v"}})

	cliHome := t.TempDir()
	runCLI(t, []string{"CODEX_HOME=" + cliHome}, bin,
		"mcp", "add", "afdrift", "--env", "K=v", "--", "/bin/echo", "a", "b")
	want := codexListed(t, bin, cliHome, "afdrift")

	afHome := t.TempDir()
	t.Setenv("HOME", afHome)
	codexHome := filepath.Join(afHome, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatalf("materializeCodex: %v", err)
	}
	got := codexListed(t, bin, codexHome, "afdrift")

	if !reflect.DeepEqual(got["transport"], want["transport"]) {
		gj, _ := json.MarshalIndent(got["transport"], "", "  ")
		wj, _ := json.MarshalIndent(want["transport"], "", "  ")
		t.Fatalf("codex's stdio config shape changed (docs/log/48 §8.1 needs updating)\n--- af\n%s\n--- codex mcp add\n%s", gj, wj)
	}
}

// TestDriftCodexRemoteKeys pins the remote half, which `codex mcp add` cannot
// express (it has no header flags — that gap is what the old docs/log/48 note mistook
// for "codex has no remote headers"). So the reference here is codex's own reader:
// af writes the file, codex parses it back.
func TestDriftCodexRemoteKeys(t *testing.T) {
	bin := cliBin(t, "codex")
	def := sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportHTTP,
		URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"},
		TimeoutMS: 12000})

	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatalf("materializeCodex: %v", err)
	}
	got := codexListed(t, bin, codexHome, "afdrift")

	tr, _ := got["transport"].(map[string]any)
	if tr["type"] != "streamable_http" || tr["url"] != "https://mcp.example.com/mcp" {
		t.Fatalf("the remote is not read back as streamable_http: %v", tr)
	}
	hdr, _ := tr["http_headers"].(map[string]any)
	if hdr["Authorization"] != "Bearer t" {
		t.Fatalf("http_headers did not get through: %v", tr)
	}
	if got["startup_timeout_sec"] != 12.0 {
		t.Fatalf("startup_timeout_sec = %v, want 12 (TimeoutMS converted ms->s)", got["startup_timeout_sec"])
	}
}

// TestDriftCodexAcceptsUserFileWithAFBlocks: codex can still read the WHOLE file after af has
// appended to a user's hand-written config. A duplicate TOML table or a broken append does not
// merely cost one MCP server — codex itself stops starting.
func TestDriftCodexAcceptsUserFileWithAFBlocks(t *testing.T) {
	bin := cliBin(t, "codex")
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte(codexUserConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	def := sessionDef(ServerDef{Name: "mine", Origin: OriginUser, Transport: TransportStdio,
		Command: "/bin/echo"}) // same name as the hand-written one: the duplicate-table case
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatalf("materializeCodex: %v", err)
	}
	got := codexListed(t, bin, codexHome, "mine")
	tr, _ := got["transport"].(map[string]any)
	if tr["command"] != "/bin/echo" {
		t.Fatalf("the hand-written table of the same name was not replaced by af's definition: %v", tr)
	}
}

// --- opencode: mcp.<name> in ~/.config/opencode/opencode.jsonc -------------------

func TestDriftOpencodeMatchesMCPAdd(t *testing.T) {
	bin := cliBin(t, "opencode")
	cases := []struct {
		name string
		def  ServerDef
		add  []string
	}{
		{
			name: "local",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportStdio,
				Command: "/bin/echo", Args: []string{"a", "b"}, Env: map[string]string{"K": "v"}}),
			add: []string{"mcp", "add", "afdrift", "--env", "K=v", "--", "/bin/echo", "a", "b"},
		},
		{
			name: "remote",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportHTTP,
				URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}),
			add: []string{"mcp", "add", "afdrift", "--url", "https://mcp.example.com/mcp",
				"--header", "Authorization=Bearer t"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cliHome := t.TempDir()
			runCLI(t, []string{"HOME=" + cliHome, "XDG_CONFIG_HOME=" + filepath.Join(cliHome, ".config")},
				bin, tc.add...)
			want := serverEntry(t, filepath.Join(cliHome, ".config", "opencode", "opencode.jsonc"), "mcp", "afdrift")

			afHome := t.TempDir()
			t.Setenv("HOME", afHome)
			if _, _, _, err := materializeOpencode([]ServerDef{tc.def}, nil); err != nil {
				t.Fatalf("materializeOpencode: %v", err)
			}
			got := serverEntry(t, opencodeConfigPath(), "mcp", "afdrift")

			// "enabled" is just af spelling out opencode's own default (true).
			requireSameKeys(t, "opencode", got, want, "enabled")
		})
	}
}

// --- copilot: mcpServers.<name> in $COPILOT_HOME/mcp-config.json -----------------

func TestDriftCopilotMatchesMCPAdd(t *testing.T) {
	bin := cliBin(t, "copilot")
	cases := []struct {
		name string
		def  ServerDef
		add  []string
	}{
		{
			name: "local",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportStdio,
				Command: "/bin/echo", Args: []string{"a", "b"}, Env: map[string]string{"K": "v"}}),
			add: []string{"mcp", "add", "afdrift", "--env", "K=v", "--", "/bin/echo", "a", "b"},
		},
		{
			name: "remote",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportHTTP,
				URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"},
				TimeoutMS: 12000}),
			add: []string{"mcp", "add", "--transport", "http", "--timeout", "12000",
				"--header", "Authorization: Bearer t", "afdrift", "https://mcp.example.com/mcp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cliHome := t.TempDir()
			runCLI(t, []string{"COPILOT_HOME=" + cliHome}, bin, tc.add...)
			want := serverEntry(t, filepath.Join(cliHome, "mcp-config.json"), "mcpServers", "afdrift")

			afHome := t.TempDir()
			t.Setenv("HOME", afHome)
			t.Setenv("COPILOT_HOME", filepath.Join(afHome, "copilot-home"))
			if _, _, _, err := materializeCopilot([]ServerDef{tc.def}, nil); err != nil {
				t.Fatalf("materializeCopilot: %v", err)
			}
			got := serverEntry(t, copilotMCPConfigPath(), "mcpServers", "afdrift")

			requireSameKeys(t, "copilot", got, want)
		})
	}
}

// --- kiro: mcpServers.<name> in ~/.kiro/settings/mcp.json ------------------------

// TestDriftKiroMatchesMCPAdd checks kiro's config shape against `kiro-cli mcp add`.
//
// kiro alone is built differently: every `mcp` subcommand demands a login, so the CLI side
// cannot run in an isolated HOME. The CLI therefore gets the real HOME's credentials while its
// writes are diverted to the workspace scope under the CWD (<cwd>/.kiro/settings/mcp.json) —
// the generated file without touching the developer's global config. The af side uses an
// isolated HOME as usual. Where nobody is logged in (CI) the test skips.
func TestDriftKiroMatchesMCPAdd(t *testing.T) {
	bin := cliBin(t, "kiro-cli")
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if out, err := exec.Command(bin, "whoami").CombinedOutput(); err != nil {
		if os.Getenv("E2E_REQUIRE") == "1" {
			t.Fatalf("not logged in to kiro-cli (E2E_REQUIRE=1):\n%s", out)
		}
		t.Skipf("not logged in to kiro-cli, so the config shape cannot be checked:\n%s", out)
	}

	cases := []struct {
		name string
		def  ServerDef
		add  []string
	}{
		{
			name: "local",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportStdio,
				Command: "/bin/echo", Args: []string{"a", "b"}, Env: map[string]string{"K": "v"},
				TimeoutMS: 12000}),
			add: []string{"mcp", "add", "--scope", "workspace", "--name", "afdrift",
				"--command", "/bin/echo", "--args", "a", "--args", "b", "--env", "K=v", "--timeout", "12000"},
		},
		{
			name: "remote",
			def: sessionDef(ServerDef{Name: "afdrift", Origin: OriginUser, Transport: TransportHTTP,
				URL: "https://mcp.example.com/mcp", TimeoutMS: 12000}),
			add: []string{"mcp", "add", "--scope", "workspace", "--name", "afdrift",
				"--url", "https://mcp.example.com/mcp", "--timeout", "12000"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cliWS := t.TempDir()
			runCLIIn(t, cliWS, []string{"HOME=" + realHome}, bin, tc.add...)
			want := serverEntry(t, filepath.Join(cliWS, ".kiro", "settings", "mcp.json"), "mcpServers", "afdrift")

			afHome := t.TempDir()
			t.Setenv("HOME", afHome)
			if _, _, _, err := materializeKiro([]ServerDef{tc.def}, nil); err != nil {
				t.Fatalf("materializeKiro: %v", err)
			}
			got := serverEntry(t, kiroMCPConfigPath(), "mcpServers", "afdrift")

			requireSameKeys(t, "kiro", got, want)
		})
	}
}

// --- cursor: mcpServers.<name> in ~/.cursor/mcp.json -----------------------------

// TestDriftCursorReadsAFConfig: cursor-agent has no `mcp add` (only list / enable / login), so
// the reference is whether cursor itself can read the file af wrote. If the names show up in
// `mcp list`, at least the mcpServers key and the discriminators of an entry are still alive.
func TestDriftCursorReadsAFConfig(t *testing.T) {
	bin := cliBin(t, "cursor-agent")
	afHome := t.TempDir()
	t.Setenv("HOME", afHome)
	defs := []ServerDef{
		sessionDef(ServerDef{Name: "afdriftlocal", Origin: OriginUser, Transport: TransportStdio,
			Command: "/bin/echo", Args: []string{"a"}}),
		sessionDef(ServerDef{Name: "afdriftremote", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}),
	}
	if _, _, _, err := materializeCursor(defs, nil); err != nil {
		t.Fatalf("materializeCursor: %v", err)
	}
	// `mcp list` prints an unreachable server as an error line, so the exit code is not read.
	cmd := exec.Command(bin, "mcp", "list")
	cmd.Dir = afHome
	cmd.Env = append(os.Environ(), "HOME="+afHome)
	out, _ := cmd.CombinedOutput()
	for _, name := range []string{"afdriftlocal", "afdriftremote"} {
		if !strings.Contains(string(out), name) {
			t.Fatalf("cursor cannot read af's ~/.cursor/mcp.json (%q does not appear):\n%s", name, out)
		}
	}
}
