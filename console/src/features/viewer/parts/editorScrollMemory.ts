// Scroll memory for the CodeMirror editor, the one File pane surface useScrollMemory cannot
// serve: the position is kept as a fractional top line, not px, because CodeMirror's height
// map is an estimate until the lines on screen have been measured. A px offset restored into
// an estimated map lands somewhere else once the real heights arrive; a line does not.
//
// Restoring is re-applied every frame until the editor reports the target twice in a row,
// because CodeMirror re-measures (and an opener's revealLine may scroll) during the first
// frames after it is shown. It stops early on reader input, and gives up after a budget.
import { loadScrollPos, saveScrollPos } from "../scrollMemory.ts";

/** What this needs from the editor (CodeEditorHandle provides it). */
export interface ScrollMemoryEditor {
  scroller: HTMLElement;
  host: HTMLElement;
  topLine(): number;
  scrollToLine(line: number): void;
}

/** Frames to keep re-applying before the target counts as unreachable. */
const RESTORE_FRAMES = 60;

const USER_EVENTS = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

/** Record the editor's top line under `key` while it is on screen and, unless `restore` is
 *  false, first bring it back to the recorded line. Returns the detach function. */
export function attachEditorScrollMemory(
  editor: ScrollMemoryEditor,
  key: string,
  restore: boolean,
): () => void {
  const { scroller, host } = editor;
  let restoring = false;
  let frame = 0;

  // A hidden editor (preview mode, a collapsed pane) reports nothing meaningful.
  const remember = () => {
    if (!restoring && scroller.clientHeight > 0) saveScrollPos(key, editor.topLine());
  };
  const stop = () => {
    restoring = false;
    if (frame) cancelAnimationFrame(frame);
    frame = 0;
  };

  const target = restore ? loadScrollPos(key) : null;
  if (target != null && target > 1) {
    restoring = true;
    let left = RESTORE_FRAMES;
    let hits = 0;
    const step = () => {
      frame = 0;
      if (!restoring) return;
      if (scroller.clientHeight > 0) {
        if (Math.abs(editor.topLine() - target) < 0.01) hits++;
        else {
          hits = 0;
          editor.scrollToLine(target);
        }
      }
      if (hits >= 2 || --left <= 0) {
        stop();
        remember();
        return;
      }
      frame = requestAnimationFrame(step);
    };
    frame = requestAnimationFrame(step);
  }

  const onUser = () => {
    if (!restoring) return;
    stop();
    remember();
  };
  scroller.addEventListener("scroll", remember, { passive: true });
  for (const type of USER_EVENTS) host.addEventListener(type, onUser, { capture: true, passive: true });
  return () => {
    stop();
    scroller.removeEventListener("scroll", remember);
    for (const type of USER_EVENTS) host.removeEventListener(type, onUser, { capture: true });
  };
}
