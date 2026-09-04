import { describe, expect, it } from "vitest";
import { indexOfNth, normalizeQuote, occurrenceOf } from "./quoteMarks.ts";

// An anchor is "quote string + occurrence number". Keeping the highlight off a passage other
// than the one it was placed on, in a plan where the same word recurs, is the responsibility
// of these two functions, so the counting is pinned here.
describe("occurrenceOf / indexOfNth", () => {
  const text = "承認する。却下する。承認する。";

  it("counts matches from the start, excluding a match beginning exactly at start", () => {
    expect(occurrenceOf(text, "承認", 0)).toBe(0);
    expect(occurrenceOf(text, "承認", text.lastIndexOf("承認"))).toBe(1);
    expect(occurrenceOf(text, "却下", text.indexOf("却下"))).toBe(0);
  });

  it("returns the position of the nth match, or -1 when there is none", () => {
    expect(indexOfNth(text, "承認", 0)).toBe(text.indexOf("承認"));
    expect(indexOfNth(text, "承認", 1)).toBe(text.lastIndexOf("承認"));
    expect(indexOfNth(text, "承認", 2)).toBe(-1);
    expect(indexOfNth(text, "存在しない", 0)).toBe(-1);
  });

  it("an empty quote is never an anchor", () => {
    expect(occurrenceOf(text, "", 5)).toBe(0);
    expect(indexOfNth(text, "", 0)).toBe(-1);
  });

  it("round-trips: the counted occurrence number resolves back to that position", () => {
    const at = text.lastIndexOf("承認する");
    const nth = occurrenceOf(text, "承認する", at);
    expect(indexOfNth(text, "承認する", nth)).toBe(at);
  });
});

// The capture side (Selection.toString, rendered text) and the restore side (textContent, raw
// text) differ in the shape of their whitespace, so both are collapsed here before counting.
// Only the shape is collapsed; no character is dropped.
describe("normalizeQuote", () => {
  it("collapses newlines, tabs and whitespace runs to one space and trims the ends", () => {
    expect(normalizeQuote("リソース\n一覧")).toBe("リソース 一覧");
    expect(normalizeQuote("one\n\ntwo")).toBe("one two");
    expect(normalizeQuote("  a \t b  ")).toBe("a b");
    expect(normalizeQuote("no\u00a0break")).toBe("no break"); // NBSP becomes a plain space, on both sides alike
  });

  it("a whitespace-only selection is never an anchor", () => {
    expect(normalizeQuote(" \n\t ")).toBe("");
  });

  it("is idempotent: collapsing a stored quote again changes nothing", () => {
    const once = normalizeQuote("リソース\n一覧に裸の出現");
    expect(normalizeQuote(once)).toBe(once);
  });
});
