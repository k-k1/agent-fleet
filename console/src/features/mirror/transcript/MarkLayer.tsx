// transcript/MarkLayer - highlight a selection, and inspect or remove an existing highlight
// (docs/log/69).
//
// A single floating layer mounted once for the whole transcript. It is not placed per turn because
// selection and click are document-level events, and one subscription per turn would mean 400
// listeners on a long transcript.
//
// Placement is SelectionFloat's job: floating by the selection on a mouse, docked to the bottom
// edge on a touch device, where the browser's own selection menu owns the space above the
// selection. Selections are captured through useSelectionCapture (a touch long-press selection
// emits no mouseup, so mouseup alone would miss it) — same as plan comments.

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../../ui/Icon.tsx";
import { SelectionFloat } from "../../../ui/SelectionFloat.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { placeFixed } from "../../../lib/placeFixed.ts";
import { useSelectionCapture } from "../../../lib/selectionCapture.ts";
import { selectionAnchor } from "../../viewer/quoteMarks.ts";
import { MARK_CLASS } from "./markPaint.ts";
import { MARKABLE_KINDS, MARK_COLORS, parseRootKey, type MarkColor, type TranscriptMark } from "./marks.ts";
import type { TranscriptMarksWiring } from "./useMarks.ts";
import { formatTS } from "./blocks.tsx";

// Look colour names up in a table rather than concatenating them, so the type system can keep
// every t() key a literal.
const COLOR_LABEL: Record<MarkColor, () => string> = {
  yellow: () => tr("mirror.mark.color.yellow"),
  green: () => tr("mirror.mark.color.green"),
  blue: () => tr("mirror.mark.color.blue"),
  pink: () => tr("mirror.mark.color.pink"),
};

/** Show the pill slightly above the selection. */
const PILL_OFFSET = 40;

interface Draft {
  root: string;
  kind: string;
  quote: string;
  nth: number;
  x: number;
  y: number;
}

interface Card {
  mark: TranscriptMark;
  x: number;
  y: number;
}

/** The markable element containing both ends of the selection. null if either end is outside it
 * (a selection whose quote spans more than one root). */
function rootOfSelection(): HTMLElement | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const range = sel.getRangeAt(0);
  const from = range.startContainer;
  const el = from.nodeType === Node.ELEMENT_NODE ? (from as Element) : from.parentElement;
  const root = el?.closest<HTMLElement>("[data-mark-root]") || null;
  if (!root || !root.contains(range.endContainer)) return null;
  return root;
}

export function MarkLayer({ marks }: { marks: TranscriptMarksWiring }) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [card, setCard] = useState<Card | null>(null);
  const cardRef = useRef<HTMLDivElement>(null);

  const capture = useCallback(() => {
    if (!marks.canEdit) return;
    const root = rootOfSelection();
    if (!root) {
      setDraft(null);
      return;
    }
    const kind = root.dataset.markKind ?? "";
    // The markable kinds are closed by the same table as on the Agent side (docs/log/69 §69.4).
    // Even as more places become drawable, never offer this over a part that carries coordinates.
    if (!MARKABLE_KINDS.has(kind)) {
      setDraft(null);
      return;
    }
    const anchor = selectionAnchor(root);
    if (!anchor) {
      setDraft(null);
      return;
    }
    setDraft({
      root: root.dataset.markRoot || "",
      kind,
      quote: anchor.quote,
      nth: anchor.nth,
      x: Math.round(anchor.rect.left),
      y: Math.round(anchor.rect.top - PILL_OFFSET),
    });
  }, [marks]);

  useSelectionCapture(capture);

  // Clicking an existing highlight shows who drew it and when (plus a remove action for whoever
  // may remove it).
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      const el = (e.target as Element | null)?.closest?.<HTMLElement>("mark." + MARK_CLASS);
      if (!el) {
        if (!(e.target as Element | null)?.closest?.(".tmark-card")) setCard(null);
        return;
      }
      const mark = marks.find(el.dataset.markId || "");
      if (!mark) return;
      const rect = el.getBoundingClientRect();
      setCard({ mark, x: Math.round(rect.left), y: Math.round(rect.bottom + 6) });
    };
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, [marks]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setDraft(null);
      setCard(null);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // The card is measured after it renders and then nudged into place, so one opened at the right
  // edge cannot push its buttons off screen. (The pill's own placement is SelectionFloat's.) The
  // card is not selection-driven — tapping an existing mark makes no selection, so the native
  // selection menu is not in play here.
  useEffect(() => {
    if (card && cardRef.current) placeFixed(cardRef.current, card.x, card.y);
  }, [card]);

  const paint = (color: MarkColor) => {
    if (!draft) return;
    const parsed = parseRootKey(draft.root);
    if (parsed) {
      marks.add({ turn: parsed.turn, part: parsed.part, kind: draft.kind, quote: draft.quote, nth: draft.nth, color });
    }
    setDraft(null);
    window.getSelection()?.removeAllRanges();
  };

  return createPortal(
    <>
      {draft && (
        <SelectionFloat x={draft.x} y={draft.y} className="tmark-pill" role="group" aria-label={tr("mirror.mark.pill")}>
          {MARK_COLORS.map((c) => (
            <button
              key={c}
              type="button"
              className={"tmark-swatch tmark-" + c}
              title={tr("mirror.mark.paint", { color: COLOR_LABEL[c]() })}
              aria-label={tr("mirror.mark.paint", { color: COLOR_LABEL[c]() })}
              onMouseDown={(e) => e.preventDefault()} // let the click happen without losing the selection
              onClick={() => paint(c)}
            />
          ))}
        </SelectionFloat>
      )}
      {card && (
        <div className="tmark-card" ref={cardRef}>
          <div className="tmark-card-who">
            <span className={"tmark-dot tmark-a" + marks.authorSlot(card.mark.author)} />
            {marks.authorLabel(card.mark.author)}
            {card.mark.created_at ? (
              <span className="muted"> · {formatTS(new Date(card.mark.created_at).toISOString())}</span>
            ) : null}
          </div>
          <div className="tmark-card-quote">{card.mark.quote}</div>
          {marks.canRemove(card.mark) && (
            <button
              type="button"
              className="ghost xs tmark-card-del"
              onClick={() => {
                marks.remove(card.mark.id);
                setCard(null);
              }}
            >
              <Icon name="trash" /> {tr("mirror.mark.remove")}
            </button>
          )}
        </div>
      )}
    </>,
    document.body,
  );
}
