package agents

// 「権限確認をスキップするか」の 3 層解決（docs/log/76）。ここが崩れると、利用者が設定で
// オフにしたのに bypass で起動する（＝選択が無かったことになる）か、逆に既定のまま
// 承認待ちで固まる。どちらも黙って起きるので表で固定する。

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func boolp(v bool) *bool { return &v }

func TestSkipPermissionsResolution(t *testing.T) {
	cases := []struct {
		name string
		meta session.Meta
		pref map[string]bool // kind → ui-prefs の既定（不在＝設定なし）
		want bool
	}{
		{"設定も指定も無ければ従来どおり bypass", session.Meta{Kind: session.KindClaude}, nil, true},
		{"kind 毎の既定でオフ", session.Meta{Kind: session.KindClaude}, map[string]bool{session.KindClaude: false}, false},
		{"他 kind の既定は混ざらない", session.Meta{Kind: session.KindCursor}, map[string]bool{session.KindClaude: false}, true},
		{"セッションの明示指定が既定に勝つ（オフ→オン）",
			session.Meta{Kind: session.KindClaude, SkipPermissions: boolp(true)}, map[string]bool{session.KindClaude: false}, true},
		{"セッションの明示指定が既定に勝つ（オン→オフ）",
			session.Meta{Kind: session.KindClaude, SkipPermissions: boolp(false)}, map[string]bool{session.KindClaude: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := SkipPermissionsPref
			t.Cleanup(func() { SkipPermissionsPref = orig })
			SkipPermissionsPref = func(kind string) (bool, bool) {
				v, ok := tc.pref[kind]
				return v, ok
			}
			if got := SkipPermissions(tc.meta); got != tc.want {
				t.Fatalf("SkipPermissions = %v, want %v", got, tc.want)
			}
		})
	}
}

// plan 起動は kind を問わず bypass を外す。利用者が「スキップする」を選んでいても、
// 全ツールを自動承認しては plan で始める意味が無い（各 kind の buildProgram/spawn は
// この 1 つの bool だけを見るので、ここが plan を折り込む唯一の場所）。
func TestBypassPermissionsFoldsPlanMode(t *testing.T) {
	orig := SkipPermissionsPref
	t.Cleanup(func() { SkipPermissionsPref = orig })
	SkipPermissionsPref = func(string) (bool, bool) { return false, false } // 設定なし＝既定 true

	if !BypassPermissions(session.Meta{Kind: session.KindClaude}) {
		t.Error("normal launch: want bypass")
	}
	if BypassPermissions(session.Meta{Kind: session.KindClaude, Mode: "plan"}) {
		t.Error("plan launch: want no bypass")
	}
	if BypassPermissions(session.Meta{Kind: session.KindClaude, Mode: "plan", SkipPermissions: boolp(true)}) {
		t.Error("plan launch with skip=true: want no bypass (plan wins)")
	}
}
