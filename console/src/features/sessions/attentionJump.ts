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
import { openNotificationTarget, opensConversation, useNotificationStore } from "../notifications/store.ts";
import type { FleetNotification } from "../notifications/store.ts";
import { attentionQueue, nextAttention, unreadAtFromNotifications } from "./attention.ts";
import type { AttentionWalk } from "./attention.ts";
import { openSessionFromList } from "./open.ts";
import { waitingAtFromNotifications } from "./order.ts";
import { shownSession } from "./shown.ts";
import { useSessionsStore } from "./store.ts";
import { isWaiting, observedWaitingAt } from "./waiting.ts";
import type { Session } from "../../types/session.ts";

// The previous press: the walk, and the layout it left behind. The walk continues only while
// the layout is still exactly that one — any navigation of the user's own (another pane,
// another tab, another session, even coming back to the same one) starts a fresh walk from
// what is on screen, in the current order. It is also what lets a jump that landed on a
// conversation, which shows no session, still know where it was. Device-local and in memory
// only: losing it (a reload) just restarts the walk.
let walk: AttentionWalk | null = null;
let landedOn: Layout | null = null;

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
 *  conversation, not the reporting session — opening the session would show neither the report
 *  nor anything new, while its visible-pane acknowledgement cleared the report as read. */
async function openStop(s: Session, items: FleetNotification[]): Promise<void> {
  const n = isWaiting(s) ? undefined : newestUnseen(items, s.name);
  if (n && opensConversation(n)) {
    const r = await openNotificationTarget(n, false);
    // A conversation pane shows no session, so nothing acknowledges it on sight; do what
    // activating the row in the notification center does.
    if (r.opened) void useNotificationStore.getState().markSeen(undefined, [n.id]);
    return;
  }
  openSessionFromList(s, false, wsRunning(useWorkspaceStore.getState().state));
}

/** Opens the next session that needs you in the active pane (or focuses the pane already
 *  showing it) and says where it went. */
export async function jumpToNextAttention(): Promise<void> {
  const sessions = useSessionsStore.getState().sessions;
  const items = useNotificationStore.getState().items;
  const fromNotifications = waitingAtFromNotifications(items);
  const waitingAt = (name: string) => Math.max(fromNotifications[name] || 0, observedWaitingAt(name));
  const queue = attentionQueue(sessions, waitingAt, unreadAtFromNotifications(items));
  const layout = useLayoutStore.getState().layout;
  const prev = walk && layout === landedOn ? walk : null;
  const current = prev ? prev.at : shownSession(activePane(layout), sessions);
  const next = nextAttention(queue.map((s) => s.name), current, prev);
  const target = next && queue.find((s) => s.name === next.at);
  if (!next || !target) {
    walk = null;
    toast(t(queue.length ? "noti.jump_only_current" : "noti.jump_none"), { kind: "info", duration: 2000 });
    return;
  }
  toast(t("noti.jump_to", { name: displayName(target), total: queue.length }), { kind: "info", duration: 1600 });
  await openStop(target, items);
  walk = next;
  landedOn = useLayoutStore.getState().layout;
}
