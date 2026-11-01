package main

// Per-kind facts that are measured or declared rather than capabilities live in the kind's own
// package (agents.UsageMeasurer, agents.NoHeadlessChat) or stay hand-kept lists (moveRefusals in
// sessionx, oneShotKinds in chatx). What keeps them from drifting is that a newly registered
// kind has to say so, and the tests below derive their rows from the registry.

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// oldUsageMeasuredForKind is the switch usageMeasuredForKind held before each kind declared its
// own accuracy. It is a deliberate literal copy over the kinds that existed then: the
// declarations must answer exactly what it did.
func oldUsageMeasuredForKind(kind string) string {
	switch kind {
	case session.KindClaude, session.KindCodex, session.KindOpencode, session.KindLcpp:
		return usagex.MeasuredExact
	case session.KindCopilot, session.KindMuse:
		return usagex.MeasuredPartial
	}
	return usagex.MeasuredNone
}

var kindsBeforeUsageDeclarations = []string{
	session.KindClaude, session.KindCodex, session.KindOpencode, session.KindLcpp, session.KindCopilot,
	session.KindMuse, session.KindKiro, session.KindCursor, session.KindAgy, session.KindShell, session.KindSSM,
}

func TestUsageMeasuredMatchesOldSwitch(t *testing.T) {
	probes := append([]string{"", "Claude", " claude", "claude-code", "unknown-kind"}, kindsBeforeUsageDeclarations...)
	for _, k := range probes {
		if got, want := usageMeasuredForKind(k), oldUsageMeasuredForKind(k); got != want {
			t.Errorf("usageMeasuredForKind(%q) = %q, old switch %q", k, got, want)
		}
	}
}

// Every registered kind declares its accuracy, "none" included: a kind that said nothing would
// silently read none. The answer must be one of the three defined values.
func TestEveryKindDeclaresItsUsageAccuracy(t *testing.T) {
	for _, k := range sessionx.RegisteredKinds() {
		a, _ := sessionx.RegisteredAgent(k)
		u, ok := a.(agents.UsageMeasurer)
		if !ok {
			t.Errorf("kind %q is registered but does not implement agents.UsageMeasurer: decide exact / partial / none in its own package (an undeclared kind silently reads none)", k)
			continue
		}
		switch got := u.UsageMeasured(); got {
		case usagex.MeasuredExact, usagex.MeasuredPartial, usagex.MeasuredNone:
		default:
			t.Errorf("%s declares usage accuracy %q, which is not a defined value", k, got)
		}
	}
}

// An alias is not a kind: it reads as unmeasured, never as the exact kind it resembles.
func TestUsageMeasuredRefusesAlternateSpellings(t *testing.T) {
	for _, k := range []string{"", "Claude", "CLAUDE", " claude", "claude-code", "codex-cli",
		"OpenCode", "llamacpp", "llama.cpp", "github-copilot", "Muse", "antigravity"} {
		if got := usageMeasuredForKind(k); got != usagex.MeasuredNone {
			t.Errorf("usageMeasuredForKind(%q) = %q, want %q", k, got, usagex.MeasuredNone)
		}
	}
}

// A registered kind either has a chat provider (and then chatx classifies it for one-shots and
// resume handles, see its own guards) or carries agents.NoHeadlessChat, never both; a chat
// provider must be a registered kind.
func TestEveryRegisteredKindHasAChatProviderOrIsExcluded(t *testing.T) {
	registered := map[string]bool{}
	for _, k := range sessionx.RegisteredKinds() {
		registered[k] = true
		a, _ := sessionx.RegisteredAgent(k)
		_, excluded := a.(agents.NoHeadlessChat)
		if _, has := chatx.ChatProviders[k]; has == excluded {
			t.Errorf("kind %q: chat provider=%v, implements agents.NoHeadlessChat=%v — exactly one must hold", k, has, excluded)
		}
	}
	for k := range chatx.ChatProviders {
		if !registered[k] {
			t.Errorf("ChatProviders has %q, which is not a registered kind", k)
		}
	}
}
