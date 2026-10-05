// maskGeometry — the arithmetic behind the inpaint mask canvas (docs/log/111 §10), kept free of
// the DOM so every rule here is a node test.
//
// The Agent treats a mask as "the same relative region of the picture" for every ComfyUI family:
// the mask is stretched over the frame with no crop (log 111 §10.1). So everything a stroke
// records is relative — points as 0..1 of the picture, widths as a ratio of its long side — and
// only the moment of drawing turns them into pixels of whatever canvas is being drawn.

/** Brush widths as a fraction of the picture's LONG side (log 111 §10.2). A pixel width would mean
 *  a different number of latent cells per family and per input: the instruction-edit families
 *  shrink to ~1 MP first (one latent cell ≈ 27 px of a 4032×3024 photo), the others do not (≈ 8 px).
 *  `warnBelow` is the thin-brush warning: below it a stroke covers about two latent cells or fewer
 *  on a shrunk input, and the repaint does not follow its shape. */
export const MASK_BRUSH = {
  thin: 0.015,
  medium: 0.03,
  thick: 0.06,
  warnBelow: 0.02,
} as const;

export type BrushSize = "thin" | "medium" | "thick";
export const BRUSH_SIZES: readonly BrushSize[] = ["thin", "medium", "thick"];

/** The largest canvas the export will allocate, in pixels. iOS Safari's widely quoted per-canvas
 *  limit is 4096² (not measurable in this workspace — log 111 §10.8); a picture above it is
 *  exported smaller at the same aspect ratio rather than relying on the canvas failing. */
export const MAX_CANVAS_AREA = 16_777_216;

/** A pixel counts as painted at or above this red value (log 111 §10.5-2): anti-aliased edges and
 *  an eraser's rim leave values of 1..a few, which must not make a wiped mask look painted. */
export const PAINTED_RED = 128;

/** Above this share of painted pixels the mask is "everything" — an edit without a mask, nearly. */
export const FULL_SHARE = 0.98;

/** A loaded mask whose aspect ratio differs from the picture's by this much or more is flagged. */
export const ASPECT_TOLERANCE = 0.01;

export interface Size {
  w: number;
  h: number;
}

export interface Point {
  x: number;
  y: number;
}

/** One undoable step. Points are 0..1 of the picture; `width` is a ratio of its long side. */
export type MaskOp =
  | { kind: "paint" | "erase"; width: number; points: Point[] }
  | { kind: "clear" }
  | { kind: "invert" }
  /** Back to the mask the canvas opened with (the reopened PNG, or black). */
  | { kind: "reset" };

/** The stroke width in pixels of a canvas of `size` for a long-side ratio. */
export function brushPx(ratio: number, size: Size): number {
  return ratio * Math.max(size.w, size.h);
}

/** Whether a long-side ratio is thin enough to warn about. */
export const brushTooThin = (ratio: number): boolean => ratio < MASK_BRUSH.warnBelow;

/**
 * The export size for a picture of `natural` (orientation already applied): the native size when
 * it fits `cap`, else the largest size under `cap` with the same aspect ratio (log 111 §10.3 (c)).
 * The short side is rounded from the long one, so the ratio error stays within half a pixel —
 * far inside one latent cell (8..27 input px).
 */
export function exportSize(natural: Size, cap = MAX_CANVAS_AREA): Size {
  const w = Math.max(1, Math.round(natural.w));
  const h = Math.max(1, Math.round(natural.h));
  if (w * h <= cap) return { w, h };
  const wide = w >= h;
  const long = wide ? w : h;
  const short = wide ? h : w;
  let l = Math.floor(long * Math.sqrt(cap / (w * h)));
  let s = Math.max(1, Math.round((l * short) / long));
  while (l > 1 && l * s > cap) {
    l -= 1;
    s = Math.max(1, Math.round((l * short) / long));
  }
  return wide ? { w: l, h: s } : { w: s, h: l };
}

/** A pointer position over the displayed picture, as 0..1 of it (clamped to the frame). */
export function toNormalized(clientX: number, clientY: number, rect: { left: number; top: number; width: number; height: number }): Point {
  const clamp = (v: number) => (v < 0 ? 0 : v > 1 ? 1 : v);
  return {
    x: rect.width > 0 ? clamp((clientX - rect.left) / rect.width) : 0,
    y: rect.height > 0 ? clamp((clientY - rect.top) / rect.height) : 0,
  };
}

/** A normalized point in the pixels of a canvas of `size`. */
export const toCanvas = (p: Point, size: Size): Point => ({ x: p.x * size.w, y: p.y * size.h });

/** Whether two sizes differ in aspect ratio by ASPECT_TOLERANCE or more. */
export function aspectDiffers(a: Size, b: Size): boolean {
  if (a.w <= 0 || a.h <= 0 || b.w <= 0 || b.h <= 0) return false;
  // The epsilon keeps a ratio that is 1% off by construction (101:100) on the flagged side.
  return Math.abs(a.w / a.h / (b.w / b.h) - 1) >= ASPECT_TOLERANCE - 1e-9;
}

export type MaskVerdict = "blank" | "full" | "ok";

/**
 * Judge a mask by its pixels (RGBA: the red of a grey mask, which ComfyUI reads, or the alpha of
 * a display mask with `channel` 3): "blank" when fewer pixels than one thin-brush dab reach
 * PAINTED_RED, "full" when nearly all of them do. Counting strokes cannot answer this: the
 * underlay is raster and the eraser removes paint.
 */
export function judgeMask(rgba: ArrayLike<number>, size: Size, channel: 0 | 3 = 0): MaskVerdict {
  const total = size.w * size.h;
  if (total <= 0) return "blank";
  let painted = 0;
  for (let i = 0; i < total; i++) if (rgba[i * 4 + channel] >= PAINTED_RED) painted++;
  const r = brushPx(MASK_BRUSH.thin, size) / 2;
  const dab = Math.max(1, Math.floor(Math.PI * r * r));
  if (painted < dab) return "blank";
  if (painted >= total * FULL_SHARE) return "full";
  return "ok";
}

/** Whether any pixel of an RGBA buffer is not fully opaque. */
export function hasTransparency(rgba: ArrayLike<number>): boolean {
  for (let i = 3; i < rgba.length; i += 4) if (rgba[i] !== 255) return true;
  return false;
}

/** Turn RGBA pixels into the opaque grey the Agent reads: each pixel's red copied to G and B,
 *  alpha 255. A coloured PNG put in the path field then shows as it will act, not as it looks. */
export function redToGrey(rgba: Uint8ClampedArray): void {
  for (let i = 0; i < rgba.length; i += 4) {
    const r = rgba[i];
    rgba[i + 1] = r;
    rgba[i + 2] = r;
    rgba[i + 3] = 255;
  }
}

/** Turn RGBA pixels into a display mask: white whose alpha is the red the Agent would read. */
export function redToAlpha(rgba: Uint8ClampedArray): void {
  for (let i = 0; i < rgba.length; i += 4) {
    rgba[i + 3] = rgba[i];
    rgba[i] = rgba[i + 1] = rgba[i + 2] = 255;
  }
}

/** A fresh file name per save (log 111 §10.5-4): overwriting would change the mask of a job still
 *  waiting in the queue and of every earlier picture whose sidecar names it. The random tail makes
 *  two browsers saving in the same millisecond differ; the upload still refuses an existing name
 *  (409) and the caller then asks for another one. */
export function maskFileName(now: Date, random: () => number = Math.random): string {
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  const tail = Math.floor(random() * 36 ** 4)
    .toString(36)
    .padStart(4, "0");
  return (
    `mask-${now.getFullYear()}${p(now.getMonth() + 1)}${p(now.getDate())}` +
    `-${p(now.getHours())}${p(now.getMinutes())}${p(now.getSeconds())}-${p(now.getMilliseconds(), 3)}-${tail}.png`
  );
}

/** The 2D context surface replayOps needs — narrowed so a test can record the calls. */
export type MaskCtx = Pick<
  CanvasRenderingContext2D,
  | "fillStyle"
  | "strokeStyle"
  | "lineWidth"
  | "lineCap"
  | "lineJoin"
  | "globalCompositeOperation"
  | "fillRect"
  | "clearRect"
  | "beginPath"
  | "moveTo"
  | "lineTo"
  | "arc"
  | "fill"
  | "stroke"
  | "save"
  | "restore"
>;

/** Rebuild a mask from its start: `drawBase` paints the starting mask (black, or the reopened PNG),
 *  and runs again at every reset. Undo and redo replay this list instead of keeping bitmaps: one
 *  full-size snapshot per step is what iOS's total canvas memory cannot afford (log 111 §10.9). */
export function replayOps(ctx: MaskCtx, ops: readonly MaskOp[], size: Size, drawBase: () => void, form: MaskForm = "grey"): void {
  drawBase();
  for (const op of ops) {
    if (op.kind === "reset") drawBase();
    else drawOp(ctx, op, size, form);
  }
}

/**
 * How a mask canvas holds the mask. "grey" is what is exported: opaque, black = keep, white =
 * repaint (red channel, log 111 §2-3). "alpha" is what is shown: white whose alpha is the mask, so
 * a tint can be laid over the picture with plain source-over. (A screen blend of a grey mask
 * adds nothing over white, so paint on a white shirt was invisible.)
 */
export type MaskForm = "grey" | "alpha";

/** Draw one op onto a mask canvas of `size`. Grey: white paints, black erases, invert is a
 *  "difference" against white (255 − v). Alpha: erase and clear remove coverage, invert is an "xor"
 *  with an opaque fill (1 − α). Neither runs a pixel loop per press. */
export function drawOp(ctx: MaskCtx, op: MaskOp, size: Size, form: MaskForm = "grey"): void {
  ctx.save();
  ctx.globalCompositeOperation = "source-over";
  if (op.kind === "clear") {
    if (form === "alpha") ctx.clearRect(0, 0, size.w, size.h);
    else {
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, size.w, size.h);
    }
  } else if (op.kind === "reset") {
    // Only replayOps knows the starting mask; drawn alone a reset changes nothing.
  } else if (op.kind === "invert") {
    ctx.globalCompositeOperation = form === "alpha" ? "xor" : "difference";
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, size.w, size.h);
  } else {
    drawStroke(ctx, op, size, 0, form);
  }
  ctx.restore();
}

/** Draw a stroke's points from index `from` on (a live stroke draws only its new segment). */
export function drawStroke(
  ctx: MaskCtx,
  op: Extract<MaskOp, { points: Point[] }>,
  size: Size,
  from: number,
  form: MaskForm = "grey",
): void {
  ctx.save();
  try {
    paintStroke(ctx, op, size, from, form);
  } finally {
    ctx.restore();
  }
}

function paintStroke(ctx: MaskCtx, op: Extract<MaskOp, { points: Point[] }>, size: Size, from: number, form: MaskForm): void {
  const erase = op.kind === "erase";
  ctx.globalCompositeOperation = erase && form === "alpha" ? "destination-out" : "source-over";
  const color = erase && form === "grey" ? "#000" : "#fff";
  const w = brushPx(op.width, size);
  const pts = op.points;
  if (!pts.length) return;
  if (pts.length === 1) {
    const p = toCanvas(pts[0], size);
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(p.x, p.y, w / 2, 0, Math.PI * 2);
    ctx.fill();
    return;
  }
  ctx.strokeStyle = color;
  ctx.lineWidth = w;
  ctx.lineCap = "round";
  ctx.lineJoin = "round";
  ctx.beginPath();
  const start = toCanvas(pts[Math.max(0, from - 1)], size);
  ctx.moveTo(start.x, start.y);
  for (let i = Math.max(1, from); i < pts.length; i++) {
    const p = toCanvas(pts[i], size);
    ctx.lineTo(p.x, p.y);
  }
  ctx.stroke();
}

/** The zoom/pan of the picture inside its stage: `translate(tx, ty) scale(s)` with the origin at the
 *  frame's top-left, the frame laid out at `origin` inside the stage. */
export interface View {
  s: number;
  tx: number;
  ty: number;
}

export const FIT: View = { s: 1, tx: 0, ty: 0 };
export const MAX_ZOOM = 8;

/** Zoom to `s` keeping the stage point `at` over the same spot of the picture (wheel, pinch, buttons).
 *  `from` is the view the gesture started with and `fromAt` where its anchor was then; for a pinch
 *  the anchor moves with the fingers' midpoint, which is the pan. */
export function zoomAbout(from: View, s: number, fromAt: Point, at: Point, origin: Point): View {
  const ns = Math.min(MAX_ZOOM, Math.max(1, s));
  const lx = (fromAt.x - origin.x - from.tx) / from.s;
  const ly = (fromAt.y - origin.y - from.ty) / from.s;
  return { s: ns, tx: at.x - origin.x - ns * lx, ty: at.y - origin.y - ns * ly };
}

/** Keep the stage's centre on the picture, so a pan can never lose it; at 1× the picture sits fitted. */
export function clampView(v: View, origin: Point, frame: Size, stage: Size): View {
  if (v.s <= 1) return FIT;
  const cx = stage.w / 2;
  const cy = stage.h / 2;
  const clamp = (t: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, t));
  return {
    s: v.s,
    tx: clamp(v.tx, cx - origin.x - frame.w * v.s, cx - origin.x),
    ty: clamp(v.ty, cy - origin.y - frame.h * v.s, cy - origin.y),
  };
}

/** The picture's width when fitted into a stage (height follows the picture's own aspect). */
export function fitWidth(natural: Size, stage: Size): number {
  if (natural.w <= 0 || natural.h <= 0 || stage.w <= 0 || stage.h <= 0) return 0;
  return Math.max(1, Math.floor(Math.min(stage.w, (stage.h * natural.w) / natural.h)));
}
