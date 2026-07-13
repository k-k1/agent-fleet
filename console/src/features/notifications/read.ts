import type { FleetNotification } from "./store.ts";

export function unseenSessionEventIDs(items: FleetNotification[], sessionName: string): string[] {
  if (!sessionName) return [];
  return items
    .filter((n) => !n.seen && n.target.type === "session" && n.target.id === sessionName)
    .map((n) => n.id);
}
