// Scroll memory for the CodeMirror editor, the one File pane surface useScrollMemory cannot
// serve: the position is kept as a fractional top line, not px, because CodeMirror's height
// map is an estimate until the lines on screen have been measured. A px offset restored into
// an estimated map lands somewhere else once the real heights arrive; a line does not.
//
// Restoring is re-applied every frame until the editor reports the target twice in a row,
// because CodeMirror re-measures (and an opener's revealLine may scroll) during the first
// frames after it is shown. It stops early on reader input, when the scroller stops moving
// (the document is now shorter than the recorded line), or after a budget of visible frames.
// While the editor has no box (a hidden pane or column) nothing is spent; a box coming back
// restarts the restore, since showing a display:none scroller resets its scrollTop.
import { loadScrollPos, saveScrollPos } from "../scrollMemory.ts";

/** What this needs from the editor (CodeEditorHandle provides it). */
export interface ScrollMemoryEditor {
  scroller: HTMLElement;
  host: HTMLElement;
  topLine(): number;
  scrollToLine(line: number): void;
}

/** Visible frames to keep re-applying before the target counts as unreachable. */
const RESTORE_FRAMES = 60;

const USER_EVENTS = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

/** Record the editor's top line under `key` while it is on screen and, unless `restore` is
 *  false, bring it back to the recorded line. Returns the detach function. */
export function attachEditorScrollMemory(
  editor: ScrollMemoryEditor,
  key: string,
  restore: boolean,
): () => void {
  const { scroller, host } = editor;
  let restoring = false;
  let frame = 0;
  let target = 0;
  let left = 0;
  let hits = 0;
  let stalls = 0;
  let lastTop = -1;
  const visible = () => scroller.clientHeight > 0;

  // A hidden editor (preview mode, a collapsed pane) reports nothing meaningful.
  const remember = () => {
    if (!restoring && visible()) saveScrollPos(key, editor.topLine());
  };
  const stop = () => {
    restoring = false;
    if (frame) cancelAnimationFrame(frame);
    frame = 0;
  };
  const finish = () => {
    stop();
    remember();
  };

  const step = () => {
    frame = 0;
    if (!restoring) return;
    if (!visible()) return; // paused; the box observer resumes it
    if (Math.abs(editor.topLine() - target) < 0.01) {
      hits++;
      stalls = 0;
    } else {
      hits = 0;
      // A write that no longer moves the scroller means the line is past the end.
      stalls = scroller.scrollTop === lastTop ? stalls + 1 : 0;
      editor.scrollToLine(target);
    }
    lastTop = scroller.scrollTop;
    if (hits >= 2 || stalls >= 2 || --left <= 0) return finish();
    frame = requestAnimationFrame(step);
  };

  const arm = () => {
    if (!restore) return;
    const saved = loadScrollPos(key);
    if (saved == null || saved <= 1) return;
    stop();
    restoring = true;
    target = saved;
    left = RESTORE_FRAMES;
    hits = 0;
    stalls = 0;
    lastTop = -1;
    frame = requestAnimationFrame(step);
  };

  let boxed = visible();
  const boxRo =
    typeof ResizeObserver === "undefined"
      ? null
      : new ResizeObserver(() => {
          const now = visible();
          if (now && !boxed) {
            if (restoring) frame ||= requestAnimationFrame(step);
            else arm();
          }
          boxed = now;
        });
  boxRo?.observe(scroller);

  const onUser = () => {
    if (restoring) finish();
  };
  scroller.addEventListener("scroll", remember, { passive: true });
  for (const type of USER_EVENTS) host.addEventListener(type, onUser, { capture: true, passive: true });
  arm();
  return () => {
    stop();
    boxRo?.disconnect();
    scroller.removeEventListener("scroll", remember);
    for (const type of USER_EVENTS) host.removeEventListener(type, onUser, { capture: true });
  };
}
