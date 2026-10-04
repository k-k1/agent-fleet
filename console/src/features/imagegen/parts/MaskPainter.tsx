// MaskPainter — paint an inpaint mask over the whole reference picture (docs/log/111 §10,
// ADR 0081 P2, ADR 0100 decision 11). What it produces is ONE uploaded path, handed to `onSaved`:
// the wire and the Agent are unchanged, and the path field stays the other way in.
//
// The painter knows nothing about where it is mounted (today MaskCanvasModal) — it takes a picture
// and a mask path and reports a new path; the shell owns closing.
//
// Three canvases, none of them full size while painting: `mask` (the grey mask at display
// resolution, white = repaint), `base` (what the mask started from — black, or the saved PNG
// reopened as an underlay) and the visible `view` (the mask tinted for the eye). Undo/redo replay
// the op list over `base`; the export canvas exists only for the moment of saving and is released
// right after (iOS limits the total canvas memory, not only one canvas's area — log 111 §10.9).
//
// Orientation: the picture is a plain <img> of the ORIGINAL bytes (not a `preview`, which is
// re-encoded without EXIF) and nothing here may set `image-orientation` on it. The browser then
// shows the picture the way ComfyUI's `exif_transpose` reads it, and `naturalWidth/Height` are the
// oriented size (measured, log 111 §10.4). The <img> is given a width only, so a display that
// ignored the orientation would lay out with the other aspect ratio — `sideways` refuses to paint.
import { useCallback, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";
import type { PointerEvent as RPointerEvent, Ref } from "react";
import { downloadURL, uploadFiles } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Button, IconButton } from "../../../ui/Button.tsx";
import {
  aspectDiffers,
  BRUSH_SIZES,
  brushPx,
  brushTooThin,
  clampView,
  drawStroke,
  exportSize,
  FIT,
  fitWidth,
  hasTransparency,
  judgeMask,
  MASK_BRUSH,
  maskFileName,
  redToAlpha,
  redToGrey,
  replayOps,
  toNormalized,
  zoomAbout,
  type BrushSize,
  type MaskForm,
  type MaskOp,
  type Point,
  type Size,
  type View,
} from "../maskGeometry.ts";

/** Where a saved mask lands: its own folder, so masks do not pile up among the references. */
export const MASK_DIR = "generated/console/masks";

/** Display canvases are capped at this many pixels: a 3× phone over a large modal would otherwise
 *  allocate three canvases of tens of megabytes for no visible gain. */
const VIEW_AREA_CAP = 4_000_000;

/** What the view tints a painted pixel with, laid over the picture at the view's CSS opacity.
 *  Magenta stands out on white, black and skin alike. */
const TINT = "#ff2bd6";

/** How many fresh names a save tries before giving up on 409s. */
const NAME_TRIES = 5;

export type ImageLoader = (url: string) => Promise<HTMLImageElement>;

const loadImageDefault: ImageLoader = (url) =>
  new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("load"));
    img.src = url;
  });

type Stroke = Extract<MaskOp, { points: Point[] }>;
type Gesture =
  | { kind: "stroke"; id: number; op: Stroke }
  | { kind: "pan"; id: number; at: Point; view: View }
  | { kind: "pinch"; ids: [number, number]; d: number; at: Point; view: View };

export interface MaskPainterHandle {
  /** Save as the button would; resolves true when the mask was handed to `onSaved`. */
  save: () => Promise<boolean>;
}

export interface MaskPainterProps {
  /** Browse-root path of the picture painted over (the first reference, which decides the frame). */
  picture: string;
  /** The current mask path, reopened as the underlay. Empty: start black. */
  mask: string;
  /** The uploaded path. Returning false means the owner refused it (the picture changed under the
   *  canvas); the drawing stays so the member can decide. */
  onSaved: (path: string) => boolean | void;
  /** Asked before uploading: false when the picture this canvas was opened on is no longer the
   *  first reference, so a mask for the wrong picture is never written into the draft. */
  pictureCurrent?: () => boolean;
  onCancel: () => void;
  /** Unsaved strokes exist (the shell asks before closing). */
  onDirty?: (dirty: boolean) => void;
  /** True while a save is uploading, so a shell can refuse to close mid-save. */
  onBusy?: (busy: boolean) => void;
  /** Tests replace the underlay loader; jsdom never loads an image. */
  loadImage?: ImageLoader;
  ref?: Ref<MaskPainterHandle>;
}

export function MaskPainter({
  picture,
  mask,
  onSaved,
  pictureCurrent,
  onCancel,
  onDirty,
  onBusy,
  loadImage = loadImageDefault,
  ref,
}: MaskPainterProps) {
  const tr = useT();
  const stageRef = useRef<HTMLDivElement>(null);
  const frameRef = useRef<HTMLDivElement>(null);
  const imgRef = useRef<HTMLImageElement>(null);
  const viewRef = useRef<HTMLCanvasElement>(null);
  const maskRef = useRef<HTMLCanvasElement | null>(null);
  const baseRef = useRef<HTMLCanvasElement | null>(null);
  const underlayRef = useRef<HTMLImageElement | null>(null);
  const gestureRef = useRef<Gesture | null>(null);
  const pointersRef = useRef(new Map<number, Point & { touch: boolean }>());

  const [natural, setNatural] = useState<Size | null>(null);
  const [picError, setPicError] = useState(false);
  const [sideways, setSideways] = useState(false);
  const [stage, setStage] = useState<Size>({ w: 0, h: 0 });
  const [viewSize, setViewSize] = useState<Size | null>(null);
  const [underlayReady, setUnderlayReady] = useState(!mask.trim());
  const [notices, setNotices] = useState<string[]>([]);
  const [ops, setOps] = useState<MaskOp[]>([]);
  const [redo, setRedo] = useState<MaskOp[]>([]);
  const [tool, setTool] = useState<"paint" | "erase">("paint");
  const [mode, setMode] = useState<"draw" | "move">("draw");
  const [size, setSize] = useState<BrushSize>("medium");
  const [view, setView] = useState<View>(FIT);
  const [peek, setPeek] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [invertWarn, setInvertWarn] = useState(false);
  const [fullAck, setFullAck] = useState(false);

  const opsRef = useRef(ops);
  opsRef.current = ops;
  const viewStateRef = useRef(view);
  viewStateRef.current = view;
  const note = useCallback((n: string) => setNotices((cur) => (cur.includes(n) ? cur : [...cur, n])), []);

  useEffect(() => onBusy?.(saving), [saving, onBusy]);
  // A stroke counts from its first point: Esc mid-stroke must ask, not drop it.
  const [drawing, setDrawing] = useState(false);
  useEffect(() => onDirty?.(ops.length > 0 || drawing), [ops.length, drawing, onDirty]);

  const ctxOf = (c: HTMLCanvasElement | null) => c?.getContext("2d") ?? null;

  const refreshView = useCallback(() => {
    const v = viewRef.current;
    const m = maskRef.current;
    const ctx = ctxOf(v);
    if (!v || !m || !ctx) return;
    ctx.save();
    ctx.globalCompositeOperation = "source-over";
    ctx.clearRect(0, 0, v.width, v.height);
    ctx.drawImage(m, 0, 0);
    // The display mask is coverage (alpha): keep it, swap its colour for the tint.
    ctx.globalCompositeOperation = "source-in";
    ctx.fillStyle = TINT;
    ctx.fillRect(0, 0, v.width, v.height);
    ctx.restore();
  }, []);

  /** Mask = base + every op, then the view. Runs on undo/redo and whenever the size changes. */
  const repaint = useCallback(
    (list: MaskOp[]) => {
      const m = maskRef.current;
      const base = baseRef.current;
      const ctx = ctxOf(m);
      if (!m || !base || !ctx) return;
      replayOps(ctx, list, { w: m.width, h: m.height }, () => {
        ctx.save();
        ctx.globalCompositeOperation = "copy";
        ctx.drawImage(base, 0, 0);
        ctx.restore();
      }, "alpha");
      refreshView();
    },
    [refreshView],
  );

  /** The starting mask on a canvas: empty (black, or no coverage), then the underlay's red channel
   *  as opaque grey (export) or as coverage (display). */
  const paintBase = useCallback((c: HTMLCanvasElement, img: HTMLImageElement | null, form: MaskForm) => {
    const ctx = ctxOf(c);
    if (!ctx) return;
    ctx.save();
    ctx.globalCompositeOperation = "source-over";
    ctx.clearRect(0, 0, c.width, c.height);
    ctx.fillStyle = "#000";
    ctx.fillRect(0, 0, c.width, c.height);
    if (img) {
      ctx.drawImage(img, 0, 0, c.width, c.height);
      const data = ctx.getImageData(0, 0, c.width, c.height);
      if (form === "alpha") redToAlpha(data.data);
      else redToGrey(data.data);
      ctx.putImageData(data, 0, 0);
    } else if (form === "alpha") {
      ctx.clearRect(0, 0, c.width, c.height);
    }
    ctx.restore();
  }, []);

  // The stage's size decides the fitted picture's width.
  useLayoutEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const measure = () => setStage((cur) => (cur.w === el.clientWidth && cur.h === el.clientHeight ? cur : { w: el.clientWidth, h: el.clientHeight }));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const fitW = natural ? fitWidth(natural, stage) : 0;

  // The fitted picture's layout box (untransformed — zoom is a CSS transform on the frame) decides
  // the display canvases' size, and whether the browser laid it out in the orientation it reports.
  useLayoutEffect(() => {
    const img = imgRef.current;
    if (!img || !natural || !fitW) return;
    const w0 = img.offsetWidth;
    const h0 = img.offsetHeight;
    if (w0 <= 0 || h0 <= 0) return;
    setSideways(aspectDiffers(natural, { w: w0, h: h0 }));
    const dpr = window.devicePixelRatio || 1;
    const k = Math.min(dpr, Math.sqrt(VIEW_AREA_CAP / (w0 * h0)));
    const w = Math.max(1, Math.round(w0 * k));
    const h = Math.max(1, Math.round(h0 * k));
    setViewSize((cur) => (cur && cur.w === w && cur.h === h ? cur : { w, h }));
    setView(FIT);
  }, [natural, fitW]);

  // Load the underlay once. Unreadable → start black with a notice (log 111 §10.5-3).
  useEffect(() => {
    const path = mask.trim();
    if (!path) return;
    let live = true;
    loadImage(downloadURL(path))
      .then((img) => {
        if (live) underlayRef.current = img;
      })
      .catch(() => {
        if (live) note(tr("imggen.mask_canvas_underlay_unreadable"));
      })
      .finally(() => {
        if (live) setUnderlayReady(true);
      });
    return () => {
      live = false;
    };
  }, [mask, loadImage, note, tr]);

  // (Re)build the display canvases whenever their size or the underlay changes.
  useEffect(() => {
    if (!viewSize || !underlayReady || !natural) return;
    const make = (cur: HTMLCanvasElement | null) => {
      const c = cur ?? document.createElement("canvas");
      c.width = viewSize.w;
      c.height = viewSize.h;
      return c;
    };
    maskRef.current = make(maskRef.current);
    baseRef.current = make(baseRef.current);
    if (viewRef.current) {
      viewRef.current.width = viewSize.w;
      viewRef.current.height = viewSize.h;
    }
    let img = underlayRef.current;
    if (img) {
      // A transparent underlay would show differently from how it acts: the canvas reads a
      // transparent pixel's RGB premultiplied to 0, ComfyUI reads the stored RGB.
      const probe = ctxOf(baseRef.current);
      if (probe) {
        probe.clearRect(0, 0, viewSize.w, viewSize.h);
        probe.drawImage(img, 0, 0, viewSize.w, viewSize.h);
        if (hasTransparency(probe.getImageData(0, 0, viewSize.w, viewSize.h).data)) {
          underlayRef.current = img = null;
          note(tr("imggen.mask_canvas_underlay_alpha"));
        }
      }
      if (img) {
        note(tr("imggen.mask_canvas_underlay_kept"));
        if (aspectDiffers(natural, { w: img.naturalWidth, h: img.naturalHeight })) note(tr("imggen.mask_canvas_underlay_aspect"));
      }
    }
    paintBase(baseRef.current, img, "alpha");
    repaint(opsRef.current);
  }, [viewSize, underlayReady, natural, paintBase, repaint, note, tr]);

  // Drop the display canvases' memory on close.
  useEffect(
    () => () => {
      for (const c of [maskRef.current, baseRef.current]) if (c) c.width = c.height = 0;
    },
    [],
  );

  const ready = !!viewSize && underlayReady && !sideways && !picError;
  const ratio = MASK_BRUSH[size];

  // ---- zoom / pan ------------------------------------------------------------------------------

  const stagePoint = (e: { clientX: number; clientY: number }): Point => {
    const r = stageRef.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };
  const origin = (): Point => ({ x: frameRef.current?.offsetLeft ?? 0, y: frameRef.current?.offsetTop ?? 0 });
  const frameSize = (): Size => ({ w: imgRef.current?.offsetWidth ?? 0, h: imgRef.current?.offsetHeight ?? 0 });
  const settle = (v: View) => setView(clampView(v, origin(), frameSize(), stage));
  const zoomBy = (k: number) => {
    const c = { x: stage.w / 2, y: stage.h / 2 };
    settle(zoomAbout(view, view.s * k, c, c, origin()));
  };

  // Wheel zoom needs a non-passive listener to keep the page from scrolling with it.
  useEffect(() => {
    const el = stageRef.current;
    if (!el || !ready) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const v = viewStateRef.current;
      const r = el.getBoundingClientRect();
      const at = { x: e.clientX - r.left, y: e.clientY - r.top };
      const o = { x: frameRef.current?.offsetLeft ?? 0, y: frameRef.current?.offsetTop ?? 0 };
      const f = { w: imgRef.current?.offsetWidth ?? 0, h: imgRef.current?.offsetHeight ?? 0 };
      setView(clampView(zoomAbout(v, v.s * Math.exp(-e.deltaY * 0.002), at, at, o), o, f, { w: el.clientWidth, h: el.clientHeight }));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [ready]);

  // ---- pointers --------------------------------------------------------------------------------

  const pointOf = (e: { clientX: number; clientY: number }): Point =>
    toNormalized(e.clientX, e.clientY, viewRef.current!.getBoundingClientRect());

  const abortStroke = () => {
    if (gestureRef.current?.kind !== "stroke") return;
    gestureRef.current = null;
    setDrawing(false);
    repaint(opsRef.current);
  };

  const startPinch = () => {
    const touches = [...pointersRef.current.entries()].filter(([, p]) => p.touch).slice(0, 2);
    const [[a, pa], [b, pb]] = touches;
    gestureRef.current = {
      kind: "pinch",
      ids: [a, b],
      d: Math.max(1, Math.hypot(pb.x - pa.x, pb.y - pa.y)),
      at: { x: (pa.x + pb.x) / 2, y: (pa.y + pb.y) / 2 },
      view: viewStateRef.current,
    };
  };

  const onPointerDown = (e: RPointerEvent<HTMLCanvasElement>) => {
    if (!ready || saving) return;
    if (e.pointerType === "mouse" && e.button !== 0) return;
    e.preventDefault();
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      // jsdom, or a pointer already gone: painting still works without capture.
    }
    const touch = e.pointerType === "touch";
    pointersRef.current.set(e.pointerId, { ...stagePoint(e), touch });
    const fingers = [...pointersRef.current.values()].filter((p) => p.touch).length;
    // A second finger turns the gesture into pan/pinch: the stroke the first finger started is
    // dropped whole rather than kept up to that moment (log 111 §4).
    if (touch && fingers === 2) {
      abortStroke();
      startPinch();
      return;
    }
    if (gestureRef.current) return; // a third finger, or a second pointer of another kind
    if (mode === "move") {
      gestureRef.current = { kind: "pan", id: e.pointerId, at: stagePoint(e), view: viewStateRef.current };
      return;
    }
    const op: Stroke = { kind: tool, width: ratio, points: [pointOf(e)] };
    gestureRef.current = { kind: "stroke", id: e.pointerId, op };
    setDrawing(true);
    const m = maskRef.current;
    const ctx = ctxOf(m);
    if (m && ctx) drawStroke(ctx, op, { w: m.width, h: m.height }, 0, "alpha");
    refreshView();
  };

  const onPointerMove = (e: RPointerEvent<HTMLCanvasElement>) => {
    const p = pointersRef.current.get(e.pointerId);
    if (p) pointersRef.current.set(e.pointerId, { ...stagePoint(e), touch: p.touch });
    const g = gestureRef.current;
    if (!g) return;
    if (g.kind === "pinch") {
      const a = pointersRef.current.get(g.ids[0]);
      const b = pointersRef.current.get(g.ids[1]);
      if (!a || !b) return;
      e.preventDefault();
      const d = Math.max(1, Math.hypot(b.x - a.x, b.y - a.y));
      settle(zoomAbout(g.view, (g.view.s * d) / g.d, g.at, { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, origin()));
      return;
    }
    if (g.id !== e.pointerId) return;
    e.preventDefault();
    if (g.kind === "pan") {
      const at = stagePoint(e);
      settle({ s: g.view.s, tx: g.view.tx + at.x - g.at.x, ty: g.view.ty + at.y - g.at.y });
      return;
    }
    const native = e.nativeEvent as PointerEvent;
    const events = typeof native.getCoalescedEvents === "function" ? native.getCoalescedEvents() : [];
    const from = g.op.points.length;
    for (const ev of events.length ? events : [e]) g.op.points.push(pointOf(ev));
    const m = maskRef.current;
    const ctx = ctxOf(m);
    if (m && ctx) drawStroke(ctx, g.op, { w: m.width, h: m.height }, from, "alpha");
    refreshView();
  };

  const onPointerEnd = (e: RPointerEvent<HTMLCanvasElement>) => {
    pointersRef.current.delete(e.pointerId);
    const g = gestureRef.current;
    if (!g) return;
    if (g.kind === "pinch") {
      // The finger left on the glass does not start drawing: strokes begin only on a new press.
      if (g.ids.includes(e.pointerId)) gestureRef.current = null;
      return;
    }
    if (g.id !== e.pointerId) return;
    gestureRef.current = null;
    if (g.kind === "stroke") {
      setDrawing(false);
      if (e.type === "pointercancel") repaint(opsRef.current);
      else commit(g.op);
    }
  };

  // ---- ops -------------------------------------------------------------------------------------

  const commit = (op: MaskOp) => {
    const next = [...opsRef.current, op];
    opsRef.current = next;
    setOps(next);
    setRedo([]);
    setFullAck(false);
    setInvertWarn(false);
    setError("");
    return next;
  };

  const applyOp = (op: MaskOp) => {
    repaint(commit(op));
    if (op.kind === "invert") {
      // Inverting a few strokes repaints nearly the whole picture — say so where it happened.
      const m = maskRef.current;
      const ctx = ctxOf(m);
      if (m && ctx) setInvertWarn(judgeMask(ctx.getImageData(0, 0, m.width, m.height).data, { w: m.width, h: m.height }, 3) === "full");
    }
  };

  const undo = () => {
    if (!ops.length) return;
    const next = ops.slice(0, -1);
    setRedo([...redo, ops[ops.length - 1]]);
    setOps(next);
    setFullAck(false);
    setInvertWarn(false);
    repaint(next);
  };

  const redoOne = () => {
    if (!redo.length) return;
    const next = [...ops, redo[redo.length - 1]];
    setRedo(redo.slice(0, -1));
    setOps(next);
    setFullAck(false);
    repaint(next);
  };

  // ---- save ------------------------------------------------------------------------------------

  const save = async (): Promise<boolean> => {
    const m = maskRef.current;
    const mctx = ctxOf(m);
    if (!m || !mctx || !natural || !ready || saving) return false;
    // A stroke still under a finger is part of what the member sees and saves: commit it now, so
    // the judgement, the export and the undo list are one list, and its late pointerup is ignored.
    let list = opsRef.current;
    const live = gestureRef.current;
    if (live?.kind === "stroke") {
      gestureRef.current = null;
      setDrawing(false);
      list = commit(live.op);
    }
    const verdict = judgeMask(mctx.getImageData(0, 0, m.width, m.height).data, { w: m.width, h: m.height }, 3);
    if (verdict === "blank") {
      setError(tr("imggen.mask_canvas_blank"));
      return false;
    }
    if (verdict === "full" && !fullAck) {
      setFullAck(true);
      setError(tr("imggen.mask_canvas_full"));
      return false;
    }
    if (pictureCurrent && !pictureCurrent()) {
      setError(tr("imggen.mask_canvas_picture_changed"));
      return false;
    }
    setSaving(true);
    setError("");
    const out = exportSize(natural);
    const c = document.createElement("canvas");
    try {
      c.width = out.w;
      c.height = out.h;
      const ctx = ctxOf(c);
      if (!ctx) throw new Error("no 2d context");
      // Black over everything first: ComfyUI reads the red channel, and a transparent pixel would
      // read as 0 only by accident of the encoder (log 111 §2-3).
      replayOps(ctx, list, out, () => paintBase(c, underlayRef.current, "grey"));
      const blob = await new Promise<Blob | null>((res) => c.toBlob(res, "image/png"));
      if (!blob) throw new Error("toBlob");
      // Never overwrite: a 409 means the name exists, so ask for another one.
      let path = "";
      for (let i = 0; i < NAME_TRIES && !path; i++) {
        const name = maskFileName(new Date());
        const r = await uploadFiles(MASK_DIR, [new File([blob], name, { type: "image/png" })]);
        if (r.status >= 200 && r.status < 300) path = `${MASK_DIR}/${name}`;
        else if (r.status !== 409) throw new Error(String(r.status));
      }
      if (!path) throw new Error("409");
      if (onSaved(path) === false) {
        setError(tr("imggen.mask_canvas_picture_changed"));
        return false;
      }
      return true;
    } catch {
      setError(tr("imggen.mask_canvas_save_failed"));
      return false;
    } finally {
      c.width = c.height = 0;
      setSaving(false);
    }
  };

  useImperativeHandle(ref, () => ({ save }));

  const brushHint = natural ? Math.round(brushPx(ratio, natural)) : 0;
  const radio = (on: boolean) => "ui-btn ui-btn-sm" + (on ? " ui-btn-primary" : "");

  return (
    // data-no-swipe: on a phone the swipe that rotates sessions must stand down while painting.
    <div className="igen-mask-painter" data-no-swipe="">
      <div className="igen-mask-tools" role="toolbar" aria-label={tr("imggen.mask_canvas_tools")}>
        <div className="igen-mask-group" role="radiogroup" aria-label={tr("imggen.mask_canvas_mode")}>
          {(["draw", "move"] as const).map((m) => (
            <button key={m} type="button" role="radio" aria-checked={mode === m} className={radio(mode === m)} onClick={() => setMode(m)}>
              {tr(m === "draw" ? "imggen.mask_canvas_mode_draw" : "imggen.mask_canvas_mode_move")}
            </button>
          ))}
        </div>
        <div className="igen-mask-group" role="radiogroup" aria-label={tr("imggen.mask_canvas_tool")}>
          {(["paint", "erase"] as const).map((t) => (
            <button key={t} type="button" role="radio" aria-checked={tool === t} className={radio(tool === t)} onClick={() => setTool(t)}>
              {tr(t === "paint" ? "imggen.mask_canvas_brush" : "imggen.mask_canvas_eraser")}
            </button>
          ))}
        </div>
        <div className="igen-mask-group" role="radiogroup" aria-label={tr("imggen.mask_canvas_width")}>
          {BRUSH_SIZES.map((s) => (
            <button key={s} type="button" role="radio" aria-checked={size === s} className={radio(size === s)} onClick={() => setSize(s)}>
              {tr(`imggen.mask_canvas_width_${s}` as "imggen.mask_canvas_width_thin")}
            </button>
          ))}
          {natural && <span className="igen-hint">{tr("imggen.mask_canvas_width_px", { n: brushHint })}</span>}
        </div>
        <div className="igen-mask-group">
          <IconButton icon="zoom-out" label={tr("imggen.mask_canvas_zoom_out")} disabled={!ready || view.s <= 1} onClick={() => zoomBy(1 / 1.5)} />
          <IconButton icon="zoom-in" label={tr("imggen.mask_canvas_zoom_in")} disabled={!ready} onClick={() => zoomBy(1.5)} />
          <IconButton icon="screen-normal" label={tr("imggen.mask_canvas_fit")} disabled={!ready || view.s <= 1} onClick={() => setView(FIT)} />
        </div>
        <div className="igen-mask-group">
          <Button small icon="discard" disabled={!ops.length || saving} onClick={undo}>
            {tr("imggen.mask_canvas_undo")}
          </Button>
          <Button small icon="redo" disabled={!redo.length || saving} onClick={redoOne}>
            {tr("imggen.mask_canvas_redo")}
          </Button>
          <Button small icon="clear-all" disabled={!ready || saving} onClick={() => applyOp({ kind: "clear" })}>
            {tr("imggen.mask_canvas_clear")}
          </Button>
          <Button small icon="history" disabled={!ready || saving || !ops.length} onClick={() => applyOp({ kind: "reset" })}>
            {tr("imggen.mask_canvas_reset")}
          </Button>
          <Button small icon="arrow-swap" disabled={!ready || saving} onClick={() => applyOp({ kind: "invert" })}>
            {tr("imggen.mask_canvas_invert")}
          </Button>
          {/* Held, not toggled: the mask comes back the moment the finger lifts, so nobody paints
              blind on a hidden layer. */}
          <Button
            small
            icon="eye"
            aria-pressed={peek}
            disabled={!ready}
            onPointerDown={() => setPeek(true)}
            onPointerUp={() => setPeek(false)}
            onPointerLeave={() => setPeek(false)}
            onPointerCancel={() => setPeek(false)}
            onKeyDown={(e) => {
              if (e.key === " " || e.key === "Enter") setPeek(true);
            }}
            onKeyUp={() => setPeek(false)}
            onBlur={() => setPeek(false)}
          >
            {tr("imggen.mask_canvas_peek")}
          </Button>
        </div>
      </div>
      {/* Notes scroll inside a capped strip: on a short screen (a phone on its side, a keyboard up)
          growing text must shrink nothing but the picture, never push Save off the bottom. */}
      <div className="igen-mask-notes">
        {sideways && <p className="igen-err" role="alert">{tr("imggen.mask_canvas_sideways")}</p>}
        {picError && <p className="igen-err" role="alert">{tr("imggen.mask_canvas_picture_failed")}</p>}
        {brushTooThin(ratio) && <p className="igen-warn">{tr("imggen.mask_canvas_thin")}</p>}
        {invertWarn && <p className="igen-warn">{tr("imggen.mask_canvas_invert_full")}</p>}
        {notices.map((n) => (
          <p key={n} className="igen-warn" role="status">
            {n}
          </p>
        ))}
        <p className="igen-hint">{tr("imggen.mask_canvas_hint")}</p>
      </div>
      <div className={"igen-mask-stage" + (mode === "move" ? " igen-mask-moving" : "")} ref={stageRef}>
        <div
          className="igen-mask-frame"
          ref={frameRef}
          style={{ transform: `translate(${view.tx}px, ${view.ty}px) scale(${view.s})` }}
        >
          <img
            ref={imgRef}
            className="igen-mask-picture"
            src={downloadURL(picture)}
            alt=""
            draggable={false}
            style={fitW ? { width: fitW } : { visibility: "hidden" }}
            onLoad={(e) => setNatural({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
            onError={() => setPicError(true)}
          />
          <canvas
            ref={viewRef}
            className="igen-mask-view"
            data-testid="mask-canvas"
            style={{ visibility: peek ? "hidden" : undefined }}
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerEnd}
            onPointerCancel={onPointerEnd}
            onContextMenu={(e) => e.preventDefault()}
          />
        </div>
      </div>
      <div className="igen-mask-actions">
        {error && (
          <p className="igen-err" role="alert">
            {error}
          </p>
        )}
        <Button onClick={onCancel} disabled={saving}>
          {tr("imggen.mask_canvas_cancel")}
        </Button>
        <Button variant="primary" icon="save" disabled={!ready || saving} onClick={() => void save()}>
          {tr(fullAck ? "imggen.mask_canvas_save_anyway" : "imggen.mask_canvas_save")}
        </Button>
      </div>
    </div>
  );
}
