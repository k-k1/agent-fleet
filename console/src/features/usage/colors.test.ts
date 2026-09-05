import { describe, it, expect } from "vitest";
import { KIND_STACK_ORDER, MAX_SLOTS, OTHER_KEY, hashKey, paintSeries, slotColor } from "./colors.ts";

const slotOf = (paints: ReturnType<typeof paintSeries>, key: string) => paints.find((p) => p.key === key)?.slot;

describe("paintSeries: a colour belongs to the entity, not to its rank", () => {
  it("feature uses a fixed table, so reordering the data does not move a colour", () => {
    const a = paintSeries("feature", ["session", "compact", "title.session"]);
    const b = paintSeries("feature", ["compact", "title.session", "session"]);
    for (const key of ["session", "compact", "title.session"]) {
      expect(slotOf(a, key)).toBe(slotOf(b, key));
    }
    expect(slotOf(a, "session")).toBe(1);
  });

  it("feature=unknown always keeps a colour, so a missing tag stays visible", () => {
    const p = paintSeries("feature", ["unknown"]);
    expect(slotOf(p, "unknown")).toBe(8);
    expect(p[0].folded).toBe(false);
  });

  it("folds a feature missing from the fixed table into grey other, never a ninth colour", () => {
    const p = paintSeries("feature", ["assistant.ask", "suggest.chat"]);
    expect(p.every((x) => x.folded && x.slot === 0)).toBe(true);
    expect(p[0].color).toBe("var(--viz-other)");
  });

  it("kind uses --kind-* as is and pins only the stacking order", () => {
    const p = paintSeries("kind", ["claude", "codex", "cursor"]);
    // Output is always in slot order = KIND_STACK_ORDER order, so touching pairs are the
    // validated adjacent ones.
    expect(p.map((x) => x.key)).toEqual(["cursor", "claude", "codex"]);
    expect(p.map((x) => x.color)).toEqual(["var(--kind-cursor)", "var(--kind-claude)", "var(--kind-codex)"]);
    expect(KIND_STACK_ORDER.indexOf("claude")).toBeGreaterThanOrEqual(0);
  });

  it("puts an unknown kind in other rather than assigning a generated colour", () => {
    const p = paintSeries("kind", ["nosuchagent"]);
    expect(p[0].slot).toBe(0);
    expect(p[0].color).toBe("var(--viz-other)");
  });

  it("uses a fixed table for the enumerated axes too (trigger / origin)", () => {
    expect(slotOf(paintSeries("trigger", ["auto", "user"]), "user")).toBe(1);
    expect(slotOf(paintSeries("trigger", ["auto", "user"]), "auto")).toBe(2);
    expect(slotOf(paintSeries("origin", ["operator"]), "operator")).toBe(2);
  });
});

describe("paintSeries: unbounded axes (model / origin_conv)", () => {
  it("gives the same model name the same slot whatever else is present", () => {
    const solo = paintSeries("model", ["claude-opus-4-8"]);
    const crowd = paintSeries("model", ["gpt-5.6-terra", "claude-opus-4-8", "claude-fable-5"]);
    expect(slotOf(crowd, "claude-opus-4-8")).toBe(slotOf(solo, "claude-opus-4-8"));
  });

  it("never assigns one slot to two series, even on a hash collision", () => {
    const keys = Array.from({ length: MAX_SLOTS }, (_, i) => `model-${i}`);
    const p = paintSeries("model", keys);
    const slots = p.filter((x) => !x.folded).map((x) => x.slot);
    expect(new Set(slots).size).toBe(slots.length);
  });

  it("folds everything past the 8th into other", () => {
    const keys = Array.from({ length: 12 }, (_, i) => `model-${i}`);
    const p = paintSeries("model", keys);
    expect(p.filter((x) => !x.folded)).toHaveLength(MAX_SLOTS);
    expect(p.filter((x) => x.folded)).toHaveLength(12 - MAX_SLOTS);
  });

  it("always returns ascending slots with other last, so stacking order is palette order", () => {
    const p = paintSeries("model", Array.from({ length: 10 }, (_, i) => `m${i}`));
    const visible = p.filter((x) => !x.folded).map((x) => x.slot);
    expect(visible).toEqual([...visible].sort((a, b) => a - b));
    expect(p[p.length - 1].folded).toBe(true);
  });
});

describe("helpers", () => {
  it("hashKey is deterministic", () => {
    expect(hashKey("claude-opus-4-8")).toBe(hashKey("claude-opus-4-8"));
    expect(hashKey("a")).not.toBe(hashKey("b"));
  });
  it("slotColor(0) is the grey of other", () => {
    expect(slotColor(0)).toBe("var(--viz-other)");
    expect(slotColor(3)).toBe("var(--viz-3)");
  });
  it("shapes OTHER_KEY so it cannot collide with a real key", () => {
    expect(OTHER_KEY.startsWith("__")).toBe(true);
  });
});
