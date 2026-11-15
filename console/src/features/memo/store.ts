// Memo-queue store (zustand): a refresh tick — bump after add (send modal) /
// flush / tidy-apply so the queue panel refetches. Replaces the old memosKey
// bump counter. The panel also polls while mounted (cross-device sync).
import { create } from "zustand";

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
}

export const useMemoStore = create<MemoStore>((set) => ({
  tick: 0,
  bump: () => set((s) => ({ tick: s.tick + 1 })),
  composeReq: 0,
  requestCompose: () => set((s) => ({ composeReq: s.composeReq + 1 })),
  reveal: { id: null, history: false, n: 0 },
  revealCategory: (id, history) => set((s) => ({ reveal: { id, history, n: s.reveal.n + 1 } })),
}));
