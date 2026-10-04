// MaskEntry — the inpaint mask's row above the press buttons: which mask the next run uses, the
// way to paint one, and the question when the picture under an existing mask changes. It sits
// outside the folded Advanced section on purpose: an inpaint cannot run without a mask, so the
// way to make one has to be in view. The path field itself stays under Advanced.
import { useEffect, useRef, useState } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";
import type { ImagegenDraft } from "../draft.ts";

export function MaskEntry({
  draft,
  patch,
  offer,
  onPaint,
}: {
  draft: Pick<ImagegenDraft, "op" | "inputs" | "mask">;
  patch: (p: Partial<ImagegenDraft>) => void;
  /** maskCanvasOffer's answer for the selected engine and model. */
  offer: "ok" | "engine" | "model";
  /** Open the canvas on this picture, with `mask` as the underlay. Absent: no canvas here at all. */
  onPaint?: (picture: string, mask: string) => void;
}) {
  const tr = useT();
  // The first reference decides the output's frame, so it is the one painted over.
  const picture = draft.inputs[0]?.trim() || "";
  const mask = draft.mask.trim();
  const canPaint = offer === "ok" && !!onPaint;

  // A mask is painted for one picture. When the first reference changes under it (a new upload,
  // a reorder, the agent), the mask still points at the old picture's regions: ask, do not guess.
  // Until the member answers, the mask is HELD out of the draft, so every press path — the form,
  // the draft bar, the lightbox's seeded trial, Ctrl+Enter — meets "inpaint needs a mask" instead
  // of running the old picture's mask. `held` is the old path; any new mask (kept, repainted and
  // saved, or typed into the path field) settles the question.
  const prev = useRef(picture);
  // True from asking the draft to drop the mask until it has: the old mask still in the draft
  // during that gap is not an answer.
  const clearing = useRef(false);
  const [held, setHeld] = useState("");
  useEffect(() => {
    if (prev.current === picture) return;
    const was = prev.current;
    prev.current = picture;
    if (was && mask && draft.op === "inpaint") {
      setHeld(mask);
      clearing.current = true;
      patch({ mask: "" });
    }
  }, [picture, mask, draft.op, patch]);
  useEffect(() => {
    if (clearing.current) {
      if (!mask) clearing.current = false;
      return;
    }
    if (held && mask) setHeld("");
  }, [held, mask]);
  const ask = !!held;

  if (draft.op !== "inpaint") return null;
  return (
    <div className="igen-mask-entry">
      <div className="igen-row">
        <span className="igen-label">{tr("imggen.mask_entry")}</span>
        <span className="igen-mask-current" title={mask || undefined}>
          {mask ? mask.split("/").pop() : tr("imggen.mask_entry_none")}
        </span>
        {canPaint && (
          <Button small icon="edit" disabled={!picture} onClick={() => onPaint!(picture, mask || held)}>
            {tr("imggen.mask_canvas_open")}
          </Button>
        )}
      </div>
      {canPaint && !picture && <span className="igen-hint">{tr("imggen.mask_canvas_needs_picture")}</span>}
      {offer === "engine" && <span className="igen-hint">{tr("imggen.mask_canvas_engine_only")}</span>}
      {offer === "model" && <span className="igen-hint">{tr("imggen.mask_canvas_model_only")}</span>}
      {ask && (
        <div className="igen-mask-ask" role="group" aria-label={tr("imggen.mask_input_changed")}>
          <span>{tr("imggen.mask_input_changed")}</span>
          <Button small onClick={() => patch({ mask: held })}>
            {tr("imggen.mask_input_keep")}
          </Button>
          {/* The question stays until a new mask is saved: cancelling the canvas leaves it here. */}
          {canPaint && picture && (
            <Button small onClick={() => onPaint!(picture, held)}>
              {tr("imggen.mask_input_repaint")}
            </Button>
          )}
          <Button small variant="danger" onClick={() => setHeld("")}>
            {tr("imggen.mask_input_clear")}
          </Button>
        </div>
      )}
    </div>
  );
}
