package chatx

// Pins down the session report's split between display text and instruction text, and that it
// follows the UI language (docs/log/28 P6).
//
// Three things are checked:
//  1. The display (card) carries facts only, with no operator-facing tool names or orders
//     leaking into it.
//  2. The instruction (prompt) is rebuilt in the display language, with not one Japanese
//     character on the English side.
//  3. A report written before P6 (no ReportKind) uses its Content as is.

import (
	"strings"
	"testing"
)

// Every kind / reason combination (covering the axes the display key branches on).
func reportCases() []reportView {
	base := func(kind, reason string, extra map[string]string) reportView {
		args := map[string]string{"display": "リファクタ作業", "name": "s7"}
		for k, v := range extra {
			args[k] = v
		}
		return reportView{kind: kind, reason: reason, args: args}
	}
	return []reportView{
		base(ReportKindAnswerReady, "", nil),
		base(ReportKindAnswerReady, ReportReasonTurnFailed, nil),
		base(ReportKindAnswerReady, ReportReasonTurnFailed, map[string]string{"resume_at": "1754000000000"}),
		base(ReportKindAnswerReady, ReportReasonTurnAborted, map[string]string{"attempts": "1"}),
		base(ReportKindAnswerReady, ReportReasonTurnAborted, map[string]string{"attempts": "9"}), // over the cap
		base("question", "", nil),
		base("plan-approval", "", nil),
		base("permission-request", "", nil),
		base(reportKindReopened, "", map[string]string{"reopen_at": "1754000000000"}),
		base(reportKindReopened, reportReasonReopenCapped, nil),
		base("exit", "oom", nil),
		base("exit", "crashed", nil),
		base("exit", "とても新しい理由", nil), // an unknown reason comes out raw
		base("なにか新しい kind", "", nil),
		base(ReportKindAnswerReady, "", map[string]string{"fold_n": "2", "fold_ats": "a / b"}),
	}
}

// The card contains no instructions aimed at the model. Mixing them back in returns to the
// original state where translating also changed the instruction text (the split itself
// breaks).
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
					t.Errorf("%s/%s (%s): operator instruction %q leaked into the display card:\n%s",
						v.kind, v.reason, lang, o, card)
				}
			}
			if strings.TrimSpace(card) == "" {
				t.Errorf("%s/%s (%s): the display card is empty", v.kind, v.reason, lang)
			}
		}
	}
}

// In the English locale both the display and the prompt are entirely English (the language
// signal is never split).
func TestReportTextEnglishHasNoJapanese(t *testing.T) {
	for _, v := range reportCases() {
		// Display name and session name are the user's own data and stay verbatim (not
		// something to translate).
		v.args["display"] = "refactor"
		// An unknown kind / exit reason is emitted raw by design, so it is out of scope here.
		if v.displayKey() == reportKeyUnknown || (v.kind == "exit" && exitLabelFor(v.reason, "en") == v.reason) {
			continue
		}
		card := v.displayText("en")
		if r := firstJapaneseRune(card); r != 0 {
			t.Errorf("%s/%s: Japanese %q found in the English card:\n%s", v.kind, v.reason, string(r), card)
		}
		m := ChatMessage{Role: "report", ReportKind: v.kind, ReportReason: v.reason, NoticeArgs: v.args}
		prompt := ReportPromptFor(m, "en")
		if r := firstJapaneseRune(prompt); r != 0 {
			t.Errorf("%s/%s: Japanese %q found in the English prompt:\n%s", v.kind, v.reason, string(r), prompt)
		}
		// The prompt is a superset of the display (it always contains the facts).
		if !strings.Contains(prompt, v.fact("en")) {
			t.Errorf("%s/%s: the prompt dropped the facts:\n%s", v.kind, v.reason, prompt)
		}
	}
	// An unknown exit reason (a raw value that may well be Japanese) passes through as an
	// exception. Better to show it raw than to blank an untranslatable value (see the comment
	// on exitLabelFor).
	v := reportView{kind: "exit", reason: "とても新しい理由", args: map[string]string{"display": "d", "name": "s7"}}
	if !strings.Contains(v.fact("en"), "とても新しい理由") {
		t.Error("the unknown exit reason was dropped on the English side")
	}
}

// The prompt follows the display language at the moment it is assembled, not at delivery (a
// report left pending across a language switch arrives in the new language).
func TestReportPromptFollowsUILocaleAtInjection(t *testing.T) {
	m := ChatMessage{
		Role: "report", ReportKind: ReportKindAnswerReady, ReportReason: ReportReasonTurnFailed,
		Content:    "（保存時の日本語本文）",
		NoticeArgs: map[string]string{"display": "d", "name": "s7"},
	}
	c := &ChatConversation{Messages: []ChatMessage{m}}

	writeUIPrefs(t, `{"locale":"en"}`)
	prompt, pending := InjectPendingReports(c, "go on")
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if r := firstJapaneseRune(prompt); r != 0 {
		t.Fatalf("Japanese %q found in the report injected under the English locale:\n%s", string(r), prompt)
	}
	if !strings.Contains(prompt, "go on") {
		t.Fatalf("the user's message was dropped:\n%s", prompt)
	}

	writeUIPrefs(t, `{"locale":"ja"}`)
	if prompt, _ = InjectPendingReports(c, "続けて"); !strings.Contains(prompt, "get_session_output でエラー本文") {
		t.Fatalf("the usual instruction text is missing under the Japanese locale:\n%s", prompt)
	}
}

// A report from before P6 (no ReportKind) uses the body stored at the time. That Content was
// written with the instructions included, so discarding it leaves the operator with no idea
// what to do.
func TestLegacyReportFallsBackToStoredContent(t *testing.T) {
	legacy := ChatMessage{Role: "report", Content: "セッション「x」(s1) からの報告: 応答が完了し、入力待ちになりました。"}
	if got := ReportPromptFor(legacy, "en"); got != legacy.Content {
		t.Fatalf("the legacy record's body was not used: %q", got)
	}
}

// Every display key must exist in the Console catalogue for both ja and en (a missing one
// leaves the card on the Japanese fallback, i.e. Japanese remains in an English Console).
func TestReportKeysExistInConsoleCatalogs(t *testing.T) {
	keys := []string{
		reportKeyAnswerReady, reportKeyTurnFailed, reportKeyTurnAborted, reportKeyTurnAbortedCapped,
		reportKeyQuestion, reportKeyPlanApproval, reportKeyPermission,
		reportKeyReopened, reportKeyReopenCapped, reportKeyExit, reportKeyUnknown,
		// Notes and exit-reason labels (the fragments the Console assembles).
		"chat.report.note.rate_limit_resume", "chat.report.note.fold", "chat.report.note.reopen_target",
		"chat.report.exit_reason.oom", "chat.report.exit_reason.crashed", "chat.report.exit_reason.killed",
	}
	for _, locale := range []string{"ja", "en"} {
		catalog := consoleCatalog(t, locale)
		for _, key := range keys {
			if !consoleCatalogHasKey(catalog, key) {
				t.Errorf("%s catalog is missing %q", locale, key)
			}
		}
	}
}
