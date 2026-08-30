package mcpreg

// Per-provider serialization (docs/log/48 §7 / P2). The assertions worth having here are
// the ones a running chat would only reveal as "the server silently isn't there":
// each CLI's exact key names, and the invariant that a credential never reaches argv.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func attachStdioDef(name string) ServerDef {
	return ServerDef{
		ID: "id-" + name, Name: name, Origin: OriginUser, Transport: TransportStdio,
		Command: "/usr/bin/thing", Args: []string{"serve", "--port=1"},
		Env: map[string]string{"THING_TOKEN": "s3cr3t"}, Enabled: true,
	}
}

func attachHTTPDef(name string) ServerDef {
	return ServerDef{
		ID: "id-" + name, Name: name, Origin: OriginUser, Transport: TransportHTTP,
		URL:     "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer tok", "X-Team": "sre"},
		Enabled: true,
	}
}

func TestClaudeServersShape(t *testing.T) {
	got := ClaudeServers([]ServerDef{attachStdioDef("a"), attachHTTPDef("b")})
	want := map[string]any{
		"a": map[string]any{
			"type": "stdio", "command": "/usr/bin/thing",
			"args": []any{"serve", "--port=1"},
			"env":  map[string]any{"THING_TOKEN": "s3cr3t"},
		},
		"b": map[string]any{
			"type": "http", "url": "https://mcp.example.com/mcp",
			"headers": map[string]any{"Authorization": "Bearer tok", "X-Team": "sre"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudeServers =\n%#v\nwant\n%#v", got, want)
	}
}

func TestOpencodeServersFoldCommandAndRenameEnv(t *testing.T) {
	got := OpencodeServers([]ServerDef{attachStdioDef("a"), attachHTTPDef("b")})
	want := map[string]any{
		"a": map[string]any{
			"type": "local", "enabled": true,
			// opencode takes ONE array, not command + args.
			"command": []any{"/usr/bin/thing", "serve", "--port=1"},
			// …and calls the env "environment".
			"environment": map[string]any{"THING_TOKEN": "s3cr3t"},
		},
		"b": map[string]any{
			"type": "remote", "enabled": true, "url": "https://mcp.example.com/mcp",
			"headers": map[string]any{"Authorization": "Bearer tok", "X-Team": "sre"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpencodeServers =\n%#v\nwant\n%#v", got, want)
	}
}

func TestAgyServersOverlayHomeAndLetDefinitionWin(t *testing.T) {
	d := attachStdioDef("a")
	d.Env = map[string]string{"HOME": "/definition/wins", "THING_TOKEN": "s3cr3t"}
	got := AgyServers([]ServerDef{d, attachStdioDef("b")}, map[string]string{"HOME": "/real/home"})

	// A server that says nothing about HOME inherits the overlay: agy runs from an
	// isolated HOME and its children must be pointed back at the real one.
	if env := got["b"].(map[string]any)["env"].(map[string]any); env["HOME"] != "/real/home" {
		t.Fatalf("b HOME = %v, want the overlay", env["HOME"])
	}
	// A server that DOES declare it keeps its own value — the definition is the
	// server's contract, and silently rewriting it would be a broken server.
	if env := got["a"].(map[string]any)["env"].(map[string]any); env["HOME"] != "/definition/wins" {
		t.Fatalf("a HOME = %v, want the definition's own value", env["HOME"])
	}
	// agy infers the transport from command/url and does not take claude's "type".
	if _, ok := got["a"].(map[string]any)["type"]; ok {
		t.Fatalf("agy entry carries a type discriminator: %#v", got["a"])
	}
}

// TestCodexOverridesKeepSecretsOutOfArgv is the security assertion of this file: argv
// is readable for the whole uid and can be echoed into a CLI's own logs, so codex —
// the one provider with no per-invocation config FILE — must pass every credential
// through the environment and put only NAMES in the args.
func TestCodexOverridesKeepSecretsOutOfArgv(t *testing.T) {
	args, env := CodexOverrides([]ServerDef{attachStdioDef("a"), attachHTTPDef("b")}, CodexOpts{})
	joined := strings.Join(args, " ")
	for _, secret := range []string{"s3cr3t", "Bearer tok", "sre"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("secret %q leaked into codex argv: %s", secret, joined)
		}
	}
	if !hasEnv(env, "THING_TOKEN=s3cr3t") {
		t.Fatalf("stdio env not forwarded through the environment: %q", env)
	}
	// Header values ride minted variable names, so two servers can carry the same
	// header without colliding and no user-chosen name can shadow a real variable.
	auth := mintedHeaderVar("b", "Authorization")
	if !hasEnv(env, auth+"=Bearer tok") {
		t.Fatalf("header value not forwarded through the environment: %q", env)
	}
	if !strings.Contains(joined, `mcp_servers.b.env_http_headers={"Authorization"="`+auth+`"`) {
		t.Fatalf("env_http_headers mapping missing: %s", joined)
	}
	if !strings.Contains(joined, `mcp_servers.b.url="https://mcp.example.com/mcp"`) {
		t.Fatalf("remote url missing: %s", joined)
	}
	if !strings.Contains(joined, `mcp_servers.a.env_vars=["THING_TOKEN"]`) {
		t.Fatalf("stdio env_vars allowlist missing: %s", joined)
	}
	if !strings.Contains(joined, `mcp_servers.a.args=["serve","--port=1"]`) {
		t.Fatalf("stdio args missing: %s", joined)
	}
}

// TestCodexOverridesFallBackToLiteralEnvOnCollision pins the one case the
// pass-by-name scheme cannot express. Forwarding is keyed by the variable name the
// CHILD expects, so two servers wanting the same name with DIFFERENT values would
// otherwise hand one of them the other's credential — a cross-server secret leak.
func TestCodexOverridesFallBackToLiteralEnvOnCollision(t *testing.T) {
	a, b := attachStdioDef("a"), attachStdioDef("b")
	b.Env = map[string]string{"THING_TOKEN": "different"}
	args, env := CodexOverrides([]ServerDef{a, b}, CodexOpts{})
	joined := strings.Join(args, " ")
	if !hasEnv(env, "THING_TOKEN=s3cr3t") || hasEnv(env, "THING_TOKEN=different") {
		t.Fatalf("the colliding value must not win the shared name: %q", env)
	}
	if !strings.Contains(joined, `mcp_servers.b.env={"THING_TOKEN"="different"}`) {
		t.Fatalf("collision did not fall back to the literal env table: %s", joined)
	}
	// Same name, same VALUE is not a collision — both can share the forward.
	c := attachStdioDef("c")
	args2, _ := CodexOverrides([]ServerDef{a, c}, CodexOpts{})
	if strings.Contains(strings.Join(args2, " "), "mcp_servers.c.env=") {
		t.Fatalf("identical values should share the forward, not go literal: %s", args2)
	}
}

func TestCodexOverridesApproveAndTimeoutAndBuiltinStoreKey(t *testing.T) {
	d := attachStdioDef("a")
	d.TimeoutMS = 20000
	b := ServerDef{
		ID: BuiltinPagerDuty, Name: BuiltinPagerDuty, Origin: OriginBuiltin,
		Transport: TransportStdio, Command: "/usr/bin/workspace-agent",
		Args: []string{"mcp-run", "pagerduty"}, Enabled: true,
	}
	args, _ := CodexOverrides([]ServerDef{d, b}, CodexOpts{Approve: true})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		`mcp_servers.a.default_tools_approval_mode="approve"`,
		`mcp_servers.a.startup_timeout_sec=20.0`,
		// The builtin's mcp-run wrapper opens the encrypted store, so it needs the
		// store key: codex's MCP child env is default-deny beyond a core set.
		`mcp_servers.pagerduty.env_vars=["AF_SECRET_KEY"]`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("codex args missing %q: %s", want, joined)
		}
	}
}

// TestTomlInlineTableQuotesKeys guards the encoding detail that silently changes
// meaning: an unquoted header name with a dash would parse as a dotted path.
func TestTomlInlineTableQuotesKeys(t *testing.T) {
	got := tomlInlineTable(map[string]string{"X-Api-Key": "v", `q"uote`: "w"})
	want := `{"X-Api-Key"="v","q\"uote"="w"}`
	if got != want {
		t.Fatalf("tomlInlineTable = %s, want %s", got, want)
	}
}

func TestForAssistantScopesByKindAndReadiness(t *testing.T) {
	withTempHome(t)
	mk := func(name string, kinds []string, headers map[string]string) string {
		d, err := Create(ServerDef{
			Name: name, Transport: TransportHTTP, URL: "https://x.example/mcp",
			Headers: headers, Enabled: true, Targets: Targets{Assistant: true}, Kinds: kinds,
		})
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		return d.ID
	}
	any := mk("anykind", nil, nil)
	codexOnly := mk("codexonly", []string{session.KindCodex}, nil)
	// A definition still waiting for its header value (the docs/log/48 §5.2 user_secret
	// shape: the name is distributed, the value is the member's to fill in) cannot
	// authenticate, so it is held back rather than attached as a server that fails on
	// first use.
	notReady := mk("pending", nil, map[string]string{"Authorization": ""})

	forClaude, err := ForAssistant(session.KindClaude)
	if err != nil {
		t.Fatalf("ForAssistant: %v", err)
	}
	if _, ok := forClaude[any]; !ok {
		t.Fatalf("unscoped server missing for claude: %v", forClaude)
	}
	if _, ok := forClaude[codexOnly]; ok {
		t.Fatalf("codex-scoped server offered to claude: %v", forClaude)
	}
	if _, ok := forClaude[notReady]; ok {
		t.Fatalf("server with an unfilled secret was attached: %v", forClaude)
	}
	if forCodex, _ := ForAssistant(session.KindCodex); len(forCodex) != 2 {
		t.Fatalf("ForAssistant(codex) = %v, want anykind + codexonly", forCodex)
	}

	// Known looks past enabled/ready on purpose: an assistant keeps its selection
	// while a connection is missing, so reconnecting restores the tools.
	if err := SetEnabled(any, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if !Known(any) || !Known(BuiltinPagerDuty) {
		t.Fatalf("Known lost a disabled registration or a builtin")
	}
	if Known("mcp-does-not-exist") {
		t.Fatalf("Known accepted an unknown id")
	}
	if forClaude, _ := ForAssistant(session.KindClaude); len(forClaude) != 0 {
		t.Fatalf("disabled server still attached: %v", forClaude)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
