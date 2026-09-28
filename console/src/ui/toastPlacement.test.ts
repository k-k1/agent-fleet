import { describe, expect, it } from "vitest";
import { chromeBottom } from "./toastPlacement.ts";

const box = (top: number, bottom: number, left = 0, right = 390) => ({ top, bottom, left, right });

// The boxes are the ones measured at 390x844 (issue #1090, headless Chromium).
describe("chromeBottom", () => {
  it("follows the run of bars from the top: top bar, workspace row, pane header", () => {
    expect(chromeBottom([box(108, 144, 7, 383), box(0, 59), box(59, 101)], 390)).toBe(144);
  });

  it("stops before a second pane's header further down", () => {
    expect(chromeBottom([box(0, 59), box(59, 101), box(108, 144, 7, 383), box(476, 513, 7, 383)], 390)).toBe(144);
  });

  it("chains the studio's tab strip and the header of the session under it", () => {
    const boxes = [box(0, 59), box(59, 101), box(108, 145, 7, 383), box(145, 177, 7, 383), box(213, 249, 19, 371)];
    expect(chromeBottom(boxes, 390)).toBe(249);
  });

  it("starts from where the frame is while a keyboard shifts it down", () => {
    expect(chromeBottom([box(300, 359), box(359, 401), box(408, 444, 7, 383)], 390)).toBe(444);
  });

  it("skips hidden and off-screen boxes", () => {
    const hidden = box(0, 0, 0, 0);
    const rail = box(0, 400, -334, -8); // the closed rail, parked left of the screen
    expect(chromeBottom([hidden, rail, box(0, 59), box(59, 101)], 390)).toBe(101);
  });

  it("answers 0 with nothing to measure", () => {
    expect(chromeBottom([], 390)).toBe(0);
  });
});
