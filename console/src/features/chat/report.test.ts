import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { reportText } from "./report.ts";

// docs/log/28 P6: 報告カードは「事実」だけをカタログから描く。オペレーターへの行動指示
// （get_session_output で…）は Agent 側でプロンプトを組むときに生成されるので、
// ここに現れてはいけない — 現れたら「訳すと指示文まで変わる」元の状態に戻っている。
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

  // exit の理由はラベルに解決する。知らない理由は生値のまま（新しい Agent が増やした
  // 理由コードで空欄になるより、生でも見えている方がよい）。
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

  // 付記は引数の有無で出る。時刻は epoch millis で運び、Console 側で表示ロケールに整形する
  // （サーバ側で「1月2日 15:04」に整形してしまうと英語 Console でも日本語のまま出る）。
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

  // 旧レコード（P6 より前・キー無し）と Console が知らないキーは content へ落ちる。
  it("falls back to the stored content for legacy and unknown reports", () => {
    expect(reportText({ content: "旧 report の本文" })).toBe("旧 report の本文");
    expect(
      reportText({ content: "旧 report の本文", notice_key: "chat.report.from_the_future" }),
    ).toBe("旧 report の本文");
  });
});
