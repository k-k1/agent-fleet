package sessionx

// The pure part of delivery verification: detecting a draft left in the composer, which is
// what decides between re-sending Enter and retyping the whole prompt.

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
	// Already submitted (empty composer) is false, so the caller branches to retyping.
	submitted := "…transcript…\n❯ \n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if promptDraftVisible(submitted, "/scout") {
		t.Fatal("an empty composer must not read as a draft")
	}
	// A long or multi-line prompt matches on the first 12 runes of its first line (rune-safe
	// and tolerant of wrapping).
	long := "セッションの定時レビューを開始してください。対象は昨日の差分すべてで、結果はレポートにまとめること。"
	capturedLong := "❯ セッションの定時レビューを開\nください。…（折り返し）\n⏵⏵ bypass permissions on\n"
	if !promptDraftVisible(capturedLong, long+"\n二行目") {
		t.Fatal("long multi-line prompt must match on its first-line head")
	}
	if promptDraftVisible("", "/scout") || promptDraftVisible(capturedLong, "") {
		t.Fatal("empty capture or empty prompt must be false")
	}
}
