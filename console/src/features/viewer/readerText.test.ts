import { describe, it, expect } from "vitest";
import { splitSentences, toReadingParagraphs } from "./readerText.ts";

describe("splitSentences", () => {
  it("句点類で文に割る（区切りは末尾に残す）", () => {
    expect(splitSentences("これは一文目。これは二文目！おわり？")).toEqual([
      "これは一文目。",
      "これは二文目！",
      "おわり？",
    ]);
  });
  it("区切りが無ければ全体で1文", () => {
    expect(splitSentences("区切りのない行")).toEqual(["区切りのない行"]);
  });
  it("末尾の区切り無し断片も拾う", () => {
    expect(splitSentences("一文目。続き")).toEqual(["一文目。", "続き"]);
  });
});

describe("toReadingParagraphs (Markdown)", () => {
  it("見出し/リストの記法を落とし、空行で段落を分ける", () => {
    // 見出し・各リスト項目は（空行が無くても）1 段落に切り出す＝読み上げの自然な区切り。
    const md = "# タイトル\n\n本文の一文目。二文目。\n\n- 項目A\n- 項目B";
    expect(toReadingParagraphs(md, true)).toEqual([["タイトル"], ["本文の一文目。", "二文目。"], ["項目A"], ["項目B"]]);
  });

  it("コードフェンスの中身は読み飛ばす", () => {
    const md = "説明です。\n\n```js\nconst x = 1;\n```\n\n続きの段落。";
    expect(toReadingParagraphs(md, true)).toEqual([["説明です。"], ["続きの段落。"]]);
  });

  it("リンク/URL/インラインコードを落とす", () => {
    const md = "詳しくは [ドキュメント](https://x.example) と `code` を参照。";
    expect(toReadingParagraphs(md, true)).toEqual([["詳しくは ドキュメント と code を参照。"]]);
  });
});

describe("toReadingParagraphs (プレーンテキスト)", () => {
  it("txt は段落（空行区切り）と文に素直に割る", () => {
    const txt = "むかしむかし。あるところに。\n\nおじいさんがいました。";
    expect(toReadingParagraphs(txt, false)).toEqual([["むかしむかし。", "あるところに。"], ["おじいさんがいました。"]]);
  });
  it("空 = 段落なし", () => {
    expect(toReadingParagraphs("\n\n  \n", false)).toEqual([]);
  });
});
