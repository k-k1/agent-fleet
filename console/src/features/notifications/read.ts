import type { FleetNotification } from "./store.ts";

const unreadFor = (n: FleetNotification, sessionName: string): boolean =>
  !n.seen && n.target.type === "session" && n.target.id === sessionName;

export function unseenSessionEventIDs(items: FleetNotification[], sessionName: string): string[] {
  if (!sessionName) return [];
  return items.filter((n) => unreadFor(n, sessionName)).map((n) => n.id);
}

/** Does this one session still carry an unseen notification? A rail row subscribes to this
 *  rather than to the item list, so another session's notification does not re-render it. */
export const hasUnreadFor = (items: FleetNotification[], sessionName: string): boolean =>
  !!sessionName && items.some((n) => unreadFor(n, sessionName));

/** Session names carrying at least one unseen notification, sorted. */
export function unreadSessionNames(items: FleetNotification[]): string[] {
  const names = new Set<string>();
  for (const n of items) {
    if (!n.seen && n.target.type === "session" && n.target.id) names.add(n.target.id);
  }
  return [...names].sort();
}

/** unreadSessionNames folded into one string, for callers that need the whole set.
 *  A store selector returning a fresh Set or array would re-render every subscriber on each
 *  poll (5s); this value is Object.is-comparable, so a render only follows a real change.
 *  Newline is safe as the separator: a session name is a generated slug. */
export const unreadSessionKey = (items: FleetNotification[]): string => unreadSessionNames(items).join("\n");
