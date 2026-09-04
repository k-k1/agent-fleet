import { describe, expect, it } from "vitest";
import { anchorForRange, applyQuoteMarks, clearQuoteMarks } from "./quoteMarks.ts";

// The side that lays quote highlights over a rendered plan. Markdown is rendered through
// innerHTML, so a target can span elements (the middle of emphasis, a selection across
// paragraphs).
const root = (html: string) => {
  const el = document.createElement("div");
  el.className = "markdown";
  el.innerHTML = html;
  return el;
};

describe("applyQuoteMarks", () => {
  it("wraps only the nth occurrence of a word, so aiming at the first never lights the second", () => {
    const el = root("<p>承認する。却下する。承認する。</p>");
    const found = applyQuoteMarks(el, [{ quote: "承認", nth: 1 }]);
    expect(found).toEqual([true]);
    const marks = el.querySelectorAll("mark.quote-mark");
    expect(marks).toHaveLength(1);
    expect(el.textContent).toBe("承認する。却下する。承認する。"); // the body is unchanged
    // The second occurrence is the one wrapped: the quote appears once more before the mark
    const before = el.innerHTML.slice(0, el.innerHTML.indexOf("<mark"));
    expect(before).toContain("承認する。却下する。");
  });

  it("wraps a quote that spans elements by cutting it per text node", () => {
    const el = root("<p>これは<strong>大事な</strong>方針です</p>");
    expect(applyQuoteMarks(el, [{ quote: "は大事な方", nth: 0 }])).toEqual([true]);
    expect(el.querySelectorAll("mark.quote-mark").length).toBeGreaterThan(1);
    expect(el.textContent).toBe("これは大事な方針です");
    expect(el.querySelector("strong")).not.toBeNull(); // the structure is not damaged
  });

  it("gives multiple comments the same numbers as the list", () => {
    const el = root("<p>alpha beta gamma</p>");
    applyQuoteMarks(el, [
      { quote: "alpha", nth: 0 },
      { quote: "gamma", nth: 0 },
    ]);
    const ns = [...el.querySelectorAll<HTMLElement>("mark.quote-mark")].map((m) => m.dataset.n);
    expect(new Set(ns)).toEqual(new Set(["1", "2"]));
  });

  it("reports a quote a revision removed as not found, rather than misfiring elsewhere", () => {
    const el = root("<p>新しい本文</p>");
    expect(applyQuoteMarks(el, [{ quote: "古い記述", nth: 0 }])).toEqual([false]);
    expect(el.querySelectorAll("mark.quote-mark")).toHaveLength(0);
  });

  it("re-applying is idempotent: nothing gets wrapped twice", () => {
    const el = root("<p>承認する。承認する。</p>");
    applyQuoteMarks(el, [{ quote: "承認", nth: 0 }]);
    applyQuoteMarks(el, [{ quote: "承認", nth: 0 }]);
    expect(el.querySelectorAll("mark.quote-mark")).toHaveLength(1);
    expect(el.textContent).toBe("承認する。承認する。");
  });

  it("restores the original text when removed, so the next count is not shifted", () => {
    const el = root("<p>承認する。承認する。</p>");
    applyQuoteMarks(el, [{ quote: "承認", nth: 1 }]);
    clearQuoteMarks(el);
    expect(el.querySelectorAll("mark.quote-mark")).toHaveLength(0);
    expect(el.innerHTML).toBe("<p>承認する。承認する。</p>");
  });

  it("paints a quote across a newline, since a stored quote is collapsed before counting", () => {
    const el = root("<p>全リソース\n一覧に裸の出現</p>");
    // Both the collapsed form captured now and the older raw form resolve to the same place.
    expect(applyQuoteMarks(el, [{ quote: "リソース 一覧に", nth: 0 }])).toEqual([true]);
    clearQuoteMarks(el);
    expect(applyQuoteMarks(el, [{ quote: "リソース\n一覧に", nth: 0 }])).toEqual([true]);
    expect(el.textContent).toBe("全リソース\n一覧に裸の出現");
  });

  it("does not paint the whitespace nodes between list items a quote spans", () => {
    const el = root("<ul><li>first bullet</li>\n<li>second bullet</li></ul>");
    expect(applyQuoteMarks(el, [{ quote: "bullet second", nth: 0 }])).toEqual([true]);
    expect([...el.querySelectorAll("mark.quote-mark")].map((m) => m.textContent)).toEqual([
      "bullet",
      "second",
    ]);
    expect(el.querySelector("ul > mark")).toBeNull(); // never create a <mark> directly under a <ul>
  });
});

// Regression guard for "the picker never appears after selecting". Selection.toString()
// returns rendered text (a source newline becomes a space; the boundaries of <br>, paragraphs
// and list items become newlines), so the selection strings a real browser would produce are
// passed in by hand to check that the difference from raw text is absorbed. jsdom's Selection
// returns raw text and cannot produce that difference at all.
describe("anchorForRange", () => {
  const rangeOver = (el: HTMLElement, from: [Node, number], to: [Node, number]) => {
    const r = el.ownerDocument.createRange();
    r.setStart(from[0], from[1]);
    r.setEnd(to[0], to[1]);
    return r;
  };
  const texts = (el: Node): Text[] => {
    const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    const out: Text[] = [];
    for (let n = w.nextNode(); n; n = w.nextNode()) out.push(n as Text);
    return out;
  };

  it("anchors across a source newline inside a paragraph (Chrome returns one space)", () => {
    const el = root("<p>の全リソース\n一覧に裸の出現</p>");
    const t = texts(el)[0];
    const r = rangeOver(el, [t, 2], [t, 10]);
    expect(anchorForRange(el, r, "リソース 一覧に")).toEqual({ quote: "リソース 一覧に", nth: 0 });
  });

  it("anchors across a <br>, where the raw text has no separator", () => {
    const el = root("<p>prompt line one<br>prompt line two</p>");
    const ns = texts(el);
    const r = rangeOver(el, [ns[0], 7], [ns[1], 13]);
    expect(anchorForRange(el, r, "line one\nprompt line")).toEqual({
      quote: "line one prompt line",
      nth: 0,
    });
  });

  it("anchors across paragraphs, whose boundary newline is absent from the raw text", () => {
    const el = root("<p>first para</p><p>second para</p>");
    const ns = texts(el);
    const r = rangeOver(el, [ns[0], 6], [ns[1], 6]);
    expect(anchorForRange(el, r, "para\n\nsecond")).toEqual({ quote: "para second", nth: 0 });
  });

  it("anchors across list items", () => {
    const el = root("<ul><li>first bullet</li><li>second bullet</li></ul>");
    const ns = texts(el);
    const r = rangeOver(el, [ns[0], 6], [ns[1], 6]);
    expect(anchorForRange(el, r, "bullet\nsecond")).toEqual({ quote: "bullet second", nth: 0 });
  });

  it("returns the occurrence number of the selected place when the word recurs", () => {
    const el = root("<p>承認する。却下する。\n承認する。</p>");
    const t = texts(el)[0];
    const r = rangeOver(el, [t, 11], [t, 13]);
    expect(anchorForRange(el, r, "承認")).toEqual({ quote: "承認", nth: 1 });
  });

  it("points at that paragraph's occurrence even when the selection starts at an element (a double click)", () => {
    const el = root("<p>承認する。</p><p>承認する。</p>");
    const second = el.querySelectorAll("p")[1];
    const r = rangeOver(el, [second, 0], [texts(second)[0], 2]);
    expect(anchorForRange(el, r, "承認")).toEqual({ quote: "承認", nth: 1 });
  });

  it("returns null for a selection leaving root and for an empty selection", () => {
    const el = root("<p>本文</p>");
    const outside = document.createElement("p");
    outside.textContent = "外";
    document.body.append(el, outside);
    const r = rangeOver(el, [texts(el)[0], 0], [texts(outside)[0], 1]);
    expect(anchorForRange(el, r, "本文外")).toBeNull();
    const inner = rangeOver(el, [texts(el)[0], 0], [texts(el)[0], 2]);
    expect(anchorForRange(el, inner, "  \n ")).toBeNull();
    el.remove();
    outside.remove();
  });
});
