package uiprefs

import (
	"testing"
)

// 累積データ（学習済みの返信候補・ピン・利用実績・キー割当…）は、事故で痩せた PUT が
// 来ると復元不能に消える。実際に消えた（返信サジェストが全端末で初期状態に戻った）ので、
// 「痩せる書き込みの直前の版を .prev に残す」ことを仕様として固定する。拒否はしない —
// 設定 > キー の「全消去」は利用者の正当な操作で、拒否すると効かなくなる。
func TestShrunkPrefKeys(t *testing.T) {
	before := map[string]any{
		"quickReplies":       map[string]any{"ok": map[string]any{"text": "OK"}},
		"quickRepliesPinned": []any{"OK"},
		"ttsUserDict":        "af=エーエフ",
		"ssmHostUsage":       map[string]any{},
		"assistantAutoTurn":  false,
	}
	tests := []struct {
		name  string
		after map[string]any
		want  []string
	}{
		{"defaults over real data flags every populated key",
			map[string]any{"quickReplies": map[string]any{}, "quickRepliesPinned": []any{}, "ttsUserDict": ""},
			[]string{"quickReplies", "quickRepliesPinned", "ttsUserDict"}},
		{"a missing key counts as lost too (an older Console omits it)",
			map[string]any{},
			[]string{"quickReplies", "quickRepliesPinned", "ttsUserDict"}},
		{"carrying the same content through is not a loss",
			before,
			nil},
		{"growing is not a loss",
			map[string]any{
				"quickReplies":       map[string]any{"ok": map[string]any{"text": "OK"}, "go": map[string]any{"text": "続けて"}},
				"quickRepliesPinned": []any{"OK", "続けて"},
				"ttsUserDict":        "af=エーエフ",
			},
			[]string{}},
		{"an already-empty key cannot shrink", map[string]any{"ssmHostUsage": map[string]any{}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShrunkKeys(before, tt.after)
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			// 順序は accumulatedPrefKeys の並び（安定）。
			if len(got) < len(tt.want) {
				t.Fatalf("shrunk = %v, want %v", got, tt.want)
			}
			for _, k := range tt.want {
				found := false
				for _, g := range got {
					if g == k {
						found = true
					}
				}
				if !found {
					t.Fatalf("shrunk = %v, missing %q", got, k)
				}
			}
		})
	}
	// 真偽値は「消えた」ではなく選ばれた値なので、false になっても退避の理由にはしない。
	if got := ShrunkKeys(before, map[string]any{"assistantAutoTurn": true}); len(got) != 3 {
		t.Fatalf("boolean flips must not be counted as accumulated loss: %v", got)
	}
}
