package mcpreg

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// withBrowserUnavailable substitutes the Agent's answer. The process env is never the input:
// the suite may itself run in a workspace that sets browserx.UnavailableEnv.
func withBrowserUnavailable(t *testing.T, runtimeID string) {
	t.Helper()
	old := BrowserUnavailable
	BrowserUnavailable = func() string { return runtimeID }
	t.Cleanup(func() { BrowserUnavailable = old })
}

// The af server learns that the workspace has no browser from its argv (#1614). Without the
// flag nothing may change: the args stay exactly what they were before the flag existed.
func TestBrowserUnavailableRunArg(t *testing.T) {
	withBrowserUnavailable(t, "")
	if args, _ := BuiltinRunArgs(BuiltinAF); !reflect.DeepEqual(args, []string{"mcp-stdio", "--self-report", "--chromium-attach"}) {
		t.Fatalf("args with browser features = %v", args)
	}
	if got := BrowserUnavailableArgs(); got != nil {
		t.Fatalf("BrowserUnavailableArgs with browser features = %v", got)
	}

	withBrowserUnavailable(t, "kubernetes")
	want := []string{"mcp-stdio", "--self-report", "--chromium-attach", BrowserUnavailableFlag, "kubernetes"}
	if args, _ := BuiltinRunArgs(BuiltinAF); !reflect.DeepEqual(args, want) {
		t.Fatalf("args without browser features = %v, want %v", args, want)
	}
	if args, _ := BuiltinRunArgs(BuiltinAWS); contains(args, BrowserUnavailableFlag) {
		t.Fatalf("a non-af builtin took the flag: %v", args)
	}
}

// Every kind that receives the af server gets the flag, whichever way its definition travels:
// a config file (MaterializedKinds), the wire (muse), an in-process call (lcpp) or a codex
// thread config. The env is not a substitute — codex and muse forward only named variables.
func TestBrowserUnavailableReachesEveryServedKind(t *testing.T) {
	withTempCLIHomes(t)
	withBrowserUnavailable(t, "kubernetes")

	for _, kind := range ServedKinds {
		defs, err := ForSession(kind)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		var af *ServerDef
		for i := range defs {
			if defs[i].ID == BuiltinAF {
				af = &defs[i]
			}
		}
		if af == nil {
			t.Fatalf("%s: no af server", kind)
		}
		if !strings.Contains(strings.Join(af.Args, " "), BrowserUnavailableFlag+" kubernetes") {
			t.Errorf("%s: af args = %v", kind, af.Args)
		}
		if kind == session.KindCodex {
			servers, ok := CodexThreadServers(defs, CodexThreadOpts{SessionName: "s1"})
			if !ok {
				t.Fatal("codex: no thread servers")
			}
			entry, _ := servers[af.Name].(map[string]any)
			if !strings.Contains(strings.Join(anyStrings(entry["args"]), " "), BrowserUnavailableFlag+" kubernetes") {
				t.Errorf("codex thread entry = %v", entry)
			}
		}
	}

	paths := map[string]string{
		session.KindClaude:   claudeJSONPath(),
		session.KindCodex:    codexConfigPath(),
		session.KindOpencode: opencodeConfigPath(),
		session.KindCursor:   cursorMCPConfigPath(),
		session.KindKiro:     kiroMCPConfigPath(),
		session.KindAgy:      agyMCPConfigPath(),
		session.KindCopilot:  copilotMCPConfigPath(),
	}
	for _, kind := range MaterializedKinds {
		path, ok := paths[kind]
		if !ok {
			t.Fatalf("%s: no config path in this test; add it", kind)
		}
		if res := Materialize(kind); res.Err != "" || res.Skipped {
			t.Fatalf("%s: %+v", kind, res)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !strings.Contains(string(b), BrowserUnavailableFlag) || !strings.Contains(string(b), `"kubernetes"`) {
			t.Errorf("%s: %s does not carry the flag:\n%s", kind, path, b)
		}
	}
}

func anyStrings(v any) []string {
	var out []string
	switch s := v.(type) {
	case []any:
		for _, e := range s {
			str, _ := e.(string)
			out = append(out, str)
		}
	case []string:
		out = s
	}
	return out
}
