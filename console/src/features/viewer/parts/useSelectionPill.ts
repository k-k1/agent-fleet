// State of the Send / Read out pill floating by the selection. There are two capture paths
// (a DOM walk of the read-only grid, and CodeMirror's own report), so `origin` records which
// one touched it last (docs/log/44 §1.8).
//
// The order in which these two effects register on the surface is load-bearing; do not move
// the call site in FileView.
import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { lineRangeOfSelection } from "./fileDom.ts";
import { editorPill, type SelectionPill } from "../selectionPill.ts";
import type { EditorSelectionReport } from "../../editor/selection.ts";

export function useSelectionPill(opts: {
  bodyRef: RefObject<HTMLDivElement | null>;
  /** While the send modal is open, capture nothing (reason below). */
  sendOpen: boolean;
  /** Whether the editing surface is shown (surfaces.editor). */
  editorSurface: boolean;
}) {
  const { bodyRef, sendOpen, editorSurface } = opts;
  // `origin` keeps the two capture paths (the read-only grid's DOM walk and the
  // editor's own report) from clearing each other's pill (docs/log/44 §1.8).
  const [sel, setSel] = useState<SelectionPill | null>(null);

  // After a mouse selection in the code/source view, surface a floating Send pill by
  // the selection. Scoped to CodeView because it queries that view's <code> element
  // (absent in md-preview / slides / image), so it stays inert elsewhere.
  const captureSelection = () => {
    // While the send modal is open, ignore mouseups — React portals bubble events through
    // the React tree, so a click inside the (body-portaled) modal reaches this handler and
    // would clear `sel` (the modal is gated on it), closing the modal on the first click.
    if (sendOpen) return;
    // A selection inside CodeMirror belongs to the editing surface, which reports
    // it from its own document (docs/log/44 §1.8). Walking the DOM for it would read
    // a virtualised, possibly truncated copy of the same selection.
    const editorEl = bodyRef.current?.querySelector(".file-editor-cm");
    const live = window.getSelection();
    if (editorEl && live?.anchorNode && editorEl.contains(live.anchorNode)) return;
    // Only one pill at a time: a selection outside the editor supersedes the
    // editor's, even when there is no code grid here to select in (a preview).
    const codeEl = bodyRef.current?.querySelector(".codeview .codegrid");
    const r = codeEl ? lineRangeOfSelection(codeEl) : null;
    if (!r) {
      setSel(null);
      return;
    }
    const rect = live!.getRangeAt(0).getBoundingClientRect();
    setSel({ ...r, x: Math.round(rect.left), y: Math.round(rect.top - 34), origin: "view" });
  };

  // The editing surface reports its own selection: line numbers and the quote
  // come from the CodeMirror document, and the pill is placed from coordsAtPos.
  const captureEditorSelection = (selection: EditorSelectionReport | null) => {
    if (sendOpen) return;
    setSel((prev) => editorPill(prev, selection, editorSurface));
  };

  // Leaving the editing surface drops its pill: the selection survives in the
  // editor state, but it is no longer on screen to send from.
  useEffect(() => {
    if (editorSurface) return;
    setSel((prev) => (prev?.origin === "editor" ? null : prev));
  }, [editorSurface]);

  // Touch text-selection (long-press + drag handles on mobile) does NOT fire mouseup/
  // keyup, so the pill never appeared on phones. `selectionchange` fires for touch too;
  // debounce it (selection updates continuously while dragging the handles) and reuse the
  // same capture. Keep a ref so the mount-once listener always calls the latest closure
  // (captureSelection closes over sendOpen). captureSelection itself is scoped to this
  // view's codegrid, so selections elsewhere just clear our pill.
  const captureRef = useRef(captureSelection);
  captureRef.current = captureSelection;
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | null = null;
    const onSelChange = () => {
      if (t) clearTimeout(t);
      t = setTimeout(() => captureRef.current(), 250);
    };
    document.addEventListener("selectionchange", onSelChange);
    return () => {
      document.removeEventListener("selectionchange", onSelChange);
      if (t) clearTimeout(t);
    };
  }, []);

  return { sel, setSel, captureSelection, captureEditorSelection };
}
