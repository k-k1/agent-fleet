// wiremap_convert_test.go — sessionx で変換した map サイトの等価証明（CONTRACT-MAP / 脚③）。
package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/wiretest"
)

type threadSettingsIn struct {
	Model, Effort, Mode                      string
	DynamicModel, DynamicEffort, DynamicMode bool
}

func TestWireEquivManagedThreadSettings(t *testing.T) {
	inputs := []threadSettingsIn{
		{Model: "opus", Effort: "high", Mode: "plan",
			DynamicModel: true, DynamicEffort: true, DynamicMode: false},
		// 未設定＝空文字。**キーは出続けなければならない**（omitempty を付けると消える）。
		{Model: "", Effort: "", Mode: "", DynamicModel: false, DynamicEffort: true, DynamicMode: true},
	}
	got := wiretest.AssertEquiv(t, "HandleSessionSettingsGet", inputs,
		func(in threadSettingsIn) any { // 旧（session_turn.go の map リテラルの写し）
			return map[string]any{
				"model": in.Model, "effort": in.Effort, "mode": in.Mode,
				"dynamicModel": in.DynamicModel, "dynamicEffort": in.DynamicEffort,
				"dynamicMode": in.DynamicMode,
			}
		},
		func(in threadSettingsIn) any {
			return managedThreadSettingsWire{
				Model: in.Model, Effort: in.Effort, Mode: in.Mode,
				DynamicModel: in.DynamicModel, DynamicEffort: in.DynamicEffort,
				DynamicMode: in.DynamicMode,
			}
		})
	t.Logf("突き合わせ方式: %s", got)
}
