// core/push/wire — wires the unified push channel's frames into the stores (traffic
// reduction P3). The transport (events.ts) imports no store; the stores import
// pushHealthy/pushStamp from it in the other direction to make polling a fallback, so the
// wiring alone lives in this module to keep the imports from going both ways.
// The stats stream alone has no global store — a value changing every 4s must not redraw
// everything (see the comment in WsBar) — so WsBar subscribes with onPush itself, and the
// Machine tab's series (wsStatsFeed) is its own module with its own subscribers.
import { onPush, onPushConnect } from "./events.ts";
import { wireWsStatsFeed } from "../store/wsStatsFeed.ts";
import { useWorkspaceStore } from "../store/workspace.ts";
import { useTenantStore } from "../store/tenant.ts";
import { useSessionsStore } from "../../features/sessions/store.ts";
import { applyPushedNotifications } from "../../features/notifications/store.ts";
import { useWorkItemStore } from "../../features/workitems/store.ts";
import { useEnginesStore } from "../../features/engines/store.ts";

/** Register the store-apply handlers. Returns the cleanup (StrictMode-safe). */
export function wirePushApply(): () => void {
  const un = [
    onPush("workspace", (d) => useWorkspaceStore.getState().applyPush(d || {})),
    // Only an actual list clears the rail. A frame that carries no `sessions` array (a
    // truncated or partial payload) used to publish [] through the same `|| []` that the
    // poll had, and the rail lost every row with no poll left to restore it — the 4s poll
    // stands down precisely while this channel is healthy.
    onPush("sessions", (d) => {
      if (Array.isArray(d?.sessions)) useSessionsStore.getState().applyList(d.sessions);
    }),
    onPush("notifications", (d) => applyPushedNotifications(d || {})),
    // Work items (docs/log/80): the frame is the CP's cache verbatim. Fetching runs in a
    // separate goroutine on the CP, so only rows already in that cache arrive here.
    onPush("workitems", (d) => useWorkItemStore.getState().applyPush(d)),
    // Engine indicator (ADR 0084 decision 1): the member row, same apply path as REST
    // (features/engines/store.ts's refresh()).
    onPush("engines", (d) => useEnginesStore.getState().applyPush(d)),
    // A reconnect signals "the CP may have restarted" — re-read whoami (deployment
    // capabilities included), which no frame carries. Throttled on the callee side.
    onPushConnect(() => void useTenantStore.getState().refreshWhoami()),
    // Re-read the session list on every (re)connect for the same reason, plus one this
    // stream cannot fix by itself: a frame is only sent when something CHANGES, so a rail
    // left empty by a failed load stays empty for as long as nobody starts or stops a
    // session. The poll cannot cover it either — it stands down while the channel is
    // healthy. Without this the only way out is a manual reload.
    onPushConnect(() => void useSessionsStore.getState().refresh()),
    // The stats stream feeds one more consumer than the WS bar: a per-tick series for the
    // Machine tab's charts. It keeps its own clock because an unchanged frame is never sent
    // (see wsStatsFeed), and it is started here rather than by the tab so that opening the
    // tab already has history behind it.
    wireWsStatsFeed(),
  ];
  return () => un.forEach((u) => u());
}
