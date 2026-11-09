package assistants

import (
	"strings"
	"testing"
)

// TestBuiltinsUseInjectedDeps guards the one thing the compiler cannot see: that the
// builtins actually USE the values they were handed. When the two main-side functions
// (ensureBuiltinKnowledge / preferredHeadlessAgent) were package-variable hooks, deleting
// both assignments left every test green (review note 1, measured with mutation testing).
// They are NewDeps arguments now, so forgetting to pass one is a compile error — but a
// builtin standing up with an empty Agent or an empty knowledge path still looks normal
// on screen, so nothing else would notice.
func TestBuiltinsUseInjectedDeps(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep uiprefs.Locale() off the real home's ui-prefs.json

	const knowPath = "/tmp/af-knowledge-probe"
	const docsPath = "/tmp/af-docs-probe"
	const agentKind = "codex-probe"
	got := Builtins(NewDeps(
		func() string { return knowPath },
		func() string { return docsPath },
		func() string { return agentKind },
	))

	if len(got) == 0 {
		t.Fatal("no builtins at all")
	}
	for _, a := range got {
		if a.Agent != agentKind {
			t.Errorf("%s: Agent = %q, want %q (DefaultAgent is not wired up)", a.ID, a.Agent, agentKind)
		}
		// Order matters only for reading; both must be there and neither may stand in
		// for the other (two func() string arguments swap without a compile error).
		if len(a.Knowledge) != 2 || a.Knowledge[0] != knowPath || a.Knowledge[1] != docsPath {
			t.Errorf("%s: Knowledge = %v, want [%s %s] (KnowledgeDir / DocsDir is not wired up)", a.ID, a.Knowledge, knowPath, docsPath)
		}
		if a.Persona == "" {
			t.Errorf("%s: Persona is empty", a.ID)
		}
	}
}

// TestZeroDepsPanics: a zero-value Deps (built without going through NewDeps) must fail
// rather than run on harmless defaults. This is the guard against sliding back to a shape
// where forgetting the wiring goes green.
func TestZeroDepsPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a zero-value Deps did not panic; it must not run on harmless defaults")
		}
		if !strings.Contains(fmtOf(r), "NewDeps") {
			t.Errorf("the panic text does not point at the missing wiring: %v", r)
		}
	}()
	_ = Builtins(Deps{})
}

func fmtOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return ""
}

func TestNewDepsRejectsNil(t *testing.T) {
	for _, c := range []struct {
		name             string
		know, docs, agnt func() string
	}{
		{"knowledgeDir is nil", nil, func() string { return "/d" }, func() string { return "claude" }},
		{"docsDir is nil", func() string { return "/k" }, nil, func() string { return "claude" }},
		{"defaultAgent is nil", func() string { return "/k" }, func() string { return "/d" }, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("passed nil but no panic")
				}
			}()
			_ = NewDeps(c.know, c.docs, c.agnt)
		})
	}
}

// TestSREPersonaDoesNotPromiseCallAWS: the SRE assistant connects AWS read-only, and the
// read-only proxy drops call_aws / run_script (secrets.AWSConn). The persona may name them
// only to forbid them, never as a read tool.
func TestSREPersonaDoesNotPromiseCallAWS(t *testing.T) {
	for name, p := range map[string]string{"ja": srePersona, "en": srePersonaEN} {
		if strings.Contains(p, "call_aws による") || strings.Contains(p, "call_aws for AWS API lookups") {
			t.Errorf("%s persona offers call_aws as a read tool", name)
		}
		if !strings.Contains(p, "call_aws") || !strings.Contains(p, "run_script") {
			t.Errorf("%s persona should tell the assistant not to use call_aws / run_script", name)
		}
	}
}
