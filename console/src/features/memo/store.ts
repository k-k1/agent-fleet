// Memo-queue store (zustand): a refresh tick — bump after add (send modal) /
// flush / tidy-apply so the queue panel refetches. Replaces the old memosKey
// bump counter. The panel also polls while mounted (cross-device sync).
import { create } from "zustand";

interface MemoStore {
  tick: number;
  bump(): void;
}

export const useMemoStore = create<MemoStore>((set) => ({
  tick: 0,
  bump: () => set((s) => ({ tick: s.tick + 1 })),
}));
