// Which sessions still carry an unseen notification — the red dot on a rail row, on a
// background pane tab, and on the folded container that hides either of them.
//
// Unread is the Control Plane's per-member seen_at, not a device-local flag, so the dot is
// the same on every browser the member uses and clears exactly where the seen mark is set:
// showing the session in a visible pane (wireNotificationReadOnVisibleSessions), activating
// the row in the notification center, or "mark all read".
import { useMemo } from "react";
import { hasUnreadFor, unreadSessionKey } from "./read.ts";
import { useNotificationStore } from "./store.ts";

/** The whole set, for callers that decide for several sessions at once (a folded project
 *  node, a pane's tab strip). */
export function useUnreadSessions(): ReadonlySet<string> {
  const key = useNotificationStore((s) => unreadSessionKey(s.items));
  return useMemo(() => new Set(key ? key.split("\n") : []), [key]);
}

/** One session. Returns a boolean, so a row re-renders only when its own dot flips. */
export function useSessionUnread(sessionName: string): boolean {
  return useNotificationStore((s) => hasUnreadFor(s.items, sessionName));
}
