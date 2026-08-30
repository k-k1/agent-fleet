package mcpproj

import "testing"

func TestDetectDialects(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"${HOME}/x", []string{DialectDollarBrace}},
		{"{env:HOME}/x", []string{DialectEnvBrace}},
		{"${env:HOME}/x", []string{DialectDollarEnvBrace}},
		{"plain/path", nil},
	}
	for _, c := range cases {
		got := detectDialects(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q: got %v want %v", c.in, got, c.want)
			}
		}
	}
}

func TestDialectWarningsOpencodeBreaksOnDollarEnvBrace(t *testing.T) {
	// docs/log/56 §2.1: "${env:HOME}" in opencode silently becomes "$/…" — this must be
	// the RED "broken" code, not the generic yellow mismatch.
	ws := dialectWarningsForValue("${env:HOME}/repos/x", "opencode.json", "srv", "command", []string{"opencode"})
	if len(ws) != 1 || ws[0].Code != CodeDialectBroken || ws[0].Severity != "red" {
		t.Fatalf("got %+v", ws)
	}
}

func TestDialectWarningsClaudeSeesOpencodeSyntax(t *testing.T) {
	// {env:HOME} in a claude/copilot .mcp.json is inert text, not expanded — yellow
	// mismatch, once per affected kind.
	ws := dialectWarningsForValue("{env:HOME}/x", ".mcp.json", "srv", "command", []string{"claude", "copilot"})
	if len(ws) != 2 {
		t.Fatalf("got %+v", ws)
	}
	for _, w := range ws {
		if w.Code != CodeDialectMismatch || w.Severity != "yellow" || w.Dialect != DialectEnvBrace {
			t.Fatalf("got %+v", w)
		}
	}
}

func TestDialectWarningsCodexNeverExpandsAnything(t *testing.T) {
	for _, val := range []string{"${HOME}/x", "{env:HOME}/x", "${env:HOME}/x"} {
		ws := dialectWarningsForValue(val, ".codex/config.toml", "srv", "command", []string{"codex"})
		if len(ws) != 1 || ws[0].Code != CodeDialectMismatch {
			t.Fatalf("%q: got %+v", val, ws)
		}
	}
}

func TestDialectWarningsNoneWhenSupported(t *testing.T) {
	// claude supports ${VAR} — no warning.
	ws := dialectWarningsForValue("${HOME}/x", ".mcp.json", "srv", "command", []string{"claude"})
	if len(ws) != 0 {
		t.Fatalf("got %+v", ws)
	}
}

func TestDialectWarningsKiroUnmeasuredNeverClaims(t *testing.T) {
	ws := dialectWarningsForValue("{env:HOME}/x", ".kiro/settings/mcp.json", "srv", "command", []string{"kiro"})
	if len(ws) != 0 {
		t.Fatalf("kiro must never get a dialect claim (unmeasured): %+v", ws)
	}
}
