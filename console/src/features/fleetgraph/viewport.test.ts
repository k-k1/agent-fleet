// The fleet graph's window arithmetic (ADR 0096 decision 16). Every direction here has a
// test with a SIGN in it, because the one defect this module was extracted for — a wheel
// panning the wrong way — cannot be seen in the figure: the clamp holds the window against
// "now" and it just looks dead (docs/log/101 §101.11).
import { describe, expect, it } from "vitest";
import { clampWindow, MAX_SPAN_MS, MIN_SPAN_MS, panByPx, pinchSpanFactor, zoomAt } from "./viewport.ts";

const NOW = 1_000_000_000_000;
const HOUR = 3_600_000;
const win = (fromAgo: number, toAgo = 0) => ({ from: NOW - fromAgo, to: NOW - toAgo });

describe("clampWindow", () => {
  it("never lets the right edge pass now", () => {
    const w = clampWindow(NOW, NOW + 4 * HOUR, NOW);
    expect(w.to).toBe(NOW);
    expect(w.to - w.from).toBe(4 * HOUR); // the span is kept; the window slides back
  });

  it("holds the span inside its two limits", () => {
    expect(clampWindow(NOW - 60_000, NOW, NOW).to - clampWindow(NOW - 60_000, NOW, NOW).from).toBe(MIN_SPAN_MS);
    const huge = clampWindow(NOW - 400 * 24 * HOUR, NOW, NOW);
    expect(huge.to - huge.from).toBe(MAX_SPAN_MS);
  });

  it("leaves a window that is already legal alone", () => {
    expect(clampWindow(NOW - 4 * HOUR, NOW, NOW)).toEqual({ from: NOW - 4 * HOUR, to: NOW });
  });
});

describe("panByPx", () => {
  // The sign. A positive px moves the VIEWPORT right — later — which is the opposite of a
  // drag, where the finger carries the CONTENT. Getting this backwards is the defect.
  it("a positive px moves the window towards now", () => {
    const start = win(8 * HOUR, 4 * HOUR); // right edge 4h in the past, so there is room
    const moved = panByPx(start, 100, 1000, NOW);
    expect(moved.to).toBeGreaterThan(start.to);
    // 100px of 1000 across a 4-hour span is 2/5 of an hour.
    expect(moved.to - start.to).toBe((100 / 1000) * 4 * HOUR);
  });

  it("a negative px moves it into the past, with no floor", () => {
    const start = win(4 * HOUR);
    const moved = panByPx(start, -1000, 1000, NOW);
    expect(moved.to).toBe(start.to - 4 * HOUR);
    expect(moved.to - moved.from).toBe(4 * HOUR); // panning never changes the span
  });

  it("panning towards the future stops at now instead of opening a blank", () => {
    const start = win(4 * HOUR);
    const moved = panByPx(start, 5000, 1000, NOW);
    expect(moved).toEqual({ from: NOW - 4 * HOUR, to: NOW });
  });

  it("a zero-width canvas cannot divide by zero", () => {
    expect(() => panByPx(win(4 * HOUR), 10, 0, NOW)).not.toThrow();
  });
});

describe("zoomAt", () => {
  it("a spanFactor below 1 zooms in", () => {
    const z = zoomAt(win(4 * HOUR), 0.5, 1, NOW);
    expect(z.to - z.from).toBe(2 * HOUR);
  });

  it("keeps the instant under the anchor pixel where it was", () => {
    const start = win(4 * HOUR, 4 * HOUR); // [-8h, -4h]: away from the now clamp
    const fraction = 0.25;
    const anchorBefore = start.from + fraction * (start.to - start.from);
    const z = zoomAt(start, 0.5, fraction, NOW);
    const anchorAfter = z.from + fraction * (z.to - z.from);
    expect(anchorAfter).toBe(anchorBefore);
  });

  it("anchoring at the right edge is the old right-edge-fixed zoom", () => {
    const z = zoomAt(win(4 * HOUR), 0.5, 1, NOW);
    expect(z.to).toBe(NOW);
    expect(z.from).toBe(NOW - 2 * HOUR);
  });

  it("gives up the anchor rather than the clamp when zooming out near now", () => {
    // Anchored at the left edge, doubling the span would put the right edge 4h into the
    // future. The window slides back to now; the span is still doubled.
    const z = zoomAt(win(4 * HOUR), 2, 0, NOW);
    expect(z.to).toBe(NOW);
    expect(z.to - z.from).toBe(8 * HOUR);
  });

  it("cannot be zoomed past either limit", () => {
    const inTooFar = zoomAt(win(4 * HOUR), 0.0001, 0.5, NOW);
    expect(inTooFar.to - inTooFar.from).toBe(MIN_SPAN_MS);
    const outTooFar = zoomAt(win(4 * HOUR), 10_000, 0.5, NOW);
    expect(outTooFar.to - outTooFar.from).toBe(MAX_SPAN_MS);
  });

  it("clamps a fraction outside the canvas instead of anchoring outside the window", () => {
    expect(zoomAt(win(4 * HOUR), 0.5, 5, NOW)).toEqual(zoomAt(win(4 * HOUR), 0.5, 1, NOW));
    expect(zoomAt(win(4 * HOUR), 0.5, -5, NOW)).toEqual(zoomAt(win(4 * HOUR), 0.5, 0, NOW));
  });
});

describe("pinchSpanFactor", () => {
  it("spreading the fingers zooms IN, which is a smaller span", () => {
    expect(pinchSpanFactor(2)).toBe(0.5);
    const z = zoomAt(win(4 * HOUR), pinchSpanFactor(2), 0.5, NOW);
    expect(z.to - z.from).toBe(2 * HOUR);
  });

  it("pinching them together zooms out", () => {
    expect(pinchSpanFactor(0.5)).toBe(2);
  });

  it("a degenerate ratio is a no-op, not a division by zero", () => {
    expect(pinchSpanFactor(0)).toBe(1);
  });
});
