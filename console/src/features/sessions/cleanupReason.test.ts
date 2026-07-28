import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { cleanupReasonText } from "./cleanupReason.ts";

describe("cleanupReasonText (ADR 0033)", () => {
  beforeEach(() => setLocale("ja"));
  afterEach(() => setLocale("ja"));

  it("renders the reason in the current locale, not the language the Agent sent", () => {
    const c = { reason_key: "clean.reason.wt_merged", reason: "マージ済み・クリーン（親に取り込み済み）" };
    expect(cleanupReasonText(c)).toBe("マージ済み・クリーン（親に取り込み済み）");
    setLocale("en");
    expect(cleanupReasonText(c)).toBe("Merged and clean (already in the parent)");
  });

  // 版ずれ: Console が知らないキー、またはキーを送らない古い Agent。空欄にはしない。
  it("falls back to the Agent's prose for an unknown or absent key", () => {
    setLocale("en");
    expect(cleanupReasonText({ reason_key: "clean.reason.future", reason: "未知の理由" })).toBe("未知の理由");
    expect(cleanupReasonText({ reason: "キーを送らない旧 Agent" })).toBe("キーを送らない旧 Agent");
  });
});
