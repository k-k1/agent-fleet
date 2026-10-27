package sessionx

import (
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// legacyPasteNeeded is the chain typePromptText used to carry: a kind list for "always paste"
// plus claude's text rule. It stays here as the literal the registry lookup must reproduce.
func legacyPasteNeeded(kind, text string) bool {
	claudeRule := kind == session.KindClaude &&
		(len(text) >= claude.PasteMinBytes || strings.ContainsAny(text, "\r\n"))
	switch kind {
	case session.KindCodex, session.KindOpencode, session.KindCopilot, session.KindCursor, session.KindKiro:
		return true
	}
	return claudeRule
}

// pasteKinds are the kinds whose composer takes a paste. Every registered kind not listed gets
// literal keys, so a new kind has to be classified by implementing agents.PromptPaste or by
// staying out of this list on purpose.
var pasteKinds = []string{
	session.KindClaude, session.KindCodex, session.KindOpencode, session.KindCopilot,
	session.KindCursor, session.KindKiro,
}

func TestPromptPasteMatchesLegacyChain(t *testing.T) {
	texts := []string{"", "short", "two\nlines", "crlf\r\nline", "cr\rline", strings.Repeat("a", claude.PasteMinBytes-1),
		strings.Repeat("a", claude.PasteMinBytes)}
	kinds := append(RegisteredKinds(), "", "Claude", "CODEX", "kiro-cli", "codex ", "unknown")
	for _, kind := range kinds {
		for _, text := range texts {
			if got, want := pasteNeeded(kind, text), legacyPasteNeeded(kind, text); got != want {
				t.Errorf("pasteNeeded(%q, %.12q) = %v, legacy chain %v", kind, text, got, want)
			}
		}
	}
}

func TestPromptPasteClassifiesEveryRegisteredKind(t *testing.T) {
	for _, kind := range RegisteredKinds() {
		_, impl := agentRegistry[kind].(agents.PromptPaste)
		if impl != slices.Contains(pasteKinds, kind) {
			t.Errorf("%s: implements agents.PromptPaste = %v, pasteKinds lists it = %v", kind, impl, !impl)
		}
	}
	for _, kind := range pasteKinds {
		if _, ok := agentRegistry[kind]; !ok {
			t.Errorf("pasteKinds has %q, which is not a registered kind", kind)
		}
	}
}

func TestPromptPasteRefusesAlternateSpellings(t *testing.T) {
	for _, kind := range []string{"Codex", "CODEX", " codex", "codex ", "kiro-cli", "Opencode", "open-code", "claude-code", "Claude", "gh-copilot", ""} {
		if pasteNeeded(kind, "a\nb") {
			t.Errorf("pasteNeeded(%q) = true; only the exact registry key may paste", kind)
		}
	}
}
