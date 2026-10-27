package chatx

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
)

// The assistant chat switches claude's native auto-memory off under the same condition as a
// session launch, and keeps its --setting-sources "" isolation either way.
func TestChatClaudeCmdAutoMemorySetting(t *testing.T) {
	t.Cleanup(func() { claude.AutoMemoryOff = nil })
	for name, tc := range map[string]struct {
		hook func() bool
		want bool
	}{
		"unset":      {nil, false},
		"memory off": {func() bool { return false }, false},
		"memory on":  {func() bool { return true }, true},
	} {
		claude.AutoMemoryOff = tc.hook
		args := chatClaudeCmd(context.Background(), "-p", "x").Args
		i := slices.Index(args, "--settings")
		if (i >= 0) != tc.want {
			t.Errorf("%s: --settings present = %v, want %v (%v)", name, i >= 0, tc.want, args)
		}
		if tc.want && (i+1 >= len(args) || args[i+1] == "" || !strings.Contains(args[i+1], `"autoMemoryEnabled":false`)) {
			t.Errorf("%s: --settings value %v", name, args)
		}
		if j := slices.Index(args, "--setting-sources"); j != 1 || args[j+1] != "" {
			t.Errorf("%s: --setting-sources isolation lost: %v", name, args)
		}
	}
}
