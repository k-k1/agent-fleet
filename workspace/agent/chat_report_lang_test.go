package main

// セッション報告の「表示テキスト／指示テキスト」分離と、その言語追随を固定する（docs/log/28 P6）。
//
// 見るのは 3 点:
//  1. 表示（カード）は事実だけ — オペレーター向けのツール名や指示文が漏れていない。
//  2. 指示（プロンプト）は表示言語で組み直され、英語側に日本語が 1 文字も無い。
//  3. P6 より前に書かれた報告（ReportKind 無し）は Content をそのまま使う。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 全 kind / reason の組み合わせ（表示キーが分岐する軸を網羅する）。
func reportCases() []reportView {
	base := func(kind, reason string, extra map[string]string) reportView {
		args := map[string]string{"display": "リファクタ作業", "name": "s7"}
		for k, v := range extra {
			args[k] = v
		}
		return reportView{kind: kind, reason: reason, args: args}
	}
	return []reportView{
		base(reportKindAnswerReady, "", nil),
		base(reportKindAnswerReady, reportReasonTurnFailed, nil),
		base(reportKindAnswerReady, reportReasonTurnFailed, map[string]string{"resume_at": "1754000000000"}),
		base(reportKindAnswerReady, reportReasonTurnAborted, map[string]string{"attempts": "1"}),
		base(reportKindAnswerReady, reportReasonTurnAborted, map[string]string{"attempts": "9"}), // 上限超え
		base("question", "", nil),
		base("plan-approval", "", nil),
		base("permission-request", "", nil),
		base(reportKindReopened, "", map[string]string{"reopen_at": "1754000000000"}),
		base(reportKindReopened, reportReasonReopenCapped, nil),
		base("exit", "oom", nil),
		base("exit", "crashed", nil),
		base("exit", "とても新しい理由", nil), // 未知の理由は生のまま出る
		base("なにか新しい kind", "", nil),
		base(reportKindAnswerReady, "", map[string]string{"fold_n": "2", "fold_ats": "a / b"}),
	}
}

// カードはモデル向けの指示を含まない。ここが混ざると「訳すと指示文まで変わる」元の状態に
// 戻ってしまう（分離そのものが壊れる）。
func TestReportDisplayCarriesNoOperatorOrders(t *testing.T) {
	orders := []string{
		"get_session_output", "get_session_status", "answer_session_question",
		"respond_session_plan", "send_to_session", "利用者に伝え",
	}
	for _, v := range reportCases() {
		for _, lang := range []string{"ja", "en"} {
			card := v.displayText(lang)
			for _, o := range orders {
				if strings.Contains(card, o) {
					t.Errorf("%s/%s (%s): 表示カードにオペレーターへの指示 %q が漏れている:\n%s",
						v.kind, v.reason, lang, o, card)
				}
			}
			if strings.TrimSpace(card) == "" {
				t.Errorf("%s/%s (%s): 表示カードが空", v.kind, v.reason, lang)
			}
		}
	}
}

// 英語ロケールの報告は表示・プロンプトとも英語一色（言語シグナルを割らない）。
func TestReportTextEnglishHasNoJapanese(t *testing.T) {
	for _, v := range reportCases() {
		// 表示名・セッション名は利用者のデータなので原文のまま（翻訳対象ではない）。
		v.args["display"] = "refactor"
		// 未知の kind / exit 理由は生の値をそのまま出す仕様なので、この検査の対象外。
		if v.displayKey() == reportKeyUnknown || (v.kind == "exit" && exitLabelFor(v.reason, "en") == v.reason) {
			continue
		}
		card := v.displayText("en")
		if r := firstJapaneseRune(card); r != 0 {
			t.Errorf("%s/%s: 英語カードに日本語 %q が混入:\n%s", v.kind, v.reason, string(r), card)
		}
		m := chatMessage{Role: "report", ReportKind: v.kind, ReportReason: v.reason, NoticeArgs: v.args}
		prompt := reportPromptFor(m, "en")
		if r := firstJapaneseRune(prompt); r != 0 {
			t.Errorf("%s/%s: 英語プロンプトに日本語 %q が混入:\n%s", v.kind, v.reason, string(r), prompt)
		}
		// プロンプトは表示の上位集合（事実は必ず含む）。
		if !strings.Contains(prompt, v.fact("en")) {
			t.Errorf("%s/%s: プロンプトが事実を落としている:\n%s", v.kind, v.reason, prompt)
		}
	}
	// exit の未知理由（＝日本語かもしれない生の値）は例外的に素通しする。翻訳できない値を
	// 空にするより、生でも見えている方がよい（exitLabelFor のコメント）。
	v := reportView{kind: "exit", reason: "とても新しい理由", args: map[string]string{"display": "d", "name": "s7"}}
	if !strings.Contains(v.fact("en"), "とても新しい理由") {
		t.Error("未知の exit 理由が英語側で落ちている")
	}
}

// プロンプトは配信時ではなく**組む瞬間**の表示言語に従う（保留のまま言語を切り替えた報告も
// 新しい言語で届く）。
func TestReportPromptFollowsUILocaleAtInjection(t *testing.T) {
	m := chatMessage{
		Role: "report", ReportKind: reportKindAnswerReady, ReportReason: reportReasonTurnFailed,
		Content:    "（保存時の日本語本文）",
		NoticeArgs: map[string]string{"display": "d", "name": "s7"},
	}
	c := &chatConversation{Messages: []chatMessage{m}}

	writeUIPrefs(t, `{"locale":"en"}`)
	prompt, pending := injectPendingReports(c, "go on")
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if r := firstJapaneseRune(prompt); r != 0 {
		t.Fatalf("英語ロケールの報告注入に日本語 %q が混入:\n%s", string(r), prompt)
	}
	if !strings.Contains(prompt, "go on") {
		t.Fatalf("利用者のメッセージが落ちている:\n%s", prompt)
	}

	writeUIPrefs(t, `{"locale":"ja"}`)
	if prompt, _ = injectPendingReports(c, "続けて"); !strings.Contains(prompt, "get_session_output でエラー本文") {
		t.Fatalf("日本語ロケールで従来の指示文が出ない:\n%s", prompt)
	}
}

// P6 より前の報告（ReportKind 無し）は当時の本文をそのまま使う。当時の Content は指示込みで
// 書かれているので、それを捨てるとオペレーターが何をすべきか分からなくなる。
func TestLegacyReportFallsBackToStoredContent(t *testing.T) {
	legacy := chatMessage{Role: "report", Content: "セッション「x」(s1) からの報告: 応答が完了し、入力待ちになりました。"}
	if got := reportPromptFor(legacy, "en"); got != legacy.Content {
		t.Fatalf("旧レコードの本文が使われていない: %q", got)
	}
}

// 表示キーは Console のカタログに ja/en 両方そろっていること（欠けるとカードが
// フォールバックの日本語のまま出る＝英語 Console で日本語が残る）。
func TestReportKeysExistInConsoleCatalogs(t *testing.T) {
	keys := []string{
		reportKeyAnswerReady, reportKeyTurnFailed, reportKeyTurnAborted, reportKeyTurnAbortedCapped,
		reportKeyQuestion, reportKeyPlanApproval, reportKeyPermission,
		reportKeyReopened, reportKeyReopenCapped, reportKeyExit, reportKeyUnknown,
		// 付記と exit 理由ラベル（Console 側で組み立てる断片）。
		"chat.report.note.rate_limit_resume", "chat.report.note.fold", "chat.report.note.reopen_target",
		"chat.report.exit_reason.oom", "chat.report.exit_reason.crashed", "chat.report.exit_reason.killed",
	}
	for _, locale := range []string{"ja", "en"} {
		path := filepath.Join("..", "..", "console", "src", "lib", "i18n", "locales", locale+".ts")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("catalog not available (%v)", err)
		}
		for _, key := range keys {
			if !strings.Contains(string(b), `"`+key+`"`) {
				t.Errorf("%s.ts is missing %q", locale, key)
			}
		}
	}
}
