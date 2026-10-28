package chatx

// Which kinds an AI assist one-shot may run on (#1020). OneShotHeadlessRun used to fall through
// to the claude path for any kind it had no case for, so a Muse pick ran — and was billed — on
// claude while the settings screen said Muse.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// notOneShotKinds are the chat backends deliberately left without a one-shot runner. Adding a
// backend forces a choice: a runOneShot method plus an oneShotKinds entry, or an entry here.
var notOneShotKinds = []string{lcppKind}

func TestEveryChatProviderIsRunnableOrExcluded(t *testing.T) {
	for k := range ChatProviders {
		if oneShotRunnable(k) == slices.Contains(notOneShotKinds, k) {
			t.Errorf("kind %q: runnable=%v, excluded=%v — exactly one must hold", k, oneShotRunnable(k), slices.Contains(notOneShotKinds, k))
		}
	}
}

// oneShotKinds is what selection trusts; a backend's oneShotRunner is what runs. They must name
// the same kinds, or a selectable kind reaches oneShotRunOn's backstop (or a runner is
// unreachable).
func TestOneShotKindsMatchTheRunners(t *testing.T) {
	var runners []string
	for k, b := range chatBackends {
		if _, ok := b.(oneShotRunner); ok {
			runners = append(runners, k)
		}
	}
	slices.Sort(runners)
	if want := slices.Sorted(slices.Values(oneShotKinds)); !slices.Equal(runners, want) {
		t.Errorf("one-shot runners %v, oneShotKinds %v — they must match", runners, want)
	}
}

// A kind with no runner is refused, never run on claude's path.
func TestOneShotRunOnRefusesAKindWithNoRunner(t *testing.T) {
	writeResolvePrefs(t, `{}`)
	if _, ran, _, err := oneShotRunOn(context.Background(), lcppKind, "title.session", OneShotShort, "", "p", ""); err == nil || ran != lcppKind {
		t.Fatalf("oneShotRunOn(lcpp): ran=%q err=%v, want a refusal recorded under lcpp", ran, err)
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

// stubMuseSafeModels answers muse's catalog of non-contributor rows, newest first.
func stubMuseSafeModels(t *testing.T, ids ...string) {
	t.Helper()
	prev := museSafeModels
	t.Cleanup(func() { museSafeModels = prev })
	museSafeModels = func() []string { return ids }
}

// hideModels makes the saved "models not to use" setting hide ids (testDeps hides nothing).
func hideModels(t *testing.T, ids ...string) {
	t.Helper()
	prev := deps.VisibleModel
	t.Cleanup(func() { deps.VisibleModel = prev })
	deps.VisibleModel = func(_, m string) string {
		if slices.Contains(ids, m) {
			return ""
		}
		return m
	}
}

func TestMuseOneShotFallsBackToTheSafeDefault(t *testing.T) {
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse")
	t.Setenv("HOME", t.TempDir()) // EnsureClamps writes ~/.config/muse/settings.json
	stubMuseSafeModels(t, "muse-spark-1.3", "muse-spark-1.2")
	hideModels(t, "muse-spark-1.3")

	var call usagex.Call
	_, err := museOneShot(t.Context(), &call, "", "hi", "")
	if err == nil || !strings.Contains(err.Error(), "muse execution failed") {
		t.Fatalf("err = %v, want the exec of the missing binary to fail", err)
	}
	if call.ModelReq != "muse-spark-1.2" {
		t.Errorf("ModelReq = %q, want the newest safe model not hidden", call.ModelReq)
	}
}

// Hiding every safe row must refuse, not fall back to a contributor row or to no --model.
func TestMuseOneShotRefusesWhenEverySafeModelIsHidden(t *testing.T) {
	t.Setenv("AGENT_MUSE_BIN", "/nonexistent/muse")
	stubMuseSafeModels(t, "muse-spark-1.3")
	hideModels(t, "muse-spark-1.3")

	var call usagex.Call
	if _, err := museOneShot(t.Context(), &call, "", "hi", ""); err == nil || !strings.Contains(err.Error(), "contributor") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// "recommended" for muse names the model a one-shot runs on, never "" (#972 reported it as the
// CLI default, which for muse is the contributor row) and never a hidden one.
func TestRecommendedModelsMuseOneShotTiers(t *testing.T) {
	stubMuseSafeModels(t, "muse-spark-1.3", "muse-spark-1.2")

	got := RecommendedModels(session.KindMuse)
	if got.Short != "muse-spark-1.3" || got.Prose != "muse-spark-1.3" {
		t.Errorf("RecommendedModels(muse) = %+v, want the newest safe model for both one-shot tiers", got)
	}
	got = RecommendedModelsWithHidden(session.KindMuse, []string{"muse-spark-1.3"}, nil)
	if got.Short != "muse-spark-1.2" || got.Prose != "muse-spark-1.2" {
		t.Errorf("with muse-spark-1.3 hidden: %+v, want muse-spark-1.2", got)
	}
}
