package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
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
	if uiprefs.ChatQuietCompletion() {
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
	if !uiprefs.ChatQuietCompletion() {
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

func TestPutUIPrefsBacksUpAShrinkingWrite(t *testing.T) {
	writeUIPrefs(t, `{"quickReplies":{"ok":{"text":"OK","count":9,"at":1}},"iconSet":"seti"}`)

	// 学習を失った既定値一式の PUT（事故の形）。書き込み自体は通る。
	rec := httptest.NewRecorder()
	handlePutUIPrefs(rec, httptest.NewRequest("PUT", "/env/ui-prefs", strings.NewReader(`{"quickReplies":{},"iconSet":"vscode"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d", rec.Code)
	}
	if got := uiprefs.Read()["iconSet"]; got != "vscode" {
		t.Fatalf("write must not be refused: iconSet = %v", got)
	}
	// 直前の版が残っていること＝復旧の手がかりがある。
	b, err := os.ReadFile(uiprefs.BackupPath())
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
	b2, err := os.ReadFile(uiprefs.BackupPath())
	if err != nil || string(b2) != string(b) {
		t.Fatalf("backup must survive later benign writes: %v", err)
	}
}
