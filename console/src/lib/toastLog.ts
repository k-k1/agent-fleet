// Client-side log of important toasts (errors + destructive-action results) so they can
// be reviewed in the notification center AFTER the transient toast is gone. Trivial toasts
// (a "copied" confirmation and the like) are NOT logged — they stay purely ephemeral. The producer is the
// ToastProvider (toast() with persist), the consumer is the NotificationCenter, which
// merges these with the server-driven fleet notifications by time.
//
// Purely local (localStorage, key af.toastLog) — a copy failure / local delete has no
// server event, and this must render instantly with no fetch. Pruned to the same 7-day
// window the panel advertises, capped at MAX so the store can't grow unbounded.
import { create } from "zustand";

export type ToastLogKind = "error" | "warn" | "info" | "success";

export interface ToastLogItem {
  id: string;
  kind: ToastLogKind;
  message: string;
  createdAt: string; // ISO
  seen: boolean;
}

const KEY = "af.toastLog";
const MAX = 50;
const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000; // last 7 days, matching the panel's advertised window

function prune(items: ToastLogItem[]): ToastLogItem[] {
  const cutoff = Date.now() - MAX_AGE_MS;
  return items.filter((i) => new Date(i.createdAt).getTime() >= cutoff).slice(0, MAX);
}

function read(): ToastLogItem[] {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return prune(
      parsed.filter(
        (x): x is ToastLogItem =>
          !!x && typeof x.id === "string" && typeof x.message === "string" && typeof x.createdAt === "string",
      ),
    );
  } catch {
    return [];
  }
}

function write(items: ToastLogItem[]): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(items));
  } catch {
    /* private mode / quota — the log is best-effort, not critical state */
  }
}

interface ToastLogStore {
  items: ToastLogItem[];
  push(kind: ToastLogKind, message: string): void;
  markAllSeen(): void;
  remove(id: string): void;
  clear(): void;
}

let seq = 0;

export const useToastLog = create<ToastLogStore>((set, get) => ({
  items: read(),
  push: (kind, message) =>
    set(() => {
      const item: ToastLogItem = {
        id: `${Date.now()}-${++seq}`,
        kind,
        message,
        createdAt: new Date().toISOString(),
        seen: false,
      };
      const items = prune([item, ...get().items]);
      write(items);
      return { items };
    }),
  markAllSeen: () =>
    set((s) => {
      if (!s.items.some((i) => !i.seen)) return s; // no unread → no re-render / write
      const items = s.items.map((i) => (i.seen ? i : { ...i, seen: true }));
      write(items);
      return { items };
    }),
  remove: (id) =>
    set((s) => {
      const items = s.items.filter((i) => i.id !== id);
      write(items);
      return { items };
    }),
  clear: () =>
    set(() => {
      write([]);
      return { items: [] };
    }),
}));

// Imperative push for the non-React toast() callback in ToastProvider.
export function pushToastLog(kind: ToastLogKind, message: string): void {
  useToastLog.getState().push(kind, message);
}

export function toastLogUnseen(items: ToastLogItem[]): number {
  return items.reduce((n, i) => (i.seen ? n : n + 1), 0);
}
