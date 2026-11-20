// Memo-queue store (zustand): a refresh tick — bump after add (send modal) /
// flush / tidy-apply so the queue panel refetches. Replaces the old memosKey
// bump counter. The panel also polls while mounted (cross-device sync).
//
// The activity bar's memo badge counts the notes waiting for approval while the panel is not
// mounted (ADR 0140, note of 2026-11-20), so the list is read here too. One read at a time: while a
// panel is mounted it publishes what it read and the feed stays quiet; with none, the feed polls.
import { useEffect } from "react";
import { create } from "zustand";
import { memoList } from "../../core/api/client.ts";
import type { Memo } from "../../types/memo.ts";
import { approvalCount } from "./queues.ts";

interface MemoStore {
  tick: number;
  bump(): void;
  /** Bumped to ask the panel to reveal + focus the composer (leader Ctrl/⌘+K → M,
   * or any other "quick-add a memo" entry point). The panel watches this. */
  composeReq: number;
  requestCompose(): void;
  /** A request to show one auto-run category (from a notification): the panel opens, unfolds it,
   *  scrolls to it and, with history, opens its run history. n makes a repeat re-trigger. */
  reveal: { id: string | null; history: boolean; n: number };
  revealCategory(id: string, history: boolean): void;
  /** Notes waiting for the member's approval; null until a read has answered. A count, not the
   *  list: the list is replaced on every read and a subscriber would redraw each time. */
  waiting: number | null;
  /** How many panels are mounted. They read the list themselves and publish it. */
  panels: number;
  attachPanel(): () => void;
  /** A panel's read, so the badge follows what the panel shows without a second request. */
  publish(list: Memo[]): void;
  /** One read for the badge; false = no usable answer, what is known stays. */
  loadWaiting(): Promise<void>;
  /** The workspace stopped or the tenant changed: the old count no longer holds. */
  reset(): void;
}

const POLL_MS = 10000;
let seq = 0;

export const useMemoStore = create<MemoStore>((set, get) => ({
  tick: 0,
  bump: () => set((s) => ({ tick: s.tick + 1 })),
  composeReq: 0,
  requestCompose: () => set((s) => ({ composeReq: s.composeReq + 1 })),
  reveal: { id: null, history: false, n: 0 },
  revealCategory: (id, history) => set((s) => ({ reveal: { id, history, n: s.reveal.n + 1 } })),
  waiting: null,
  panels: 0,
  attachPanel: () => {
    set((s) => ({ panels: s.panels + 1 }));
    return () => set((s) => ({ panels: s.panels - 1 }));
  },
  publish: (list) => {
    seq++; // a read of the feed still in flight is older than this one
    const n = approvalCount(list);
    if (get().waiting !== n) set({ waiting: n });
  },
  loadWaiting: async () => {
    const mine = ++seq;
    const list = await memoList().catch(() => null);
    // An error body is a failed read, not an empty list; a newer read or a reset made this one stale.
    if (mine !== seq || !Array.isArray(list)) return;
    const n = approvalCount(list);
    if (get().waiting !== n) set({ waiting: n });
  },
  reset: () => {
    seq++;
    set({ waiting: null });
  },
}));

/**
 * Keeps `waiting` current while the workspace runs: at once, after every write (`tick`), and on
 * the panel's own poll interval, but only while no panel is mounted: the panel publishes its reads.
 */
export function useMemoWaitingFeed(running: boolean, tenant: string): void {
  const tick = useMemoStore((s) => s.tick);
  const panelUp = useMemoStore((s) => s.panels > 0);
  useEffect(() => {
    useMemoStore.getState().reset();
  }, [running, tenant]);
  useEffect(() => {
    if (!running || panelUp) return;
    const load = () => void useMemoStore.getState().loadWaiting();
    load();
    const id = window.setInterval(() => {
      if (!document.hidden) load();
    }, POLL_MS);
    return () => window.clearInterval(id);
  }, [running, tenant, tick, panelUp]);
}
