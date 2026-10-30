package sessionx

import (
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// legacyPasteNeeded is the chain typePromptText used to carry: a kind list for "always paste"
// plus claude's text rule. It stays here as the literal the registry lookup must reproduce.
func legacyPasteNeeded(kind, text string) bool {
	const legacyMinBytes = 2048 // the old claudePasteMinBytes, deliberately not claude.PasteMinBytes
	claudeRule := kind == session.KindClaude &&
		(len(text) >= legacyMinBytes || strings.ContainsAny(text, "\r\n"))
	switch kind {
	case session.KindCodex, session.KindOpencode, session.KindCopilot, session.KindCursor, session.KindKiro:
		return true
	}
	return claudeRule
}

// pasteKinds are the kinds measured to need a paste for multi-line text, the ones legacyPasteNeeded
// names plus claude. Opting a kind in means implementing agents.PromptPaste AND listing it here,
// after checking that a paste reaches its composer. A kind that implements nothing types literal
// keys, which is the default, so a new kind needs no row unless it opts into paste.
var pasteKinds = []string{
	session.KindClaude, session.KindCodex, session.KindOpencode, session.KindCopilot,
	session.KindCursor, session.KindKiro,
}

func TestPromptPasteMatchesLegacyChain(t *testing.T) {
	// The boundary is pinned as literals (2047 / 2048 bytes, ASCII and multi-byte), not through
	// claude.PasteMinBytes, so changing the threshold turns this red.
	texts := []string{"", "short", "two\nlines", "crlf\r\nline", "cr\rline",
		strings.Repeat("a", 2047), strings.Repeat("a", 2048),
		strings.Repeat("あ", 682) + "a",  // 2047 bytes
		strings.Repeat("あ", 682) + "aa", // 2048 bytes
		strings.Repeat("あ", 683)}        // 2049 bytes, 683 runes
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
		_, impl := AgentOf(kind).(agents.PromptPaste)
		if paste := slices.Contains(pasteKinds, kind); impl != paste {
			t.Errorf("%s: implements agents.PromptPaste = %v, pasteKinds lists it = %v", kind, impl, paste)
		}
	}
	for _, kind := range pasteKinds {
		if _, ok := RegisteredAgent(kind); !ok {
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
