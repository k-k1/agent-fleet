import { useEffect, useRef, useState } from "react";
import { DRAIN_SETTLE_MS, shouldDrain, type QueuedSend } from "./queue.ts";
import { useSendQueueStore } from "./store.ts";

const NONE: QueuedSend[] = [];

/**
 * useSendQueue runs one session's pre-send queue: it hands the view the items and the operations,
 * and drains the head FIFO when the session goes idle.
 *
 * Exactly once: the head is TAKEN out of the store (read fresh, never from a render's closure)
 * before it is sent, and nothing else is released while a send is in flight or until the session
 * has been seen busy since (see DRAIN_SETTLE_MS). A failed send puts the item back where it
 * was and pauses the drain so the same refusal is not retried in a loop.
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
  const inflight = useRef(false);
  const awaitingSince = useRef<number | null>(null);
  // The effect below must call the sender of the LATEST render, not the one it was created in.
  const sender = useRef(sendItem);
  sender.current = sendItem;
  const [tick, setTick] = useState(0);

  // A new session starts with a clean slate; another session's settle window means nothing here.
  useEffect(() => {
    awaitingSince.current = null;
  }, [session]);
  useEffect(() => {
    if (busy) awaitingSince.current = null;
  }, [busy]);

  const dispatch = async (id: string, drained: boolean) => {
    const st = useSendQueueStore.getState();
    const taken = st.take(session, id);
    if (!taken) return;
    inflight.current = true;
    if (drained) awaitingSince.current = Date.now();
    let ok = false;
    try {
      ok = await sender.current(taken.item);
    } finally {
      inflight.current = false;
      if (!ok) {
        const s = useSendQueueStore.getState();
        s.restore(session, taken.item, taken.index);
        if (drained) {
          awaitingSince.current = null;
          s.setPaused(session, true);
        }
      }
      setTick((n) => n + 1);
    }
  };

  useEffect(() => {
    const head = useSendQueueStore.getState().bySession[session]?.[0];
    if (!head) return;
    const state = {
      count: items.length, busy, canSend, paused, inflight: inflight.current,
      awaitingSince: awaitingSince.current, now: Date.now(),
    };
    if (shouldDrain(state)) {
      void dispatch(head.id, true);
      return;
    }
    // Only the settle window is time-based; wake up when it lapses.
    const since = awaitingSince.current;
    if (since !== null && !busy && canSend && !paused && !inflight.current) {
      const h = setTimeout(() => setTick((n) => n + 1), Math.max(0, since + DRAIN_SETTLE_MS - state.now) + 20);
      return () => clearTimeout(h);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- dispatch is a per-render closure over `session` only
  }, [items, busy, canSend, paused, tick, session]);

  return {
    items,
    paused,
    resume: () => useSendQueueStore.getState().setPaused(session, false),
    edit: (id: string, text: string) => useSendQueueStore.getState().edit(session, id, text),
    remove: (id: string) => useSendQueueStore.getState().remove(session, id),
    move: (id: string, delta: -1 | 1) => useSendQueueStore.getState().move(session, id, delta),
    /** Send this item now, ahead of the others — whatever the session is doing. */
    sendNow: (id: string) => {
      if (!inflight.current) void dispatch(id, false);
    },
  };
}
