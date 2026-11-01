package mcpreg

import (
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/kinds"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// oldWriterFor is a literal copy of writerFor as it was before the table replaced the switch.
func oldWriterFor(kind string) writer {
	switch kind {
	case session.KindClaude:
		return materializeClaude
	case session.KindCodex:
		return materializeCodex
	case session.KindOpencode:
		return materializeOpencode
	case session.KindCursor:
		return materializeCursor
	case session.KindKiro:
		return materializeKiro
	case session.KindAgy:
		return materializeAgy
	case session.KindCopilot:
		return materializeCopilot
	}
	return nil
}

// TestWriterForMatchesOldSwitch holds the table lookup to the old switch over every registered
// kind and the alias, case and unknown spellings, comparing the function identity.
func TestWriterForMatchesOldSwitch(t *testing.T) {
	spellings := append([]string(nil), kinds.All()...)
	spellings = append(spellings, "", "Claude", " claude", "claude-code", "cursor-agent",
		"kiro-cli", "github-copilot", "antigravity", "unknown", "CODEX")
	for _, k := range spellings {
		got, want := writerFor(k), oldWriterFor(k)
		if (got == nil) != (want == nil) {
			t.Errorf("%q: has a writer = %v, old switch %v", k, got != nil, want != nil)
			continue
		}
		if got != nil && reflect.ValueOf(got).Pointer() != reflect.ValueOf(want).Pointer() {
			t.Errorf("%q: a different writer than the old switch", k)
		}
	}
}
