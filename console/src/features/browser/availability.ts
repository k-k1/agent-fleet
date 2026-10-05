// Whether this workspace offers browser features (the browser pane, Chromium attachments).
//
// The CP's runtime adapter decides and sends the answer on the workspace payload
// (control-plane/internal/runtime/browser_support.go); the Console never guesses it from the
// runtime name. Every browser entry point reads this one hook, so a runtime that withholds
// the features (kubernetes, ADR 0106) loses all of them together and shows the same reason.
import { useWorkspaceStore } from "../../core/store/workspace.ts";

/** The runtime id that withholds browser features ("kubernetes"), or "" when they are
 *  available — including while the workspace payload has not arrived yet, so nothing is
 *  hidden on a runtime that has them. */
export function useBrowserUnavailable(): string {
  return useWorkspaceStore((s) => s.browserUnavailable);
}
