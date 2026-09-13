import { useEffect, useRef, useState } from "react";
import type { PointerEvent as RPointerEvent, MouseEvent as RMouseEvent } from "react";
import { ImageView, type ImageViewHandle } from "./ImageView.tsx";
import { ImageProps } from "./ImageProps.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useT } from "../../lib/i18n/index.ts";

// ImageLightbox — one enlarged image over whatever opened it: the mirror's transcript
// cards and the gallery grid (ADR 0080 decision 5). The zoom itself is the shared viewer
// ImageView (wheel / pinch / double-click, drag to pan while zoomed), so an image opened
// from a card behaves exactly like one opened in the file pane.
//
// That costs the old "click anywhere to close": a click on the image is now the first
// half of the double-click that zooms, so closing it there would make zoom unreachable.
// Closing is the backdrop around the image, the ✕, Escape and Back. A pan that ends over
// the backdrop must not close either — hence the drag test below.
//
// Back (`useBackClose`) is deliberately NOT in here: the host owns it, and both hosts
// must wire it (MirrorView does, GalleryView does). Without it a phone's Back press
// jumps past the lightbox and navigates the pane away underneath it.
//
// Paging and "open the folder" are the host's too, and each control appears only when
// its callback is passed — the mirror passes neither, so its bar is unchanged.
//
// The `mirror-lightbox` class names stay as they were when this lived under
// features/mirror: they are what the (moved) stylesheet and the phone-width override
// are written against, and renaming them would be a restyle disguised as a move.
const STEP = 1.4; // per button press; the wheel stays continuous
const DRAG_SLOP = 6; // px of pointer travel that turns a click into a drag
const SWIPE_MIN = 48; // px of touch travel that counts as "next / previous", not a stray finger

interface Props {
  src: string;
  onClose: () => void;
  /** Alt text; defaults to the mirror's "enlarged image" wording. */
  alt?: string;
  /** Paging, when the host has an ordered list. Both the buttons and the ←/→ keys
   *  appear with them; an end of the list is expressed by passing nothing. */
  onPrev?: () => void;
  onNext?: () => void;
  /** 1-based position in that list, shown as "3 / 12". Needs both to render. */
  index?: number;
  total?: number;
  /** Show this image's folder as a gallery. Absent = no button (the gallery itself
   *  is already looking at the folder). */
  onOpenFolder?: () => void;
  /**
   * The picture's browse-root-relative PATH, which `src` (a download URL) is not (ADR 0081
   * decision 3). Every host holds it — the gallery, the mirror's file card, the studio — and
   * passing it is what turns on the properties toggle. Absent = no toggle at all: without a
   * path there is nothing to ask `GET api/imagegen/props` about.
   */
  path?: string;
}

export function ImageLightbox({ src, onClose, alt, onPrev, onNext, index, total, onOpenFolder, path }: Props) {
  const tr = useT();
  const view = useRef<ImageViewHandle>(null);
  const [scale, setScale] = useState(1);
  const [showProps, setShowProps] = useState(false);
  const down = useRef<{ x: number; y: number } | null>(null);
  // Where a TOUCH went down, kept apart from `down`: that one decides click vs pan for
  // every pointer, this one exists only for the swipe and only on a finger.
  const swipe = useRef<{ x: number; y: number } | null>(null);
  const paging = !!onPrev || !!onNext;

  // Paging to another picture closes the panel: it is read on open only, and leaving it up
  // would show the previous image's seed under the new one — the one failure a properties
  // panel must not have.
  useEffect(() => setShowProps(false), [path]);

  useEscLayer(onClose);

  // ←/→ page through the host's list. Read through a ref-free effect that re-registers
  // on every change of the callbacks: they are recreated per render anyway (they close
  // over the current index), and a stale one would page from the wrong position.
  useEffect(() => {
    if (!paging) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.key === "ArrowLeft" && onPrev) onPrev();
      else if (e.key === "ArrowRight" && onNext) onNext();
      else return;
      e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [paging, onPrev, onNext]);

  const onPointerDown = (e: RPointerEvent) => {
    down.current = { x: e.clientX, y: e.clientY };
    swipe.current = e.pointerType === "touch" ? { x: e.clientX, y: e.clientY } : null;
  };

  /**
   * Swipe left / right pages, on TOUCH and only while the picture is at fit.
   *
   * Both halves of that sentence are load-bearing. Zoomed in, a horizontal drag is the pan
   * that ImageView owns, and stealing it would make a picture larger than the screen
   * unreadable. On a mouse, a horizontal drag is not a gesture anyone means — the buttons
   * and ←/→ are already there — so hijacking it would only produce accidental paging.
   *
   * The overlay already carries `data-no-swipe`, so the session-rotation swipe is standing
   * down while this is open (ADR 0080 decision 5 rejected paging by swipe for P0 exactly
   * because of that tug of war; inside the overlay there is no contest).
   */
  const onPointerUp = (e: RPointerEvent) => {
    const from = swipe.current;
    swipe.current = null;
    if (!from || !paging || scale > 1) return;
    const dx = e.clientX - from.x;
    const dy = e.clientY - from.y;
    // Horizontal intent, not a scroll that drifted: past the threshold AND mostly sideways.
    if (Math.abs(dx) < SWIPE_MIN || Math.abs(dx) < Math.abs(dy) * 1.5) return;
    if (dx > 0) onPrev?.();
    else onNext?.();
  };

  const onClick = (e: RMouseEvent) => {
    const from = down.current;
    down.current = null;
    if (from && Math.hypot(e.clientX - from.x, e.clientY - from.y) > DRAG_SLOP) return; // a pan, not a click
    const el = e.target as HTMLElement | null;
    if (el?.closest(".imgview-img, .mirror-lightbox-bar, .imgprops")) return; // the image, the controls and the properties panel keep their clicks
    onClose();
  };

  return (
    // data-no-swipe: on a phone the whole overlay owns horizontal drags (panning a zoomed
    // image), so the swipe that rotates sessions must stand down while it is open.
    <div
      className="mirror-lightbox"
      data-no-swipe=""
      onPointerDown={onPointerDown}
      onPointerUp={onPointerUp}
      onPointerCancel={() => (swipe.current = null)}
      onClick={onClick}
      role="presentation"
    >
      <div className="mirror-lightbox-bar">
        {paging && (
          <>
            <button
              type="button"
              className="mirror-lightbox-prev"
              onClick={() => onPrev?.()}
              disabled={!onPrev}
              title={tr("gallery.prev")}
              aria-label={tr("gallery.prev")}
            >
              <Icon name="chevron-left" />
            </button>
            {index != null && total != null && (
              <span className="mirror-lightbox-pos">{tr("gallery.position", { index, total })}</span>
            )}
            <button
              type="button"
              className="mirror-lightbox-next"
              onClick={() => onNext?.()}
              disabled={!onNext}
              title={tr("gallery.next")}
              aria-label={tr("gallery.next")}
            >
              <Icon name="chevron-right" />
            </button>
          </>
        )}
        <button
          type="button"
          onClick={() => view.current?.zoomBy(1 / STEP)}
          disabled={scale <= 1}
          title={tr("view.zoom_out")}
        >
          <Icon name="dash" />
        </button>
        <button
          type="button"
          className="mirror-lightbox-level"
          onClick={() => view.current?.reset()}
          title={tr("view.reset_to_fit")}
        >
          {Math.round(scale * 100)}%
        </button>
        <button type="button" onClick={() => view.current?.zoomBy(STEP)} title={tr("view.zoom_in")}>
          <Icon name="add" />
        </button>
        {path && (
          <button
            type="button"
            className={"mirror-lightbox-props" + (showProps ? " on" : "")}
            aria-pressed={showProps}
            onClick={() => setShowProps((v) => !v)}
            title={tr("imggen.props_toggle")}
            aria-label={tr("imggen.props_toggle")}
          >
            <Icon name="info" />
          </button>
        )}
        {onOpenFolder && (
          <button
            type="button"
            className="mirror-lightbox-folder"
            onClick={onOpenFolder}
            title={tr("gallery.open_folder")}
            aria-label={tr("gallery.open_folder")}
          >
            <Icon name="folder" />
          </button>
        )}
        <button type="button" onClick={onClose} title={tr("common.close")}>
          <Icon name="close" />
        </button>
      </div>
      {path && showProps && <ImageProps path={path} />}
      <ImageView ref={view} src={src} alt={alt || tr("mirror.pasted_image_zoom")} onZoom={setScale} />
    </div>
  );
}
