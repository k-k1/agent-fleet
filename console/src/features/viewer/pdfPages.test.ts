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
    // 横向きが 1 枚混ざっただけでページごとに倍率が変われば、読んでいる最中に
    // 段組みが飛ぶ。合わせる先は常にいちばん広いページ。
    const mixed = [A4, LANDSCAPE, A4];
    expect(fitScale(mixed, 942, 12)).toBeCloseTo(918 / 842, 6);
  });

  it("stays at 1 before the container has been measured", () => {
    expect(fitScale(sixA4, 0, 12)).toBe(1);
    expect(fitScale([], 900, 12)).toBe(1);
    // 余白のほうが広い（極端に細いペイン）ときも倍率を壊さない
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
    // 先読みは画面の 0.5 倍。上端にいるなら 1 ページ目とその次まで。
    expect(visibleRange(layout, 0, 800, 0.5)).toEqual({ start: 0, end: 2 });
  });

  it("follows the scroll position, and pre-renders the page above", () => {
    // 先読みは上下の両方向。上へ戻したときに白いページが出ないよう、画面の上端より
    // 手前のページも範囲に入る（4 ページ目の頭にいるなら 3 ページ目から）。
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
    // 上端で決めると、ページの境目が画面上端に来た瞬間に、まだ数ピクセルしか
    // 見えていない次ページへ番号が飛ぶ。
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
    // 同じページの、同じ割合の位置に戻る（拡大したぶん絶対値は動く）。
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
    // 4 倍に拡大した A4 を Retina で持つと 3000 万画素になり、タブごと落ちる。
    const ratio = canvasPixelRatio(595 * 4, 842 * 4, 3);
    expect(ratio).toBeLessThan(3);
    expect(595 * 4 * 842 * 4 * ratio * ratio).toBeLessThanOrEqual(MAX_CANVAS_PIXELS + 1);
  });
});
