package mcpproj

import "testing"

func TestCanTranslate(t *testing.T) {
	for _, k := range []string{"claude", "copilot", "cursor", "opencode"} {
		if !CanTranslate(k) {
			t.Errorf("%s should be translatable into", k)
		}
	}
	if CanTranslate("codex") {
		t.Error("codex expands nothing (docs/log/56 §2.1) — must not offer translate")
	}
	if CanTranslate("kiro") {
		t.Error("kiro dialect support is unmeasured — must not claim translate")
	}
}

func TestTranslateValueClaudeToOpencode(t *testing.T) {
	// The exact novel-lab direction (docs/log/56 §1): claude's ${HOME} -> opencode's
	// {env:HOME}.
	got := translateValue("${HOME}/repos/narou-mcp-stdio/narou_mcp.py", DialectEnvBrace)
	want := "{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTranslateValueOpencodeToClaude(t *testing.T) {
	got := translateValue("{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py", DialectDollarBrace)
	want := "${HOME}/repos/narou-mcp-stdio/narou_mcp.py"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTranslateValueHandlesDollarEnvBraceSource(t *testing.T) {
	// cursor's ${env:VAR} (the one dialect no kind writes natively but claude and
	// codex both read literally) must still translate cleanly into either target.
	got := translateValue("${env:HOME}/x", DialectEnvBrace)
	if got != "{env:HOME}/x" {
		t.Fatalf("got %q", got)
	}
	got = translateValue("${env:HOME}/x", DialectDollarBrace)
	if got != "${HOME}/x" {
		t.Fatalf("got %q", got)
	}
}

func TestTranslateValueMultiplePlaceholders(t *testing.T) {
	got := translateValue("${HOME}/a/${USER}/b", DialectEnvBrace)
	want := "{env:HOME}/a/{env:USER}/b"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTranslateValueNoPlaceholderIsNoop(t *testing.T) {
	got := translateValue("/plain/path", DialectEnvBrace)
	if got != "/plain/path" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandValueUsesRealEnv(t *testing.T) {
	t.Setenv("AF_TEST_MCP_VAR", "/resolved/value")
	got := expandValue("${AF_TEST_MCP_VAR}/tail")
	if got != "/resolved/value/tail" {
		t.Fatalf("got %q", got)
	}
	got = expandValue("{env:AF_TEST_MCP_VAR}/tail")
	if got != "/resolved/value/tail" {
		t.Fatalf("got %q", got)
	}
}

func TestConvertServerTranslateAllFields(t *testing.T) {
	s := Server{
		Name:      "syosetu",
		Transport: TransportStdio,
		Command:   "uv",
		Args:      []string{"run", "--quiet", "${HOME}/repos/narou-mcp-stdio/narou_mcp.py"},
		Env:       map[string]string{"HOME_AGAIN": "${HOME}/again"},
	}
	out := convertServer(s, "translate", "opencode")
	wantArg := "{env:HOME}/repos/narou-mcp-stdio/narou_mcp.py"
	if out.Args[2] != wantArg {
		t.Fatalf("args[2] = %q, want %q", out.Args[2], wantArg)
	}
	if out.Env["HOME_AGAIN"] != "{env:HOME}/again" {
		t.Fatalf("env = %q", out.Env["HOME_AGAIN"])
	}
	// The original must be untouched (convertServer returns a copy).
	if s.Args[2] != "${HOME}/repos/narou-mcp-stdio/narou_mcp.py" {
		t.Fatalf("source mutated: %q", s.Args[2])
	}
}

func TestConvertServerTranslateIntoCodexIsNoop(t *testing.T) {
	s := Server{Command: "${HOME}/x"}
	out := convertServer(s, "translate", "codex")
	if out.Command != s.Command {
		t.Fatalf("expected no-op for a kind with no native dialect: %q", out.Command)
	}
}

func TestConvertServerAsIsUnchanged(t *testing.T) {
	s := Server{Command: "${HOME}/x"}
	out := convertServer(s, "as-is", "opencode")
	if out.Command != s.Command {
		t.Fatalf("as-is must not rewrite: %q", out.Command)
	}
}

func TestConvertServerExpand(t *testing.T) {
	t.Setenv("HOME", "/home/dev")
	s := Server{Command: "${HOME}/x"}
	out := convertServer(s, "expand", "opencode")
	if out.Command != "/home/dev/x" {
		t.Fatalf("got %q", out.Command)
	}
}
