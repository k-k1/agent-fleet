// 選択範囲に浮く「送る / 読み上げ」ピルの状態。取り込み口が 2 つある
// （読み取り専用グリッドの DOM 走査と、CodeMirror 自身の報告）ので、
// どちらが最後に触ったかを origin が持つ（docs/log/44 §1.8）。
//
// FileView での呼び出し位置は元の captureSelection の定義位置のまま＝この 2 つの
// effect が、その面で登録される順序は変わっていない。
import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { lineRangeOfSelection } from "./fileDom.ts";
import { editorPill, type SelectionPill } from "../selectionPill.ts";
import type { EditorSelectionReport } from "../../editor/selection.ts";

export function useSelectionPill(opts: {
  bodyRef: RefObject<HTMLDivElement | null>;
  /** 送信モーダルが開いている間は取り込まない（下の理由）。 */
  sendOpen: boolean;
  /** 編集面が出ているか（surfaces.editor）。 */
  editorSurface: boolean;
}) {
  const { bodyRef, sendOpen, editorSurface } = opts;
  // `origin` keeps the two capture paths (the read-only grid's DOM walk and the
  // editor's own report) from clearing each other's pill (docs/log/44 §1.8).
  const [sel, setSel] = useState<SelectionPill | null>(null);

  // After a mouse selection in the code/source view, surface a floating "送る" pill by
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
