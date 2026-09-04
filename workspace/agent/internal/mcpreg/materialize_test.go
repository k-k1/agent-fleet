package mcpreg

// docs/log/48 §13, "materialize is non-destructive": write then remove, round trip, against a
// hand-written user config, and pin that the hand-written part survives while only af's part
// goes. Broken, this produces failures heavier than the feature itself — "af registered an MCP
// server and claude's trust dialog was gone" / "codex can no longer read its config".

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// withTempCLIHomes isolates BOTH the af store and every CLI config tree. HOME covers
// most of them, but the three that have their own environment override do NOT: the
// workspace points CLAUDE_CONFIG_DIR at the shared claude state mount, so a test that
// forgot these would rewrite the developer's real config files.
func withTempCLIHomes(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-cfg"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, "copilot-home"))
	return home
}

func sessionDef(d ServerDef) ServerDef {
	d.Enabled = true
	d.Targets = Targets{Session: true}
	return d
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// --- claude ------------------------------------------------------------------

func TestMaterializeClaudeKeepsUserState(t *testing.T) {
	withTempCLIHomes(t)
	path := claudeJSONPath()
	// State claude writes itself (onboarding done, trust granted), plus a server the user added
	// with `claude mcp add`.
	writeFile(t, path, `{
  "hasCompletedOnboarding": true,
  "projects": {"/home/dev/repos/x": {"hasTrustDialogAccepted": true}},
  "mcpServers": {"mine": {"type": "stdio", "command": "/usr/bin/mine"}}
}`)

	defs := []ServerDef{
		sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio,
			Command: "npx", Args: []string{"-y", "wiki-mcp"}, Env: map[string]string{"TOKEN": "s3cret"}}),
		sessionDef(ServerDef{Name: "tickets", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}),
	}
	written, removed, changed, err := materializeClaude(defs, nil)
	if err != nil || !changed {
		t.Fatalf("materializeClaude = %v, changed=%v", err, changed)
	}
	if len(written) != 2 || len(removed) != 0 {
		t.Fatalf("written=%v removed=%v", written, removed)
	}

	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	if v, _ := root["hasCompletedOnboarding"].(bool); !v {
		t.Fatal("claude's own state was wiped (onboarding would start over)")
	}
	if root["projects"] == nil {
		t.Fatal("the trusted projects were wiped")
	}
	srv, _ := root["mcpServers"].(map[string]any)
	for _, want := range []string{"mine", "wiki", "tickets"} {
		if srv[want] == nil {
			t.Fatalf("mcpServers has no %q: %v", want, srv)
		}
	}
	if got := srv["tickets"].(map[string]any)["headers"].(map[string]any)["Authorization"]; got != "Bearer t" {
		t.Fatalf("the remote server's header was not materialized: %v", got)
	}

	// The second run changes nothing. claude rewrites this file constantly, so a pointless
	// write-back is avoided: it would widen the window in which a concurrent claude write is
	// stamped over.
	if _, _, changed, err := materializeClaude(defs, written); err != nil || changed {
		t.Fatalf("second run = changed %v, err %v (not idempotent)", changed, err)
	}

	// Remove everything from the registry: only the 2 rows af wrote go, the user's "mine" stays.
	_, removed, changed, err = materializeClaude(nil, written)
	if err != nil || !changed {
		t.Fatalf("removing materialize = %v, changed=%v", err, changed)
	}
	if len(removed) != 2 {
		t.Fatalf("removed=%v, want 2 rows", removed)
	}
	root = map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	srv, _ = root["mcpServers"].(map[string]any)
	if len(srv) != 1 || srv["mine"] == nil {
		t.Fatalf("the user's hand-written rows were taken down with it: %v", srv)
	}
}

func TestMaterializeClaudeNoKeyWhenEmpty(t *testing.T) {
	withTempCLIHomes(t)
	path := claudeJSONPath()
	writeFile(t, path, `{"hasCompletedOnboarding": true}`)

	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "x"})
	if _, _, _, err := materializeClaude([]ServerDef{def}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := materializeClaude(nil, []string{"wiki"}); err != nil {
		t.Fatal(err)
	}
	root := map[string]any{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["mcpServers"]; ok {
		t.Fatalf("an empty mcpServers was left behind: %v", root)
	}
}

func TestMaterializeClaudeRefusesUnparseable(t *testing.T) {
	withTempCLIHomes(t)
	path := claudeJSONPath()
	broken := "{ this is not json"
	writeFile(t, path, broken)

	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "x"})
	if _, _, _, err := materializeClaude([]ServerDef{def}, nil); err == nil {
		t.Fatal("a corrupt .claude.json was silently overwritten")
	}
	if got := readFile(t, path); got != broken {
		t.Fatalf("the write was refused, yet the file was touched: %q", got)
	}
}

func TestMaterializeClaudeCreatesFile(t *testing.T) {
	withTempCLIHomes(t)
	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "x"})
	if _, _, changed, err := materializeClaude([]ServerDef{def}, nil); err != nil || !changed {
		t.Fatalf("= %v, changed=%v", err, changed)
	}
	fi, err := os.Stat(claudeJSONPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (this file holds secrets)", fi.Mode().Perm())
	}
}

// --- codex -------------------------------------------------------------------

const codexUserConfig = `# 利用者のコメント
model = "gpt-5"

[projects."/home/dev/repos/x"]
trust_level = "trusted"

[mcp_servers.mine]
command = "/usr/bin/mine"
`

func TestMaterializeCodexRoundTrip(t *testing.T) {
	withTempCLIHomes(t)
	path := codexConfigPath()
	writeFile(t, path, codexUserConfig)

	defs := []ServerDef{
		sessionDef(ServerDef{Name: "tickets", Origin: OriginUser, Transport: TransportHTTP,
			URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer t", "X-Team": "sre"},
			TimeoutMS: 12000}),
		sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio,
			Command: "npx", Args: []string{"-y", "wiki-mcp"}, Env: map[string]string{"TOKEN": "s3cret"}}),
	}
	written, _, changed, err := materializeCodex(defs, nil)
	if err != nil || !changed {
		t.Fatalf("materializeCodex = %v, changed=%v", err, changed)
	}
	got := readFile(t, path)
	for _, want := range []string{
		"# 利用者のコメント",
		`[projects."/home/dev/repos/x"]`,
		"[mcp_servers.mine]",
		"[mcp_servers.tickets]",
		`url = "https://mcp.example.com/mcp"`,
		"startup_timeout_sec = 12.0",
		"[mcp_servers.tickets.http_headers]",
		`Authorization = "Bearer t"`,
		"[mcp_servers.wiki]",
		`args = ["-y","wiki-mcp"]`,
		"[mcp_servers.wiki.env]",
		`TOKEN = "s3cret"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the generated config has no %q:\n%s", want, got)
		}
	}

	// Idempotent: writing the same definitions again does not move the contents.
	if _, _, changed, err := materializeCodex(defs, written); err != nil || changed {
		t.Fatalf("second run = changed %v, err %v (not idempotent)", changed, err)
	}

	// Removing everything restores the original file down to the last byte.
	if _, _, _, err := materializeCodex(nil, written); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != codexUserConfig {
		t.Fatalf("the round trip did not restore the original:\n--- got\n%s\n--- want\n%s", got, codexUserConfig)
	}
}

// Codex gives stdio MCP children a default-deny environment. The built-in af
// server needs these names forwarded or af_report/Chromium calls reach the Agent REST
// without the bearer token and get 401.
func TestMaterializeCodexBuiltinAFForwardsAgentAuth(t *testing.T) {
	withTempCLIHomes(t)
	defs := []ServerDef{sessionDef(ServerDef{
		ID: BuiltinAF, Name: BuiltinAF, Origin: OriginBuiltin,
		Transport: TransportStdio, Command: "/usr/bin/workspace-agent",
		Args: []string{"mcp-stdio", "--self-report", "--chromium-attach"},
	})}
	if _, _, _, err := materializeCodex(defs, nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, codexConfigPath())
	if want := `env_vars = ["AF_SESSION_NAME","AGENT_ADDR","AGENT_TOKEN"]`; !strings.Contains(got, want) {
		t.Fatalf("af's Agent auth environment is not forwarded to the Codex MCP child:\n%s", got)
	}
	if strings.Contains(got, "AF_SECRET_KEY") {
		t.Fatalf("the secret-store key is forwarded although af session tools do not need it:\n%s", got)
	}
}

// TestMaterializeCodexReplacesSameName: an existing table of the same name is always folded
// into one. TOML makes a duplicate table an error, so missing this leaves the whole config.toml
// unreadable — not just one MCP server missing, but codex failing to start at all.
func TestMaterializeCodexReplacesSameName(t *testing.T) {
	withTempCLIHomes(t)
	path := codexConfigPath()
	writeFile(t, path, "[mcp_servers.wiki]\ncommand = \"/old/wiki\"\n")

	def := sessionDef(ServerDef{Name: "wiki", Origin: OriginUser, Transport: TransportStdio, Command: "/new/wiki"})
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if n := strings.Count(got, "[mcp_servers.wiki]"); n != 1 {
		t.Fatalf("%d occurrences of [mcp_servers.wiki] (a duplicate TOML table):\n%s", n, got)
	}
	if strings.Contains(got, "/old/wiki") {
		t.Fatalf("the old definition survived:\n%s", got)
	}
}

func TestMaterializeCodexQuotesOddHeaderKey(t *testing.T) {
	withTempCLIHomes(t)
	def := sessionDef(ServerDef{Name: "s", Origin: OriginUser, Transport: TransportHTTP,
		URL: "https://e.com/mcp", Headers: map[string]string{"X.Odd": "v"}})
	if _, _, _, err := materializeCodex([]ServerDef{def}, nil); err != nil {
		t.Fatal(err)
	}
	// Unquoted, `X.Odd` becomes a nested table through the dot notation and turns into a
	// different header.
	if got := readFile(t, codexConfigPath()); !strings.Contains(got, `"X.Odd" = "v"`) {
		t.Fatalf("the header name was not quoted:\n%s", got)
	}
}

func TestStripCodexServersLeavesOtherTables(t *testing.T) {
	src := "[mcp_servers.a]\ncommand = \"a\"\n\n[[profiles]]\nname = \"p\"\n\n[mcp_servers.b]\ncommand = \"b\"\n"
	got := stripCodexServers(src, func(n string) bool { return n == "a" })
	if strings.Contains(got, "[mcp_servers.a]") {
		t.Fatalf("a was not removed:\n%s", got)
	}
	for _, want := range []string{"[[profiles]]", `name = "p"`, "[mcp_servers.b]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q was removed as collateral:\n%s", want, got)
		}
	}
}

// --- dispatch and the ledger --------------------------------------------------

func TestMaterializeUsesSessionTargetAndKind(t *testing.T) {
	withTempCLIHomes(t)
	mustCreate := func(d ServerDef) {
		t.Helper()
		if _, err := Create(d); err != nil {
			t.Fatalf("Create(%s): %v", d.Name, err)
		}
	}
	mustCreate(ServerDef{Name: "both", Transport: TransportStdio, Command: "x",
		Enabled: true, Targets: Targets{Assistant: true, Session: true}})
	mustCreate(ServerDef{Name: "chatonly", Transport: TransportStdio, Command: "x",
		Enabled: true, Targets: Targets{Assistant: true}})
	mustCreate(ServerDef{Name: "off", Transport: TransportStdio, Command: "x",
		Targets: Targets{Session: true}})
	mustCreate(ServerDef{Name: "codexonly", Transport: TransportStdio, Command: "x",
		Enabled: true, Targets: Targets{Session: true}, Kinds: []string{session.KindCodex}})

	res := Materialize(session.KindClaude)
	if res.Err != "" {
		t.Fatalf("claude: %s", res.Err)
	}
	// af is the built-in server on the self-report fast path (docs/log/51 Phase 3): it needs no
	// connection and is distributed to every kind, so it is in every kind's materialize.
	if !reflect.DeepEqual(res.Written, []string{"af", "both"}) {
		t.Fatalf("claude written = %v, want [af both]", res.Written)
	}
	res = Materialize(session.KindCodex)
	if res.Err != "" {
		t.Fatalf("codex: %s", res.Err)
	}
	if !reflect.DeepEqual(res.Written, []string{"af", "both", "codexonly"}) {
		t.Fatalf("codex written = %v, want [af both codexonly]", res.Written)
	}

	// The ledger records per kind (this list is the only thing removal is allowed to touch).
	m, err := loadManagedNames()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Kinds[session.KindClaude], []string{"af", "both"}) ||
		!reflect.DeepEqual(m.Kinds[session.KindCodex], []string{"af", "both", "codexonly"}) {
		t.Fatalf("wrong ledger: %+v", m.Kinds)
	}
}

// TestMaterializeSkipsKindsWithoutCLI: a kind with no agent CLI (shell / ssm) has nowhere to
// write, and must come back Skipped rather than as an error.
func TestMaterializeSkipsKindsWithoutCLI(t *testing.T) {
	withTempCLIHomes(t)
	for _, k := range []string{"shell", "ssm"} {
		if res := Materialize(k); !res.Skipped || res.Err != "" {
			t.Fatalf("%s = %+v, want skipped (having nowhere to write is not a failure)", k, res)
		}
	}
}

func TestMaterializeAllCoversImplementedKinds(t *testing.T) {
	withTempCLIHomes(t)
	res := MaterializeAll()
	if len(res) != len(MaterializedKinds) {
		t.Fatalf("MaterializeAll = %d results, want %d", len(res), len(MaterializedKinds))
	}
	for _, r := range res {
		if r.Err != "" || r.Skipped {
			t.Fatalf("%s = %+v", r.Kind, r)
		}
	}
}

// TestMaterializeRefusesCorruptLedger: a corrupt ledger must stop the write entirely, rather
// than being read as "af owns nothing" and orphaning the existing rows.
func TestMaterializeRefusesCorruptLedger(t *testing.T) {
	withTempCLIHomes(t)
	writeFile(t, managedNamesPath(), "not json")
	if res := Materialize(session.KindClaude); res.Err == "" {
		t.Fatalf("materialize carried on with a corrupt ledger: %+v", res)
	}
}
