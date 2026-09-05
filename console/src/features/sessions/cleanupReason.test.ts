import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import { cleanupReasonText, cleanupReasonParts } from "./cleanupReason.ts";

describe("cleanupReasonText (ADR 0033)", () => {
  beforeEach(() => setLocale("ja"));
  afterEach(() => setLocale("ja"));

  it("renders the reason in the current locale, not the language the Agent sent", () => {
    const c = { reason_key: "clean.reason.wt_merged", reason: "マージ済み・クリーン（親に取り込み済み）" };
    expect(cleanupReasonText(c)).toBe("マージ済み・クリーン（親に取り込み済み）");
    setLocale("en");
    expect(cleanupReasonText(c)).toBe("Merged and clean (already in the parent)");
  });

  // Version skew: a key this Console does not know, or an old Agent that sends no key.
  // Never leave the cell blank.
  it("falls back to the Agent's prose for an unknown or absent key", () => {
    setLocale("en");
    expect(cleanupReasonText({ reason_key: "clean.reason.future", reason: "未知の理由" })).toBe("未知の理由");
    expect(cleanupReasonText({ reason: "キーを送らない旧 Agent" })).toBe("キーを送らない旧 Agent");
  });
});

describe("cleanupReasonParts (split into state badge + supporting note)", () => {
  beforeEach(() => setLocale("ja"));
  afterEach(() => setLocale("ja"));

  it("splits a known key into a locale-following badge and hint", () => {
    const c = { reason_key: "clean.reason.wt_merged", reason: "マージ済み・クリーン（親に取り込み済み）" };
    expect(cleanupReasonParts(c)).toEqual({ badge: "マージ済み", text: "クリーン・親に取り込み済み" });
    setLocale("en");
    expect(cleanupReasonParts(c)).toEqual({ badge: "Merged", text: "Clean; already in the parent" });
  });

  // Version skew (an Agent newer than the Console sends an unknown reason key): degrade to
  // the full sentence with no badge.
  it("degrades to the plain sentence when the badge catalog has no entry", () => {
    expect(cleanupReasonParts({ reason_key: "clean.reason.future", reason: "未知の理由" })).toEqual({
      text: "未知の理由",
    });
    expect(cleanupReasonParts({ reason: "キーを送らない旧 Agent" })).toEqual({ text: "キーを送らない旧 Agent" });
  });
});
