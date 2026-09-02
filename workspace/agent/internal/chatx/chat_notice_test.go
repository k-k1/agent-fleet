package chatx

import (
	"strings"
	"testing"
)

// 全 notice はカタログキーと引数を刻む（ADR 0033）。Content は正本言語の
// フォールバックとして残るので、両方が揃っていることを確かめる。
func TestNoticesCarryCatalogKeys(t *testing.T) {
	withTempHome(t)

	t.Run("ctx_pressure", func(t *testing.T) {
		c := &ChatConversation{ID: RandUUID()}
		setChatContext(c, 170000, 0, 0, 200000, "claude-sonnet-5")
		NoteContextPressure(c)
		m := lastNotice(t, c)
		assertNoticeKey(t, m, noticeKeyCtxPressure)
		// 引数は表示文と同じ値（85% / 170k / 200k）。
		if m.NoticeArgs["pct"] != "85" || m.NoticeArgs["tokens"] != "170k" || m.NoticeArgs["window"] != "200k" {
			t.Fatalf("args = %v", m.NoticeArgs)
		}
	})

	t.Run("ctx_overflow", func(t *testing.T) {
		c := &ChatConversation{ID: RandUUID()}
		NoteContextOverflow(c)
		assertNoticeKey(t, lastNotice(t, c), noticeKeyCtxOverflow)
	})

	t.Run("auto_paused", func(t *testing.T) {
		c := &ChatConversation{ID: RandUUID(), Messages: []ChatMessage{
			{Role: "report", Content: "報告", Session: "s000001"},
		}}
		noteAutoTurnPaused(c, 3)
		m := lastNotice(t, c)
		assertNoticeKey(t, m, noticeKeyAutoPaused)
		if m.NoticeArgs["limit"] != "3" || m.NoticeArgs["pending"] != "1" {
			t.Fatalf("args = %v", m.NoticeArgs)
		}
	})

	t.Run("compact", func(t *testing.T) {
		for reason, want := range map[string]string{
			CompactReasonManual:   noticeKeyCompactManual,
			CompactReasonAuto:     noticeKeyCompactAuto,
			CompactReasonRecovery: noticeKeyCompactRecovery,
			"":                    noticeKeyCompactManual, // 未設定は手動扱い
		} {
			if got := compactNoticeKey(reason); got != want {
				t.Fatalf("compactNoticeKey(%q) = %q, want %q", reason, got, want)
			}
		}
	})
}

func lastNotice(t *testing.T, c *ChatConversation) ChatMessage {
	t.Helper()
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "notice" {
			return c.Messages[i]
		}
	}
	t.Fatal("no notice appended")
	return ChatMessage{}
}

func assertNoticeKey(t *testing.T, m ChatMessage, want string) {
	t.Helper()
	if m.NoticeKey != want {
		t.Fatalf("notice_key = %q, want %q", m.NoticeKey, want)
	}
	if strings.TrimSpace(m.Content) == "" {
		t.Fatal("notice has no source-language fallback content")
	}
}

// Console のカタログに全 notice キーが存在することを確かめる（Go 側でキーを足して
// 翻訳を忘れると、英語 Console が正本言語のフォールバックに落ちる — 見た目は動くので
// 気づけない）。console/ を含まない配布物でビルドする場合に備えて、カタログが無ければ
// スキップする。
func TestNoticeKeysExistInConsoleCatalogs(t *testing.T) {
	keys := []string{
		noticeKeyCtxPressure, noticeKeyCtxOverflow,
		noticeKeyCompactManual, noticeKeyCompactAuto, noticeKeyCompactRecovery,
		noticeKeyAgentSwitched,
	}
	// auto_paused は 3 断片（見出し・保留件数・締め）を Console 側で組み立てる。
	autoPaused := []string{
		noticeKeyAutoPaused + ".head",
		noticeKeyAutoPaused + ".pending_other",
		noticeKeyAutoPaused + ".tail",
	}
	for _, locale := range []string{"ja", "en"} {
		catalog := consoleCatalog(t, locale)
		for _, k := range append(append([]string{}, keys...), autoPaused...) {
			if !consoleCatalogHasKey(catalog, k) {
				t.Errorf("%s catalog is missing %q", locale, k)
			}
		}
	}
}
