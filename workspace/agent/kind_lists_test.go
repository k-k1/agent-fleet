package main

// Per-kind facts that are measured or declared rather than capabilities stay hand-kept lists
// (usageMeasuredForKind here, moveTargetKinds in sessionx, oneShotKinds in chatx). What keeps
// them from drifting is that a newly registered kind has to be classified in a table.

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// usageMeasuredByKind is the answer for every registered kind, as it was before the guard.
var usageMeasuredByKind = map[string]string{
	session.KindClaude:   usagex.MeasuredExact,
	session.KindCodex:    usagex.MeasuredExact,
	session.KindOpencode: usagex.MeasuredExact,
	session.KindLcpp:     usagex.MeasuredExact,
	session.KindCopilot:  usagex.MeasuredPartial,
	session.KindMuse:     usagex.MeasuredPartial,
	session.KindKiro:     usagex.MeasuredNone,
	session.KindCursor:   usagex.MeasuredNone,
	session.KindAgy:      usagex.MeasuredNone,
	session.KindShell:    usagex.MeasuredNone,
	session.KindSSM:      usagex.MeasuredNone,
}

func TestEveryKindDeclaresItsUsageAccuracy(t *testing.T) {
	for _, k := range sessionx.RegisteredKinds() {
		want, ok := usageMeasuredByKind[k]
		if !ok {
			t.Errorf("kind %q is registered but has no row in usageMeasuredByKind: decide exact / partial / none in usageMeasuredForKind (an unlisted kind silently reads none) and add the row", k)
			continue
		}
		if got := usageMeasuredForKind(k); got != want {
			t.Errorf("usageMeasuredForKind(%q) = %q, want %q", k, got, want)
		}
	}
	registered := map[string]bool{}
	for _, k := range sessionx.RegisteredKinds() {
		registered[k] = true
	}
	for k := range usageMeasuredByKind {
		if !registered[k] {
			t.Errorf("usageMeasuredByKind has %q, which is not a registered kind", k)
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

// noChatProvider are the registered kinds that run no headless chat/one-shot backend.
var noChatProvider = map[string]bool{
	session.KindKiro: true, session.KindCopilot: true, session.KindShell: true, session.KindSSM: true,
}

// A registered kind either has a chat provider (and then chatx classifies it for one-shots and
// resume handles, see its own guards) or is named here; a chat provider must be a registered kind.
func TestEveryRegisteredKindHasAChatProviderOrIsExcluded(t *testing.T) {
	registered := map[string]bool{}
	for _, k := range sessionx.RegisteredKinds() {
		registered[k] = true
		if _, has := chatx.ChatProviders[k]; has == noChatProvider[k] {
			t.Errorf("kind %q: chat provider=%v, listed in noChatProvider=%v — exactly one must hold", k, has, noChatProvider[k])
		}
	}
	for k := range chatx.ChatProviders {
		if !registered[k] {
			t.Errorf("ChatProviders has %q, which is not a registered kind", k)
		}
	}
}
