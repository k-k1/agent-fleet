// The per-session queue state. Module-level (zustand), so it survives the mirror unmounting when
// the member switches panes or sessions inside the same page, and is gone on a reload — on
// purpose: a held follow-up must not fire into a different moment than the one it was written for.
import { create } from "zustand";
import { editItem, enqueue, insertAt, moveItem, removeItem, type QueuedSend } from "./queue.ts";

interface SendQueueStore {
  bySession: Record<string, QueuedSend[]>;
  /** Sessions whose drain is held because the member stopped the turn. */
  paused: Record<string, boolean>;
  add(session: string, text: string, paths: string[]): void;
  edit(session: string, id: string, text: string): void;
  remove(session: string, id: string): void;
  move(session: string, id: string, delta: -1 | 1): void;
  /** Take the item out for sending; returns it with the place it held. */
  take(session: string, id: string): { item: QueuedSend; index: number } | null;
  restore(session: string, item: QueuedSend, index: number): void;
  setPaused(session: string, paused: boolean): void;
  /** Pause only when something is held — a stale flag would otherwise hold the NEXT queue. */
  pauseIfHolding(session: string): void;
}

let seq = 0;

function put(s: SendQueueStore, session: string, items: QueuedSend[]): Partial<SendQueueStore> {
  const by = { ...s.bySession };
  if (items.length) by[session] = items;
  else delete by[session];
  // An emptied queue forgets its pause: the next item must drain normally.
  if (!items.length && s.paused[session]) {
    const paused = { ...s.paused };
    delete paused[session];
    return { bySession: by, paused };
  }
  return { bySession: by };
}

export const useSendQueueStore = create<SendQueueStore>((set, get) => ({
  bySession: {},
  paused: {},
  add: (session, text, paths) =>
    set((s) => put(s, session, enqueue(s.bySession[session] ?? [], { id: `q${++seq}`, text, paths }))),
  edit: (session, id, text) => set((s) => put(s, session, editItem(s.bySession[session] ?? [], id, text))),
  remove: (session, id) => set((s) => put(s, session, removeItem(s.bySession[session] ?? [], id))),
  move: (session, id, delta) => set((s) => put(s, session, moveItem(s.bySession[session] ?? [], id, delta))),
  take: (session, id) => {
    const items = get().bySession[session] ?? [];
    const index = items.findIndex((i) => i.id === id);
    if (index < 0) return null;
    const item = items[index];
    set((s) => put(s, session, removeItem(s.bySession[session] ?? [], id)));
    return { item, index };
  },
  restore: (session, item, index) => set((s) => put(s, session, insertAt(s.bySession[session] ?? [], item, index))),
  pauseIfHolding: (session) => {
    if (get().bySession[session]?.length) get().setPaused(session, true);
  },
  setPaused: (session, paused) =>
    set((s) => {
      if (!!s.paused[session] === paused) return s;
      const next = { ...s.paused };
      if (paused) next[session] = true;
      else delete next[session];
      return { paused: next };
    }),
}));
