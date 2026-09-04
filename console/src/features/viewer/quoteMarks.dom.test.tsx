import { describe, expect, it } from "vitest";
import { anchorForRange, applyQuoteMarks, clearQuoteMarks } from "./quoteMarks.ts";

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

  it("改行をまたぐ引用も塗れる（保存済みの引用は畳んでから数える）", () => {
    const el = root("<p>全リソース\n一覧に裸の出現</p>");
    // 採取時に畳まれた形でも、古い生の形でも、同じ場所へ戻る。
    expect(applyQuoteMarks(el, [{ quote: "リソース 一覧に", nth: 0 }])).toEqual([true]);
    clearQuoteMarks(el);
    expect(applyQuoteMarks(el, [{ quote: "リソース\n一覧に", nth: 0 }])).toEqual([true]);
    expect(el.textContent).toBe("全リソース\n一覧に裸の出現");
  });

  it("箇条書きの項目をまたぐ引用は、隙間の空白ノードを塗らない", () => {
    const el = root("<ul><li>first bullet</li>\n<li>second bullet</li></ul>");
    expect(applyQuoteMarks(el, [{ quote: "bullet second", nth: 0 }])).toEqual([true]);
    expect([...el.querySelectorAll("mark.quote-mark")].map((m) => m.textContent)).toEqual([
      "bullet",
      "second",
    ]);
    expect(el.querySelector("ul > mark")).toBeNull(); // <ul> の直下に <mark> を作らない
  });
});

// ⚠️ ここが「選択したのにピッカーが出ない」の回帰テスト。Selection.toString() は描画テキスト
// （ソース改行は空白、<br>・段落・箇条書きの境界は改行）を返すので、実ブラウザの選択文字列を
// 手で渡して、生テキストとの差を吸収できているかを見る。jsdom の Selection は生テキストを
// 返してしまい、この差をそもそも作れない。
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

  it("段落内のソース改行をまたいでもアンカーになる（Chrome は空白 1 個で返す）", () => {
    const el = root("<p>の全リソース\n一覧に裸の出現</p>");
    const t = texts(el)[0];
    const r = rangeOver(el, [t, 2], [t, 10]);
    expect(anchorForRange(el, r, "リソース 一覧に")).toEqual({ quote: "リソース 一覧に", nth: 0 });
  });

  it("<br> をまたいでもアンカーになる（生テキストには区切りが無い）", () => {
    const el = root("<p>prompt line one<br>prompt line two</p>");
    const ns = texts(el);
    const r = rangeOver(el, [ns[0], 7], [ns[1], 13]);
    expect(anchorForRange(el, r, "line one\nprompt line")).toEqual({
      quote: "line one prompt line",
      nth: 0,
    });
  });

  it("段落をまたいでもアンカーになる（境界の改行は生テキストに無い）", () => {
    const el = root("<p>first para</p><p>second para</p>");
    const ns = texts(el);
    const r = rangeOver(el, [ns[0], 6], [ns[1], 6]);
    expect(anchorForRange(el, r, "para\n\nsecond")).toEqual({ quote: "para second", nth: 0 });
  });

  it("箇条書きの項目をまたいでもアンカーになる", () => {
    const el = root("<ul><li>first bullet</li><li>second bullet</li></ul>");
    const ns = texts(el);
    const r = rangeOver(el, [ns[0], 6], [ns[1], 6]);
    expect(anchorForRange(el, r, "bullet\nsecond")).toEqual({ quote: "bullet second", nth: 0 });
  });

  it("同じ語が何度も出るとき、選んだ場所の出現番号を返す", () => {
    const el = root("<p>承認する。却下する。\n承認する。</p>");
    const t = texts(el)[0];
    const r = rangeOver(el, [t, 11], [t, 13]);
    expect(anchorForRange(el, r, "承認")).toEqual({ quote: "承認", nth: 1 });
  });

  it("要素から始まる選択（ダブルクリック）でも、その段落の出現を指す", () => {
    const el = root("<p>承認する。</p><p>承認する。</p>");
    const second = el.querySelectorAll("p")[1];
    const r = rangeOver(el, [second, 0], [texts(second)[0], 2]);
    expect(anchorForRange(el, r, "承認")).toEqual({ quote: "承認", nth: 1 });
  });

  it("root の外へ出た選択と空の選択は null", () => {
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
