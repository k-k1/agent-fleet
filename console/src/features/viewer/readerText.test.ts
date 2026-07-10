import { describe, it, expect } from "vitest";
import { parseRuby, buildReadUnits, type ReadUnit } from "./readerText.ts";

// 単位の表示テキストを平坦化（ルビは [base:ruby] で表現）してアサートしやすくする。
const disp = (u: ReadUnit) => u.segs.map((s) => (s.ruby !== undefined ? `[${s.base}:${s.ruby}]` : s.base)).join("");

describe("parseRuby (なろう形式ルビ)", () => {
  it("｜親文字《ルビ》 を base+ruby に割る", () => {
    expect(parseRuby("｜東京《とうきょう》へ")).toEqual([{ base: "東京", ruby: "とうきょう" }, { base: "へ" }]);
  });

  it("｜省略の自動ルビは直前の漢字連続にだけ付く（手前のかなは素のまま）", () => {
    expect(parseRuby("私は東京《とうきょう》へ")).toEqual([
      { base: "私は" },
      { base: "東京", ruby: "とうきょう" },
      { base: "へ" },
    ]);
  });

  it("自動ルビ: 漢字連続の切り出し", () => {
    expect(parseRuby("あの日暮里《にっぽり》駅")).toEqual([
      { base: "あの" },
      { base: "日暮里", ruby: "にっぽり" },
      { base: "駅" },
    ]);
  });

  it("直前に漢字が無い《》は素の文字として扱う", () => {
    expect(parseRuby("これは《見出し》です")).toEqual([{ base: "これは《見出し》です" }]);
  });

  it("半角 | はルビ制御にしない（Markdown 表と衝突回避）", () => {
    expect(parseRuby("| a | b |")).toEqual([{ base: "| a | b |" }]);
  });
});

describe("buildReadUnits (原文忠実＋文/行区切り)", () => {
  it("改行・行頭スペースを表示に保持し、読み上げ文は trim/整形する", () => {
    const units = buildReadUnits("　吾輩は猫である。名前はまだ無い。\n次の行。", false);
    expect(units.map(disp)).toEqual(["　吾輩は猫である。", "名前はまだ無い。\n", "次の行。"]);
    expect(units.map((u) => u.spoken)).toEqual(["吾輩は猫である。", "名前はまだ無い。", "次の行。"]);
  });

  it("ルビは表示に残し、読み上げは読みを採用する", () => {
    const units = buildReadUnits("｜東京《とうきょう》タワー。", false);
    expect(units).toHaveLength(1);
    expect(units[0].segs).toEqual([{ base: "東京", ruby: "とうきょう" }, { base: "タワー。" }]);
    expect(units[0].spoken).toBe("とうきょうタワー。");
  });

  it("空行は表示に残す（改行として保持）が読み上げ対象にしない", () => {
    const units = buildReadUnits("一行目。\n\n三行目。", false);
    expect(units.map(disp).join("")).toBe("一行目。\n\n三行目。"); // 空行の改行を保持
    expect(units.map((u) => u.spoken).filter((s) => s)).toEqual(["一行目。", "三行目。"]);
  });

  it("Markdown のコードフェンス内は表示するが読み上げない", () => {
    const units = buildReadUnits("説明。\n```\ncode();\n```\n続き。", true);
    expect(units.map((u) => u.spoken).filter((s) => s)).toEqual(["説明。", "続き。"]);
    expect(units.map(disp).join("")).toContain("code();"); // 表示には残る
  });
});
