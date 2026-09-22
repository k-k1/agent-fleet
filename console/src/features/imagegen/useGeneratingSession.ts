// The store half of "which session made this picture" (the rule itself is generatedBy.ts):
// reading the answer out of the session list, and putting that session on screen.
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionFromList } from "../sessions/open.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { displayName } from "../../lib/sessionview.ts";
import { sessionOfGeneratedFolder } from "./generatedBy.ts";

/** What a jump control needs: the session's immutable slug and the label to show. */
export interface GeneratingSession {
  name: string;
  label: string;
}

/**
 * The session that generated the pictures in `dir` (a caller holding a file path passes
 * `folderOf(path)`), or null.
 *
 * Two PRIMITIVE selectors rather than one returning the session object: `sessions` is a fresh
 * array of fresh objects on every poll, so a selector returning the session itself would
 * re-render its host once a second (the gallery's notes on `named` make the same point). A
 * string changes only when the answer does.
 */
export function useGeneratingSession(dir: string | undefined): GeneratingSession | null {
  const name = useSessionsStore((s) => (dir ? (sessionOfGeneratedFolder(s.sessions, dir)?.name ?? "") : ""));
  const label = useSessionsStore((s) => {
    const hit = dir ? sessionOfGeneratedFolder(s.sessions, dir) : undefined;
    return hit ? displayName(hit) : "";
  });
  return name ? { name, label } : null;
}

/**
 * Put that session on screen. `openSessionFromList` is the single source of truth for where a
 * session opens (mirror / terminal / read-only history), so this must not pick a surface of its
 * own — jumping from a picture has to land where clicking the rail row lands.
 *
 * Returns false when nothing could be opened (stopped, no transcript, and the folder gone), so
 * the caller can say so instead of letting a click do nothing.
 */
export function openGeneratingSession(name: string, split: boolean): boolean {
  const s = useSessionsStore.getState().sessions.find((x) => x.name === name);
  if (!s) return false;
  return openSessionFromList(s, split, wsRunning(useWorkspaceStore.getState().state));
}
