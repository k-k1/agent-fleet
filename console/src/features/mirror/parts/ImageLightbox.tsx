import { useRef, useState } from "react";
import type { PointerEvent as RPointerEvent, MouseEvent as RMouseEvent } from "react";
import { ImageView, type ImageViewHandle } from "../../viewer/ImageView.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { useEscLayer } from "../../../lib/escLayer.ts";
import { useT } from "../../../lib/i18n/index.ts";

// ImageLightbox — the enlarged image over the transcript. The zoom itself is the
// shared viewer ImageView (wheel / pinch / double-click, drag to pan while zoomed),
// so an image opened from a card behaves exactly like one opened in the file pane.
//
// That costs the old "click anywhere to close": a click on the image is now the first
// half of the double-click that zooms, so closing it there would make zoom unreachable.
// Closing is the backdrop around the image, the ✕, Escape and Back (useBackClose, in
// MirrorView). A pan that ends over the backdrop must not close either — hence the
// drag test below.
const STEP = 1.4; // per button press; the wheel stays continuous
const DRAG_SLOP = 6; // px of pointer travel that turns a click into a drag

interface Props {
  src: string;
  onClose: () => void;
}

export function ImageLightbox({ src, onClose }: Props) {
  const tr = useT();
  const view = useRef<ImageViewHandle>(null);
  const [scale, setScale] = useState(1);
  const down = useRef<{ x: number; y: number } | null>(null);

  useEscLayer(onClose);

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
        <button type="button" onClick={onClose} title={tr("common.close")}>
          <Icon name="close" />
        </button>
      </div>
      <ImageView ref={view} src={src} alt={tr("mirror.pasted_image_zoom")} onZoom={setScale} />
    </div>
  );
}
