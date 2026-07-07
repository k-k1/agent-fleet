// Chat store (zustand): rail-list refresh tick + per-conversation busy flags
// (進行中 chips published by ChatView, read by the AssistantSection rail).
// Replaces the old chatListKey / chatBusy context slices.
import { create } from "zustand";

interface ChatStore {
  listTick: number;
  bumpList(): void;
  busy: Record<string, boolean>;
  markBusy(id: string, busy: boolean): void;
}

export const useChatStore = create<ChatStore>((set) => ({
  listTick: 0,
  bumpList: () => set((s) => ({ listTick: s.listTick + 1 })),
  busy: {},
  markBusy: (id, b) =>
    set((s) => {
      if (b) return s.busy[id] ? s : { busy: { ...s.busy, [id]: true } };
      if (!s.busy[id]) return s;
      const n = { ...s.busy };
      delete n[id];
      return { busy: n };
    }),
}));
