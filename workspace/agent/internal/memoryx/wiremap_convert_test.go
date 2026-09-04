// wiremap_convert_test.go — memoryx で変換した map サイトの等価証明（CONTRACT-MAP / 脚③）。
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
	LastSnapshot string // "" = head がゼロ値（キーごと出ない）
}

func TestWireEquivMemoryRoots(t *testing.T) {
	inputs := []memoryRootsIn{
		{
			Roots:      []memoryRootView{{Kind: "claude", Label: "Claude", Files: 3, Bytes: 120}},
			Inactive:   []memoryInactiveRoot{{Kind: "codex", Reason: "codex_memories_disabled"}},
			Auto:       true,
			AutoLocked: false,
			// 🔴 条件付きキーの「在る」側。
			LastSnapshot: "2026-09-03T12:00:00Z",
		},
		{
			Roots:    []memoryRootView{},
			Inactive: []memoryInactiveRoot{},
			// 🔴 条件付きキーの「無い」側。旧 map はキーを入れない＝JSON に出ない。
			// 新 struct は omitempty がそれを再現する。**値が RFC3339 で必ず非空**なので、
			// omitempty が消すのは「無い」場合だけ——「在って空文字」は起こらない。
			LastSnapshot: "",
		},
	}
	got := wiretest.AssertEquiv(t, "HandleMemoryRoots", inputs,
		func(in memoryRootsIn) any { // 旧（memory_handlers.go の map リテラルの写し）
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
	t.Logf("突き合わせ方式: %s", got)
}
