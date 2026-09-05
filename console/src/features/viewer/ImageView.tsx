import { useCallback, useEffect, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, PointerEvent as RPointerEvent } from "react";
import { useT } from "../../lib/i18n/index.ts";

// ImageView previews a single image (CodeLeaf-style affordances): the image is
// fit to the viewport, the wheel / pinch zooms (anchored at the pointer), drag
// pans while zoomed, and double-click toggles 1× ↔ 2.5×. A small overlay shows
// the zoom level with a reset when not at fit. `src` is the raw-bytes URL (the
// download endpoint); the browser sniffs and decodes it regardless of the
// attachment Content-Type. `onLoad` reports the natural pixel size to the caller
// so the info bar can show W×H.
const MIN = 1;
const MAX = 8;

const clamp = (v: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, v));

interface ImageViewProps {
  src: string;
  alt?: string;
  onLoad?: (size: { w: number; h: number }) => void;
}

// A point relative to the box center, derived from an event carrying client coords.
type ClientPoint = { clientX: number; clientY: number };

// The full transform as one state value so a zoom updates scale+pan atomically
// (no side-effecting setState inside another setState's updater).
interface Transform {
  scale: number;
  tx: number;
  ty: number;
}
const FIT: Transform = { scale: 1, tx: 0, ty: 0 };

export function ImageView({ src, alt, onLoad }: ImageViewProps) {
  const tr = useT();
  const boxRef = useRef<HTMLDivElement>(null);
  const [t, setT] = useState<Transform>(FIT);
  const { scale, tx, ty } = t;
  const [broken, setBroken] = useState(false);

  // Active pointers (id -> {x,y}) drive drag (one pointer) and pinch (two).
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ dist: number; scale: number } | null>(null); // captured when the 2nd pointer lands

  // Reset transform whenever the image source changes.
  useEffect(() => {
    setT(FIT);
    setBroken(false);
  }, [src]);

  // Pan is clamped so the (assumed viewport-filling) image can't be dragged off
  // past its own edges. Letterboxed images may over-pan slightly; harmless.
  const clampPan = useCallback((s: number, x: number, y: number) => {
    const box = boxRef.current;
    if (!box) return { x, y };
    const mx = ((s - 1) * box.clientWidth) / 2;
    const my = ((s - 1) * box.clientHeight) / 2;
    return { x: clamp(x, -mx, mx), y: clamp(y, -my, my) };
  }, []);

  // Zoom toward a screen point (relative to the box center) so that point stays
  // put — the standard anchored-zoom math for `translate() scale()`.
  const applyZoom = useCallback(
    (prev: Transform, nextScale: number, cx: number, cy: number): Transform => {
      const s = clamp(nextScale, MIN, MAX);
      const px = (cx - prev.tx) / prev.scale;
      const py = (cy - prev.ty) / prev.scale;
      const next = clampPan(s, cx - s * px, cy - s * py);
      return { scale: s, tx: next.x, ty: next.y };
    },
    [clampPan],
  );
  const zoomTo = useCallback(
    (nextScale: number, cx: number, cy: number) => setT((prev) => applyZoom(prev, nextScale, cx, cy)),
    [applyZoom],
  );

  const pointFromEvent = useCallback((e: ClientPoint) => {
    const box = boxRef.current;
    if (!box) return { x: 0, y: 0 };
    const r = box.getBoundingClientRect();
    return { x: e.clientX - r.left - r.width / 2, y: e.clientY - r.top - r.height / 2 };
  }, []);

  // Wheel zoom must swallow the scroll, but React's onWheel is registered passive
  // (preventDefault is a no-op) — attach a native non-passive listener instead
  // (same pattern as ReaderView). Re-attach when the box remounts (`broken` flips).
  useEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const { x, y } = pointFromEvent(e);
      setT((prev) => applyZoom(prev, prev.scale * Math.exp(-e.deltaY * 0.0015), x, y));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [broken, applyZoom, pointFromEvent]);

  const onDoubleClick = (e: RMouseEvent) => {
    const { x, y } = pointFromEvent(e);
    if (scale > 1) {
      setT(FIT);
    } else {
      zoomTo(2.5, x, y);
    }
  };

  const onPointerDown = (e: RPointerEvent) => {
    boxRef.current?.setPointerCapture?.(e.pointerId);
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()];
      pinch.current = { dist: Math.hypot(a.x - b.x, a.y - b.y), scale };
    }
  };

  const onPointerMove = (e: RPointerEvent) => {
    const prev = pointers.current.get(e.pointerId);
    if (!prev) return;
    const cur = { x: e.clientX, y: e.clientY };
    pointers.current.set(e.pointerId, cur);

    if (pointers.current.size >= 2 && pinch.current) {
      // Pinch: scale by the change in finger distance, anchored at the midpoint.
      const [a, b] = [...pointers.current.values()];
      const dist = Math.hypot(a.x - b.x, a.y - b.y);
      const box = boxRef.current?.getBoundingClientRect();
      if (!box) return;
      const mx = (a.x + b.x) / 2 - box.left - box.width / 2;
      const my = (a.y + b.y) / 2 - box.top - box.height / 2;
      zoomTo((pinch.current.scale * dist) / pinch.current.dist, mx, my);
      return;
    }
    if (scale > 1) {
      const dx = cur.x - prev.x;
      const dy = cur.y - prev.y;
      setT((p) => {
        const next = clampPan(p.scale, p.tx + dx, p.ty + dy);
        return { ...p, tx: next.x, ty: next.y };
      });
    }
  };

  const endPointer = (e: RPointerEvent) => {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size < 2) pinch.current = null;
  };

  const reset = () => setT(FIT);

  if (broken) return <div className="imgview muted">{tr("view.cannot_show_image")}</div>;

  return (
    <div
      ref={boxRef}
      className={"imgview" + (scale > 1 ? " zoomed" : "")}
      // While zoomed, a horizontal drag pans the image, so the phone's swipe-to-rotate
      // must stand down (swipeGuard.ts): panning a zoomed image used to switch session
      // out from under the finger. The pan is a CSS transform, not a scroll container,
      // so the guard cannot detect it on its own. At fit nothing here consumes the
      // drag, and the swipe keeps working.
      {...(scale > 1 ? { "data-no-swipe": "" } : {})}
      onDoubleClick={onDoubleClick}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endPointer}
      onPointerCancel={endPointer}
    >
      <img
        className="imgview-img"
        src={src}
        alt={alt}
        draggable={false}
        onLoad={(e) => onLoad?.({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
        onError={() => setBroken(true)}
        style={{ transform: `translate(${tx}px, ${ty}px) scale(${scale})` }}
      />
      {scale > 1 && (
        <button type="button" className="imgview-zoom" onClick={reset} title={tr("view.reset_to_fit")}>
          {Math.round(scale * 100)}%
        </button>
      )}
    </div>
  );
}
