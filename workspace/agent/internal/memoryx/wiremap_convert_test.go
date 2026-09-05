// wiremap_convert_test.go — equivalence proofs for the map sites converted in memoryx
// (CONTRACT-MAP / leg 3).
package memoryx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/wiretest"
)

type memoryRootsIn struct {
	Roots        []memoryRootView
	Inactive     []memoryInactiveRoot
	Auto         bool
	AutoLocked   bool
	LastSnapshot string // "" = head is the zero value (the key is omitted entirely)
}

func TestWireEquivMemoryRoots(t *testing.T) {
	inputs := []memoryRootsIn{
		{
			Roots:      []memoryRootView{{Kind: "claude", Label: "Claude", Files: 3, Bytes: 120}},
			Inactive:   []memoryInactiveRoot{{Kind: "codex", Reason: "codex_memories_disabled"}},
			Auto:       true,
			AutoLocked: false,
			// The "present" side of the conditional key.
			LastSnapshot: "2026-09-03T12:00:00Z",
		},
		{
			Roots:    []memoryRootView{},
			Inactive: []memoryInactiveRoot{},
			// The "absent" side of the conditional key: the old map leaves the key out, so it
			// never reaches the JSON, and omitempty reproduces that on the new struct. The value
			// is an RFC3339 stamp and therefore never legitimately empty, so omitempty only ever
			// drops the absent case — "present but empty" cannot happen.
			LastSnapshot: "",
		},
	}
	got := wiretest.AssertEquiv(t, "HandleMemoryRoots", inputs,
		func(in memoryRootsIn) any { // old (a copy of the map literal in memory_handlers.go)
			m := map[string]any{
				"roots":      in.Roots,
				"inactive":   in.Inactive,
				"auto":       in.Auto,
				"autoLocked": in.AutoLocked,
			}
			if in.LastSnapshot != "" {
				m["lastSnapshot"] = in.LastSnapshot
			}
			return m
		},
		func(in memoryRootsIn) any {
			return memoryRootsWire{
				Roots: in.Roots, Inactive: in.Inactive,
				Auto: in.Auto, AutoLocked: in.AutoLocked, LastSnapshot: in.LastSnapshot,
			}
		})
	t.Logf("comparison mode: %s", got)
}
