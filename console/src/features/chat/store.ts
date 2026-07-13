// Chat store (zustand): rail-list refresh tick + per-conversation busy flags
// (進行中 chips published by ChatView, read by the AssistantSection rail).
// Replaces the old chatListKey / chatBusy context slices.
//
// It also parks the state of an *in-flight* streaming turn keyed by conversation id
// (`live` accumulating text, `snapshots` final conversation). A streaming turn is a
// floating fetch that outlives the ChatView that started it, so if the pane is closed
// mid-answer the turn still finishes on the backend — but its result would otherwise be
// delivered to an unmounted component and dropped. Publishing here lets a pane that is
// re-opened on the same conversation re-attach to the live turn and pick up the final
// answer instead of a stale pre-turn snapshot (docs/19: "close pane mid-stream" fix).
import { create } from "zustand";
import type { Conversation, ChatStep } from "../../types/chat.ts";

// Live in-flight turn state: the tentative answer text plus the working steps committed so
// far (docs/19 分離), so a re-opened pane re-attaches to both the process and the answer.
export interface LiveTurn {
  text: string;
  steps: ChatStep[];
}

interface ChatStore {
  listTick: number;
  bumpList(): void;
  busy: Record<string, boolean>;
  markBusy(id: string, busy: boolean): void;
  // Accumulating assistant reply + working steps of an in-flight turn, so a re-opened pane
  // can show the live answer/process rather than an empty spinner.
  live: Record<string, LiveTurn>;
  setLive(id: string, turn: LiveTurn): void;
  clearLive(id: string): void;
  // Latest conversation after a turn completes. Published even when the ChatView that ran
  // the turn is gone, so any pane still showing this conversation picks up the final answer.
  snapshots: Record<string, Conversation>;
  publishSnapshot(c: Conversation): void;
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
  live: {},
  setLive: (id, turn) => set((s) => ({ live: { ...s.live, [id]: turn } })),
  clearLive: (id) =>
    set((s) => {
      if (!(id in s.live)) return s;
      const n = { ...s.live };
      delete n[id];
      return { live: n };
    }),
  snapshots: {},
  publishSnapshot: (c) => set((s) => ({ snapshots: { ...s.snapshots, [c.id]: c } })),
}));
