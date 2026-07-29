import { describe, expect, it } from "vitest";
import { applyQuoteMarks, clearQuoteMarks } from "./quoteMarks.ts";

// 描画済みプランの上に引用ハイライトを被せる側。Markdown は innerHTML で描かれるので、
// 対象は要素をまたぐこともある（強調の途中、段落をまたぐ選択）。
const root = (html: string) => {
  const el = document.createElement("div");
  el.className = "markdown";
  el.innerHTML = html;
  return el;
};

describe("applyQuoteMarks", () => {
  it("同じ語の n 番目だけを囲む（1つめを狙って2つめが光らない）", () => {
    const el = root("<p>承認する。却下する。承認する。</p>");
    const found = applyQuoteMarks(el, [{ quote: "承認", nth: 1 }]);
    expect(found).toEqual([true]);
    const marks = el.querySelectorAll("mark.quote-mark");
    expect(marks).toHaveLength(1);
    expect(el.textContent).toBe("承認する。却下する。承認する。"); // 本文は変わらない
    // 2つめの「承認」が囲まれている = マークより前に「承認」がもう1度出ている
    const before = el.innerHTML.slice(0, el.innerHTML.indexOf("<mark"));
    expect(before).toContain("承認する。却下する。");
  });

  it("要素をまたぐ引用も、テキストノードごとに切って囲む", () => {
    const el = root("<p>これは<strong>大事な</strong>方針です</p>");
    expect(applyQuoteMarks(el, [{ quote: "は大事な方", nth: 0 }])).toEqual([true]);
    expect(el.querySelectorAll("mark.quote-mark").length).toBeGreaterThan(1);
    expect(el.textContent).toBe("これは大事な方針です");
    expect(el.querySelector("strong")).not.toBeNull(); // 構造は壊さない
  });

  it("複数のコメントには一覧と同じ番号が入る", () => {
    const el = root("<p>alpha beta gamma</p>");
    applyQuoteMarks(el, [
      { quote: "alpha", nth: 0 },
      { quote: "gamma", nth: 0 },
    ]);
    const ns = [...el.querySelectorAll<HTMLElement>("mark.quote-mark")].map((m) => m.dataset.n);
    expect(new Set(ns)).toEqual(new Set(["1", "2"]));
  });

  it("改訂で本文から消えた引用は「見つからなかった」と返るだけ（誤爆しない）", () => {
    const el = root("<p>新しい本文</p>");
    expect(applyQuoteMarks(el, [{ quote: "古い記述", nth: 0 }])).toEqual([false]);
    expect(el.querySelectorAll("mark.quote-mark")).toHaveLength(0);
  });

  it("貼り直しは冪等 — 二重に囲まれない", () => {
    const el = root("<p>承認する。承認する。</p>");
    applyQuoteMarks(el, [{ quote: "承認", nth: 0 }]);
    applyQuoteMarks(el, [{ quote: "承認", nth: 0 }]);
    expect(el.querySelectorAll("mark.quote-mark")).toHaveLength(1);
    expect(el.textContent).toBe("承認する。承認する。");
  });

  it("剥がすと元のテキストに戻る（次に数えるときズレない）", () => {
    const el = root("<p>承認する。承認する。</p>");
    applyQuoteMarks(el, [{ quote: "承認", nth: 1 }]);
    clearQuoteMarks(el);
    expect(el.querySelectorAll("mark.quote-mark")).toHaveLength(0);
    expect(el.innerHTML).toBe("<p>承認する。承認する。</p>");
  });
});
