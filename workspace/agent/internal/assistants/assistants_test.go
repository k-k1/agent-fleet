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
	const agentKind = "codex-probe"
	got := Builtins(NewDeps(
		func() string { return knowPath },
		func() string { return agentKind },
	))

	if len(got) == 0 {
		t.Fatal("no builtins at all")
	}
	for _, a := range got {
		if a.Agent != agentKind {
			t.Errorf("%s: Agent = %q, want %q (DefaultAgent is not wired up)", a.ID, a.Agent, agentKind)
		}
		if len(a.Knowledge) != 1 || a.Knowledge[0] != knowPath {
			t.Errorf("%s: Knowledge = %v, want [%s] (KnowledgeDir is not wired up)", a.ID, a.Knowledge, knowPath)
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
		name       string
		know, agnt func() string
	}{
		{"knowledgeDir is nil", nil, func() string { return "claude" }},
		{"defaultAgent is nil", func() string { return "/k" }, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("passed nil but no panic")
				}
			}()
			_ = NewDeps(c.know, c.agnt)
		})
	}
}
