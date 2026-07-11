//go:build e2e

// 実 API キー・スモーク（L4）: 焼き込みの claude CLI が実際に Anthropic API と会話
// できるかの最終確認。トークン課金があるため自動トリガには載せず、
// E2E_ANTHROPIC_API_KEY がある時だけ動く（CI では e2e.yml の workflow_dispatch 専用
// ジョブが secrets を渡す）。
//
// TUI のオンボーディング（テーマ選択・API キー確認）に依存しないよう、対話セッション
// ではなく shell セッション内で `claude -p`（headless print mode）を実行し、出力
// ファイルを fs API で読み戻して判定する。ANTHROPIC_API_KEY は CP の WS_ENV 経由で
// コンテナ env に注入する（打鍵やログに載せない）。
package e2e

import (
	"os"
	"testing"
	"time"
)

func TestClaudeLive(t *testing.T) {
	key := os.Getenv("E2E_ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("E2E_ANTHROPIC_API_KEY 未設定 — 実 API スモークはスキップ（課金を伴う opt-in）")
	}
	base := startFleet(t, "e2e-live", "WS_ENV=ANTHROPIC_API_KEY="+key)

	created := postJSON(t, base+"/api/sessions", map[string]any{"kind": "shell", "title": "e2e-live"}, 201)
	name, _ := created["name"].(string)
	if name == "" {
		t.Fatalf("session create returned no name: %v", created)
	}

	// -p は非対話（オンボーディング無し）。応答全文でなく決め打ちトークンの包含だけを
	// 見る（モデルの言い回し揺れに依存しない）。exit code も末尾に落として観測する。
	sendPrompt(t, base, name,
		`claude -p 'Reply with exactly: E2E_OK' > live-out.txt 2>&1; echo "exit=$?" >> live-out.txt`)
	waitFileContains(t, base, "live-out.txt", "E2E_OK", 4*time.Minute)

	postJSON(t, base+"/api/sessions/"+name+"/stop", nil, 200)
	postJSON(t, base+"/api/workspace/stop", nil, 200)
}
