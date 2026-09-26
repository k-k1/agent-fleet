package chatx

// Which kinds an AI assist one-shot may run on (#1020). OneShotHeadlessRun used to fall through
// to the claude path for any kind it had no case for, so a Muse pick ran — and was billed — on
// claude while the settings screen said Muse.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// notOneShotKinds are the ChatProviders kinds deliberately left without a one-shot runner.
// Adding a kind to ChatProviders forces a choice: a case in OneShotHeadlessRun plus an
// oneShotKinds entry, or an entry here.
var notOneShotKinds = []string{lcppKind}

func TestEveryChatProviderIsRunnableOrExcluded(t *testing.T) {
	for k := range ChatProviders {
		if oneShotRunnable(k) == slices.Contains(notOneShotKinds, k) {
			t.Errorf("kind %q: runnable=%v, excluded=%v — exactly one must hold", k, oneShotRunnable(k), slices.Contains(notOneShotKinds, k))
		}
	}
}

// oneShotKinds is what selection trusts; the switch is what runs. They must name the same
// kinds, or a selectable kind reaches the default branch (or a case is unreachable).
func TestOneShotKindsMatchTheRunnerSwitch(t *testing.T) {
	consts := map[string]string{
		"KindClaude":   session.KindClaude,
		"KindCodex":    session.KindCodex,
		"KindOpencode": session.KindOpencode,
		"KindCursor":   session.KindCursor,
		"KindAgy":      session.KindAgy,
		"KindMuse":     session.KindMuse,
	}
	f, err := parser.ParseFile(token.NewFileSet(), "chat_providers.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cases []string
	sawDefault := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "OneShotHeadlessRun" {
			continue
		}
		for _, st := range fn.Body.List {
			sw, ok := st.(*ast.SwitchStmt)
			if !ok {
				continue
			}
			if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "kind" {
				continue
			}
			for _, c := range sw.Body.List {
				cc := c.(*ast.CaseClause)
				if cc.List == nil {
					sawDefault = true
				}
				for _, e := range cc.List {
					sel, ok := e.(*ast.SelectorExpr)
					v, known := "", false
					if ok {
						v, known = consts[sel.Sel.Name]
					}
					if !known {
						t.Fatalf("unrecognised case expression %#v: extend consts in this test", e)
					}
					cases = append(cases, v)
				}
			}
		}
	}
	if !sawDefault {
		t.Error("the kind switch has no default: an unknown kind would fall through to the claude path again")
	}
	slices.Sort(cases)
	want := slices.Sorted(slices.Values(oneShotKinds))
	if !slices.Equal(cases, want) {
		t.Errorf("switch cases %v, oneShotKinds %v — they must match", cases, want)
	}
}

func TestOneShotKindSkipsAPinWithNoRunner(t *testing.T) {
	writeResolvePrefs(t, `{"aiFeatureAgents":{"title.session":"lcpp"}}`)
	forceHeadlessAvailable(t, lcppKind, true)
	forceHeadlessAvailable(t, "claude", true)

	if kind, source := oneShotKind("title.session"); kind != "claude" || source != OneShotSourceDefault {
		t.Fatalf("kind=%q source=%q, want claude/default (lcpp has no one-shot runner)", kind, source)
	}
	if kind, source, ok := oneShotKindCached("title.session"); !ok || kind != "claude" || source != OneShotSourceDefault {
		t.Fatalf("cached: kind=%q source=%q ok=%v, want claude/default", kind, source, ok)
	}
}

func TestOneShotOrderDropsKindsWithNoRunner(t *testing.T) {
	writeResolvePrefs(t, `{}`)
	prev := deps.AiAssistOrderPref
	t.Cleanup(func() { deps.AiAssistOrderPref = prev })
	order := []string{lcppKind, session.KindMuse, session.KindClaude}
	deps.AiAssistOrderPref = func() []string { return order }
	forceHeadlessAvailable(t, lcppKind, true)
	forceHeadlessAvailable(t, session.KindMuse, true)

	if got := PreferredAssistAgent(); got != session.KindMuse {
		t.Fatalf("PreferredAssistAgent = %q, want muse (lcpp is first but has no runner)", got)
	}
	if kind, _, ok := oneShotKindCached("branch.suggest"); !ok || kind != session.KindMuse {
		t.Fatalf("cached: kind=%q ok=%v, want muse", kind, ok)
	}
	if order[0] != lcppKind {
		t.Errorf("oneShotOrder rewrote the preference's own slice: %v", order)
	}
}

func TestOneShotKindKeepsAMusePin(t *testing.T) {
	writeResolvePrefs(t, `{"aiFeatureAgents":{"title.session":"muse"}}`)
	forceHeadlessAvailable(t, session.KindMuse, true)
	forceHeadlessAvailable(t, "claude", true)

	if kind, source := oneShotKind("title.session"); kind != session.KindMuse || source != OneShotSourcePin {
		t.Fatalf("kind=%q source=%q, want muse/pin", kind, source)
	}
}

// A one-shot never resumes, so it must not leave a session behind, and it must never go out
// without --model (clamp 8: the CLI's own default is the contributor row).
func TestMuseOneShotArgs(t *testing.T) {
	args := museOneShotArgs("muse-spark-1.3", "/p")
	base := museChatBaseArgs()
	if !slices.Equal(args[:len(base)], base) {
		t.Errorf("argv does not start with the chat clamps: %v", args)
	}
	for _, want := range [][2]string{{"--model", "muse-spark-1.3"}, {"--prompt-file", "/p"}} {
		if i := slices.Index(args, want[0]); i < 0 || i+1 >= len(args) || args[i+1] != want[1] {
			t.Errorf("argv lacks %s %s: %v", want[0], want[1], args)
		}
	}
	if !slices.Contains(args, "--no-session-log") {
		t.Errorf("argv lacks --no-session-log: %v", args)
	}
	if slices.Contains(args, "--session-id") {
		t.Errorf("a one-shot must not continue a session: %v", args)
	}
}

func TestMuseOneShotRefusesWithoutAModel(t *testing.T) {
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse")
	var call usagex.Call
	_, err := museOneShot(t.Context(), &call, "", "hi", "  ")
	if err == nil || !strings.Contains(err.Error(), "contributor") {
		t.Fatalf("err = %v, want a refusal naming the contributor model", err)
	}
	if call.OK || call.ModelReq != "" {
		t.Errorf("call = %+v on the refusal path", call)
	}
}

func TestMuseOneShotFallsBackToTheSafeDefault(t *testing.T) {
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse")
	t.Setenv("HOME", t.TempDir()) // EnsureClamps writes ~/.config/muse/settings.json
	prev := museSafeDefault
	t.Cleanup(func() { museSafeDefault = prev })
	museSafeDefault = func() string { return "muse-spark-1.3" }

	var call usagex.Call
	_, err := museOneShot(t.Context(), &call, "", "hi", "")
	if err == nil || !strings.Contains(err.Error(), "muse execution failed") {
		t.Fatalf("err = %v, want the exec of the missing binary to fail", err)
	}
	if call.ModelReq != "muse-spark-1.3" {
		t.Errorf("ModelReq = %q, want the safe default", call.ModelReq)
	}
}

// "recommended" for muse names the model a one-shot runs on, never "" (#972 reported it as the
// CLI default, which for muse is the contributor row).
func TestRecommendedModelsMuseOneShotTiers(t *testing.T) {
	prev := museSafeDefault
	t.Cleanup(func() { museSafeDefault = prev })
	museSafeDefault = func() string { return "muse-spark-1.3" }

	got := RecommendedModels(session.KindMuse)
	if got.Short != "muse-spark-1.3" || got.Prose != "muse-spark-1.3" {
		t.Errorf("RecommendedModels(muse) = %+v, want the safe default for both one-shot tiers", got)
	}
}
