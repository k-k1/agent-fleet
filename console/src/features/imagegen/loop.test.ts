import { describe, expect, it } from "vitest";
import { pressBlocked, promptSummary, unseen } from "./loop.ts";

describe("promptSummary", () => {
  it("folds the prompt to one line and cuts it with an ellipsis", () => {
    expect(promptSummary("a cat\n  on a   mat ")).toBe("a cat on a mat");
    expect(promptSummary("x".repeat(100), 10)).toBe("x".repeat(9) + "…");
    expect(promptSummary("   \n ")).toBe("");
  });
});

describe("pressBlocked (the form's rule for trial and enqueue)", () => {
  const gate = { busy: false, trialFull: false, queueFull: false };
  const gen = { op: "generate", mask: "" };
  it("allows both when nothing stands in the way", () => {
    expect(pressBlocked(gen, gate)).toEqual({ trial: false, enqueue: false });
  });
  it("busy (a press in flight, or the engine unavailable) blocks both", () => {
    expect(pressBlocked(gen, { ...gate, busy: true })).toEqual({ trial: true, enqueue: true });
  });
  it("a full trial slot blocks only the trial, a full queue only the enqueue", () => {
    expect(pressBlocked(gen, { ...gate, trialFull: true })).toEqual({ trial: true, enqueue: false });
    expect(pressBlocked(gen, { ...gate, queueFull: true })).toEqual({ trial: false, enqueue: true });
  });
  it("inpaint without a mask blocks both, with one it does not", () => {
    expect(pressBlocked({ op: "inpaint", mask: " " }, gate)).toEqual({ trial: true, enqueue: true });
    expect(pressBlocked({ op: "inpaint", mask: "m.png" }, gate)).toEqual({ trial: false, enqueue: false });
  });
});

describe("unseen", () => {
  it("keeps the new paths in order", () => {
    expect(unseen(["c", "b", "a"], new Set(["a"]))).toEqual(["c", "b"]);
  });
});
