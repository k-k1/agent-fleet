// core/push/wire — wires the unified push channel's frames into the stores (traffic
// reduction P3). The transport (events.ts) imports no store; the stores import
// pushHealthy/pushStamp from it in the other direction to make polling a fallback, so the
// wiring alone lives in this module to keep the imports from going both ways.
// The stats stream alone has no global store — a value changing every 4s must not redraw
// everything (see the comment in WsBar) — so WsBar subscribes with onPush itself.
import { onPush, onPushConnect } from "./events.ts";
import { useWorkspaceStore } from "../store/workspace.ts";
import { useTenantStore } from "../store/tenant.ts";
import { useSessionsStore } from "../../features/sessions/store.ts";
import { applyPushedNotifications } from "../../features/notifications/store.ts";
import { useWorkItemStore } from "../../features/workitems/store.ts";

/** Register the store-apply handlers. Returns the cleanup (StrictMode-safe). */
export function wirePushApply(): () => void {
  const un = [
    onPush("workspace", (d) => useWorkspaceStore.getState().applyPush(d || {})),
    onPush("sessions", (d) => useSessionsStore.getState().applyList(d?.sessions || [])),
    onPush("notifications", (d) => applyPushedNotifications(d || {})),
    // Work items (docs/log/80): the frame is the CP's cache verbatim. Fetching runs in a
    // separate goroutine on the CP, so only rows already in that cache arrive here.
    onPush("workitems", (d) => useWorkItemStore.getState().applyPush(d)),
    // A reconnect signals "the CP may have restarted" — re-read whoami (deployment
    // capabilities included), which no frame carries. Throttled on the callee side.
    onPushConnect(() => void useTenantStore.getState().refreshWhoami()),
  ];
  return () => un.forEach((u) => u());
}
