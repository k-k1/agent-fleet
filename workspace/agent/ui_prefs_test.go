package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeUIPrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(homeDir(), ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantAgentOrderPref(t *testing.T) {
	tests := []struct {
		name  string
		prefs string
		want  []string
	}{
		{"unset", `{}`, []string{"claude", "codex", "opencode", "cursor", "agy"}},
		{"full order", `{"assistantAgentOrder":["agy","opencode","codex","claude"]}`,
			[]string{"agy", "opencode", "codex", "claude", "cursor"}},
		{"partial order appends the rest in default order",
			`{"assistantAgentOrder":["opencode"]}`,
			[]string{"opencode", "claude", "codex", "cursor", "agy"}},
		{"junk and dupes dropped",
			`{"assistantAgentOrder":["gemini","codex",42,"codex"]}`,
			[]string{"codex", "claude", "opencode", "cursor", "agy"}},
		{"legacy pin promotes to front", `{"assistantAgent":"opencode"}`,
			[]string{"opencode", "claude", "codex", "cursor", "agy"}},
		{"legacy auto falls through to default", `{"assistantAgent":"auto"}`,
			[]string{"claude", "codex", "opencode", "cursor", "agy"}},
		{"order wins over legacy pin",
			`{"assistantAgent":"opencode","assistantAgentOrder":["codex"]}`,
			[]string{"codex", "claude", "opencode", "cursor", "agy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeUIPrefs(t, tt.prefs)
			if got := assistantAgentOrderPref(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAssistantTokenSavingPrefs pins the token-saving knobs (設定 > アシスタント):
// 既定値・設定値・クランプ・「設定が env より優先」。
func TestAssistantTokenSavingPrefs(t *testing.T) {
	// 未設定 → 既定。
	writeUIPrefs(t, `{}`)
	if got := chatAutoTurnModel(); got != "" {
		t.Fatalf("auto-turn model default = %q", got)
	}
	if chatQuietCompletionEnabled() {
		t.Fatal("quiet completion must default OFF")
	}
	if got := chatAutoCompactTokenThreshold(); got != chatCtxAutoCompactTokens {
		t.Fatalf("compact tokens default = %d", got)
	}
	if got := chatAutoTurnDelay(); got != chatAutoTurnDelayDefault {
		t.Fatalf("auto-turn delay default = %v", got)
	}
	if got := mcpSessionOutputTail(); got != mcpSessionOutputTailBytes {
		t.Fatalf("output tail default = %d", got)
	}

	// 設定値が効く（モデルは trim される）。
	writeUIPrefs(t, `{"assistantAutoTurnModel":" haiku ","assistantQuietCompletion":true,
		"assistantAutoCompactTokens":80000,"assistantAutoTurnDelay":120,"assistantOutputTailKiB":64}`)
	if got := chatAutoTurnModel(); got != "haiku" {
		t.Fatalf("auto-turn model = %q", got)
	}
	if !chatQuietCompletionEnabled() {
		t.Fatal("quiet completion should be ON")
	}
	if got := chatAutoCompactTokenThreshold(); got != 80000 {
		t.Fatalf("compact tokens = %d", got)
	}
	if got := chatAutoTurnDelay(); got != 120*time.Second {
		t.Fatalf("auto-turn delay = %v", got)
	}
	if got := mcpSessionOutputTail(); got != 64<<10 {
		t.Fatalf("output tail = %d", got)
	}

	// 設定は env（デプロイ/E2E 用）より優先。
	t.Setenv("AF_CHAT_AUTOCOMPACT_TOKENS", "999999")
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "1")
	if got := chatAutoCompactTokenThreshold(); got != 80000 {
		t.Fatalf("pref should beat env: %d", got)
	}
	if got := chatAutoTurnDelay(); got != 120*time.Second {
		t.Fatalf("pref should beat env: %v", got)
	}

	// クランプ: 圧縮閾値の下限・束ね時間の上限・出力上限の上下限。
	writeUIPrefs(t, `{"assistantAutoCompactTokens":5000,"assistantAutoTurnDelay":100000,"assistantOutputTailKiB":100000}`)
	if got := chatAutoCompactTokenThreshold(); got != chatCtxAutoCompactTokensMin {
		t.Fatalf("compact tokens floor = %d", got)
	}
	if got := chatAutoTurnDelay(); got != chatAutoTurnDelayMax {
		t.Fatalf("auto-turn delay cap = %v", got)
	}
	if got := mcpSessionOutputTail(); got != 1<<20 {
		t.Fatalf("output tail cap = %d", got)
	}
	writeUIPrefs(t, `{"assistantOutputTailKiB":1}`)
	if got := mcpSessionOutputTail(); got != 4<<10 {
		t.Fatalf("output tail floor = %d", got)
	}
}

func TestAssistantModelPrefs(t *testing.T) {
	writeUIPrefs(t, `{
		"assistantModels":{"opencode":"opencode-go/glm-5.2"},
		"assistantUtilityModels":{"opencode":"","claude":"haiku"}
	}`)
	if got, ok := assistantChatModelPref("opencode"); !ok || got != "opencode-go/glm-5.2" {
		t.Fatalf("chat model = %q, %v", got, ok)
	}
	if got, ok := assistantUtilityModelPref("opencode"); !ok || got != "" {
		t.Fatalf("explicit utility default = %q, %v", got, ok)
	}
	if _, ok := assistantUtilityModelPref("codex"); ok {
		t.Fatal("missing backend must remain distinguishable from explicit default")
	}
}

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
			got := shrunkPrefKeys(before, tt.after)
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
	if got := shrunkPrefKeys(before, map[string]any{"assistantAutoTurn": true}); len(got) != 3 {
		t.Fatalf("boolean flips must not be counted as accumulated loss: %v", got)
	}
}

func TestPutUIPrefsBacksUpAShrinkingWrite(t *testing.T) {
	writeUIPrefs(t, `{"quickReplies":{"ok":{"text":"OK","count":9,"at":1}},"iconSet":"seti"}`)

	// 学習を失った既定値一式の PUT（事故の形）。書き込み自体は通る。
	rec := httptest.NewRecorder()
	handlePutUIPrefs(rec, httptest.NewRequest("PUT", "/env/ui-prefs", strings.NewReader(`{"quickReplies":{},"iconSet":"vscode"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d", rec.Code)
	}
	if got := readUIPrefs()["iconSet"]; got != "vscode" {
		t.Fatalf("write must not be refused: iconSet = %v", got)
	}
	// 直前の版が残っていること＝復旧の手がかりがある。
	b, err := os.ReadFile(uiPrefsBackupPath())
	if err != nil {
		t.Fatalf("no backup kept: %v", err)
	}
	var prev map[string]any
	if err := json.Unmarshal(b, &prev); err != nil {
		t.Fatal(err)
	}
	learned, _ := prev["quickReplies"].(map[string]any)
	if len(learned) != 1 {
		t.Fatalf("backup lost the learned replies: %v", prev)
	}

	// 痩せない書き込みは退避を上書きしない（事故の直前の版を最新の平穏な版で流さない）。
	rec = httptest.NewRecorder()
	handlePutUIPrefs(rec, httptest.NewRequest("PUT", "/env/ui-prefs", strings.NewReader(`{"quickReplies":{},"iconSet":"material"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("second PUT = %d", rec.Code)
	}
	b2, err := os.ReadFile(uiPrefsBackupPath())
	if err != nil || string(b2) != string(b) {
		t.Fatalf("backup must survive later benign writes: %v", err)
	}
}
