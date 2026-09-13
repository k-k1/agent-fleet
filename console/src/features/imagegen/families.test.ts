// The family cards (ADR 0081 decision 7, layer A) and the size list.
//
// The card is advice, so the only thing a test can hold is that it is the RIGHT card and
// that an unknown family gets none — an invented card would advise on a dialect nobody
// checked, which is worse than no card at all.
import { describe, expect, it } from "vitest";
import { FAMILY_CARDS, familyCard, parseSize, sizeOptions } from "./families.ts";

describe("族カードの選択", () => {
  it("五つの族に一つずつ", () => {
    expect(FAMILY_CARDS.map((c) => c.id)).toEqual(["sdxl", "sd35", "flux1", "flux2-klein", "zimage"]);
  });

  it("base_model で引ける（大小・空白は無視）", () => {
    expect(familyCard("sdxl")?.dialect).toBe("tags");
    expect(familyCard(" SDXL ")?.id).toBe("sdxl");
    expect(familyCard("flux2-klein")?.trialSteps).toBe(4);
  });

  it("知らない族・空は null（勝手に手引きを作らない）", () => {
    expect(familyCard("pixart")).toBeNull();
    expect(familyCard("")).toBeNull();
    expect(familyCard(undefined)).toBeNull();
  });

  it("cfg を読まない族には推奨範囲が無い", () => {
    expect(familyCard("flux1")?.cfg).toBeUndefined();
    expect(familyCard("flux2-klein")?.cfg).toBeUndefined();
    expect(familyCard("sdxl")?.cfg).toEqual([5, 9]);
  });
});

describe("大きさの選択肢", () => {
  it("行が持っていれば行のものが勝つ", () => {
    expect(sizeOptions(["512x512", "768x768"], "sdxl")).toEqual(["512x512", "768x768"]);
  });

  it("行が無い・壊れているときは族の既定に落ちる", () => {
    expect(sizeOptions(undefined, "sdxl")).toContain("1024x1024");
    expect(sizeOptions(["huge", ""], "sdxl")).toContain("1216x832");
  });

  it("族も分からなければ Agent の既定 5 つ", () => {
    expect(sizeOptions(undefined, undefined)).toHaveLength(5);
  });
});

describe("大きさの読み取り", () => {
  it("WxH だけを受ける", () => {
    expect(parseSize("1216x832")).toEqual([1216, 832]);
    expect(parseSize("1216 x 832")).toBeNull();
    expect(parseSize("0x512")).toBeNull();
    expect(parseSize(undefined)).toBeNull();
  });
});
