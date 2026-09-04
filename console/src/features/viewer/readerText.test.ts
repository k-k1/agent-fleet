import { describe, it, expect } from "vitest";
import { parseRuby, buildReadUnits, readPreGaps, type ReadUnit } from "./readerText.ts";

// Flattens a unit's display text (ruby written as [base:ruby]) to make assertions easier.
const disp = (u: ReadUnit) => u.segs.map((s) => (s.ruby !== undefined ? `[${s.base}:${s.ruby}]` : s.base)).join("");

describe("parseRuby (Narou-style ruby)", () => {
  it("splits ｜base《ruby》 into base + ruby", () => {
    expect(parseRuby("｜東京《とうきょう》へ")).toEqual([{ base: "東京", ruby: "とうきょう" }, { base: "へ" }]);
  });

  it("auto ruby without ｜ attaches only to the preceding kanji run, leaving earlier kana plain", () => {
    expect(parseRuby("私は東京《とうきょう》へ")).toEqual([
      { base: "私は" },
      { base: "東京", ruby: "とうきょう" },
      { base: "へ" },
    ]);
  });

  it("auto ruby: extracting the kanji run", () => {
    expect(parseRuby("あの日暮里《にっぽり》駅")).toEqual([
      { base: "あの" },
      { base: "日暮里", ruby: "にっぽり" },
      { base: "駅" },
    ]);
  });

  it("treats 《》 with no preceding kanji as plain characters", () => {
    expect(parseRuby("これは《見出し》です")).toEqual([{ base: "これは《見出し》です" }]);
  });

  it("does not treat a halfwidth | as ruby control, avoiding a clash with Markdown tables", () => {
    expect(parseRuby("| a | b |")).toEqual([{ base: "| a | b |" }]);
  });
});

describe("buildReadUnits (faithful to the source, split by sentence and line)", () => {
  it("keeps newlines and leading spaces in the display and trims the spoken text", () => {
    const units = buildReadUnits("　吾輩は猫である。名前はまだ無い。\n次の行。", false);
    expect(units.map(disp)).toEqual(["　吾輩は猫である。", "名前はまだ無い。\n", "次の行。"]);
    expect(units.map((u) => u.spoken)).toEqual(["吾輩は猫である。", "名前はまだ無い。", "次の行。"]);
  });

  it("keeps ruby in the display and speaks the reading", () => {
    const units = buildReadUnits("｜東京《とうきょう》タワー。", false);
    expect(units).toHaveLength(1);
    expect(units[0].segs).toEqual([{ base: "東京", ruby: "とうきょう" }, { base: "タワー。" }]);
    expect(units[0].spoken).toBe("とうきょうタワー。");
  });

  it("ruby=false (a non-ja locale) disables ruby parsing and treats 《》｜ as plain characters", () => {
    const units = buildReadUnits("｜東京《とうきょう》タワー。", false, undefined, false);
    expect(units).toHaveLength(1);
    // No ruby segment ({base, ruby}) is produced; the source text lands in base as it is.
    expect(units[0].segs.every((s) => s.ruby === undefined)).toBe(true);
    expect(units[0].segs.map((s) => s.base).join("")).toBe("｜東京《とうきょう》タワー。");
  });

  it("keeps a blank line in the display as a newline but never speaks it", () => {
    const units = buildReadUnits("一行目。\n\n三行目。", false);
    expect(units.map(disp).join("")).toBe("一行目。\n\n三行目。"); // the blank line's newline is preserved
    expect(units.map((u) => u.spoken).filter((s) => s)).toEqual(["一行目。", "三行目。"]);
  });

  it("keeps emphasis-dot ruby (・) as dots in the display and speaks the base characters", () => {
    const units = buildReadUnits("｜イ《・》｜カ《・》", false);
    expect(units).toHaveLength(1);
    expect(units[0].segs).toEqual([{ base: "イ", ruby: "・" }, { base: "カ", ruby: "・" }]);
    expect(units[0].spoken).toBe("イカ"); // the original characters, not the dots
  });

  it("shows a symbol-only separator line (＊ and the like) but does not speak it", () => {
    const units = buildReadUnits("前の場面。\n＊\n＊＊＊\n次の場面。", false);
    expect(units.map((u) => u.spoken).filter((s) => s)).toEqual(["前の場面。", "次の場面。"]);
    expect(units.map(disp).join("")).toContain("＊＊＊"); // still present in the display
  });

  it("shows the inside of a Markdown code fence but does not speak it", () => {
    const units = buildReadUnits("説明。\n```\ncode();\n```\n続き。", true);
    expect(units.map((u) => u.spoken).filter((s) => s)).toEqual(["説明。", "続き。"]);
    expect(units.map(disp).join("")).toContain("code();"); // still present in the display
  });
});

describe("readPreGaps (pre-beat: pause > paragraph > sentence end > hard wrap)", () => {
  const B = 0.3; // blockBeat
  const S = 0.15; // sentBeat
  const D = 0.6; // tameBeat

  it("gives a sentence boundary within a line the short beat, and a line break after a finished sentence the full beat", () => {
    const units = buildReadUnits("一文目。二文目。\n次の行。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, S, B]);
  });

  it("inserts no gap at a newline mid-sentence (hard-wrapped prose)", () => {
    const units = buildReadUnits("この文は途中で\n折り返されている。次の文。", false);
    // unit 1 -> unit 2 is only a wrap, so no gap; unit 2 -> unit 3 crosses a sentence end.
    expect(readPreGaps(units, B, S, D)).toEqual([0, 0, S]);
  });

  it("gives a marker line (a list item and the like) the full beat however the previous sentence ended", () => {
    const units = buildReadUnits("説明\n- 項目A。\n- 項目B。", true);
    expect(readPreGaps(units, B, S, D)).toEqual([0, B, B]);
  });

  it("counts a sentence ending in a closing corner bracket as a sentence end", () => {
    const units = buildReadUnits("「終わりです。」\n次の段落。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, B]);
  });

  it("gives the first unit no pre-beat", () => {
    const units = buildReadUnits("最初の文。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0]);
  });

  it("gives a line starting with a pause dash (―― and the like) tameBeat, longer than a marker line", () => {
    const units = buildReadUnits("何する？\n――また、行く。\n――行ってる。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, D, D]);
    // the marker itself stays in the display but drops out of the spoken text
    expect(units[1].spoken).toBe("また、行く。");
  });

  it("also gives a line starting with an ellipsis (…… and the like) tameBeat", () => {
    const units = buildReadUnits("何する？\n……一日中、って。", false);
    expect(readPreGaps(units, B, S, D)).toEqual([0, D]);
    expect(units[1].spoken).toBe("一日中、って。");
  });
});
