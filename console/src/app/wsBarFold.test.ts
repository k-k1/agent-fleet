import { describe, expect, it } from "vitest";
import { planFold, planPhoneFold, STEP_LABELS, STEP_MORE, STEP_NONE, STEP_TIGHT, UNFOLD_SLACK, type FoldStep } from "./wsBarFold.ts";

// Widths the bar's content needs at each CSS step: labels save 200, ⋯ saves 300 more.
const widths = (base: number) => (s: FoldStep) => base - (s >= STEP_LABELS ? 200 : 0) - (s >= STEP_MORE ? 300 : 0);

describe("planFold", () => {
  it("folds nothing while everything fits", () => {
    expect(planFold(widths(1000), 1000, { folded: false, saving: 0 })).toEqual({ step: STEP_NONE, foldUsage: false });
  });

  it("takes the cheapest step that fits, in order", () => {
    expect(planFold(widths(1000), 999, { folded: false, saving: 0 })).toEqual({ step: STEP_LABELS, foldUsage: false });
    expect(planFold(widths(1000), 800, { folded: false, saving: 0 })).toEqual({ step: STEP_LABELS, foldUsage: false });
    expect(planFold(widths(1000), 799, { folded: false, saving: 0 })).toEqual({ step: STEP_MORE, foldUsage: false });
  });

  it("folds the usage chips only once ⋯ is not enough, and goes TIGHT only after that", () => {
    expect(planFold(widths(1000), 499, { folded: false, saving: 0 })).toEqual({ step: STEP_MORE, foldUsage: true });
    // Folded, and the chips' saving still leaves it short: TIGHT.
    expect(planFold(widths(900), 399, { folded: true, saving: 100 })).toEqual({ step: STEP_TIGHT, foldUsage: true });
  });

  it("unfolds the usage chips only with the remembered saving plus slack to spare", () => {
    // Folded widths: 900/700/400; unfolded at MORE = 400 + 150.
    const m = widths(900);
    expect(planFold(m, 550 + UNFOLD_SLACK - 1, { folded: true, saving: 150 })).toEqual({ step: STEP_MORE, foldUsage: true });
    expect(planFold(m, 550 + UNFOLD_SLACK, { folded: true, saving: 150 })).toEqual({ step: STEP_MORE, foldUsage: false });
    // Plenty of room: everything comes back at once.
    expect(planFold(m, 2000, { folded: true, saving: 150 })).toEqual({ step: STEP_NONE, foldUsage: false });
  });

  it("keeps a fold that saved nothing (all chips pinned) instead of flapping", () => {
    // Unfolded it did not fit at MORE (400 > 399); folding saved 0, so it must not unfold.
    expect(planFold(widths(900), 399, { folded: true, saving: 0 })).toEqual({ step: STEP_TIGHT, foldUsage: true });
  });
});

describe("planPhoneFold", () => {
  // Pane buttons are ~42px each: step 1 frees two of them, step 2 two more.
  const phone = (base: number) => (s: 0 | 1 | 2) => base - (s >= 1 ? 84 : 0) - (s >= 2 ? 84 : 0);

  it("folds nothing while the bar fits", () => {
    expect(planPhoneFold(phone(390), 390)).toBe(0);
  });

  it("takes the first step that fits, in order", () => {
    expect(planPhoneFold(phone(400), 390)).toBe(1);
    expect(planPhoneFold(phone(480), 390)).toBe(2);
  });

  it("ends at the last step when nothing fits, never past it", () => {
    expect(planPhoneFold(phone(900), 320)).toBe(2);
  });
});
