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

// Every registered kind is in exactly one of these two lists. A new kind must be placed on
// purpose: pasteKinds by implementing agents.PromptPaste, literalKinds after checking that its
// composer keeps an Enter that follows literal keys (copilot, cursor and kiro lose it).
var literalKinds = []string{session.KindAgy, session.KindMuse, session.KindLcpp, session.KindShell, session.KindSSM}

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
		_, impl := agentRegistry[kind].(agents.PromptPaste)
		paste, literal := slices.Contains(pasteKinds, kind), slices.Contains(literalKinds, kind)
		if paste == literal {
			t.Errorf("%s: must be in exactly one of pasteKinds / literalKinds (paste=%v literal=%v)", kind, paste, literal)
		}
		if impl != paste {
			t.Errorf("%s: implements agents.PromptPaste = %v, pasteKinds lists it = %v", kind, impl, paste)
		}
	}
	for _, kind := range append(slices.Clone(pasteKinds), literalKinds...) {
		if _, ok := agentRegistry[kind]; !ok {
			t.Errorf("pasteKinds / literalKinds has %q, which is not a registered kind", kind)
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
