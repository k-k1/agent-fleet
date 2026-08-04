import { describe, it, expect } from "vitest";
import { chronoInsertIndex } from "./handoffPlacement.ts";

const AT = Date.parse("2026-08-04T20:45:00Z");
const t = (s: string) => `2026-08-04T${s}Z`;

describe("chronoInsertIndex", () => {
  it("puts a fresh proposal last (nothing newer yet)", () => {
    expect(chronoInsertIndex([t("20:40:00"), t("20:44:00")], AT)).toBe(2);
  });

  it("puts the card before the turns that came after it — the whole point: a later\n     message renders BELOW the card, so the landing position is the conversation", () => {
    expect(chronoInsertIndex([t("20:40:00"), t("20:46:00"), t("20:47:00")], AT)).toBe(1);
  });

  it("treats a stamp-less group (optimistic echo) as newer", () => {
    expect(chronoInsertIndex([t("20:40:00"), undefined], AT)).toBe(1);
    expect(chronoInsertIndex([""], AT)).toBe(0);
  });

  it("skips an unparseable stamp instead of anchoring on it", () => {
    expect(chronoInsertIndex(["not-a-date", t("20:46:00")], AT)).toBe(1);
    expect(chronoInsertIndex(["not-a-date"], AT)).toBe(1);
  });

  it("falls back to last when the proposal has no usable time", () => {
    expect(chronoInsertIndex([t("20:46:00")], Number.NaN)).toBe(1);
  });

  it("handles an empty transcript", () => {
    expect(chronoInsertIndex([], AT)).toBe(0);
  });
});
