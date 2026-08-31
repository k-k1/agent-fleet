import type { EditorState } from "@codemirror/state";

// selection — the editing surface's answers to the two questions the read-only
// CodeView used to answer from the DOM (docs/log/44 §1.8).
//
// Both are derived from the CodeMirror document, never from the rendered DOM or
// window.getSelection(): CodeMirror virtualises rows, so a selection that runs
// past the rendered range exists in the state but only partially in the DOM.
// Reading it from the DOM silently truncates the quote.

export interface EditorSelectionRange {
  /** The selected text, sliced from the document. */
  quote: string;
  /** 1-based, inclusive. */
  startLine: number;
  /** 1-based, inclusive. */
  endLine: number;
  /** Document offset of the selection start, for positioning UI beside it. */
  from: number;
}

/** A selection, plus where to put UI beside it. `coords` is null when the
 *  selection start is scrolled out of the rendered range. */
export interface EditorSelectionReport extends EditorSelectionRange {
  coords: { left: number; top: number } | null;
  /** `selection` when the user moved the selection or edited the document,
   *  `geometry` when only the layout moved and the same selection is being
   *  re-measured. A re-measurement must not be mistaken for a new selection. */
  reason: "selection" | "geometry";
}

/** The main selection as line numbers + text, or null when there is nothing
 *  quotable (an empty selection, or only whitespace). */
export function selectionRangeOf(state: EditorState): EditorSelectionRange | null {
  const { from, to } = state.selection.main;
  if (from === to) return null;
  const quote = state.sliceDoc(from, to);
  if (!quote.trim()) return null;
  return {
    quote,
    startLine: state.doc.lineAt(from).number,
    endLine: state.doc.lineAt(to).number,
    from,
  };
}

/** Document offset of the start of a 1-based line, clamped into the document.
 *  Out-of-range citations (a file that shrank since the link was made) land on
 *  the nearest line instead of failing. */
export function lineStartOf(state: EditorState, line: number): number {
  const clamped = Math.min(Math.max(Math.trunc(line), 1), state.doc.lines);
  return state.doc.line(clamped).from;
}
