import { useEffect, useState } from "react";
import { DRAIN_SETTLE_MS, type QueuedSend } from "./queue.ts";
import { useSendQueueStore, type Claim } from "./store.ts";

const NONE: QueuedSend[] = [];

/**
 * useSendQueue runs one session's pre-send queue: it hands the view the items and the operations,
 * and drains the head FIFO when the session is idle.
 *
 * Exactly once, and one at a time: the in-flight lock and the settle window live in the shared
 * store, per session, and a claim checks them, takes the head and sets the lock in one store
 * update — so two mounts of the same session, a remount, or a switch away and back cannot send a
 * second item. The head is read fresh from the store, never from a render's closure. The settle
 * window ends when the POLL shows the session busy (useTranscriptPoll), not on the optimistic
 * "working" sendPrompt sets, or a stale idle poll right after the send would release the next
 * item. A refused send puts the item back where it was and pauses the drain.
 */
export function useSendQueue({
  session,
  busy,
  canSend,
  sendItem,
}: {
  session: string;
  busy: boolean;
  /** Alive, writable, not locked by a pending decision, no composer send in flight. */
  canSend: boolean;
  /** Send one item; true when the session accepted it. The caller owns echo and attachments. */
  sendItem: (item: QueuedSend) => Promise<boolean>;
}) {
  const items = useSendQueueStore((s) => s.bySession[session] ?? NONE);
  const paused = useSendQueueStore((s) => !!s.paused[session]);
  const editing = useSendQueueStore((s) => s.editing[session] ?? null);
  const gate = useSendQueueStore((s) => s.gate[session]);
  const [tick, setTick] = useState(0);

  const run = async (claim: Claim) => {
    let ok = false;
    try {
      ok = await sendItem(claim.item);
    } finally {
      useSendQueueStore.getState().release(session, claim, ok);
    }
  };

  useEffect(() => {
    const now = Date.now();
    const claim = useSendQueueStore.getState().claimHead(session, now, busy, canSend);
    if (claim) {
      void run(claim);
      return;
    }
    // Only the settle window is time-based; wake up when it lapses.
    const since = gate?.awaitingSince ?? null;
    if (since !== null && !busy && canSend && !paused && !gate?.inflight) {
      const h = setTimeout(() => setTick((n) => n + 1), Math.max(0, since + DRAIN_SETTLE_MS - now) + 20);
      return () => clearTimeout(h);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `run` is a per-render closure over session/sendItem; the sender of the render that fires is the latest
  }, [items, busy, canSend, paused, editing, gate, tick, session]);

  return {
    items,
    paused,
    resume: () => useSendQueueStore.getState().setPaused(session, false),
    edit: (id: string, text: string) => useSendQueueStore.getState().edit(session, id, text),
    remove: (id: string) => useSendQueueStore.getState().remove(session, id),
    move: (id: string, delta: -1 | 1) => useSendQueueStore.getState().move(session, id, delta),
    /** The row being edited, or null — set by the list so the drain stands still meanwhile. */
    setEditing: (id: string | null) => useSendQueueStore.getState().setEditing(session, id),
    /** Send this item now, ahead of the others — whatever the session is doing. */
    sendNow: (id: string) => {
      const claim = useSendQueueStore.getState().claimItem(session, id);
      if (claim) void run(claim);
    },
  };
}
