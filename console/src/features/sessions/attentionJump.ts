// The jump-to-next "needs you" session (keys: session.nextAttention) — the side-effecting half
// of attention.ts. A module of its own because it reaches both sessions/open.ts (which pulls
// in the terminal service, i.e. xterm) and the notification store (which imports open.ts back);
// the keyboard command table loads it lazily so the node test project never evaluates either.
import { activePane } from "../../layout/ops.ts";
import { useLayoutStore } from "../../layout/store.ts";
import type { Layout } from "../../layout/types.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { displayName } from "../../lib/sessionview.ts";
import { t } from "../../lib/i18n/index.ts";
import { toast } from "../../ui/toast.ts";
import { openNotificationTarget, useNotificationStore } from "../notifications/store.ts";
import { destinationShown, opensConversation } from "../notifications/read.ts";
import type { FleetNotification } from "../notifications/store.ts";
import { attentionQueue, nextAttention, unreadAtFromNotifications } from "./attention.ts";
import type { AttentionWalk } from "./attention.ts";
import { openSessionFromList } from "./open.ts";
import { waitingAtFromNotifications } from "./order.ts";
import { shownSession } from "./shown.ts";
import { useSessionsStore } from "./store.ts";
import { isWaiting, observedWaitingAt } from "./waiting.ts";
import type { Session } from "../../types/session.ts";

// The previous press. The walk continues only while the user stays where it landed: the
// active pane still shows the same view with the same content. Resizing, wrapping or
// polling do not move that; any navigation of the user's own does — even coming back to
// the same session later — and drops the walk, so the next press starts afresh from what is
// on screen, in the current order. Keyed on the place rather than on the session because a
// report lands on a conversation, which shows no session. Device-local and in memory only:
// losing it (a reload) just restarts the walk.
let walk: AttentionWalk | null = null;
let landedAt = "";
let watching = false;
// When the press in flight started. A second press meanwhile would pick the same stop again,
// so it is ignored — but only for a while: opening a conversation waits on a fetch with no
// timeout of its own, and a hung one must not swallow every later press.
let busySince = 0;
const BUSY_MS = 5000;

const placeKey = (l: Layout): string => {
  const v = activePane(l);
  return v ? JSON.stringify([l.activeCellId, v.id, v.session, v.content]) : "";
};

function watchLayout(): void {
  if (watching) return;
  watching = true;
  useLayoutStore.subscribe((st) => {
    if (walk && placeKey(st.layout) !== landedAt) walk = null;
  });
}

/** Newest unseen notification for a session. */
function newestUnseen(items: FleetNotification[], name: string): FleetNotification | undefined {
  let best: FleetNotification | undefined;
  for (const n of items) {
    if (n.seen || n.target.type !== "session" || n.target.id !== name) continue;
    if (!best || n.createdAt > best.createdAt) best = n;
  }
  return best;
}

/** Where a stop of the walk leads. A session waiting on an answer opens the session. An unread
 *  one opens what its newest notification points at: a report lives in the operator
 *  conversation, not the reporting session. Being on screen there is what acknowledges it
 *  (wireNotificationReadOnVisibleSessions). A session with both a report and a newer finished
 *  turn keeps its place in the queue after the first stop and is visited again for the other. */
async function openStop(s: Session, items: FleetNotification[]): Promise<void> {
  const n = isWaiting(s) ? undefined : newestUnseen(items, s.name);
  if (n && opensConversation(n)) {
    const r = await openNotificationTarget(n, false);
    // The conversation is gone, so nothing can ever show this report again: acknowledge it
    // here, or it would hold the session in the queue and every press would come back to it.
    if (r.missingConversation !== undefined) void useNotificationStore.getState().markSeen(undefined, [n.id]);
    return;
  }
  openSessionFromList(s, false, wsRunning(useWorkspaceStore.getState().state));
}

/** Opens the next session that needs you in the active pane (or focuses the pane already
 *  showing it) and says where it went. */
export async function jumpToNextAttention(): Promise<void> {
  if (busySince && Date.now() - busySince < BUSY_MS) return;
  const started = (busySince = Date.now());
  try {
    watchLayout();
    const sessions = useSessionsStore.getState().sessions;
    const items = useNotificationStore.getState().items;
    const fromNotifications = waitingAtFromNotifications(items);
    const waitingAt = (name: string) => Math.max(fromNotifications[name] || 0, observedWaitingAt(name));
    const queue = attentionQueue(sessions, waitingAt, unreadAtFromNotifications(items));
    const layout = useLayoutStore.getState().layout;
    const prev = walk && placeKey(layout) === landedAt ? walk : null;
    const place = activePane(layout);
    const current = prev ? prev.at : shownSession(place, sessions);
    // Being at a session is not being at every one of its stops: with an unseen report left,
    // the session on screen still needs you — in the report's conversation.
    const here = sessions.find((s) => s.name === current);
    const stop = here && !isWaiting(here) ? newestUnseen(items, here.name) : undefined;
    const currentDone = !stop || destinationShown(stop, place, sessions);
    const next = nextAttention(queue.map((s) => s.name), current, prev, currentDone);
    const target = next && queue.find((s) => s.name === next.at);
    walk = null;
    if (!next || !target) {
      toast(t(queue.length ? "noti.jump_only_current" : "noti.jump_none"), { kind: "info", duration: 2000 });
      return;
    }
    toast(t("noti.jump_to", { name: displayName(target), total: queue.length }), { kind: "info", duration: 1600 });
    await openStop(target, items);
    landedAt = placeKey(useLayoutStore.getState().layout);
    walk = next;
  } finally {
    if (busySince === started) busySince = 0;
  }
}

/** Test seam: forget the walk. */
export function resetAttentionWalkForTest(): void {
  walk = null;
  landedAt = "";
  busySince = 0;
}
