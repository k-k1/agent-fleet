import { describe, expect, it } from "vitest";
import { planFold, planPhoneFold, STEP_MORE, STEP_NONE, STEP_TIGHT, UNFOLD_SLACK, type FoldStep } from "./wsBarFold.ts";

// Widths the bar's content needs at each CSS step: ⋯ saves 300.
const widths = (base: number) => (s: FoldStep) => base - (s >= STEP_MORE ? 300 : 0);

describe("planFold", () => {
  it("folds nothing while everything fits", () => {
    expect(planFold(widths(800), 800, { folded: false, saving: 0 })).toEqual({ step: STEP_NONE, foldUsage: false });
  });

  it("takes the cheapest step that fits, in order", () => {
    expect(planFold(widths(800), 799, { folded: false, saving: 0 })).toEqual({ step: STEP_MORE, foldUsage: false });
    expect(planFold(widths(800), 500, { folded: false, saving: 0 })).toEqual({ step: STEP_MORE, foldUsage: false });
  });

  it("folds the usage chips only once ⋯ is not enough, and goes TIGHT only after that", () => {
    expect(planFold(widths(800), 499, { folded: false, saving: 0 })).toEqual({ step: STEP_MORE, foldUsage: true });
    // Folded, and the chips' saving still leaves it short: TIGHT.
    expect(planFold(widths(700), 399, { folded: true, saving: 100 })).toEqual({ step: STEP_TIGHT, foldUsage: true });
  });

  it("unfolds the usage chips only with the remembered saving plus slack to spare", () => {
    // Folded widths: 700/400; unfolded at MORE = 400 + 150.
    const m = widths(700);
    expect(planFold(m, 550 + UNFOLD_SLACK - 1, { folded: true, saving: 150 })).toEqual({ step: STEP_MORE, foldUsage: true });
    expect(planFold(m, 550 + UNFOLD_SLACK, { folded: true, saving: 150 })).toEqual({ step: STEP_MORE, foldUsage: false });
    // Plenty of room: everything comes back at once.
    expect(planFold(m, 2000, { folded: true, saving: 150 })).toEqual({ step: STEP_NONE, foldUsage: false });
  });

  it("keeps a fold that saved nothing (all chips pinned) instead of flapping", () => {
    // Unfolded it did not fit at MORE (400 > 399); folding saved 0, so it must not unfold.
    expect(planFold(widths(700), 399, { folded: true, saving: 0 })).toEqual({ step: STEP_TIGHT, foldUsage: true });
  });
});

describe("planPhoneFold", () => {
  // The pane group is ~84px: folding it is the only step there is.
  const phone = (base: number) => (s: 0 | 1) => base - (s >= 1 ? 84 : 0);

  it("folds nothing while the bar fits", () => {
    expect(planPhoneFold(phone(390), 390)).toBe(0);
  });

  it("folds the pane group once the bar does not fit", () => {
    expect(planPhoneFold(phone(400), 390)).toBe(1);
  });

  it("ends at that step when nothing fits, never past it", () => {
    expect(planPhoneFold(phone(900), 320)).toBe(1);
  });
});
