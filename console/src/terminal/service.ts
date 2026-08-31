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
import { keepOnly, terminalRtt, terminalRttAll, probeTerminalRtt } from "./term.ts";
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
  onTermRtt,
  terminalRtt,
  terminalRttAll,
  probeTerminalRtt,
} from "./term.ts";
export type { RttStats } from "./term.ts";

/** wireTerminalReconcile subscribes to the layout store and disposes terminals
 * whose pane no longer exists (pane closed, browser back, tenant switch) —
 * each pane keeps its xterm alive while it exists, even hidden behind another
 * view. Returns the unsubscribe; called once from the app shell boot effect. */
export function wireTerminalReconcile(): () => void {
  // Diagnostic handle for the moment someone reports "typing is slow". The head chip
  // (TermRtt) samples every 5 s, which characterises a steady path but not a burst;
  // `probe` fires N back-to-back round trips over the live PTY socket and returns their
  // spread, which is what distinguishes a uniformly distant workspace from one that
  // stalls. Console-only, no UI, no network of its own — it reuses the ping the socket
  // already sends. `all()` lists every pane id with its current readout.
  /* eslint-disable-next-line @typescript-eslint/no-explicit-any */
  (window as any).__afTerm = { all: terminalRttAll, rtt: terminalRtt, probe: probeTerminalRtt };
  return useLayoutStore.subscribe((s, prev) => {
    if (s.layout === prev.layout) return;
    keepOnly(allViews(s.layout).map((p) => p.id));
  });
}
