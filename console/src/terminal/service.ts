// Terminal service — the next console's ONLY doorway to xterm.
//
// The battle-tested xterm logic (heartbeat zombie detection, WebGL context-loss
// recovery, clipboard/Keyboard-Lock, soft-keyboard handling) stays in src/term.ts,
// shared with the frozen console during the parallel-entry transition (docs/log/22:
// keep the asset, re-draw the ownership boundary). This module narrows the
// surface components may touch and owns the layout⇄terminal reconciliation that
// used to be a loose effect in the old God-context.
//
// Contract (see layout/types.ts): paneId == terminal identity. Terminals for
// panes that left the layout are disposed here; nothing else may call keepOnly.
import { keepOnly } from "./term.ts";
import { useLayoutStore } from "../layout/store.ts";
import { allViews } from "../layout/ops.ts";

export {
  ensureTerm,
  attach,
  detach,
  reconnect,
  ensureAttached,
  reconnectSession,
  clearTerm,
  fit,
  repaint,
  focusTerm,
  sendInput,
  setTermBackground,
  onSession,
  sessionOf,
  hideTerm,
  revealTerm,
} from "./term.ts";

/** wireTerminalReconcile subscribes to the layout store and disposes terminals
 * whose pane no longer exists (pane closed, browser back, tenant switch) —
 * each pane keeps its xterm alive while it exists, even hidden behind another
 * view. Returns the unsubscribe; called once from the app shell boot effect. */
export function wireTerminalReconcile(): () => void {
  return useLayoutStore.subscribe((s, prev) => {
    if (s.layout === prev.layout) return;
    keepOnly(allViews(s.layout).map((p) => p.id));
  });
}
