// The per-session queue state. Module-level (zustand), so it survives the mirror unmounting when
// the member switches panes or sessions inside the same page, and is gone on a reload — on
// purpose: a held follow-up must not fire into a different moment than the one it was written for.
import { create } from "zustand";
import { editItem, enqueue, insertAt, moveItem, removeItem, shouldDrain, type QueuedSend } from "./queue.ts";

/** One session's send gate. Shared by every hook instance, so two mounts cannot both send. */
export interface Gate {
  /** A queue send is awaiting its answer. */
  inflight: boolean;
  /** When the last drain send left, until the POLL has shown the session busy since. */
  awaitingSince: number | null;
}
const IDLE_GATE: Gate = { inflight: false, awaitingSince: null };

export interface Claim {
  item: QueuedSend;
  index: number;
}

interface SendQueueStore {
  bySession: Record<string, QueuedSend[]>;
  /** Sessions whose drain is held because the member stopped the turn. */
  paused: Record<string, boolean>;
  add(session: string, text: string, paths: string[]): void;
  edit(session: string, id: string, text: string): void;
  remove(session: string, id: string): void;
  move(session: string, id: string, delta: -1 | 1): void;
  gate: Record<string, Gate>;
  /**
   * Rows open for editing, per session and per owner (one hook instance each): nothing may drain
   * while one is open, and an owner only ever releases its own — two views of one session
   * cannot unlock each other's edit.
   */
  editing: Record<string, Record<string, string>>;
  setEditing(session: string, owner: string, id: string | null): void;
  /**
   * The drain's one synchronous step: if the head may leave now, take it out and lock the gate.
   * Check, take and lock happen in one store update, so no second hook instance, remount or
   * effect re-run can claim another item while this one is in flight.
   */
  claimHead(session: string, now: number, busy: boolean, canSend: boolean): Claim | null;
  /** Same for "send now" on a chosen row; refused while a send is in flight or the row is being edited. */
  claimItem(session: string, id: string): Claim | null;
  /** The send answered. A refusal puts the item back in place and pauses the drain. */
  release(session: string, claim: Claim, ok: boolean): void;
  /** The poll showed the session busy: the turn we sent has really started. */
  observeBusy(session: string): void;
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

/** A lock counts only while its row still exists (a deleted or drained row cannot hold the queue forever). */
function isEditing(s: SendQueueStore, session: string, id?: string): boolean {
  const rows = new Set((s.bySession[session] ?? []).map((i) => i.id));
  return Object.values(s.editing[session] ?? {}).some((r) => rows.has(r) && (id === undefined || r === id));
}

function claim(
  set: (f: (s: SendQueueStore) => Partial<SendQueueStore>) => void,
  get: () => SendQueueStore,
  session: string,
  id: string,
  drainedAt: number | null,
): Claim | null {
  const items = get().bySession[session] ?? [];
  const index = items.findIndex((i) => i.id === id);
  if (index < 0) return null;
  const item = items[index];
  set((s) => ({
    ...put(s, session, removeItem(s.bySession[session] ?? [], id)),
    gate: { ...s.gate, [session]: { inflight: true, awaitingSince: drainedAt ?? s.gate[session]?.awaitingSince ?? null } },
  }));
  return { item, index };
}

export const useSendQueueStore = create<SendQueueStore>((set, get) => ({
  bySession: {},
  paused: {},
  add: (session, text, paths) =>
    set((s) => put(s, session, enqueue(s.bySession[session] ?? [], { id: `q${++seq}`, text, paths }))),
  edit: (session, id, text) => set((s) => put(s, session, editItem(s.bySession[session] ?? [], id, text))),
  remove: (session, id) => set((s) => put(s, session, removeItem(s.bySession[session] ?? [], id))),
  move: (session, id, delta) => set((s) => put(s, session, moveItem(s.bySession[session] ?? [], id, delta))),
  gate: {},
  editing: {},
  setEditing: (session, owner, id) =>
    set((s) => {
      const cur = s.editing[session] ?? {};
      if ((cur[owner] ?? null) === id) return s;
      const next = { ...cur };
      if (id === null) delete next[owner];
      else next[owner] = id;
      const editing = { ...s.editing };
      if (Object.keys(next).length) editing[session] = next;
      else delete editing[session];
      return { editing };
    }),
  claimHead: (session, now, busy, canSend) => {
    const s = get();
    const items = s.bySession[session] ?? [];
    const g = s.gate[session] ?? IDLE_GATE;
    const ok = shouldDrain({
      count: items.length, busy, canSend, paused: !!s.paused[session], inflight: g.inflight,
      awaitingSince: g.awaitingSince, now,
    });
    if (!ok || isEditing(s, session)) return null;
    return claim(set, get, session, items[0].id, now);
  },
  claimItem: (session, id) => {
    const s = get();
    if (s.gate[session]?.inflight || isEditing(s, session, id)) return null;
    // A manual send starts the settle window too: a stale idle poll right after it must not
    // release the next item before the turn it began has been seen.
    return claim(set, get, session, id, Date.now());
  },
  release: (session, c, ok) =>
    set((s) => {
      const gate = { ...s.gate, [session]: { inflight: false, awaitingSince: ok ? (s.gate[session]?.awaitingSince ?? null) : null } };
      if (ok) return { gate };
      const back = put(s, session, insertAt(s.bySession[session] ?? [], c.item, c.index));
      return { ...back, gate, paused: { ...s.paused, [session]: true } };
    }),
  observeBusy: (session) =>
    set((s) => {
      const g = s.gate[session];
      if (!g || g.awaitingSince === null) return s;
      return { gate: { ...s.gate, [session]: { ...g, awaitingSince: null } } };
    }),
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
