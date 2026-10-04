import { describe, expect, it } from "vitest";
import {
  aspectDiffers,
  brushPx,
  brushTooThin,
  drawOp,
  exportSize,
  hasTransparency,
  judgeMask,
  MASK_BRUSH,
  MAX_CANVAS_AREA,
  maskFileName,
  clampView,
  FIT,
  fitWidth,
  MAX_ZOOM,
  redToGrey,
  replayOps,
  zoomAbout,
  toCanvas,
  toNormalized,
  type MaskCtx,
  type Size,
} from "./maskGeometry.ts";

// What the browser reports as naturalWidth/Height for a JPEG stored at `stored` with an EXIF
// Orientation — and what ComfyUI's exif_transpose produces (log 111 §10.4): 5..8 swap the sides.
const oriented = (stored: Size, orientation: number): Size =>
  orientation >= 5 ? { w: stored.h, h: stored.w } : stored;

describe("orientation", () => {
  const stored = { w: 4032, h: 3024 };
  for (const o of [1, 6, 8]) {
    it(`orientation ${o}: the export takes the oriented size and a top-quarter stroke stays on top`, () => {
      const natural = oriented(stored, o);
      const out = exportSize(natural);
      expect(out).toEqual(natural);
      // Painted on the picture as shown: a point a quarter of the way down the displayed box.
      const shown = { left: 10, top: 20, width: natural.w / 8, height: natural.h / 8 };
      const p = toNormalized(shown.left + shown.width / 2, shown.top + shown.height / 4, shown);
      const px = toCanvas(p, out);
      expect(px.y).toBeCloseTo(out.h / 4, 6);
      expect(px.x).toBeCloseTo(out.w / 2, 6);
    });
    it(`orientation ${o}: a display that ignored the orientation is caught`, () => {
      const natural = oriented(stored, o);
      // `image-orientation: none` would lay the stored pixels out as they are in the file.
      expect(aspectDiffers(natural, stored)).toBe(o >= 5);
      expect(aspectDiffers(natural, { w: natural.w / 3, h: natural.h / 3 })).toBe(false);
    });
  }
});

describe("exportSize", () => {
  const ratios: [number, number][] = [
    [1, 4],
    [9, 16],
    [3, 4],
    [1, 1],
    [4, 3],
    [16, 9],
    [3, 1],
    [4, 1],
  ];
  it("keeps the native size at or under the cap", () => {
    expect(exportSize({ w: 1820, h: 1024 })).toEqual({ w: 1820, h: 1024 });
    expect(exportSize({ w: 4096, h: 4096 })).toEqual({ w: 4096, h: 4096 });
    expect(exportSize({ w: 752, h: 1400 })).toEqual({ w: 752, h: 1400 });
  });
  for (const [a, b] of ratios) {
    it(`${a}:${b} above the cap shrinks under it with the aspect kept within half a pixel`, () => {
      const k = Math.ceil(Math.sqrt((MAX_CANVAS_AREA * 1.7) / (a * b))) + 1;
      const natural = { w: a * k, h: b * k };
      expect(natural.w * natural.h).toBeGreaterThan(MAX_CANVAS_AREA);
      const out = exportSize(natural);
      expect(out.w * out.h).toBeLessThanOrEqual(MAX_CANVAS_AREA);
      // Not shrunk further than it has to be: within 1% of the cap's area.
      expect(out.w * out.h).toBeGreaterThan(MAX_CANVAS_AREA * 0.99);
      // The short side measured against the exact ratio of the long one.
      const wide = out.w >= out.h;
      const exactShort = wide ? (out.w * b) / a : (out.h * a) / b;
      expect(Math.abs((wide ? out.h : out.w) - exactShort)).toBeLessThanOrEqual(0.5);
    });
  }
  it("a 24 MP photo (5712×4284) shrinks; a 12 MP one does not", () => {
    const big = exportSize({ w: 5712, h: 4284 });
    expect(big.w * big.h).toBeLessThanOrEqual(MAX_CANVAS_AREA);
    expect(Math.abs(big.h - (big.w * 4284) / 5712)).toBeLessThanOrEqual(0.5);
    expect(exportSize({ w: 4032, h: 3024 })).toEqual({ w: 4032, h: 3024 });
  });
  it("honours a smaller cap", () => {
    const out = exportSize({ w: 400, h: 100 }, 100 * 25);
    expect(out).toEqual({ w: 100, h: 25 });
  });
});

describe("brush", () => {
  it("is a ratio of the long side, in either orientation", () => {
    expect(brushPx(MASK_BRUSH.medium, { w: 1184, h: 888 })).toBeCloseTo(35.52, 6);
    expect(brushPx(MASK_BRUSH.medium, { w: 888, h: 1184 })).toBeCloseTo(35.52, 6);
    // The same stroke is the same share of the picture on the display and on the export.
    const display = brushPx(MASK_BRUSH.thick, { w: 600, h: 450 }) / 600;
    const exported = brushPx(MASK_BRUSH.thick, { w: 4032, h: 3024 }) / 4032;
    expect(display).toBeCloseTo(exported, 9);
  });
  it("warns on the thin width only", () => {
    expect(brushTooThin(MASK_BRUSH.thin)).toBe(true);
    expect(brushTooThin(MASK_BRUSH.medium)).toBe(false);
    expect(brushTooThin(MASK_BRUSH.thick)).toBe(false);
  });
});

const rgba = (size: Size, red: (i: number) => number, alpha = 255): Uint8ClampedArray => {
  const a = new Uint8ClampedArray(size.w * size.h * 4);
  for (let i = 0; i < size.w * size.h; i++) {
    a[i * 4] = red(i);
    a[i * 4 + 3] = alpha;
  }
  return a;
};

describe("judgeMask", () => {
  const size = { w: 200, h: 100 };
  it("all black is blank", () => {
    expect(judgeMask(rgba(size, () => 0), size)).toBe("blank");
  });
  it("only anti-aliased edges and eraser rims left is still blank", () => {
    expect(judgeMask(rgba(size, (i) => (i % 3 === 0 ? 127 : 3)), size)).toBe("blank");
  });
  it("one thin-brush dab is a mask", () => {
    // Thin on a 200-wide canvas = 3 px wide; a dab is ~7 px. Paint 10.
    expect(judgeMask(rgba(size, (i) => (i < 10 ? 255 : 0)), size)).toBe("ok");
    expect(judgeMask(rgba(size, (i) => (i < 3 ? 255 : 0)), size)).toBe("blank");
  });
  it("nearly everything painted is full", () => {
    expect(judgeMask(rgba(size, () => 200), size)).toBe("full");
    expect(judgeMask(rgba(size, (i) => (i < size.w * size.h * 0.5 ? 255 : 0)), size)).toBe("ok");
  });
});

describe("pixels", () => {
  it("hasTransparency finds a single non-opaque pixel", () => {
    const a = rgba({ w: 4, h: 4 }, () => 255);
    expect(hasTransparency(a)).toBe(false);
    a[7 * 4 + 3] = 254;
    expect(hasTransparency(a)).toBe(true);
  });
  it("redToGrey copies red to G and B and makes every pixel opaque", () => {
    const a = new Uint8ClampedArray([200, 10, 20, 0, 0, 255, 255, 128]);
    redToGrey(a);
    expect([...a]).toEqual([200, 200, 200, 255, 0, 0, 0, 255]);
  });
});

describe("toNormalized", () => {
  it("clamps a pointer outside the picture to its edge", () => {
    const r = { left: 100, top: 50, width: 200, height: 100 };
    expect(toNormalized(50, 0, r)).toEqual({ x: 0, y: 0 });
    expect(toNormalized(400, 500, r)).toEqual({ x: 1, y: 1 });
    expect(toNormalized(150, 75, r)).toEqual({ x: 0.25, y: 0.25 });
  });
});

describe("maskFileName", () => {
  it("carries the time and a random tail, so the same millisecond still gives two names", () => {
    const at = new Date(2026, 9, 4, 9, 5, 7, 42);
    expect(maskFileName(at, () => 0)).toBe("mask-20261004-090507-042-0000.png");
    expect(maskFileName(at, () => 0.5)).not.toBe(maskFileName(at, () => 0.25));
    expect(maskFileName(at)).toMatch(/^mask-20261004-090507-042-[0-9a-z]{4}\.png$/);
  });
});

describe("aspectDiffers", () => {
  it("flags 1% and more, not less", () => {
    expect(aspectDiffers({ w: 101, h: 100 }, { w: 100, h: 100 })).toBe(true);
    expect(aspectDiffers({ w: 1009, h: 1000 }, { w: 100, h: 100 })).toBe(false);
  });
});

describe("replayOps", () => {
  it("draws the base first and again at a reset, so a reset drops what came before it", () => {
    const { ctx, calls } = recorder();
    const base = () => calls.push("BASE");
    replayOps(
      ctx,
      [
        { kind: "paint", width: 0.05, points: [{ x: 0.1, y: 0.1 }] },
        { kind: "reset" },
        { kind: "invert" },
      ],
      { w: 100, h: 100 },
      base,
    );
    expect(calls.filter((c) => c === "BASE" || c.startsWith("arc") || c.startsWith("fillRect"))).toEqual([
      "BASE",
      "arc 10,10 r2.5 #fff",
      "BASE",
      "fillRect difference #fff 0,0,100,100",
    ]);
  });
});

describe("zoom and pan", () => {
  const origin = { x: 50, y: 20 };
  it("keeps the anchor over the same spot of the picture", () => {
    const at = { x: 150, y: 120 };
    const v = zoomAbout(FIT, 2, at, at, origin);
    // The picture point under `at` before: (at - origin) / 1 = (100, 100); after it maps to at again.
    expect(origin.x + v.tx + v.s * 100).toBeCloseTo(at.x, 9);
    expect(origin.y + v.ty + v.s * 100).toBeCloseTo(at.y, 9);
  });
  it("a pinch whose midpoint moves also pans", () => {
    const v = zoomAbout(FIT, 1, { x: 100, y: 100 }, { x: 130, y: 90 }, origin);
    expect(v).toEqual({ s: 1, tx: 30, ty: -10 });
  });
  it("stays between 1× and the maximum", () => {
    expect(zoomAbout(FIT, 0.2, { x: 0, y: 0 }, { x: 0, y: 0 }, origin).s).toBe(1);
    expect(zoomAbout(FIT, 99, { x: 0, y: 0 }, { x: 0, y: 0 }, origin).s).toBe(MAX_ZOOM);
  });
  it("clamping keeps the stage centre on the picture and snaps 1× back to fit", () => {
    const frame = { w: 400, h: 200 };
    const stage = { w: 500, h: 240 };
    expect(clampView({ s: 1, tx: 80, ty: -5 }, origin, frame, stage)).toEqual(FIT);
    const far = clampView({ s: 2, tx: 5000, ty: -5000 }, origin, frame, stage);
    // The stage centre (250, 120) must land inside the scaled frame.
    expect(250 - origin.x - far.tx).toBeGreaterThanOrEqual(0);
    expect(250 - origin.x - far.tx).toBeLessThanOrEqual(frame.w * 2);
    expect(120 - origin.y - far.ty).toBeGreaterThanOrEqual(0);
    expect(120 - origin.y - far.ty).toBeLessThanOrEqual(frame.h * 2);
  });
  it("fits the picture by whichever side runs out first", () => {
    expect(fitWidth({ w: 400, h: 200 }, { w: 1000, h: 100 })).toBe(200);
    expect(fitWidth({ w: 400, h: 200 }, { w: 300, h: 1000 })).toBe(300);
    expect(fitWidth({ w: 1, h: 4 }, { w: 500, h: 400 })).toBe(100);
    expect(fitWidth({ w: 0, h: 0 }, { w: 500, h: 400 })).toBe(0);
  });
});

function recorder() {
  const calls: string[] = [];
  const ctx = {
    fillStyle: "",
    strokeStyle: "",
    lineWidth: 0,
    lineCap: "butt",
    lineJoin: "miter",
    globalCompositeOperation: "source-over",
    fillRect: (x: number, y: number, w: number, h: number) =>
      calls.push(`fillRect ${ctx.globalCompositeOperation} ${ctx.fillStyle} ${x},${y},${w},${h}`),
    beginPath: () => calls.push("beginPath"),
    moveTo: (x: number, y: number) => calls.push(`moveTo ${x},${y}`),
    lineTo: (x: number, y: number) => calls.push(`lineTo ${x},${y}`),
    arc: (x: number, y: number, r: number) => calls.push(`arc ${x},${y} r${r} ${ctx.fillStyle}`),
    fill: () => calls.push("fill"),
    stroke: () => calls.push(`stroke ${ctx.strokeStyle} w${ctx.lineWidth} ${ctx.lineCap}`),
    save: () => calls.push("save"),
    restore: () => calls.push("restore"),
  };
  return { ctx: ctx as unknown as MaskCtx, calls };
}

describe("drawOp", () => {
  const size = { w: 400, h: 200 };
  it("paints white and erases black, at the long-side width of the target canvas", () => {
    const { ctx, calls } = recorder();
    drawOp(ctx, { kind: "paint", width: 0.05, points: [{ x: 0, y: 0 }, { x: 0.5, y: 0.5 }] }, size);
    expect(calls).toContain("moveTo 0,0");
    expect(calls).toContain("lineTo 200,100");
    expect(calls).toContain("stroke #fff w20 round");
    const e = recorder();
    drawOp(e.ctx, { kind: "erase", width: 0.05, points: [{ x: 0.5, y: 0.5 }] }, size);
    expect(e.calls).toContain("arc 200,100 r10 #000");
  });
  it("clears to black and inverts with a difference against white", () => {
    const { ctx, calls } = recorder();
    drawOp(ctx, { kind: "clear" }, size);
    drawOp(ctx, { kind: "invert" }, size);
    expect(calls).toContain("fillRect source-over #000 0,0,400,200");
    expect(calls).toContain("fillRect difference #fff 0,0,400,200");
  });
});
