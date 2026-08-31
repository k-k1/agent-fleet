// Cross-pane memo drag & drop (docs/log/21). A memo dragged from the left-pane queue can be
// dropped onto a session's mirror composer, where it inserts its text (the note stays
// queued — a drop is a copy, never a move out of the queue). This is separate from the
// in-queue reorder DnD, which tracks the subject via a ref and ignores dataTransfer.
//
// The payload rides on a private MIME type so only our composer accepts it; text/plain is
// set alongside so a plain OS text field (or another editor) also receives the text.
import type { Memo } from "../../types/memo.ts";

export const MEMO_DND_MIME = "application/x-af-memo";

// The text a dragged memo carries into a drop target: the raw body for a note, or the
// referenced path (plus any comment) for a file memo.
export function memoDragText(m: Memo): string {
  if (m.kind === "file") {
    const ref = m.refPath || "";
    return m.body ? (ref ? ref + "\n" + m.body : m.body) : ref;
  }
  return m.body || "";
}

// Populate a drag event's dataTransfer so both the mirror composer (private MIME, exact
// text) and plain text targets (text/plain) accept the drop. copyMove keeps the in-queue
// reorder ("move") working while a drop onto a composer reads as a copy.
export function setMemoDragData(dt: DataTransfer, m: Memo): void {
  const text = memoDragText(m);
  try {
    dt.setData(MEMO_DND_MIME, text);
    dt.setData("text/plain", text);
    dt.effectAllowed = "copyMove";
  } catch {
    // setData can throw in exotic environments; the in-queue reorder (ref-based) still works.
  }
}
