import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { reportText } from "./report.ts";

// docs/log/28 P6: the report card renders facts only, from the catalogue. Operator instructions
// ("use get_session_output ...") are built on the Agent side when it assembles the prompt and must
// never appear here; if they do, we are back to the state where translating a card also rewrote the
// instructions in it.
describe("reportText (docs/log/28 P6)", () => {
  beforeEach(() => setLocale("ja"));
  afterEach(() => setLocale("ja"));

  const answerReady = {
    content: "セッション「リファクタ」(s7) からの報告: 応答が完了し、入力待ちになりました。",
    notice_key: "chat.report.answer_ready",
    notice_args: { display: "リファクタ", name: "s7" },
    report_kind: "answer-ready",
  };

  it("renders the card in the current locale, not the language it was stored in", () => {
    expect(reportText(answerReady)).toBe("セッション「リファクタ」(s7) からの報告: 応答が完了し、入力待ちになりました。");
    setLocale("en");
    expect(reportText(answerReady)).toBe("Report from session “リファクタ” (s7): The session answered and is now waiting for input.");
  });

  it("carries no operator instructions", () => {
    for (const locale of ["ja", "en"] as const) {
      setLocale(locale);
      const q = reportText({
        content: "",
        notice_key: "chat.report.question",
        notice_args: { display: "d", name: "s7" },
        report_kind: "question",
      });
      expect(q).not.toContain("get_session_status");
      expect(q).not.toContain("answer_session_question");
    }
  });

  // An exit reason resolves to a label; an unknown one stays verbatim, because a raw code is
  // better than a blank left by a reason code some newer Agent added.
  it("resolves the exit reason label and keeps an unknown one verbatim", () => {
    const exit = (reason: string) =>
      reportText({
        content: "",
        notice_key: "chat.report.exit",
        notice_args: { display: "d", name: "s7" },
        report_kind: "exit",
        report_reason: reason,
      });
    expect(exit("oom")).toContain("OOM（メモリ不足で強制終了）");
    setLocale("en");
    expect(exit("oom")).toContain("OOM (killed — out of memory)");
    expect(exit("from-the-future")).toContain("from-the-future");
  });

  // A note appears exactly when its argument is present. Times travel as epoch millis and are
  // formatted in the Console's display locale; formatted server-side they would stay Japanese even
  // in an English Console.
  it("appends the notes their arguments call for", () => {
    const withNotes = reportText({
      content: "",
      notice_key: "chat.report.turn_failed",
      notice_args: {
        display: "d",
        name: "s7",
        resume_at: String(Date.UTC(2026, 7, 1, 12, 0)),
        fold_n: "2",
        fold_ats: "a / b",
      },
      report_kind: "answer-ready",
      report_reason: "turn-failed",
    });
    expect(withNotes).toContain("利用上限による停止です");
    expect(withNotes).toContain("指示 2 件ぶんの完了");
    expect(withNotes).toContain("a / b");

    const bare = reportText({
      content: "",
      notice_key: "chat.report.turn_failed",
      notice_args: { display: "d", name: "s7" },
      report_kind: "answer-ready",
      report_reason: "turn-failed",
    });
    expect(bare).not.toContain("利用上限");
    expect(bare).not.toContain("件ぶんの完了");
  });

  // Legacy records (pre-P6, no key) and keys the Console does not know fall back to content.
  it("falls back to the stored content for legacy and unknown reports", () => {
    expect(reportText({ content: "旧 report の本文" })).toBe("旧 report の本文");
    expect(
      reportText({ content: "旧 report の本文", notice_key: "chat.report.from_the_future" }),
    ).toBe("旧 report の本文");
  });
});
