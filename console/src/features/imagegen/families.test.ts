// The family facts as read off the Agent's model row (ADR 0100 decision 7) and the size list.
//
// The card is advice, so the only thing a test can hold is that it reads what the Agent said
// and that nothing is invented when it said nothing.
import { describe, expect, it } from "vitest";
import { familyFacts, parseSize, sizeOptions } from "./families.ts";

describe("族の事実（ADR 0100 決定 7: Agent の表から読むだけ）", () => {
  it("模型行の 4 欄と試走 steps をそのまま読む", () => {
    expect(
      familyFacts({
        id: "m",
        dialect: "tags",
        quality_prefixes: ["masterpiece, best quality", ""],
        steps_range: [20, 40],
        cfg_range: [5, 9],
        trial_steps: 10,
      }),
    ).toEqual({ dialect: "tags", quality: ["masterpiece, best quality"], steps: [20, 40], cfg: [5, 9], trialSteps: 10 });
  });

  it("cfg を読まない族は cfg の範囲を持たない", () => {
    expect(familyFacts({ id: "m", dialect: "sentences", steps_range: [16, 32] })?.cfg).toBeUndefined();
  });

  // 勝手に手引きを作らない: 族の名前だけでは何も言わない（Console に第 2 の表は無い）。
  it("Agent が何も言わなければ null（族の名前だけでは作らない）", () => {
    expect(familyFacts({ id: "m", family: "sdxl" })).toBeNull();
    expect(familyFacts(null)).toBeNull();
  });

  it("壊れた範囲は捨てる", () => {
    expect(familyFacts({ id: "m", dialect: "tags", steps_range: [1] as unknown as [number, number] })?.steps).toBeUndefined();
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
  it.each(["qwen-image-edit-2509", "qwen-image-edit-2511"])("%s は行の宣言があっても空のまま", (id) => {
    expect(sizeOptions(["1024x1024"], id)).toEqual([]);
    expect(sizeOptions(undefined, id)).toEqual([]);
  });
});

describe("qwen-image-2.1 の既定寸法", () => {
  // ADR 0098: 指示編集の族でありながら生成もするので寸法を持つ。空にすると生成側の選択肢が消える。
  it("1024 級の 5 つ＋その倍の辺の 5 つ", () => {
    const s = sizeOptions(undefined, "qwen-image-2.1");
    expect(s).toHaveLength(10);
    expect(s[0]).toBe("1024x1024");
    expect(s).toContain("2048x2048");
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
