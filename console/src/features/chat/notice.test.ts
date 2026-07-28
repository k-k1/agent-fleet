import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { noticeText } from "./notice.ts";

describe("noticeText (ADR 0033)", () => {
  beforeEach(() => setLocale("ja"));
  afterEach(() => setLocale("ja"));

  it("renders the notice in the current locale, not the stored language", () => {
    const m = {
      content: "（保存時の正本言語フォールバック）",
      notice_key: "chat.notice.ctx_pressure",
      notice_args: { pct: "80", tokens: "805k", window: "1000k" },
    };
    expect(noticeText(m)).toContain("上限の約80%（805k / 1000k トークン）");
    setLocale("en");
    expect(noticeText(m)).toContain("about 80% of its context limit (805k / 1000k tokens)");
  });

  it("keeps the LLM-written summary verbatim inside the compaction notice", () => {
    const summary = "- 決めたこと: A\n- 次にやること: B";
    const text = noticeText({ content: "", notice_key: "chat.notice.compact_auto", notice_args: { summary } });
    expect(text).toContain("自動で圧縮しました");
    expect(text).toContain(summary);
    expect(text).toContain("\n\n---\n\n"); // 要約の前の区切り線は残す
  });

  // 要約は LLM が書いた任意のテキスト。{...} を含んでいても二重展開されない
  // （interpolate は 1 パスなので、差し込んだ中身は再走査されない）。
  it("does not re-expand placeholders that appear inside the summary", () => {
    const text = noticeText({
      content: "",
      notice_key: "chat.notice.compact_manual",
      notice_args: { summary: "テンプレートに {summary} と {pct} を残す話" },
    });
    expect(text).toContain("テンプレートに {summary} と {pct} を残す話");
  });

  it("assembles the auto-pause notice and drops the sentence when nothing is pending", () => {
    const withPending = noticeText({
      content: "",
      notice_key: "chat.notice.auto_paused",
      notice_args: { limit: "5", pending: "2" },
    });
    expect(withPending).toContain("連続 5 回");
    expect(withPending).toContain("2 件残っています");

    const none = noticeText({
      content: "",
      notice_key: "chat.notice.auto_paused",
      notice_args: { limit: "5", pending: "0" },
    });
    expect(none).not.toContain("残っています");
    expect(none).toContain("続ける場合は");
  });

  it("pluralizes the pending-report count in English", () => {
    setLocale("en");
    const one = noticeText({
      content: "",
      notice_key: "chat.notice.auto_paused",
      notice_args: { limit: "5", pending: "1" },
    });
    expect(one).toContain("1 session report is still unprocessed");
    const many = noticeText({
      content: "",
      notice_key: "chat.notice.auto_paused",
      notice_args: { limit: "5", pending: "3" },
    });
    expect(many).toContain("3 session reports are still unprocessed");
  });

  // 旧レコード（キー無し）と、Console が知らないキーは content へ落ちる。空カードにしない。
  it("falls back to the stored content for legacy and unknown notices", () => {
    expect(noticeText({ content: "旧 notice の本文" })).toBe("旧 notice の本文");
    expect(noticeText({ content: "旧 notice の本文", notice_key: "chat.notice.from_the_future" })).toBe(
      "旧 notice の本文",
    );
  });
});
