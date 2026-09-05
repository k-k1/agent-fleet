// transcript/markPaint — paints marks onto the real DOM (docs/log/69 / ADR 0050).
//
// The splicing itself lives in features/viewer/quoteMarks.ts (shared with plan comments). This
// module only decides whether to repaint a whole turn or do nothing.
//
// That decision is needed because the transcript re-renders on every poll. Repainting every turn
// every time means 400 turns' worth of DOM rewriting per second; repainting only "when the marks
// changed" loses them for good on the renders where MarkdownView rebuilt the body (a theme
// change, say). So the decision reads three things: the marks, the body's length, and how many
// marks are actually mounted right now.

import { applyPaintedMarks } from "../../viewer/quoteMarks.ts";
import type { TranscriptMark } from "./marks.ts";

export const MARK_CLASS = "tmark";
const MARK_SELECTOR = "mark." + MARK_CLASS;

function classOf(m: TranscriptMark, slot: number): string {
  return `${MARK_CLASS} ${MARK_CLASS}-${m.color} ${MARK_CLASS}-a${slot}`;
}

/**
 * Repaints every [data-mark-root] under `body`. The return value is the signature used for the
 * next decision: an unchanged signature means the caller can skip the repaint.
 */
export function paintTurnMarks(
  body: HTMLElement,
  byRoot: Map<string, TranscriptMark[]>,
  authorSlot: (author: string | undefined) => number,
  prevSignature: string,
): string {
  const roots = [...body.querySelectorAll<HTMLElement>("[data-mark-root]")];
  if (!roots.length) return "";
  const work: Array<{ el: HTMLElement; list: TranscriptMark[] }> = [];
  let want = 0;
  let sig = "";
  for (const el of roots) {
    const list = byRoot.get(el.dataset.markRoot || "") || [];
    want += list.length;
    if (list.length) {
      sig += list.map((m) => m.id + ":" + m.color + ":" + m.nth).join(",") + ";";
      sig += (el.textContent || "").length + "|";
    }
    work.push({ el, list });
  }
  // A turn with no marks is left alone when nothing is mounted on it (the vast majority).
  const have = body.querySelectorAll(MARK_SELECTOR).length;
  if (want === 0 && have === 0) return "";
  // Repaint even on an unchanged signature when the body was rebuilt and lost its marks.
  if (sig === prevSignature && !(want > 0 && have === 0)) return sig;
  for (const { el, list } of work) {
    applyPaintedMarks(
      el,
      list.map((m) => ({
        quote: m.quote,
        nth: m.nth,
        className: classOf(m, authorSlot(m.author)),
        dataset: { markId: m.id },
      })),
      MARK_SELECTOR,
    );
  }
  return sig;
}
