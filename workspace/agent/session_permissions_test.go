package main

// 「権限確認をスキップする」の kind 毎の既定を ui-prefs から読む層（docs/76）。
// 解決そのものは internal/agents の表テストが持つので、ここが見るのは 2 点だけ:
// prefs の形（agentLaunchDefaults[kind].skipPermissions）を読めること、そして
// **承認待ちを Console から答えられない kind では、書いてあっても効かせない**こと。

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestSkipPermissionsPref(t *testing.T) {
	writeUIPrefs(t, `{"agentLaunchDefaults":{
		"claude":{"model":"opus","skipPermissions":false},
		"cursor":{"model":""},
		"codex":{"model":"","skipPermissions":false}
	}}`)

	if v, ok := skipPermissionsPref(session.KindClaude); !ok || v {
		t.Errorf("claude: got (%v,%v), want (false,true)", v, ok)
	}
	// 設定はあるが skipPermissions を持たない行 = 未設定（既定に従う）。
	if _, ok := skipPermissionsPref(session.KindCursor); ok {
		t.Error("cursor: a row without skipPermissions must read as unset")
	}
	// codex は承認導線が無く PermissionChoice を立てていない。prefs に false が
	// 書かれていても無視する — 答えようのない承認ダイアログで固まるより、従来どおり
	// bypass で起動する方が確実に良い。
	if _, ok := skipPermissionsPref(session.KindCodex); ok {
		t.Error("codex: PermissionChoice を持たない kind の設定は効かせない")
	}
}

func TestSkipPermissionsPrefMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := skipPermissionsPref(session.KindClaude); ok {
		t.Error("prefs が無いときは未設定を返す（既定 true は agents 側が持つ）")
	}
}
