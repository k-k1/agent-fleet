// The sessions overview (ADR 0078) opens like the other non-session views the rail offers:
// in the active pane, or by focusing the one that already shows it (sameTarget dedupes on
// the kind alone, so a layout holds one overview whatever its toggle says).
//
// Its own module, apart from sessions/open.ts, because that file reaches the terminal
// service (xterm) and the keyboard command table imports this — the node test project has
// no window for xterm to attach to.
import { useLayoutStore } from "../../layout/store.ts";

export function openSessionsOverview(): void {
  useLayoutStore.getState().openTarget({ content: { kind: "sessions", showStopped: false } });
}
