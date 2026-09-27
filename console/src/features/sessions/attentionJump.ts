// The jump-to-next "needs you" session (keys: session.nextAttention) — the side-effecting half
// of attention.ts. A module of its own because it reaches both sessions/open.ts (which pulls
// in the terminal service, i.e. xterm) and the notification store (which imports open.ts back);
// the keyboard command table loads it lazily so the node test project never evaluates either.
import { activePane } from "../../layout/ops.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { displayName } from "../../lib/sessionview.ts";
import { t } from "../../lib/i18n/index.ts";
import { toast } from "../../ui/toast.ts";
import { useNotificationStore } from "../notifications/store.ts";
import { attentionQueue, nextAttention, unreadAtFromNotifications } from "./attention.ts";
import type { AttentionWalk } from "./attention.ts";
import { openSessionFromList } from "./open.ts";
import { waitingAtFromNotifications } from "./order.ts";
import { shownSession } from "./shown.ts";
import { useSessionsStore } from "./store.ts";
import { observedWaitingAt } from "./waiting.ts";

// Where the last press landed. Device-local and in memory only: losing it (a reload) just
// restarts the walk from the head of the queue.
let walk: AttentionWalk | null = null;

/** Opens the next session that needs you in the active pane (or focuses the pane already
 *  showing it) and says where it went. Showing it acknowledges its unread notifications. */
export function jumpToNextAttention(): void {
  const sessions = useSessionsStore.getState().sessions;
  const items = useNotificationStore.getState().items;
  const fromNotifications = waitingAtFromNotifications(items);
  const waitingAt = (name: string) => Math.max(fromNotifications[name] || 0, observedWaitingAt(name));
  const queue = attentionQueue(sessions, waitingAt, unreadAtFromNotifications(items));
  const current = shownSession(activePane(useLayoutStore.getState().layout), sessions);
  const next = nextAttention(queue.map((s) => s.name), current, walk);
  const target = next && queue.find((s) => s.name === next.at);
  if (!next || !target) {
    toast(t(queue.length ? "noti.jump_only_current" : "noti.jump_none"), { kind: "info", duration: 2000 });
    return;
  }
  walk = next;
  openSessionFromList(target, false, wsRunning(useWorkspaceStore.getState().state));
  toast(t("noti.jump_to", { name: displayName(target), total: queue.length }), { kind: "info", duration: 1600 });
}
