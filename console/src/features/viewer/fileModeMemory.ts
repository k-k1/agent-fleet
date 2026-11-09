// Remembers the File pane's display mode (Markdown edit / preview / split and the preview
// renderer, a diagram's figure / source, plain view / edit) per pane x file.
//
// Why it exists: a tabbed cell renders only its selected view, so switching tabs unmounts
// FileView and the mode, held in React state, would restart from initialFileMode (a preview
// for Markdown). Same lifetime and key as scrollMemory.ts: module scope, lost on reload.
//
// The opener's request (a cited line, "open in edit") lives in the layout descriptor and so
// comes back on every remount. A remembered mode is reused only under the request it was
// chosen under; a different request is a new one and starts from initialFileMode again.
//
// Store only: no React, no DOM, so it runs in the node project.
import type { FileModeState, PaneMode } from "./fileMode.ts";

/** The opener's request a mode was chosen under. */
export interface FileModeRequest {
  targetLine: number | undefined;
  openMode: PaneMode | undefined;
}

interface Entry extends FileModeRequest {
  mode: FileModeState;
}

/** Worth some hundreds of files; on overflow the least recently saved goes first. */
const MAX_ENTRIES = 200;

const modes = new Map<string, Entry>();

export function saveFileMode(key: string | null, request: FileModeRequest, mode: FileModeState): void {
  if (!key) return;
  modes.delete(key); // re-insert so the map stays in least-recently-used order
  modes.set(key, { targetLine: request.targetLine, openMode: request.openMode, mode });
  if (modes.size > MAX_ENTRIES) {
    const oldest = modes.keys().next();
    if (!oldest.done) modes.delete(oldest.value);
  }
}

/** The remembered mode, or null when there is none or it was chosen under another request. */
export function loadFileMode(key: string | null, request: FileModeRequest): FileModeState | null {
  if (!key) return null;
  const entry = modes.get(key);
  if (!entry) return null;
  if (entry.targetLine !== request.targetLine || entry.openMode !== request.openMode) return null;
  return entry.mode;
}

/** For tests. */
export function clearFileModes(): void {
  modes.clear();
}
