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
  /** Notes waiting for the member's approval, with the scope (see memoScope) the read was made
   *  for; null until a read has answered. A count, not the list: the list is replaced on every
   *  read and a subscriber would redraw each time. */
  waiting: { scope: string; n: number } | null;
  /** The scope on screen now, set by the feed. A read made for another scope is dropped. */
  scope: string;
  setScope(scope: string): void;
  /** How many panels are mounted. They read the list themselves and publish it. */
  panels: number;
  attachPanel(): () => void;
  /** Starts a read for the badge and returns its ticket. Taken when the request STARTS, so a slow
   *  answer cannot overwrite the result of a later one. */
  begin(): number;
  /** A read's answer: dropped when a later read already answered, or when `scope` is no longer the
   *  one on screen (the workspace stopped, another tenant). */
  publish(list: Memo[], ticket: number, scope: string): void;
  /** One read for the badge when no panel is mounted. */
  loadWaiting(scope: string): Promise<void>;
}

/** What a count belongs to: the tenant while the workspace runs, "" (no count) otherwise. */
export const memoScope = (running: boolean, tenant: string): string => (running ? "t:" + tenant : "");

const POLL_MS = 10000;
let seq = 0; // tickets handed out
let answered = 0; // the newest ticket published

export const useMemoStore = create<MemoStore>((set, get) => ({
  tick: 0,
  bump: () => set((s) => ({ tick: s.tick + 1 })),
  composeReq: 0,
  requestCompose: () => set((s) => ({ composeReq: s.composeReq + 1 })),
  reveal: { id: null, history: false, n: 0 },
  revealCategory: (id, history) => set((s) => ({ reveal: { id, history, n: s.reveal.n + 1 } })),
  waiting: null,
  scope: "",
  setScope: (scope) => {
    if (get().scope === scope) return;
    // The old scope's count is gone with it, and so are the answers still on their way.
    set({ scope, waiting: null });
  },
  panels: 0,
  attachPanel: () => {
    set((s) => ({ panels: s.panels + 1 }));
    return () => set((s) => ({ panels: s.panels - 1 }));
  },
  begin: () => ++seq,
  publish: (list, ticket, scope) => {
    const cur = get();
    if (!scope || scope !== cur.scope || ticket < answered) return;
    answered = ticket;
    const n = approvalCount(list);
    if (cur.waiting?.scope !== scope || cur.waiting.n !== n) set({ waiting: { scope, n } });
  },
  loadWaiting: async (scope) => {
    const ticket = get().begin();
    const list = await memoList().catch(() => null);
    // An error body is a failed read, not an empty list.
    if (Array.isArray(list)) get().publish(list, ticket, scope);
  },
}));

/**
 * Keeps `waiting` current while the workspace runs. The panel is mounted for the whole session
 * and publishes each read it makes anyway, so this stands down while one is attached; it reads by
 * itself only when none is (at once after a write, then on the panel's own interval).
 */
export function useMemoWaitingFeed(running: boolean, tenant: string): void {
  const tick = useMemoStore((s) => s.tick);
  const panelUp = useMemoStore((s) => s.panels > 0);
  const scope = memoScope(running, tenant);
  useEffect(() => {
    useMemoStore.getState().setScope(scope);
  }, [scope]);
  useEffect(() => {
    if (!scope || panelUp) return;
    const load = () => void useMemoStore.getState().loadWaiting(scope);
    // A tick later: a panel attaching in the same commit makes this read redundant.
    const first = window.setTimeout(() => {
      if (useMemoStore.getState().panels === 0) load();
    }, 0);
    const id = window.setInterval(() => {
      if (!document.hidden) load();
    }, POLL_MS);
    return () => {
      window.clearTimeout(first);
      window.clearInterval(id);
    };
  }, [scope, tick, panelUp]);
}
