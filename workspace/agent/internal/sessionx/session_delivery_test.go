package sessionx

// 配達検証の純粋部分: コンポーザ下書き検出（Enter 再送 vs 全文再タイプの分岐材料）。

import "testing"

func TestPromptDraftVisible(t *testing.T) {
	captured := "…transcript…\n" +
		"──────────────── [AF] 定時 ──\n" +
		"❯ /scout\n" +
		"───────────────────────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if !promptDraftVisible(captured, "/scout") {
		t.Fatal("draft sitting in the composer must be detected")
	}
	// 提出済み（コンポーザ空）なら false → 再タイプ側に分岐する。
	submitted := "…transcript…\n❯ \n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if promptDraftVisible(submitted, "/scout") {
		t.Fatal("an empty composer must not read as a draft")
	}
	// 長い/複数行プロンプトは先頭行の頭 12 ルーンで照合される（rune 安全・折り返し耐性）。
	long := "セッションの定時レビューを開始してください。対象は昨日の差分すべてで、結果はレポートにまとめること。"
	capturedLong := "❯ セッションの定時レビューを開\nください。…（折り返し）\n⏵⏵ bypass permissions on\n"
	if !promptDraftVisible(capturedLong, long+"\n二行目") {
		t.Fatal("long multi-line prompt must match on its first-line head")
	}
	if promptDraftVisible("", "/scout") || promptDraftVisible(capturedLong, "") {
		t.Fatal("empty capture or empty prompt must be false")
	}
}
