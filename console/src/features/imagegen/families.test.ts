// The family cards (ADR 0081 decision 7, layer A) and the size list.
//
// The card is advice, so the only thing a test can hold is that it is the RIGHT card and
// that an unknown family gets none — an invented card would advise on a dialect nobody
// checked, which is worse than no card at all.
import { describe, expect, it } from "vitest";
import { FAMILY_CARDS, familyCard, parseSize, sizeOptions } from "./families.ts";

describe("族カードの選択", () => {
  it("族の数だけ一つずつ", () => {
    expect(FAMILY_CARDS.map((c) => c.id)).toEqual([
      "sd15",
      "sdxl",
      "sd35",
      "flux1",
      "flux2-klein",
      "zimage",
    ]);
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

  // 🔴 SD1.5 は 512 学習で、1024 を頼むと失敗ではなく被写体が二重になった絵が返る。
  // 既定の一覧が族別であることが、宣言を忘れた行を救う唯一の場所（comfyDefaultSizes と対）。
  it("SD1.5 の既定寸法は 512 系で、メガピクセルの一覧を含まない", () => {
    expect(sizeOptions(undefined, "sd15")).toEqual([
      "512x512",
      "512x768",
      "768x512",
      "640x512",
      "512x640",
    ]);
    expect(sizeOptions(undefined, "sd15")).not.toContain("1024x1024");
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
