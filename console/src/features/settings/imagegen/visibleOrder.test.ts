import { describe, it, expect } from "vitest";
import { mergeVisibleOrder } from "./visibleOrder.ts";

describe("mergeVisibleOrder", () => {
  it("keeps a hidden provider before the visible ones in its slot", () => {
    expect(mergeVisibleOrder(["agy", "image", "codex"], ["image", "codex"], ["codex", "image"])).toEqual([
      "agy",
      "codex",
      "image",
    ]);
  });

  it("keeps a hidden provider between the visible ones in its slot", () => {
    expect(mergeVisibleOrder(["image", "agy", "codex"], ["image", "codex"], ["codex", "image"])).toEqual([
      "codex",
      "agy",
      "image",
    ]);
  });

  // Allowed again, the hidden provider still ranks where the member put it: the stored list read
  // without the policy is the member's order with only the visible swap applied.
  it("leaves the hidden provider's rank intact for when the policy allows it again", () => {
    const stored = ["agy", "image", "codex"];
    const merged = mergeVisibleOrder(stored, ["image", "codex"], ["codex", "image"]);
    expect(merged.indexOf("agy")).toBe(stored.indexOf("agy"));
  });

  it("places a visible id the stored list does not name where it was shown", () => {
    expect(mergeVisibleOrder(["agy", "codex"], ["image", "codex"], ["codex", "image"])).toEqual([
      "codex",
      "agy",
      "image",
    ]);
  });

  it("keeps a legacy alias in place", () => {
    expect(mergeVisibleOrder(["comfy", "codex", "agy"], ["codex", "agy"], ["agy", "codex"])).toEqual([
      "comfy",
      "agy",
      "codex",
    ]);
  });

  it("writes nothing new for an answer that is not a permutation of what was shown", () => {
    expect(mergeVisibleOrder(["agy", "codex"], ["codex"], ["agy"])).toEqual(["agy", "codex"]);
  });
});
