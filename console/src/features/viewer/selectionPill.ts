import type { EditorSelectionReport } from "../editor/selection.ts";

// selectionPill — who owns the floating "send" pill (「送る」) (docs/log/44 §1.8).
//
// Two surfaces can produce a selection: the read-only grid, whose selection is
// read by walking the DOM, and the editing surface, which reports its own from
// the CodeMirror document. Exactly one pill may be on screen, so each surface
// may only clear or replace a pill it owns.

export interface SelectionPill {
  quote: string;
  startLine: number;
  endLine: number;
  /** Viewport position of the pill. */
  x: number;
  y: number;
  origin: "view" | "editor";
}

/** Vertical offset that lifts the pill clear of the selected text. */
const PILL_OFFSET = 34;

/** The pill after the editing surface reported `report`.
 *
 *  `editorVisible` is false while the surface is hidden: CodeMirror keeps its
 *  selection and still reports layout changes there, but nothing it says can put
 *  a pill on screen. */
export function editorPill(
  previous: SelectionPill | null,
  report: EditorSelectionReport | null,
  editorVisible: boolean,
): SelectionPill | null {
  const owned = previous?.origin === "editor";
  if (!editorVisible || !report || !report.coords) return owned ? null : previous;
  // Re-measuring an unchanged selection after a scroll or resize may move this
  // surface's own pill, but must never take the pill back from a selection made
  // elsewhere — losing that is how two surfaces end up fighting over one pill.
  if (report.reason === "geometry" && !owned) return previous;
  return {
    quote: report.quote,
    startLine: report.startLine,
    endLine: report.endLine,
    x: Math.round(report.coords.left),
    y: Math.round(report.coords.top - PILL_OFFSET),
    origin: "editor",
  };
}
