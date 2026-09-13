import { useEffect, useRef, useState } from "react";
import type { PointerEvent as RPointerEvent, MouseEvent as RMouseEvent } from "react";
import { ImageView, type ImageViewHandle } from "./ImageView.tsx";
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
}

export function ImageLightbox({ src, onClose, alt, onPrev, onNext, index, total, onOpenFolder }: Props) {
  const tr = useT();
  const view = useRef<ImageViewHandle>(null);
  const [scale, setScale] = useState(1);
  const down = useRef<{ x: number; y: number } | null>(null);
  const paging = !!onPrev || !!onNext;

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
  };

  const onClick = (e: RMouseEvent) => {
    const from = down.current;
    down.current = null;
    if (from && Math.hypot(e.clientX - from.x, e.clientY - from.y) > DRAG_SLOP) return; // a pan, not a click
    const el = e.target as HTMLElement | null;
    if (el?.closest(".imgview-img, .mirror-lightbox-bar")) return; // the image and the controls keep their clicks
    onClose();
  };

  return (
    // data-no-swipe: on a phone the whole overlay owns horizontal drags (panning a zoomed
    // image), so the swipe that rotates sessions must stand down while it is open.
    <div
      className="mirror-lightbox"
      data-no-swipe=""
      onPointerDown={onPointerDown}
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
      <ImageView ref={view} src={src} alt={alt || tr("mirror.pasted_image_zoom")} onZoom={setScale} />
    </div>
  );
}
