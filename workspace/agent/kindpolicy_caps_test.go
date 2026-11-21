package main

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpproj"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The kind-literal branches these capabilities replaced named specific kinds; this pins which
// kind answers each one, so a registry change cannot silently move a behaviour.
func TestKindPolicyCapabilityHolders(t *testing.T) {
	has := func(kind string, f func(agents.Agent) bool) bool {
		a, ok := agentreg.Lookup(kind)
		return ok && f(a)
	}
	type probe struct {
		name string
		f    func(agents.Agent) bool
		want []string
	}
	probes := []probe{
		{"ModelInfoSuppressor", func(a agents.Agent) bool { _, ok := a.(agents.ModelInfoSuppressor); return ok }, []string{session.KindClaude}},
		{"ModelRetirer", func(a agents.Agent) bool { _, ok := a.(agents.ModelRetirer); return ok }, []string{session.KindCodex}},
		{"ChatModelDefaulter", func(a agents.Agent) bool { _, ok := a.(agents.ChatModelDefaulter); return ok }, []string{session.KindClaude}},
		{"ChatFallbackModeler", func(a agents.Agent) bool { _, ok := a.(agents.ChatFallbackModeler); return ok }, []string{session.KindCodex, session.KindOpencode}},
		{"MessageLimitReader", func(a agents.Agent) bool { _, ok := a.(agents.MessageLimitReader); return ok }, []string{session.KindClaude, session.KindMuse}},
		{"DeliveryBaseliner", func(a agents.Agent) bool { _, ok := a.(agents.DeliveryBaseliner); return ok }, []string{session.KindClaude}},
		{"SubmitRejectionConfirmer", func(a agents.Agent) bool { _, ok := a.(agents.SubmitRejectionConfirmer); return ok }, []string{session.KindCursor}},
		{"TranscriptBusyReporter", func(a agents.Agent) bool { _, ok := a.(agents.TranscriptBusyReporter); return ok }, []string{session.KindClaude}},
		{"LiveContextReader", func(a agents.Agent) bool { _, ok := a.(agents.LiveContextReader); return ok }, []string{session.KindKiro, session.KindMuse}},
		{"PlaceholderDialectWriter", func(a agents.Agent) bool { _, ok := a.(agents.PlaceholderDialectWriter); return ok },
			[]string{session.KindClaude, session.KindCopilot, session.KindCursor, session.KindOpencode}},
	}
	for _, p := range probes {
		for _, kind := range agentKinds(t) {
			want := false
			for _, w := range p.want {
				want = want || w == kind
			}
			if got := has(kind, p.f); got != want {
				t.Errorf("%s on %s = %v, want %v", p.name, kind, got, want)
			}
		}
	}
}

func TestNativeDialectValues(t *testing.T) {
	for kind, want := range map[string]string{
		session.KindClaude: mcpproj.DialectDollarBrace, session.KindCopilot: mcpproj.DialectDollarBrace,
		session.KindCursor: mcpproj.DialectDollarBrace, session.KindOpencode: mcpproj.DialectEnvBrace,
	} {
		a, _ := agentreg.Lookup(kind)
		if got := a.(agents.PlaceholderDialectWriter).NativeDialect(); got != want {
			t.Errorf("%s native dialect = %q, want %q", kind, got, want)
		}
	}
	if mcpproj.CanTranslate(session.KindCodex) {
		t.Error("codex expands nothing, so nothing translates into it")
	}
}

func TestMessageLimitReaders(t *testing.T) {
	now := time.Now()
	c, _ := agentreg.Lookup(session.KindClaude)
	if _, ok := c.(agents.MessageLimitReader).LimitInMessage("plain error", now); ok {
		t.Error("claude read a limit out of unrelated text")
	}
}

func TestKindPolicyCapabilityValues(t *testing.T) {
	cl, _ := agentreg.Lookup(session.KindClaude)
	cu, _ := agentreg.Lookup(session.KindCursor)
	if !cl.(agents.TranscriptBusyReporter).ReportsTranscriptBusy() ||
		!cl.(agents.ChatModelDefaulter).UsesDeploymentChatModel() ||
		!cl.(agents.ModelInfoSuppressor).SuppressesModelInfo("opus") ||
		cl.(agents.ModelInfoSuppressor).SuppressesModelInfo("claude-opus-4-8") ||
		!cu.(agents.SubmitRejectionConfirmer).ConfirmsTerminalSubmit() {
		t.Error("a capability answers differently from the kind branch it replaced")
	}
}
