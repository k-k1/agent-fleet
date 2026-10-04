// MaskCanvasModal — the dialog shell around MaskPainter (log 111 §3: painting is one focused task).
// All painting lives in the painter; the shell adds the title, the close paths (×, backdrop, Esc,
// the browser's back), the question those ask while strokes are unsaved, and the lock that keeps
// the dialog open while a save is uploading.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { Modal } from "../../../ui/Modal.tsx";
import { Button } from "../../../ui/Button.tsx";
import { useBackClose } from "../../../lib/backClose.ts";
import type { ImagegenModel, ImagegenProvider } from "../wire.ts";
import { MaskPainter, type MaskPainterHandle, type MaskPainterProps } from "./MaskPainter.tsx";

/**
 * Whether the canvas may be offered: "ok", or why not. Only where the mask's meaning is measured —
 * every ComfyUI family stretches it over the whole frame after exif_transpose (log 111 §10.1) —
 * and only for a model that declares inpaint. openai_compat forwards the mask untouched to a
 * server nobody has measured (§10.8), so there the path field is the only way in.
 */
export function maskCanvasOffer(provider: ImagegenProvider | null, model: ImagegenModel | null): "ok" | "engine" | "model" {
  if (provider?.kind !== "comfy") return "engine";
  if (!model?.ops?.includes("inpaint")) return "model";
  return "ok";
}

type ShellProps = Omit<MaskPainterProps, "onCancel" | "onBusy" | "onDirty" | "ref"> & { onClose: () => void };

export function MaskCanvasModal({ onClose, ...painter }: ShellProps) {
  const tr = useT();
  const handle = useRef<MaskPainterHandle>(null);
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [asking, setAsking] = useState(false);

  // A focused field behind the dialog keeps the phone's keyboard up over the canvas.
  useLayoutEffect(() => {
    const el = document.activeElement as HTMLElement | null;
    if (el && el !== document.body && el.matches("input, textarea, select, [contenteditable]")) el.blur();
  }, []);

  const requestClose = () => {
    if (busy) return;
    if (dirty) setAsking(true);
    else onClose();
  };

  // The browser's back is guarded here, not by Modal: Modal drops its history entry while
  // `lockClose` is on, so a back press during an upload left the page with the drawing on it.
  // Every back press uses up the entry, so it is re-armed at once (off for one render, then on,
  // which pushes a fresh one); while uploading or with unsaved strokes, back never leaves.
  const [armed, setArmed] = useState(true);
  useEffect(() => {
    if (!armed) setArmed(true);
  }, [armed]);
  useBackClose(() => {
    setArmed(false);
    requestClose();
  }, armed);

  return (
    <Modal
      title={tr("imggen.mask_canvas_title")}
      onClose={requestClose}
      lockClose={busy}
      backClose={false}
      className="igen-mask-modal"
    >
      <div className="ui-modal-body igen-mask-body">
        {asking && (
          <div className="igen-mask-ask" role="alertdialog" aria-label={tr("imggen.mask_canvas_unsaved")}>
            <span>{tr("imggen.mask_canvas_unsaved")}</span>
            <Button
              variant="primary"
              small
              disabled={busy}
              onClick={async () => {
                setAsking(false);
                await handle.current?.save();
              }}
            >
              {tr("imggen.mask_canvas_save_close")}
            </Button>
            <Button variant="danger" small onClick={onClose}>
              {tr("imggen.mask_canvas_discard")}
            </Button>
            <Button small onClick={() => setAsking(false)}>
              {tr("imggen.mask_canvas_keep")}
            </Button>
          </div>
        )}
        <MaskPainter {...painter} ref={handle} onCancel={requestClose} onBusy={setBusy} onDirty={setDirty} />
      </div>
    </Modal>
  );
}
