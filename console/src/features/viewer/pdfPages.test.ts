import { describe, expect, it } from "vitest";
import {
  anchorOf,
  canvasPixelRatio,
  currentPage,
  fitScale,
  layoutPages,
  MAX_CANVAS_PIXELS,
  PAGE_GAP,
  scrollTopForAnchor,
  stepZoom,
  visibleRange,
  type PageSize,
} from "./pdfPages.ts";

const A4: PageSize = { w: 595, h: 842 };
const LANDSCAPE: PageSize = { w: 842, h: 595 };
const sixA4 = Array.from({ length: 6 }, () => A4);

describe("fitScale", () => {
  it("fits the widest page, not the first one", () => {
    // If a single landscape page made the zoom differ per page, the column would jump
    // mid-read. Always fit to the widest page.
    const mixed = [A4, LANDSCAPE, A4];
    expect(fitScale(mixed, 942, 12)).toBeCloseTo(918 / 842, 6);
  });

  it("stays at 1 before the container has been measured", () => {
    expect(fitScale(sixA4, 0, 12)).toBe(1);
    expect(fitScale([], 900, 12)).toBe(1);
    // Padding wider than the pane (an extremely narrow pane) must not break the zoom
    expect(fitScale(sixA4, 20, 12)).toBe(1);
  });
});

describe("layoutPages", () => {
  it("stacks pages with a gap and reports the total height", () => {
    const layout = layoutPages([A4, A4], 1);
    expect(layout.tops).toEqual([0, 842 + PAGE_GAP]);
    expect(layout.total).toBe(842 * 2 + PAGE_GAP);
  });

  it("scales every page and keeps at least one pixel", () => {
    const layout = layoutPages([A4], 0.5);
    expect(layout.sizes[0]).toEqual({ w: 298, h: 421 });
    expect(layoutPages([{ w: 0.2, h: 0.2 }], 0.1).sizes[0]).toEqual({ w: 1, h: 1 });
  });
});

describe("visibleRange", () => {
  const layout = layoutPages(sixA4, 1);

  it("returns only the pages near the viewport", () => {
    // Overscan is 0.5 viewports, so at the top that is page 1 and the one after it.
    expect(visibleRange(layout, 0, 800, 0.5)).toEqual({ start: 0, end: 2 });
  });

  it("follows the scroll position, and pre-renders the page above", () => {
    // Overscan runs both ways: the page above the viewport top is in range too, so
    // scrolling back up never shows a blank page (at the head of page 4, start at page 3).
    const range = visibleRange(layout, (842 + PAGE_GAP) * 3, 800, 0.5);
    expect(range.start).toBe(2);
    expect(range.end).toBeGreaterThan(3);
  });

  it("is empty before the pane has a height", () => {
    expect(visibleRange(layout, 0, 0)).toEqual({ start: 0, end: 0 });
    expect(visibleRange(layoutPages([], 1), 0, 800)).toEqual({ start: 0, end: 0 });
  });
});

describe("currentPage", () => {
  const layout = layoutPages(sixA4, 1);

  it("names the page under the middle of the pane, not under its top edge", () => {
    // Deciding by the top edge would jump the number to the next page the moment a page
    // boundary reaches the viewport top, with only a few pixels of it visible.
    const boundary = 842 + PAGE_GAP;
    expect(currentPage(layout, boundary - 10, 800)).toBe(2);
    expect(currentPage(layout, 0, 800)).toBe(1);
  });

  it("is 1 for an empty document", () => {
    expect(currentPage(layoutPages([], 1), 0, 800)).toBe(1);
  });
});

describe("scroll anchor", () => {
  it("keeps the reading position across a zoom change", () => {
    const before = layoutPages(sixA4, 1);
    const scrollTop = before.tops[3] + 400;
    const anchor = anchorOf(before, scrollTop);
    expect(anchor.page).toBe(3);

    const after = layoutPages(sixA4, 2);
    const restored = scrollTopForAnchor(after, anchor);
    // Back to the same fraction of the same page; the absolute value moves with the zoom.
    expect(currentPage(after, restored, 800)).toBe(4);
    expect(restored - after.tops[3]).toBeCloseTo(anchor.fraction * (after.sizes[3].h + PAGE_GAP), 0);
  });

  it("clamps an anchor that points past a shorter document", () => {
    const short = layoutPages([A4], 1);
    expect(scrollTopForAnchor(short, { page: 9, fraction: 0.5 })).toBeGreaterThanOrEqual(0);
    expect(scrollTopForAnchor(layoutPages([], 1), { page: 0, fraction: 0 })).toBe(0);
  });
});

describe("stepZoom", () => {
  it("moves one step and stops at the ends", () => {
    expect(stepZoom(1, 1)).toBe(1.25);
    expect(stepZoom(1, -1)).toBe(0.75);
    expect(stepZoom(4, 1)).toBe(4);
    expect(stepZoom(0.5, -1)).toBe(0.5);
  });

  it("lands on the next step from an off-grid zoom (wheel)", () => {
    expect(stepZoom(1.1, 1)).toBe(1.25);
    expect(stepZoom(1.1, -1)).toBe(1);
  });
});

describe("canvasPixelRatio", () => {
  it("honours the display density for an ordinary page", () => {
    expect(canvasPixelRatio(595, 842, 2)).toBe(2);
    expect(canvasPixelRatio(595, 842, 1)).toBe(1);
  });

  it("caps the bitmap of a hugely zoomed page", () => {
    // An A4 page at 4x zoom on a Retina display is 30 million pixels and takes the tab
    // down with it.
    const ratio = canvasPixelRatio(595 * 4, 842 * 4, 3);
    expect(ratio).toBeLessThan(3);
    expect(595 * 4 * 842 * 4 * ratio * ratio).toBeLessThanOrEqual(MAX_CANVAS_PIXELS + 1);
  });
});
