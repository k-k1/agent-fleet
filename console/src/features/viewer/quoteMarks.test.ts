import { describe, expect, it } from "vitest";
import { indexOfNth, normalizeQuote, occurrenceOf } from "./quoteMarks.ts";

// アンカーは「引用文字列 + 出現番号」。同じ語が何度も出るプランで、付けた場所と違う場所が
// ハイライトされないことがこの2関数の責任なので、数え方を固定する。
describe("occurrenceOf / indexOfNth", () => {
  const text = "承認する。却下する。承認する。";

  it("先頭からの一致回数を数える（開始位置ちょうどの一致は数えない）", () => {
    expect(occurrenceOf(text, "承認", 0)).toBe(0);
    expect(occurrenceOf(text, "承認", text.lastIndexOf("承認"))).toBe(1);
    expect(occurrenceOf(text, "却下", text.indexOf("却下"))).toBe(0);
  });

  it("n 番目の一致位置を返し、無ければ -1", () => {
    expect(indexOfNth(text, "承認", 0)).toBe(text.indexOf("承認"));
    expect(indexOfNth(text, "承認", 1)).toBe(text.lastIndexOf("承認"));
    expect(indexOfNth(text, "承認", 2)).toBe(-1);
    expect(indexOfNth(text, "存在しない", 0)).toBe(-1);
  });

  it("空の引用はアンカーにならない", () => {
    expect(occurrenceOf(text, "", 5)).toBe(0);
    expect(indexOfNth(text, "", 0)).toBe(-1);
  });

  it("往復する: 数えた番号でその位置に戻れる", () => {
    const at = text.lastIndexOf("承認する");
    const nth = occurrenceOf(text, "承認する", at);
    expect(indexOfNth(text, "承認する", nth)).toBe(at);
  });
});

// 採取側（Selection.toString＝描画テキスト）と復元側（textContent＝生テキスト）で空白の形が
// 違うので、両方をここで畳んでから数える。畳むのは形だけで、文字は落とさない。
describe("normalizeQuote", () => {
  it("改行・タブ・連続空白を空白 1 個にし、前後は落とす", () => {
    expect(normalizeQuote("リソース\n一覧")).toBe("リソース 一覧");
    expect(normalizeQuote("one\n\ntwo")).toBe("one two");
    expect(normalizeQuote("  a \t b  ")).toBe("a b");
    expect(normalizeQuote("no\u00a0break")).toBe("no break"); // NBSP も普通の空白へ（両側とも同じ扱い）
  });

  it("空白だけの選択はアンカーにならない", () => {
    expect(normalizeQuote(" \n\t ")).toBe("");
  });

  it("冪等（保存済みの引用をもう一度畳んでも変わらない）", () => {
    const once = normalizeQuote("リソース\n一覧に裸の出現");
    expect(normalizeQuote(once)).toBe(once);
  });
});
