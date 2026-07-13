import { describe, it, expect } from "vitest";
import { parseRuby, buildReadUnits, readPreGaps, type ReadUnit } from "./readerText.ts";

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

  it("傍点ルビ（・）は表示は点のまま、読み上げは親文字を読む", () => {
    const units = buildReadUnits("｜イ《・》｜カ《・》", false);
    expect(units).toHaveLength(1);
    expect(units[0].segs).toEqual([{ base: "イ", ruby: "・" }, { base: "カ", ruby: "・" }]);
    expect(units[0].spoken).toBe("イカ"); // 点ではなく元の文字
  });

  it("記号だけの区切り行（＊ 等）は表示するが読み上げない", () => {
    const units = buildReadUnits("前の場面。\n＊\n＊＊＊\n次の場面。", false);
    expect(units.map((u) => u.spoken).filter((s) => s)).toEqual(["前の場面。", "次の場面。"]);
    expect(units.map(disp).join("")).toContain("＊＊＊"); // 表示には残る
  });

  it("Markdown のコードフェンス内は表示するが読み上げない", () => {
    const units = buildReadUnits("説明。\n```\ncode();\n```\n続き。", true);
    expect(units.map((u) => u.spoken).filter((s) => s)).toEqual(["説明。", "続き。"]);
    expect(units.map(disp).join("")).toContain("code();"); // 表示には残る
  });
});

describe("readPreGaps (前拍: 溜め > 段落 > 句点 > ハードラップ)", () => {
  const B = 0.3; // blockBeat
  const S = 0.15; // sentBeat
  const D = 0.6; // tameBeat

  it("行内の文境界（。区切り）は短い一拍、行の切れ目（文が終わった後の改行）は一拍", () => {
    const units = buildReadUnits("一文目。二文目。\n次の行。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, S, B]);
  });

  it("文の途中の改行（ハードラップされた散文）には間を入れない", () => {
    const units = buildReadUnits("この文は途中で\n折り返されている。次の文。", false);
    // 「この文は途中で」→（ラップ）→「折り返されている。」→（句点）→「次の文。」
    expect(readPreGaps(units, B, S, D)).toEqual([0, 0, S]);
  });

  it("マーカー行（リスト等）は前の文の終わり方に依らず一拍", () => {
    const units = buildReadUnits("説明\n- 項目A。\n- 項目B。", true);
    expect(readPreGaps(units, B, S, D)).toEqual([0, B, B]);
  });

  it("閉じ鉤括弧で終わる文も「文の終わり」とみなす", () => {
    const units = buildReadUnits("「終わりです。」\n次の段落。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, B]);
  });

  it("先頭の単位に前拍は付けない", () => {
    const units = buildReadUnits("最初の文。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0]);
  });

  it("溜め（――等）で始まる行は tameBeat（マーカー行より長い前拍）", () => {
    const units = buildReadUnits("何する？\n――また、行く。\n――行ってる。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, D, D]);
    // マーカー自体は表示に残るが読み上げからは落ちる
    expect(units[1].spoken).toBe("また、行く。");
  });

  it("三点リーダ（……等）で始まる行も tameBeat", () => {
    const units = buildReadUnits("何する？\n……一日中、って。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, D]);
    expect(units[1].spoken).toBe("一日中、って。");
  });
});
