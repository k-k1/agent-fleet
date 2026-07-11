//go:build e2e

// 実クレデンシャル・スモーク（L4）: 焼き込みの claude CLI が実際に Anthropic と会話
// できるかの最終確認。認証は 2 経路のどちらか:
//   - E2E_ANTHROPIC_API_KEY   … API キー（従量課金）→ ANTHROPIC_API_KEY として注入
//   - E2E_CLAUDE_OAUTH_TOKEN  … `claude setup-token` で発行した OAuth トークン
//     （Max/Pro サブスク枠を消費・追加課金なし）→ CLAUDE_CODE_OAUTH_TOKEN として注入
//
// どちらも無ければ skip（課金/サブスク枠を伴うため opt-in。CI では e2e.yml の
// workflow_dispatch 専用ジョブが secrets を渡す）。
//
// TUI のオンボーディング（テーマ選択・API キー確認）に依存しないよう、対話セッション
// ではなく shell セッション内で `claude -p`（headless print mode）を実行し、出力
// ファイルを fs API で読み戻して判定する。クレデンシャルは CP の WS_ENV 経由で
// コンテナ env に注入する（打鍵やログに載せない）。
package e2e

import (
	"os"
	"testing"
	"time"
)

func TestClaudeLive(t *testing.T) {
	var env string
	switch {
	case os.Getenv("E2E_ANTHROPIC_API_KEY") != "":
		env = "WS_ENV=ANTHROPIC_API_KEY=" + os.Getenv("E2E_ANTHROPIC_API_KEY")
	case os.Getenv("E2E_CLAUDE_OAUTH_TOKEN") != "":
		env = "WS_ENV=CLAUDE_CODE_OAUTH_TOKEN=" + os.Getenv("E2E_CLAUDE_OAUTH_TOKEN")
	default:
		t.Skip("E2E_ANTHROPIC_API_KEY / E2E_CLAUDE_OAUTH_TOKEN のいずれも未設定 — 実クレデンシャル・スモークはスキップ（opt-in）")
	}
	base := startFleet(t, "e2e-live", env)

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
