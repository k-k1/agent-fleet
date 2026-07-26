import { describe, it, expect } from "vitest";
import { KIND_STACK_ORDER, MAX_SLOTS, OTHER_KEY, hashKey, paintSeries, slotColor } from "./colors.ts";

const slotOf = (paints: ReturnType<typeof paintSeries>, key: string) => paints.find((p) => p.key === key)?.slot;

describe("paintSeries — 色は実体に付く（順位ではなく）", () => {
  it("feature は固定表。データの並び順が変わっても色は動かない", () => {
    const a = paintSeries("feature", ["session", "compact", "title.session"]);
    const b = paintSeries("feature", ["compact", "title.session", "session"]);
    for (const key of ["session", "compact", "title.session"]) {
      expect(slotOf(a, key)).toBe(slotOf(b, key));
    }
    expect(slotOf(a, "session")).toBe(1);
  });

  it("feature=unknown は必ず色を持つ（タグ付け忘れを見えなくしない）", () => {
    const p = paintSeries("feature", ["unknown"]);
    expect(slotOf(p, "unknown")).toBe(8);
    expect(p[0].folded).toBe(false);
  });

  it("固定表に無い feature はグレーのその他へ畳む（9色目を作らない）", () => {
    const p = paintSeries("feature", ["assistant.ask", "suggest.chat"]);
    expect(p.every((x) => x.folded && x.slot === 0)).toBe(true);
    expect(p[0].color).toBe("var(--viz-other)");
  });

  it("kind は --kind-* をそのまま使い、積み順だけを固定する", () => {
    const p = paintSeries("kind", ["claude", "codex", "cursor"]);
    // 出力は必ずスロット順 = KIND_STACK_ORDER 順（触れ合うペアが検証済みの隣接になる）
    expect(p.map((x) => x.key)).toEqual(["cursor", "claude", "codex"]);
    expect(p.map((x) => x.color)).toEqual(["var(--kind-cursor)", "var(--kind-claude)", "var(--kind-codex)"]);
    expect(KIND_STACK_ORDER.indexOf("claude")).toBeGreaterThanOrEqual(0);
  });

  it("未知の kind はその他（生成した色を割り当てない）", () => {
    const p = paintSeries("kind", ["nosuchagent"]);
    expect(p[0].slot).toBe(0);
    expect(p[0].color).toBe("var(--viz-other)");
  });

  it("列挙軸（trigger / origin）も固定表", () => {
    expect(slotOf(paintSeries("trigger", ["auto", "user"]), "user")).toBe(1);
    expect(slotOf(paintSeries("trigger", ["auto", "user"]), "auto")).toBe(2);
    expect(slotOf(paintSeries("origin", ["operator"]), "operator")).toBe(2);
  });
});

describe("paintSeries — 無限に増えうる軸（model / origin_conv）", () => {
  it("同じモデル名は常に同じスロット（他に何が居ても）", () => {
    const solo = paintSeries("model", ["claude-opus-4-8"]);
    const crowd = paintSeries("model", ["gpt-5.6-terra", "claude-opus-4-8", "claude-fable-5"]);
    expect(slotOf(crowd, "claude-opus-4-8")).toBe(slotOf(solo, "claude-opus-4-8"));
  });

  it("衝突しても同じスロットを2系列に割り当てない", () => {
    const keys = Array.from({ length: MAX_SLOTS }, (_, i) => `model-${i}`);
    const p = paintSeries("model", keys);
    const slots = p.filter((x) => !x.folded).map((x) => x.slot);
    expect(new Set(slots).size).toBe(slots.length);
  });

  it("8件を超えた分はその他へ畳む", () => {
    const keys = Array.from({ length: 12 }, (_, i) => `model-${i}`);
    const p = paintSeries("model", keys);
    expect(p.filter((x) => !x.folded)).toHaveLength(MAX_SLOTS);
    expect(p.filter((x) => x.folded)).toHaveLength(12 - MAX_SLOTS);
  });

  it("出力は常にスロット昇順・その他は最後（積み順＝パレット順）", () => {
    const p = paintSeries("model", Array.from({ length: 10 }, (_, i) => `m${i}`));
    const visible = p.filter((x) => !x.folded).map((x) => x.slot);
    expect(visible).toEqual([...visible].sort((a, b) => a - b));
    expect(p[p.length - 1].folded).toBe(true);
  });
});

describe("補助", () => {
  it("hashKey は決定的", () => {
    expect(hashKey("claude-opus-4-8")).toBe(hashKey("claude-opus-4-8"));
    expect(hashKey("a")).not.toBe(hashKey("b"));
  });
  it("slotColor(0) はその他のグレー", () => {
    expect(slotColor(0)).toBe("var(--viz-other)");
    expect(slotColor(3)).toBe("var(--viz-3)");
  });
  it("OTHER_KEY は実キーと衝突しない形", () => {
    expect(OTHER_KEY.startsWith("__")).toBe(true);
  });
});
