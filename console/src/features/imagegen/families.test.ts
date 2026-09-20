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
      "anima",
      "krea2",
      "qwen-image-edit-2509",
    ]);
  });

  it("krea2 は蒸留版と非蒸留版の両端を出す（行が params でどちらかを宣言する）", () => {
    expect(familyCard("krea2")?.dialect).toBe("sentences");
    expect(familyCard("krea2")?.steps).toEqual([8, 52]);
    expect(familyCard("krea2")?.quality).toEqual([]);
  });

  it("anima は tags 方言で、推奨接頭辞を 2 つ持つ", () => {
    expect(familyCard("anima")?.dialect).toBe("tags");
    // Aesthetic 版は score_* を使わない、という model card の但し書きが chip 2 つの理由。
    expect(familyCard("anima")?.quality).toHaveLength(2);
    expect(familyCard("anima")?.cfg).toEqual([4, 5]);
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

  // ADR 0094: 指示編集は文章の方言で、sizes は空（出力寸法は入力画像のアスペクト比が決める）。
  it("qwen-image-edit-2509 は sentences 方言で sizes が空", () => {
    const card = familyCard("qwen-image-edit-2509");
    expect(card?.dialect).toBe("sentences");
    expect(card?.sizes).toEqual([]);
    expect(card?.trialSteps).toBe(8);
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

  // 🔴 ADR 0094 決定 4: sizes が空だと宣言している族は、行が候補を持っていても勝つ唯一の例外
  // ——出力寸法は入力画像のアスペクト比で決まり、候補を出しても効かない。
  it("qwen-image-edit-2509 は行の宣言があっても空のまま", () => {
    expect(sizeOptions(["1024x1024"], "qwen-image-edit-2509")).toEqual([]);
    expect(sizeOptions(undefined, "qwen-image-edit-2509")).toEqual([]);
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
